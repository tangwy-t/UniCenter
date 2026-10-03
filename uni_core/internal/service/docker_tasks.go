package service

import (
	"cmp"
	"context"
	"errors"
	"slices"

	"go.uber.org/zap"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/response"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerpolicy"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/permission"
	"github.com/tangwy-t/UniCenter/uni_core/internal/repository"
)

// Docker 域的**任务面**（6b）：任务中心消费的「最近指令」读面 —— 长任务
// （pull/compose:up 等）不再靠前端弹窗转圈，收口到一张跨主机的任务列表上。
//
// 数据来源是**两条腿**（8d 起）：
//   - **实时面**：CmdStore 的记录（含 pending 在途），靠最近受理索引
//     （docker:cmd:recent）枚举 —— 窗口 = 最近受理的 RecentCmdKeep 条；
//   - **历史面**：docker_task_history 表（终态落库，8d），读面按 ref 合并去重
//     （**实时胜**：Redis 记录是活的，可能刚被迟到的事实覆盖过）、按受理时刻
//     整体重排后分页。
//
// 存储纪律的变更（8d，用户令：任务历史彻底可追）：本文件曾以「只读不新建存储」
// 为纪律（任务中心只消费 CmdStore 的既有记录）。那条纪律已由本轮「用持久层替代
// ~30 分钟 TTL 易失」的指令**取代** —— 终态记录 TTL 10 分钟（pending 兜底 2 小时）
// 意味着任务中心的「历史」实际只是一个易失窗口，半小时前的失败拉取无从追起。
// 现在：终态由 CmdStore 的终态钩子在**唯一写入点**落库一份（service/
// docker_task_history.go），读面合并两条腿；在途不回填（历史从落库启用后的终态
// 开始记 —— 不编造过去），Redis 侧的行为零改动（钩子是观察者）。
//
// 主机名/用户名仍是读面联查（设备表/用户表），**不存快照**：实时条目与历史条目
// 走同一条联查路径，设备改名/删号后两边说同一句话。
//
// 取消纪律（本波语义收口）：任务中心的「取消」= **显式取消端点**（POST
// /docker/hosts/:id/cmds/:ref/cancel → DockerStreamService.CancelProgress 向
// agent 下发 cancel 帧），覆盖进度三族 pull/build/push；断开进度流**不再**触发
// 取消 —— 观看与执行解耦，关对话框/切页/收起行只是停止观看，任务照常跑完。
// **非进度族任务仍不提供取消**：CLI/daemon 侧的 kill 面大（compose up 的中断点、
// 超时上下文、半态回滚都要重新论证），硬做只会出一个半吊子的「取消按钮」，
// 留后续位（接口形态已在任务条目里备好）。
//
// 取消与完成的竞态（B4 裁决，显式取消后同款）：下发的 cancel 是 best-effort 动作，
// 与「daemon 恰好干完活」只差毫秒 —— 终态以 agent 侧拉取的**实际结局**为准（完成
// 即成功，迟到的 cancel 是 no-op，不得改写成「拉取已取消」），本服务只如实投影结果，
// 不在 core 侧二次推断（推断会把 agent 的事实覆盖掉，与 sweep 的超时同一纪律）。
type DockerTaskService struct {
	cmds    *dockerstate.CmdStore
	devices DockerDeviceReader
	users   DockerUserLookup
	// history 是历史面（8d）。nil = 未装配：读面退化为纯实时（与引入持久层之前
	// 逐字一致），只应出现在测试构造里 —— 装配点（wireup）没有理由交 nil。
	history DockerTaskHistoryReader
	log     logger.LoggerInterface
}

// DockerUserLookup 是任务面需要的用户字段（由 repository.UserRepo 满足）：发起人
// 用户名是任务条目的必备列（用户已删时降级为空串，条目不消失）。
type DockerUserLookup interface {
	FindByID(ctx context.Context, id uint64) (*entity.SysUser, error)
}

// DockerTaskHistoryReader 是历史读面的窄接口（由 repository.DockerTaskHistoryRepo
// 满足；消费方定义接口是仓库既有约定）。
//
// 三个方法对应合并读面的三件事：FindTop 取上界条数、Count 取过滤总数、
// CountByRefs 取与实时面的重叠数（合并后总数 = 实时数 + 历史数 − 重叠数）。
type DockerTaskHistoryReader interface {
	FindTop(ctx context.Context, q *request.DockerTasksQuery, limit int) ([]entity.DockerTaskHistory, error)
	Count(ctx context.Context, q *request.DockerTasksQuery) (int64, error)
	CountByRefs(ctx context.Context, refs []string) (int64, error)
}

