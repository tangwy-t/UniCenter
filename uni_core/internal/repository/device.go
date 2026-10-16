package repository

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
)

// DeviceRepo 是设备表的数据访问层。类型名沿用既有 *Repo 命名（见 RoleRepo/NoticeRepo）。
type DeviceRepo struct {
	db *gorm.DB
}

func NewDeviceRepository(db *gorm.DB) *DeviceRepo { return &DeviceRepo{db: db} }

// Create 插入设备。instance_id / token_hash 的唯一约束冲突由调用方
// 用 database.IsDuplicateKey 判定（enroll 幂等依赖它）。
func (r *DeviceRepo) Create(ctx context.Context, d *entity.Device) error {
	return r.db.WithContext(ctx).Create(d).Error
}

// FindByID 按主键查设备。未命中返回 ErrNotFound 哨兵（而非裸的
// gorm.ErrRecordNotFound）：service 层必须能分辨「设备不存在」（404）与
// 「DB 故障」（500），而哨兵与原始错误之间 errors.Is 是**单向**的
// （见 errors.go 的 notFoundOr）。errors.Is(err, gorm.ErrRecordNotFound) 仍然成立。
func (r *DeviceRepo) FindByID(ctx context.Context, id uint64) (*entity.Device, error) {
	var d entity.Device
	if err := r.db.WithContext(ctx).First(&d, id).Error; err != nil {
		return nil, notFoundOr(err)
	}
	return &d, nil
}

