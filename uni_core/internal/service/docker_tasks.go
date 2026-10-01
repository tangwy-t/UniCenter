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
	"github.com/tangwy-t/UniCenter/uni_core/internal/repository"
)

// Docker 域的**任务面**（6b）：任务中心消费的「最近指令」读面 —— 长任务
// （pull/compose:up 等）不再靠前端弹窗转圈，收口到一张跨主机的任务列表上。
//
// 存储纪律：**只读不新建存储**。条目数据全部来自 CmdStore 的既有记录（ref/action/
// target/发起人/受理时刻/终态/结论句），枚举靠 CmdStore 的最近受理索引
// （docker:cmd:recent，6b 补进数据面的唯一新键 —— 没有它任务中心只能 SCAN
// docker:cmd:*，那是对共享 Redis 的全键空间遍历）。主机名/用户名是读面联查
// （设备表/用户表），不落任何新表。
//
// 取消纪律（6b）：本切片不提供新取消动作 —— 任务中心的「取消」对拉取类任务 =
// 前端复用既有进度流 Abort（断开 /cmds/:ref/pull 即下发 cancel，语义不变）；
// **非流任务本切片不做取消**：CLI/daemon 侧的 kill 面大（compose up 的中断点、
// 超时上下文、半态回滚都要重新论证），硬做只会出一个半吊子的「取消按钮」，
// 留后续位（接口形态已在任务条目里备好）。
type DockerTaskService struct {
	cmds    *dockerstate.CmdStore
	devices DockerDeviceReader
	users   DockerUserLookup
	log     logger.LoggerInterface
}

// DockerUserLookup 是任务面需要的用户字段（由 repository.UserRepo 满足）：发起人
// 用户名是任务条目的必备列（用户已删时降级为空串，条目不消失）。
type DockerUserLookup interface {
	FindByID(ctx context.Context, id uint64) (*entity.SysUser, error)
}

// dockerTaskCap 是端点返回上限（spec：最近 N=100）。
const dockerTaskCap = 100

// NewDockerTaskService 构造任务面。
func NewDockerTaskService(cmds *dockerstate.CmdStore, devices DockerDeviceReader,
	users DockerUserLookup, log logger.LoggerInterface) *DockerTaskService {
	return &DockerTaskService{cmds: cmds, devices: devices, users: users, log: log}
}

// Tasks 返回最近受理的任务（≤100 条、受理时刻降序、跨主机聚合）。
//
// 枚举窗口本身是契约（「最近」不是全量）：**不报 Total** —— 窗口外的条目数
// 无从知晓，报一个「窗口内匹配数」会被前端误读成全量口径（与 /docker/containers
// 的 Total 语义不同：那边枚举是完整的，Total 是诚实的全量数）。
//
// 逐条读取的取舍：
//   - 记录已过期（TTL 到）→ 跳过并顺手剔除报名（索引是加速器，判定以记录为准 ——
//     与 Inflight 的陈旧索引自愈同一纪律）；
//   - 单条读失败（键损坏/Redis 抖动）→ 告警并跳过：少一行好过整页 500（读面
//     既有纪律，见 DockerService.Hosts）。
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
	refs, err := s.cmds.RecentRefs(ctx, dockerstate.RecentCmdKeep)
	if err != nil {
		return nil, apperror.Internal("读取任务列表失败", err)
	}
	type item struct {
		entry  response.DockerTaskItem
		hostID uint64
		userID uint64
	}
	matched := make([]item, 0, dockerTaskCap)
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
		matched = append(matched, item{
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
	// 排序在截断**之前**（与 Workloads 同纪律：截断砍掉的必须是排序后的尾部）。
	// 索引按受理时刻枚举，但「判定以记录为准」：按记录自己的 CreatedAt 重排一次，
	// 索引的评分顺序就不是一个需要被信任的事实。
	slices.SortFunc(matched, func(a, b item) int {
		if a.entry.CreatedAt != b.entry.CreatedAt {
			return cmp.Compare(b.entry.CreatedAt, a.entry.CreatedAt)
		}
		// 同一毫秒内的两条：ref 降序作为稳定次序（ref 本身是纳秒级单调串，
		// 降序 = 后受理在前 —— 与「最近在前」一致）。
		return cmp.Compare(b.entry.Ref, a.entry.Ref)
	})
	if len(matched) > dockerTaskCap {
		matched = matched[:dockerTaskCap]
	}
	// 联查主机名/用户名只发生在**过滤与截断之后**（至多 100 行），且同一主机/
	// 用户只查一次 —— 逐行现查会把最坏路径放大成 2×窗口 的小查询风暴。
	items := make([]response.DockerTaskItem, 0, len(matched))
	hostnames := map[uint64]string{}
	usernames := map[uint64]string{}
	for _, m := range matched {
		m.entry.Hostname = s.hostnameOf(ctx, m.hostID, hostnames)
		m.entry.Username = s.usernameOf(ctx, m.userID, usernames)
		items = append(items, m.entry)
	}
	return &response.DockerTaskListResp{Items: items}, nil
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