// dockerTaskPageSizeMax 是单页条数上限（兜底）。
//
// HTTP 绑定的 app.PageRequest 已经封顶 100（并封顶页码 10000，防天文偏移）；
// 这里再夹一次是因为本服务不止 HTTP 一个调用方，而合并读面的取数上界直接由
// 页大小推导（need = 偏移 + 页大小 + 实时条数）—— 不夹的话，一个直调会把它放大成
// 无界取数。值与 app.PageRequest 的 max 逐字一致。
const dockerTaskPageSizeMax = 100

// NewDockerTaskService 构造任务面。
func NewDockerTaskService(cmds *dockerstate.CmdStore, devices DockerDeviceReader,
	users DockerUserLookup, log logger.LoggerInterface) *DockerTaskService {
	return &DockerTaskService{cmds: cmds, devices: devices, users: users, log: log}
}

// WithHistory 注入历史面（8d）。
func (s *DockerTaskService) WithHistory(h DockerTaskHistoryReader) *DockerTaskService {
	s.history = h
	return s
}

// taskRow 是合并序列的一行：条目 + 联查需要的两个主键（主机/用户）。
//
// 为什么把主键留在行上而不是构建条目时就联查名字：名字联查要打设备表/用户表，
// 而合并、去重、排序、切片都只认 ref 与时刻 —— 联查推到切片之后，只对真正返回的
// 那一页发生（至多 pageSize 行），与 6b 的「联查只在过滤与截断之后」同一纪律。
type taskRow struct {
	entry  response.DockerTaskItem
	hostID uint64
	userID uint64
}

// Tasks 返回任务中心的一页：实时（CmdStore，含在途）∪ 历史（DB）按 ref 去重
// （**实时胜**）、按受理时刻降序、整体分页。
//
// 分页与合计口径：
//   - 取数上界 need = 页偏移 + 页大小 + 实时条数。去重只会砍掉「同时在两条腿里」
//     的历史行，而这些行的上界就是实时条数（实时里 pending 的那些压根不在历史里）
//     —— 故前 need 条历史足以填满本页：排在 need 之后的历史行，前面至少有
//     (偏移 + 页大小) 条历史同行，去重最多砍掉「实时条数」条，剩下的仍多于本页
//     所需；
//   - Total = 实时条数 + 历史总数 − 重叠数，是本合并列表的**真实全量**（不再是
//     「窗口内匹配数」—— 6b 的「不报 Total」随持久层落地而作废：窗口外的终态
//     现在有历史面兜底，报得出就如实报）；
//   - 页码越界（如第 3 页在并发清理后空了）返回空 items 且 Total 不变 —— 与全站
//     仓储分页的越界语义一致，由前端把页码拉回第一页。
//
// 过滤是并集语义：pending 档只有实时面有内容（历史表只存终态），done/不筛时两条腿
// 都参与。实时面的逐条读取纪律照旧：
//   - **读面不进列表**（QA 路 1 P2 的裁定）：只读动作（inspect 档的「只看不碰」
//     家族：container:inspect/logs/stats、image:inspect、compose:logs、
//     compose.file:read）从任务列表剔除 —— 「任务」的语义是变更/长任务，而这些
//     动作会在页面浏览时被**自动受理**（容器详情页每打开一次就为日志与统计各留
//     一条记录）。历史面用**同一把尺子**在写入侧就剔除了它们（dockerTaskReadOnly），
//     两边不会出现「实时看不见、历史翻出来」的分裂；
//   - 记录已过期（TTL 到）→ 跳过并顺手剔除报名（索引是加速器，判定以记录为准）；
//   - 单条读失败（键损坏/Redis 抖动）→ 告警并跳过：少一行好过整页 500。
func (s *DockerTaskService) Tasks(ctx context.Context, q *request.DockerTasksQuery) (*response.DockerTaskListResp, error) {
	if q == nil {
		q = &request.DockerTasksQuery{}
	}
	if q.Status != "" && q.Status != "pending" && q.Status != "done" {
		// 400 结论句而不是静默当不过滤（与 Workloads.state 同一句纪律）。
		return nil, apperror.BadRequest("参数错误: status 仅支持 pending 或 done")
	}
	if q.Action != "" {
		if _, ok := dockerpolicy.Lookup(q.Action); !ok {
			// 动作码的策略表是唯一事实源：未登记的 action 在这里 400（与受理处同款）。
			return nil, apperror.BadRequest("未知操作")
		}
	}
	page, pageSize := q.GetPage(), q.GetPageSize()
	if pageSize > dockerTaskPageSizeMax {
		pageSize = dockerTaskPageSizeMax
	}
	offset := (page - 1) * pageSize

	live, err := s.collectLive(ctx, q)
	if err != nil {
		return nil, err
	}
	rows := live
	total := int64(len(live))

	if s.history != nil && q.Status != "pending" {
		hist, err := s.history.FindTop(ctx, q, offset+pageSize+len(live))
		if err != nil {
			return nil, apperror.Internal("读取任务历史失败", err)
		}
		histTotal, err := s.history.Count(ctx, q)
		if err != nil {
			return nil, apperror.Internal("读取任务历史失败", err)
		}
		liveRefs := make([]string, 0, len(live))
		for _, r := range live {
			liveRefs = append(liveRefs, r.entry.Ref)
		}
		overlap, err := s.history.CountByRefs(ctx, liveRefs)
		if err != nil {
			return nil, apperror.Internal("读取任务历史失败", err)
		}
		total = int64(len(live)) + histTotal - overlap
		rows = append(live, historyRows(hist, liveRefs)...)
	}

	// 排序在切片**之前**（实时 ∪ 历史是一个序列，分段各排会得到假的「前页」）。
	// 主键受理时刻降序；同一毫秒用 ref 降序做稳定次序（ref 是纳秒级单调串，
	// 降序 = 后受理在前 —— 与「最近在前」一致）。
	slices.SortFunc(rows, func(a, b taskRow) int {
		if a.entry.CreatedAt != b.entry.CreatedAt {
			return cmp.Compare(b.entry.CreatedAt, a.entry.CreatedAt)
		}
		return cmp.Compare(b.entry.Ref, a.entry.Ref)
	})
	start := min(offset, len(rows))
	end := min(start+pageSize, len(rows))
	pageRows := rows[start:end]

	// 联查主机名/用户名只发生在切片之后（至多一页），且同一主机/用户只查一次。
	items := make([]response.DockerTaskItem, 0, len(pageRows))
	hostnames := map[uint64]string{}
	usernames := map[uint64]string{}
	for _, r := range pageRows {
		r.entry.Hostname = s.hostnameOf(ctx, r.hostID, hostnames)
		r.entry.Username = s.usernameOf(ctx, r.userID, usernames)
		items = append(items, r.entry)
	}
	return &response.DockerTaskListResp{
		Items: items, Total: total, Page: page, PageSize: pageSize,
	}, nil
}

