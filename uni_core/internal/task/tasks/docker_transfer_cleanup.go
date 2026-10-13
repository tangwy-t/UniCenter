package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerpolicy"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// DockerTransferCleanupTask 是**上传产物回收**：向全部可管 docker 主机逐台派发
// system:transfer-cleanup，让每台按策略清掉自己 transferDir 里超龄的上传产物
//（`build-ctx-*`，构建上下文与载入镜像共用同一条上传通道与同一个落点）。
//
// 为什么它必须是一条**定时任务表里的显式任务**（而不是像以前那样挂在别人的路径上）：
// 回收此前挂在**每次上传的开头**，理由自述为「上传是产物唯一产生源，清扫跟着产生源走」。
// 那条前提在载入镜像复用同一通道后**已被打破** —— 产物可到 GB 级（实测 520MB /
// 1.6GB / 2.87GB），而「传完就不再传」的机器再也没有任何东西触发清扫，产物会永久
// 占着 agent 的盘。回收因此有了自己的时刻表：一条 cron、一行 job、一个能手工触发的
// 入口（POST /jobs/:id/run），以及一份看得见战果的执行日志。
//
// 形态（照 AgentUpgradePatrolTask 的先例：定时任务去操作 agent）：
//   - **逐台失败不拖垮整轮** —— 枚举某台快照失败、记录写不进去、agent 离线，
//     都只记一条日志并跳过那一台（整轮照常做完）。Execute 只在**整轮不可执行**时
//     返回错误（枚举源都读不到）：那才是「这一轮没发生过」，也是调度器重试
//     真正有意义的情形（重试口径见 scheduler/executor.go 的重试循环）；
//   - **有界等待 + 有界阻塞**：派发完在 wait 窗口内收结果（默认 2 分钟），
//     窗口到点即收工，不把调度器的 worker 无限期占住；
//   - **战果落日志**：每台的 detail（agent 给的 `removed=… freed_bytes=…`）原文进
//     日志。这不是装饰 —— 这批指令记录的发起人是 core 自己（UserID=0），既不入任务
//     历史也不入审计，**日志是唯一能回答「昨天到底清了几个产物」的地方**。
type DockerTransferCleanupTask struct {
	hosts  DockerTransferHosts
	cmds   DockerTransferCmdStore
	sender DockerTransferSender
	log    logger.LoggerInterface

	// now 是挂钟（受理时刻与收结果窗口的起点）；wait/poll 是收结果的窗口与轮询间隔。
	// 三个都是字段而非常量：测试要一个确定的现在与毫秒级的窗口，不必真的等两分钟。
	now  func() time.Time
	wait time.Duration
	poll time.Duration
	// idGen 生成指令 ref（**必须与用户指令共用同一个计数器**，见 Deps.RequestIDGen）。
	idGen func() string
}

// dockerTransferCleanupAction 是本任务派发的唯一动作（协议白名单里的主机级维护）。
const dockerTransferCleanupAction = agentproto.DockerActionTransferCleanup

// 收结果窗口的两个默认值。
//
// wait 取 2 分钟：agent 侧的这条指令是本地 ReadDir + 几个 unlink（毫秒级），
// 而它的执行档与记录档都是 60 秒（协议超时；core 的 5 秒一轮的 docker sweep 会在
// 60 秒把没回来的记录判成 timeout）—— 窗口留出「记录被判超时 + 结果回写」的余量，
// 让每一台**都能拿到一个明确结论**（成功/失败/超时），而不是一片「未回报」。
// 上限同样是纪律：窗口到点就收工，绝不为了等一台卡住的主机把调度器 worker 占住
// （调度器的执行上限是 5 分钟，本任务必须在它之内收手）。
//
// poll 取 2 秒：一台主机的回包在派发后一两秒内到（连接是本地的 WSS 长连接），
// 这个间隔让「快的那批」在第一轮就收完，也远小于 5 秒一轮的 docker sweep。
const (
	dockerTransferResultWait = 2 * time.Minute
	dockerTransferPoll       = 2 * time.Second
)

// NewDockerTransferCleanupTask 创建任务实例（log 为 nil 时退化成 Nop）。
func NewDockerTransferCleanupTask(hosts DockerTransferHosts, cmds DockerTransferCmdStore,
	sender DockerTransferSender, idGen func() string, log logger.LoggerInterface) *DockerTransferCleanupTask {
	if log == nil {
		log = logger.NewNop()
	}
	if idGen == nil {
		// 未注入（测试/降级）：本包内置的纳秒 + 自增。生产必须注入与用户指令共用的
		// 那个计数器（见 Deps.RequestIDGen）—— 两套计数器会把「同一纳秒、两计数器
		// 等值」的撞号窗口重新引进来（docker:cmd:<ref> 是同一个键空间）。
		idGen = defaultRequestID
	}
	return &DockerTransferCleanupTask{
		hosts: hosts, cmds: cmds, sender: sender, log: log,
		now: time.Now, wait: dockerTransferResultWait, poll: dockerTransferPoll, idGen: idGen,
	}
}