// UpdateEnroll 按主键更新「注册时上报的库存字段 + 轮换后的 token_hash」。
//
// 只更新列出的列：**不动 status**（停用机器重新 enroll 不自动启用，
// 否则会绕过后台的管理意图），也不动 last_seen_at（那是 Touch 的职责）。
// 用 map 形式的 Updates 是为了让「零值也要写入」（如 hostname 变空）也生效 ——
// struct 形式的 Updates 会跳过零值字段。
func (r *DeviceRepo) UpdateEnroll(ctx context.Context, d *entity.Device) error {
	res := r.db.WithContext(ctx).Model(&entity.Device{}).
		Where("id = ?", d.ID).
		Updates(map[string]any{
			"hostname":      d.Hostname,
			"os":            d.OS,
			"arch":          d.Arch,
			"kernel":        d.Kernel,
			"agent_version": d.AgentVersion,
			"platform":      d.Platform,
			"platform_ver":  d.PlatformVer,
			"cpu_model":     d.CPUModel,
			"cpu_cores":     d.CPUCores,
			"mem_total_mb":  d.MemTotalMB,
			"boot_time":     d.BootTime,
			"token_hash":    d.TokenHash,
			// agent_upgrade_supported 随 enroll 更新：重装/重注册的机器可能换了
			// 一份带升级运行时的二进制。
			"agent_upgrade_supported": d.AgentUpgradeSupported,
			// primary_ip 是**每次 enroll 都刷新**的观测值（agent 换网络后重新
			// enroll 即更新）；用 map 形式保证「置空」也能写入。
			"primary_ip": d.PrimaryIP,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// FindByInstanceID 按 agent 落盘指纹查设备（enroll 幂等键）。
// 未命中返回 ErrNotFound 哨兵（语义与 FindByID 一致）。
func (r *DeviceRepo) FindByInstanceID(ctx context.Context, instanceID string) (*entity.Device, error) {
	var d entity.Device
	if err := r.db.WithContext(ctx).Where("instance_id = ?", instanceID).First(&d).Error; err != nil {
		return nil, notFoundOr(err)
	}
	return &d, nil
}

// FindByTokenHash 按 sha256(agent_token) 查设备（鉴权路径）。
// 未命中返回 ErrNotFound 哨兵：鉴权把它映射成「token 无效」，而 DB 故障必须
// 留成 500 —— 否则一次数据库抖动会伪装成「所有 agent 的 token 都失效」。
func (r *DeviceRepo) FindByTokenHash(ctx context.Context, hash string) (*entity.Device, error) {
	var d entity.Device
	if err := r.db.WithContext(ctx).Where("token_hash = ?", hash).First(&d).Error; err != nil {
		return nil, notFoundOr(err)
	}
	return &d, nil
}

// Touch 只更新 last_seen_at 一列，避免整行写放大（该路径每 report 周期触发）。
func (r *DeviceRepo) Touch(ctx context.Context, id uint64, at time.Time) error {
	return r.db.WithContext(ctx).Model(&entity.Device{}).
		Where("id = ?", id).
		UpdateColumn("last_seen_at", at).Error
}

// deviceSortClause 把排序参数收敛成白名单 SQL 片段（防 ORDER BY 注入），
// 与 repository/file.go 的 fileSortClause 同款纪律。
//
// 白名单**严格限定为 DB 列**：lastSeenAt(默认) / hostname / agentVersion / group。
// 水位列（CPU/内存/磁盘使用率）由 service 从 Redis latest 拼进响应，**不是
// DB 列**，无法也不得出现在这里 —— 对它们排序需要先落库（读时拼装的行
// 无法 SQL ORDER BY）。
//
// 非法值（不在白名单 / 非 asc|desc）**静默回落默认**而不是报 400：
//   - sortBy 未登记 → last_seen_at；
//   - sortOrder 不是 asc → desc。
//
// 这样旧客户端、笔误的参数都拿到与「不传排序」完全一致的行为，而不是被
// 400 挡在外面。回落后的默认组合 = lastSeenAt desc, id desc，与引入排序
// 参数之前 FindPage 的硬编码**逐字一致**（评审回归点：默认排序不变）。
//
// id DESC 是所有方向的稳定次序锚（last_seen_at 存在 NULL 与并列值，
// 只按它排序会因页间并列而抖动）。
func deviceSortClause(sortBy, sortOrder string) string {
	column := "last_seen_at"
	switch sortBy {
	case "hostname":
		column = "hostname"
	case "agentVersion":
		column = "agent_version"
	case "group":
		// 分组列：按分区分组浏览时把同组设备聚在一起（空串 = 未分组，排在最前/最后
		// 由方向决定，与普通字符串一致）。
		column = "group_name"
	}
	direction := " DESC"
	if sortOrder == "asc" {
		direction = " ASC"
	}
	return column + direction + ", id DESC"
}

// applyFilters 把列表筛选条件链到给定会话上（与 config.go/file.go 的
// applyFilters 同款：纯函数式地把 db 链下去，不共享语句状态）。
func (r *DeviceRepo) applyFilters(db *gorm.DB, q *request.DeviceQuery, onlineSince time.Time) *gorm.DB {
	if q.Hostname != "" {
		db = db.Where("hostname LIKE ?", "%"+q.Hostname+"%")
	}
	if q.Status != nil {
		db = db.Where("status = ?", *q.Status)
	}
	if q.Online != nil {
		if *q.Online {
			db = db.Where("last_seen_at IS NOT NULL AND last_seen_at >= ?", onlineSince)
		} else {
			db = db.Where("last_seen_at IS NULL OR last_seen_at < ?", onlineSince)
		}
	}
	// IP 模糊（与 Hostname 同语义）：定位场景按片段/前缀搜。
	if q.PrimaryIp != "" {
		db = db.Where("primary_ip LIKE ?", "%"+q.PrimaryIp+"%")
	}
	// Agent 版本精确（见 request.DeviceQuery.AgentVersion 的注释：模糊会
	// 把 0.6 与 0.6.0 混进同一结果）。
	if q.AgentVersion != "" {
		db = db.Where("agent_version = ?", q.AgentVersion)
	}
	// 主机分组精确匹配（见 request.DeviceQuery.Group 的语义裁决：单值精确，
	// 空 = 不过滤）。精确而非 LIKE：分组名作为「桶」的身份，子串匹配会让
	// 选「prod」也带出「prod-archive」，与「看这一组」的意图不符。
	if q.Group != "" {
		db = db.Where("group_name = ?", q.Group)
	}
	return db
}

// FindPage 分页查询。
//
// onlineSince 是在线阈值折算出的时间点（service 依 sys.agent.offlineThreshold 计算）：
//   - Online == nil  → 不过滤
//   - Online == true → last_seen_at >= onlineSince
//   - Online == false→ last_seen_at < onlineSince 或 IS NULL
//
// Count 与取页走 pagination.go 的公共 paginate[T]（本包 11 个仓储的统一入口）：
// countDB 不带 Order/Offset/Limit，dataDB 才链排序；两个会话相互独立，
// 避免 GORM 语句状态在 COUNT 与 SELECT 之间串味（评审 F4：device 曾是唯一
// 手写 Count + Offset/Limit/Find 的例外）。
func (r *DeviceRepo) FindPage(ctx context.Context, q *request.DeviceQuery, onlineSince time.Time) ([]entity.Device, int64, error) {
	countDB := r.applyFilters(r.db.WithContext(ctx).Model(&entity.Device{}), q, onlineSince)
	dataDB := r.applyFilters(r.db.WithContext(ctx).Model(&entity.Device{}), q, onlineSince).
		Order(deviceSortClause(q.SortBy, q.SortOrder))
	return paginate[entity.Device](countDB, dataDB, q)
}

// FindForOverview 取总览要展示的设备（**不分页**，按 limit 截断）。
//
// 与 FindPage 分开而不是复用它的理由：
//   - 分页的「第几页」语义对总览没有意义（总览要的是「一屏看全」，
//     而不是「翻页看」）；硬用 FindPage(page=1, pageSize=100) 会同时
//     受 pageSize 上限（100）与「用户翻页」两条无关语义牵引。
//   - FindPage 要跑一次 COUNT(*)，而总览的 total 只用于文案说明，
//     且需要知道「是否被截断」。用 limit+1 取一行来判定截断，
//     比 COUNT(*) 便宜，也**不需要**在截断时再算一遍。
//
// 过滤条件与 FindPage **完全同源**（同一个 applyFilters），故「列表页筛出
// 3 台」与「总览页筛出 3 台」永远不会出现口径差。
//
// ids 是先按 id IN (...) 过滤的设备白名单（空 = 不限制）。
// limit 是返回上限；多取一行用于判定是否被截断（返回值 truncated）。
func (r *DeviceRepo) FindForOverview(ctx context.Context, q *request.DeviceOverviewQuery,
	ids []uint64, onlineSince time.Time, limit int) (list []entity.Device, truncated bool, err error) {
	if limit <= 0 {
		return nil, false, nil
	}
	db := r.applyFilters(r.db.WithContext(ctx).Model(&entity.Device{}), &request.DeviceQuery{
		Hostname: q.Hostname,
		Status:   q.Status,
		Online:   q.Online,
	}, onlineSince)
	if len(ids) > 0 {
		db = db.Where("id IN ?", ids)
	}
	// 多取一行判截断：拿回 limit+1 行即说明还有更多，据此置 truncated 并裁掉多出的那行。
	if err := db.Order("last_seen_at DESC, id DESC").Limit(limit + 1).
		Find(&list).Error; err != nil {
		return nil, false, err
	}
	if len(list) > limit {
		return list[:limit], true, nil
	}
	return list, false, nil
}

// SetStatus 设置启停态（管理侧属性，与在线状态正交）。
//
// 未命中返回 ErrNotFound 哨兵（而非任意错误）：调用方据此回 404，
// 同时把「DB 故障」留给 500 —— 见 errors.go 的说明。
func (r *DeviceRepo) SetStatus(ctx context.Context, id uint64, status int8) error {
	res := r.db.WithContext(ctx).Model(&entity.Device{}).
		Where("id = ?", id).Update("status", status)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateGroupTags 写主机分组 / 标签（**部分更新**：只写这次提供的列）。
//
// group / tags 为 nil 表示「这次不改这一列」；提供空串表示**清空**。用 map 形式的
// Updates 让「清空（写入空串）」也生效（struct 形式的 Updates 会跳过零值字段）。
//
// 两列都未提供（双 nil）时没有列可写：此时仍按 id 做一次存在性检查 —— PATCH 端点
// 依赖「不存在的 id → ErrNotFound」这一契约，不能因为请求体恰好是空对象就静默成功。
//
// RowsAffected==0 有两种可能，必须分开（否则幂等重放会误报 404）：
//   - 行不存在 → ErrNotFound；
//   - 写入值与该行现值相同（**值未改变**）→ 合法 no-op。
//
// MySQL 默认把「值未改变的 UPDATE」计为 0 行受影响（DSN 未开 clientFoundRows，
// 见 config.mysqlDSN），故「把分组设成它已经是的值」会命中 0 行 —— 若直接据此报
// 404，用户重放同一次 PATCH 就会看到「设备不存在」。故 0 行时再用一次存在性检查
// 把二者分开。未命中返回 ErrNotFound 哨兵（与 SetStatus/Delete 同契约）；其它错误 500。
func (r *DeviceRepo) UpdateGroupTags(ctx context.Context, id uint64, group, tags *string) error {
	updates := make(map[string]any, 2)
	if group != nil {
		updates["group_name"] = *group
	}
	if tags != nil {
		updates["tags"] = *tags
	}
	if len(updates) == 0 {
		return r.ensureDeviceExists(ctx, id)
	}
	res := r.db.WithContext(ctx).Model(&entity.Device{}).
		Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return r.ensureDeviceExists(ctx, id)
	}
	return nil
}

// ensureDeviceExists 命中（含软删排除）返回 nil，未命中返回 ErrNotFound 哨兵，
// 查询故障原样返回。供 UpdateGroupTags 在「无列可写」与「0 行受影响」两处复用。
func (r *DeviceRepo) ensureDeviceExists(ctx context.Context, id uint64) error {
	var n int64
	if err := r.db.WithContext(ctx).Model(&entity.Device{}).
		Where("id = ?", id).Count(&n).Error; err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete 软删（BaseEntity 带 gorm.DeletedAt）。
// 指标行与 Redis 键的清理由 service 层负责编排（见 spec §7.3）。
// 未命中返回 ErrNotFound 哨兵（语义同 SetStatus）。
func (r *DeviceRepo) Delete(ctx context.Context, id uint64) error {
	res := r.db.WithContext(ctx).Delete(&entity.Device{}, id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// ── Agent 升级（设计 §5.1）────────────────────────────────────────────

// RefreshStaticFromHello 用 hello 载荷刷新**设备自述的静态信息**。
//
// 这条路存在的原因：此前只有 enroll 会写这些列，于是**重连鉴权不刷新任何东西** ——
// 一台设备升级完 agent 重连，库里的 agent_version 还是旧的，「升级成功」根本观测不到。
// 同类后果还有：换主机名、换内核、加内存后，控制台一直显示升级前的值。
//
// 调用时机是每次**鉴权成功**（不是每次上报）：这些字段只随进程启动变化，
// 按上报频率写库纯属浪费。
//
// 只更新列出的列：不动 status（管理意图）、不动 last_seen_at（那是 Touch 的职责）、
// 不动 target_agent_version（那是运维意图，不能被设备自述覆盖 —— 这条如果错了，
// 一次重连就会把升级目标清掉）。
func (r *DeviceRepo) RefreshStaticFromHello(ctx context.Context, d *entity.Device) error {
	res := r.db.WithContext(ctx).Model(&entity.Device{}).
		Where("id = ?", d.ID).
		Updates(map[string]any{
			"hostname":                d.Hostname,
			"os":                      d.OS,
			"arch":                    d.Arch,
			"kernel":                  d.Kernel,
			"agent_version":           d.AgentVersion,
			"platform":                d.Platform,
			"platform_ver":            d.PlatformVer,
			"cpu_model":               d.CPUModel,
			"cpu_cores":               d.CPUCores,
			"mem_total_mb":            d.MemTotalMB,
			"boot_time":               d.BootTime,
			"agent_upgrade_supported": d.AgentUpgradeSupported,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// SetUpgradeTarget 写设备级目标版本（空串 = 清空 = 恢复跟随全站）。
//
// 刻意**不动** agent_upgrade_state：终态字段描述「最近一次尝试的结果」，
// 是历史事实；改目标不改变历史（设计 §3.4 的数据写入路径表）。
func (r *DeviceRepo) SetUpgradeTarget(ctx context.Context, id uint64, target string) error {
	res := r.db.WithContext(ctx).Model(&entity.Device{}).
		Where("id = ?", id).Update("target_agent_version", target)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// SetUpgradeTerminal 写设备行的**升级终态**（无/已达成/失败/已回滚 + 原因码 + 时间）。
//
// 过程态（待升级/升级中）不在此列：它们是读时推导的（entity.Device 的注释）。
func (r *DeviceRepo) SetUpgradeTerminal(ctx context.Context, id uint64, state int8,
	reasonCode string, at time.Time) error {
	res := r.db.WithContext(ctx).Model(&entity.Device{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"agent_upgrade_state":  state,
			"agent_upgrade_reason": reasonCode,
			"agent_upgrade_at":     at,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// FindByIDs 按主键批量取设备（软删的设备不会返回）：**一条 `id IN (...)`**，
// 取代「逐台 FindByID」的 N+1（升级下发的 ids 路径等批量场景用它，
// 一百台设备原来会变成 100 条 SELECT，全在同一个请求里）。
//
// ids 原样交给 GORM（[]uint64，不打成 []any / 不过 float）：主键是雪花 id（19 位，
// 超过 float64 能精确表达的 2^53），中途转一次 float 就会取到**另一台设备**的行。
// 缺席 = 那台设备不在了（软删 / 真删）——调用方据此自愈，故障则原样返回错误。
func (r *DeviceRepo) FindByIDs(ctx context.Context, ids []uint64) ([]entity.Device, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var list []entity.Device
	if err := r.db.WithContext(ctx).Where("id IN ?", ids).Order("id ASC").
		Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// FindByUpgradeFilter 返回筛选命中的**全部**设备（不分页）。
//
// 与 FindPage / FindForOverview 共用同一个 applyFilters —— 这是「按筛选下发」
// 防呆（preview 与下发两边数字必须一致）的底层保证：口径只有一份实现。
// 调用方拿到结果后自行做「平台是否有产物 / 是否支持远程升级 / 是否已在该版本」的分类。
func (r *DeviceRepo) FindByUpgradeFilter(ctx context.Context, q *request.DeviceQuery,
	onlineSince time.Time) ([]entity.Device, error) {
	var list []entity.Device
	db := r.applyFilters(r.db.WithContext(ctx).Model(&entity.Device{}), q, onlineSince)
	if err := db.Order("id ASC").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}