// collectLive 枚举实时面（CmdStore 窗口 → 过滤 → 行集合）。
//
// 窗口本身是枚举上界（RecentCmdKeep 条最新受理）：实时面的存在形态就是一个
// 有界窗口，它的边界不构成「列表不完整」的承诺 —— 终态条目在历史面兜底。
func (s *DockerTaskService) collectLive(ctx context.Context, q *request.DockerTasksQuery) ([]taskRow, error) {
	refs, err := s.cmds.RecentRefs(ctx, dockerstate.RecentCmdKeep)
	if err != nil {
		return nil, apperror.Internal("读取任务列表失败", err)
	}
	live := make([]taskRow, 0, len(refs))
	for _, ref := range refs {
		rec, err := s.cmds.Get(ctx, ref)
		switch {
		case err != nil:
			if s.log != nil {
				s.log.Warn("docker task record read failed", zap.String("ref", ref), zap.Error(err))
			}
			continue
		case rec == nil:
			if err := s.cmds.ForgetRecent(ctx, ref); err != nil && s.log != nil {
				s.log.Warn("docker task recent index forget failed", zap.String("ref", ref), zap.Error(err))
			}
			continue
		}
		if rec.UserID == 0 {
			// docker:events 的常驻订阅记录不是用户任务（无发起人），任务中心不列它
			// —— 它们的存在形态在事件聚合流自己的页面里。
			continue
		}
		if dockerTaskReadOnly(rec.Action) {
			continue // 读面剔除，理由见 Tasks 头注释（「任务」的语义是变更）
		}
		if q.HostID != 0 && rec.DeviceID != q.HostID {
			continue
		}
		if q.Status == "pending" && rec.Status != dockerstate.StatusPending {
			continue
		}
		if q.Status == "done" && rec.Status == dockerstate.StatusPending {
			continue
		}
		if q.Action != "" && rec.Action != q.Action {
			continue
		}
		live = append(live, taskRow{
			entry: response.DockerTaskItem{
				Ref:       rec.Ref,
				HostID:    rec.DeviceID,
				Action:    rec.Action,
				Target:    rec.Target,
				CreatedAt: rec.CreatedAt,
				Status:    rec.Status,
				Summary:   terminalSummary(rec),
			},
			hostID: rec.DeviceID,
			userID: rec.UserID,
		})
	}
	return live, nil
}