// Name 是任务唯一标识（**必须与迁移种子的 invoke_target 逐字一致**，
// 由 v020 的守卫测试钉住）。
func (t *DockerTransferCleanupTask) Name() string { return "docker-transfer-cleanup" }

// DisplayName 是任务管理页展示名。
func (t *DockerTransferCleanupTask) DisplayName() string { return "上传产物回收" }

// transferCleanupStats 是一轮的结果计数（落日志的唯一来源）。
//
// 每个计数各自对应一个可行动的问题：
//   - hosts/skipped：「有几台被数到了、几台跳过」—— 跳过 = 这台没有快照（从未上报/
//     键被清）或快照读失败，不是失败，但数量突增就是设备侧的信号；
//   - dispatched/undelivered/createFailed：派发了几台、几台没送到（离线/未送达）、
//     几台的记录或消息没建起来（Redis 写失败/组装失败）；
//   - succeeded/failedResult：收到了几台的明确结论（战果在 detail 里）；
//   - unreported：窗口内没等到结论（agent 卡住/连接抖动/记录已过期）。
type transferCleanupStats struct {
	hosts        int
	skipped      int
	dispatched   int
	undelivered  int
	createFailed int
	succeeded    int
	failedResult int
	unreported   int
}

// Execute 执行一轮回收。参数为空（策略与窗口都是服务侧常量）。
func (t *DockerTransferCleanupTask) Execute(ctx context.Context, _ json.RawMessage) error {
	if t.hosts == nil || t.cmds == nil || t.sender == nil {
		return errors.New("docker-transfer-cleanup: 依赖未装配")
	}
	// 策略行的两个值直接取自唯一事实源（权限码 = 记录的可见性档；超时 = 记录档，
	// 与 agent 侧 writeTimeouts 逐行对齐）。缺行是**契约破裂**（dockerpolicy 的
	// 双向守卫就是为了不出现它）：宁可整轮不派发并报红，也不要拿零值超时去建记录。
	pol, ok := dockerpolicy.Lookup(dockerTransferCleanupAction)
	if !ok {
		return fmt.Errorf("docker-transfer-cleanup: 策略表缺 %s 行，指令未派发", dockerTransferCleanupAction)
	}
	ids, err := t.hosts.Hosts(ctx)
	if err != nil {
		// 整轮不可执行：枚举源读不到（Redis 抖动/未装配），这一轮什么都没发生 ——
		// 交给调度器的重试口径（默认 3 次），而不是记一句日志当作「做完了」。
		return fmt.Errorf("docker-transfer-cleanup: 枚举可管 docker 主机失败: %w", err)
	}
	// 升序：主机清单是 Redis 集合（无序），而日志要可复现地对照（哪台在哪个位置）。
	slices.Sort(ids)

	var st transferCleanupStats
	st.hosts = len(ids)
	pending := make(map[uint64]string, len(ids))
	for _, id := range ids {
		env, err := t.hosts.Get(ctx, id)
		if err != nil {
			// 单台快照读失败：跳过这一台（**不是**整轮失败）—— 读不到快照只说明
			// 「此刻拿不到这台的状态」，另外几十台照常。
			st.skipped++
			t.log.Warn("docker 上传产物回收：主机快照读取失败，跳过",
				zap.Uint64("deviceId", id), zap.Error(err))
			continue
		}
		if env == nil {
			// 从未上报过快照（或键被清）：这台是不是 docker 主机无从判断，跳过。
			// **判据是「有没有快照」而不是「docker_ok」** —— 本动作一个字节都不碰
			// daemon（agent 侧只是 ReadDir + unlink），而它要救的恰恰包括
			// 「daemon 挂了 + 盘被产物占满」这种局面：拿 daemon 健康当闸，会把最需要
			// 回收的那台挡在门外（事件流常驻订阅用 docker_ok 是因为它真的在订 daemon）。
			st.skipped++
			continue
		}
		ref := t.idGen()
		rec := &dockerstate.CmdRecord{
			Ref: ref, DeviceID: id, Action: dockerTransferCleanupAction,
			// UserID=0 = 无发起人（core 自己派的）：这批记录因此不入任务历史、
			// 不入审计（两处都按 UserID=0 剔除内部指令），只活在实时记录与日志里。
			// Perm 取策略行 = 谁能读到这条记录（与「谁能删这些东西」同一档）。
			Perm: pol.Perm, CreatedAt: t.now().UnixMilli(),
		}
		// 记录必须**先于**下发建立：agent 的执行是毫秒级的，result 可能在我们建好
		// 记录之前就回到 core —— 那时没有记录可回链，结果会被当「过期或伪造」丢掉。
		if err := t.cmds.Create(ctx, rec, pol.AcceptTimeout()); err != nil {
			st.createFailed++
			t.log.Warn("docker 上传产物回收：指令记录写入失败，该台跳过",
				zap.Uint64("deviceId", id), zap.Error(err))
			continue
		}
		msg, err := agentproto.NewMessage(ref, agentproto.TypeCoreDockerCmd, &agentproto.DockerCmd{
			Ref: ref, Action: dockerTransferCleanupAction,
		})
		if err != nil {
			st.createFailed++
			t.log.Warn("docker 上传产物回收：指令组装失败，该台跳过",
				zap.Uint64("deviceId", id), zap.Error(err))
			continue
		}
		if err := t.sender.SendToDevice(id, msg); err != nil {
			// 未送达（离线/连接抖动）：该台这一轮就到此为止，记录留给 docker sweep
			// 按超时收尾。**不返回错误**：一台离线不该让另外几十台的回收入口变成红。
			st.undelivered++
			t.log.Warn("docker 上传产物回收：指令未送达，该台跳过",
				zap.Uint64("deviceId", id), zap.String("ref", ref), zap.Error(err))
			continue
		}
		st.dispatched++
		pending[id] = ref
	}

	t.collect(ctx, pending, &st)
	t.log.Info("docker 上传产物回收完成",
		zap.Int("hosts", st.hosts), zap.Int("skipped", st.skipped),
		zap.Int("dispatched", st.dispatched), zap.Int("undelivered", st.undelivered),
		zap.Int("createFailed", st.createFailed),
		zap.Int("succeeded", st.succeeded), zap.Int("failedResult", st.failedResult),
		zap.Int("unreported", st.unreported))
	return nil
}

// collect 在有界窗口内把已派发的 ref 收成终态计数，并把每台的战果/失败原因写进日志。
//
// 窗口用 **context 超时**而不是挂钟比较：这里的「两分钟」是真实时间的上界（调度器
// worker 被占住的时长），不该跟着可注入的 now 走 —— 测试注入一个冻结的时钟时，
// 挂钟比较会让「等两分钟」变成「等一万年」（死循环），而 context 超时恒成立。
//
// 逐台的读失败**不判死**（留在 pending 里下一轮再试）：一次 Redis 抖动不该把一台
// 已经清完产物的主机报成失败。窗口到期时仍留在 pending 里的才算「未回报」。
func (t *DockerTransferCleanupTask) collect(ctx context.Context, pending map[uint64]string, st *transferCleanupStats) {
	if len(pending) == 0 {
		return
	}
	window, cancel := context.WithTimeout(ctx, t.wait)
	defer cancel()
	for len(pending) > 0 {
		for id, ref := range pending {
			rec, err := t.cmds.Get(ctx, ref)
			if err != nil {
				continue // 读失败：留到下一轮（不判死也不丢）
			}
			if rec == nil {
				// 记录已过期（终态记录的 TTL 是 10 分钟，窗口远短于它；走到这里说明
				// 记录被别的东西清掉了）：如实计未回报。
				st.unreported++
				delete(pending, id)
				continue
			}
			switch rec.Status {
			case dockerstate.StatusPending:
				continue // 还没回来：下一轮再看
			case dockerstate.StatusSucceeded:
				st.succeeded++
				// 战果原文（agent 的 `removed=… freed_bytes=…`）：这条日志是本任务
				// 唯一能回答「清了几个」的地方（见任务头注释）。
				t.log.Info("docker 上传产物回收：一台完成",
					zap.Uint64("deviceId", id), zap.String("detail", rec.Detail))
			default:
				// failed / timeout（超时是服务端推断的结论，agent 侧可能仍在跑）。
				st.failedResult++
				t.log.Warn("docker 上传产物回收：一台未成功",
					zap.Uint64("deviceId", id), zap.String("status", rec.Status),
					zap.String("error", rec.Error))
			}
			delete(pending, id)
		}
		if len(pending) == 0 {
			return
		}
		select {
		case <-window.Done():
			st.unreported += len(pending)
			return
		case <-time.After(t.poll):
		}
	}
}

// defaultRequestID 是 idGen 的降级实现（纳秒时间戳 + 包内自增序号）——与
// dockerevents.defaultRefGen 同算法同理由：生产的 ref 必须与用户指令共用同一个
// 计数器（service.NewRequestID），这里只为「未注入也能跑」的测试与降级留一份。
var requestIDSeq atomic.Uint64

func defaultRequestID() string {
	return strconv.FormatInt(time.Now().UnixNano(), 10) + strconv.FormatUint(requestIDSeq.Add(1), 10)
}