// historyRows 把历史行折成合并序列的行（跳过与实时面重叠的 ref —— **实时胜**：
// 活的记录可能刚被迟到的事实覆盖过，历史行最多与它等价，绝不更可信）。
func historyRows(rows []entity.DockerTaskHistory, liveRefs []string) []taskRow {
	out := make([]taskRow, 0, len(rows))
	for i := range rows {
		row := &rows[i]
		if slices.Contains(liveRefs, row.Ref) {
			continue
		}
		out = append(out, taskRow{
			entry: response.DockerTaskItem{
				Ref:       row.Ref,
				HostID:    row.DeviceID,
				Action:    row.Action,
				Target:    row.Target,
				CreatedAt: row.AcceptedAt,
				Status:    row.Status,
				Summary:   row.Summary,
			},
			hostID: row.DeviceID,
			userID: row.UserID,
		})
	}
	return out
}

// dockerTaskReadOnly 判定一条动作是否属于「读面」：任务列表对 inspect 档收口。
//
// 判定依据是**策略表的权限列而不是一份并列的动作清单**：inspect 档就是「只看不碰」
// 的语义档（dockerpolicy 里 container:logs/stats/inspect、image:inspect、compose:logs、
// compose.file:read 同档的书写口径），将来新增只读动作会自动继承这份剔除，不会漏；
// 而 image:scan 这类「读语义但长任务」的动作在 manage 档，照旧留在列表里（扫描的
// 任务中心留痕是 P3 的既有设计，见 scanFastPath）。
//
// 历史面的写入侧（DockerTaskHistoryRecorder）用的是**同一个函数** —— 剔除尺子只有
// 一把，两边各写一份就会在策略表调整时出现「历史里翻得出实时看不见的行」。
//
// 策略表查不到的动作**不剔除**（放行）：记录只会经受理路径产生，而受理路径要求
// 动作在策略表里；万一将来表里删了某个动作，把历史记录当任务显示出来（多一行）
// 比静默吞掉它（丢一行）安全。
func dockerTaskReadOnly(action string) bool {
	perm, ok := dockerpolicy.RequiredPerm(action)
	return ok && perm == permission.PermDockerInspect
}

// hostnameOf 带记忆的联查：同一台主机多个任务只查一次设备表；未命中（设备已删）
// 与**查询失败**同降级为空串 —— 条目不消失（hostId 仍是可导航的主键），
// 真故障告警（与 Hosts 的 per-host warn 同族）。
func (s *DockerTaskService) hostnameOf(ctx context.Context, id uint64, memo map[uint64]string) string {
	if id == 0 {
		return ""
	}
	if v, ok := memo[id]; ok {
		return v
	}
	if s.devices == nil {
		memo[id] = ""
		return ""
	}
	d, err := s.devices.FindByID(ctx, id)
	if err != nil && !errors.Is(err, repository.ErrNotFound) && s.log != nil {
		s.log.Warn("docker task hostname lookup failed", zap.Uint64("deviceId", id), zap.Error(err))
	}
	name := ""
	if d != nil {
		name = d.Hostname
	}
	memo[id] = name
	return name
}

// usernameOf 带记忆的联查：同 hostnameOf 的降级口径（用户已删 → 空用户名，
// 条目不消失 —— 审计里 UserID 仍在）。
func (s *DockerTaskService) usernameOf(ctx context.Context, id uint64, memo map[uint64]string) string {
	if id == 0 {
		return ""
	}
	if v, ok := memo[id]; ok {
		return v
	}
	if s.users == nil {
		memo[id] = ""
		return ""
	}
	u, err := s.users.FindByID(ctx, id)
	if err != nil && !errors.Is(err, repository.ErrNotFound) && s.log != nil {
		s.log.Warn("docker task username lookup failed", zap.Uint64("userId", id), zap.Error(err))
	}
	name := ""
	if u != nil {
		name = u.Username
	}
	memo[id] = name
	return name
}
