package dockerops

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/distribution/reference"
)

// ── 完成证据框架（pull / build / push 三族共用的结算底座）────────────────────
//
// 三族的终态结算共用**一条纪律**：以「活干完了没有」的实际事实结算，不以「我们的
// ctx 被中断了没有」结算 —— 迟到 cancel（用户经显式取消端点下发：core 端点 →
// cancel 帧 → 会话收摊）与真截止（指令的执行时限到点）对一场**已经完成**的活都是
// no-op：用户放弃的是「等待」，不是「结果」；时限记的是「我们不再等」，不是
// 「它没干完」。反过来，
// 证据不成立时也绝不编一条完成（误报成功比漏救更糟：任务中心说成功而产物不在，
// 是系统在撒谎）。
//
// 事实分两类，互为补位（各自都可能单独开不了口）：
//   - 证据一（流内）：daemon 的收尾行。直接、最快，但**会随被中断的连接一起丢**
//     —— ctx 取消直接关连接，缓冲里还没读到的尾数据不复存在。各族形态见
//     isPullCompletionLine / isPushCompletionLine / buildFacts.note；
//   - 证据二/三（流外）：不依赖流的事实。各族不同（本机引用位移、本机 canonical
//     引用增加、registry 侧清单摘要变化、tag 事件），见各族函数头。
//
// 本文件只放三族共用的三件原语：结算收口（settleWrite）、本机引用位移对照
// （refMoved / refPointsAt）、镜像 ID 的形态比对（imageIDMatches）。

// progressFinisher 是「把终态项写进流」的窄接口（三族折叠器都满足）。
//
// 为什么是接口而不是具体类型：pull 的折叠器与 build/push 共用的 progressFramer
// 是两个类型（历史分层：4b 的折叠器先落地，P2 才把折叠纪律公共化），但收口动作
// 完全同形 —— 结算只关心「能不能补一条终态项」，不关心谁实现的。
type progressFinisher interface {
	finish(done bool, errMsg string)
}

// settleWrite 收口一场带进度流的写操作（三族共用**唯一**的结算表）：
//
//	完成证据成立               → 成功（nil）；会话还在就补一条终态 Done 项 + eof
//	证据不成立 + 会话已收摊     → 该族的**取消**措辞（用户放弃不是故障）
//	证据不成立 + 会话还在       → 该族的**失败**措辞 + daemon 原文进 detail
//
// 「会话还在」这一档为什么要判证据：真截止（执行时限到点）与 daemon 报错都会以
// 非 nil 的 err 收场，而活可能恰在那之前干完了 —— 此时结论句若写「失败」，页面
// 会说一件与事实相反的事（产物都在，却显示失败）。三条分支的措辞与帧纪律与各族
// 既有实现逐字一致（本函数只是把它们从三份重复收敛成一份）。
func settleWrite(done, closing bool, fr progressFinisher, sess *streamSession,
	cancelMsg, failMsg string, err error) error {
	if done {
		if !closing {
			// 会话还在：把完成事实补进流（终态项恰一条、eof 先于 result —— 时序
			// 纪律照旧）。会话已消失时无从发出，只回结论句。
			fr.finish(true, "")
			waitStreamEnded(sess)
		}
		return nil
	}
	if closing {
		// 会话已收摊 = 显式取消端点的 cancel 帧已到（core 端点 → cancel 帧 → 会话
		// 收摊），且完成证据不成立。
		return &ExecError{Msg: cancelMsg}
	}
	fr.finish(false, err.Error())
	waitStreamEnded(sess)
	return wrapDocker(failMsg, err)
}

// settlementQueryTimeout 是结算判据查询的**自身时限**（见 evidenceCtx）。
//
// 取值考虑：判据是几次本机 daemon 查询（ImageRefID / RepoDigests / tag 事件回放）
// 外加至多一趟 registry 探测（后者另有 registryProbeTimeout 兜着）—— 正常在毫秒级；
// 这个上限只为「daemon 卡死时结算仍必须收场」而设，不参与正常时序。
const settlementQueryTimeout = 20 * time.Second

// evidenceCtx 给结算判据一个**不受指令 ctx 影响**的查询上下文。
//
// 为什么需要它（本机真机实测踩到）：**真截止**（指令的执行时限到点）会把指令 ctx
// 一起置为过期 —— 而结算恰恰发生在那一刻：判据的每一次查询都会立刻失败，证据整场
// 开不了口，一场「时限到点时其实已经干完」的活被记成失败（与迟到 cancel 那类
// 不诚实正好反向）。WithoutCancel 摘掉取消信号（保留 ctx 上的值），再给判据自己的
// 时限：结算判据是**事后取证**，它要回答的正是「刚才那场活干完了没有」，不能因为
// 「我们不等了」而放弃。
func evidenceCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), settlementQueryTimeout)
}

// refMoved 报告「这场活把 ref 落到了本机一个新镜像上」这条**独立于流**的事实：
// 前后同一个引用的本机镜像 ID 变了（此前没有 = 空串，现在有了，也算变）。
//
// pull（镜像落地）与 build（产物打上目标 tag）共用这一条判据 —— 两处语义相同：
// 引用现在指向的镜像不是开工前那一个。push 不用它（推的是本机镜像，落的笔在
// registry 那侧，本机可能毫无位移）。
//
// 两处刻意不判：开工前查不了（beforeErr != nil）或现在查不了 —— 没有对照基线时
// 「现在有这个引用」无法区分「这场活落的」与「本来就有的」，宁可开不了口
// （落回「取消」），也不编一条完成。
func (e *WriteExecutor) refMoved(ctx context.Context, ref, beforeID string, beforeErr error) bool {
	if beforeErr != nil {
		return false
	}
	afterID, err := e.api.ImageRefID(ctx, ref)
	if err != nil {
		return false
	}
	return afterID != "" && afterID != beforeID
}

// refPointsAt 报告 ref 现在是不是指向 id（同一个产物）。查询失败或引用不存在一律
// false —— 这是「证明弱于事实」的判据，开不了口就不能开口。
func (e *WriteExecutor) refPointsAt(ctx context.Context, ref, id string) bool {
	now, err := e.api.ImageRefID(ctx, ref)
	if err != nil {
		return false
	}
	return now != "" && imageIDMatches(now, id)
}

// imageIDMatches 报告两个镜像 ID 指的是不是同一个产物。
//
// 归一的两件事：
//   - "sha256:" 前缀可有可无（daemon 各处给的形态不统一）；
//   - legacy builder 的 "Successfully built <id>" 行只给 **12 位截断**形态
//     （本机真流实测），aux 行给的是完整 ID —— 短的是长的前缀即同一产物。
//     少于 12 位的短串不认（不是 docker 的 ID 形态，宁可判不匹配）。
func imageIDMatches(a, b string) bool {
	a = strings.TrimPrefix(a, "sha256:")
	b = strings.TrimPrefix(b, "sha256:")
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	short, long := a, b
	if len(short) > len(long) {
		short, long = long, short
	}
	return len(short) >= 12 && strings.HasPrefix(long, short)
}

// sameTagRef 报告 daemon 打印的引用名与协议里的 target 是不是同一个 tag。
//
// 两种写法都要认：daemon 的收尾行/事件里的 name 是它自己打印的引用（"app:1"），
// 而协议 target 可能是补全形态（"docker.io/library/app:1"）或反过来 —— 统一走
// docker 的引用归一化，而不是字符串比较。
func sameTagRef(printed, target string) bool {
	if printed == target {
		return true
	}
	a, err1 := referenceParseNormalized(printed)
	b, err2 := referenceParseNormalized(target)
	return err1 == nil && err2 == nil && a == b
}

// referenceParseNormalized 把引用折成 docker 的归一形态（形态比对用）。
func referenceParseNormalized(s string) (string, error) {
	named, err := reference.ParseNormalizedNamed(s)
	if err != nil {
		return "", err
	}
	return named.String(), nil
}

// buildFacts 是构建期间从流里采到的**完成读数**（emit 回调逐条喂，结算时一次读）。
//
// 为什么不用 atomic：emit 是 adapter 的**同步**回调（progressFramer.emit 的契约），
// 但这里仍上锁 —— 与 pull/push 的 atomic 标记同一取向：将来若有 adapter 从另一个
// 协程回调，读数不会变成一个 race。
type buildFacts struct {
	mu sync.Mutex
	// builtID 是本场构建**产物**的镜像 ID：aux 行给的完整形态优先，
	// "Successfully built <12位>" 兜底（aux 先到，见 adapter.consumeBuildStream）。
	builtID string
	// tagged 表示 legacy builder 的 "Successfully tagged <target>" 行到过读循环
	// —— 它是整条构建流的最后一行（本机真流实测），到过即 daemon 把 tag 落完了。
	tagged bool
}

// note 喂一条构建进度记录（执行器的 emit 回调里逐条调用）。
func (f *buildFacts) note(b BuildProgress, target string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if b.ImageID != "" {
		// aux 行：完整产物 ID（两代 builder 都在成功收口时发，见 api.go 的
		// BuildProgress.ImageID）。
		f.builtID = b.ImageID
		return
	}
	if b.Stream == "" {
		return
	}
	line := strings.TrimSpace(b.Stream)
	if id, ok := strings.CutPrefix(line, "Successfully built "); ok {
		if f.builtID == "" {
			f.builtID = strings.TrimSpace(id) // 12 位截断形态：比对时按前缀
		}
		return
	}
	if tag, ok := strings.CutPrefix(line, "Successfully tagged "); ok {
		if sameTagRef(strings.TrimSpace(tag), target) {
			f.tagged = true
		}
	}
}

// read 取一次读数（结算口）。
func (f *buildFacts) read() (builtID string, tagged bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.builtID, f.tagged
}

// pushFacts 是推送期间从流里采到的完成事实：**按 tag 记名**的收尾行集合。
//
// 为什么按 tag 记名而不是一个布尔：不带 tag 的推送会逐个 tag 发收尾行（daemon 的
// all=1 语义），「第一行到了」只说明**那一个** tag 落了 registry —— 把首行当整场
// 完成正是要根除的边界（见 build_push.go 的 pushImage）。
type pushFacts struct {
	mu sync.Mutex
	// byTag 是已收到收尾行的 tag 名（daemon 的行前缀就是 tag 本身）。
	byTag map[string]bool
}

// note 喂一条推送进度行（执行器的 emit 回调里逐条调用）。
func (f *pushFacts) note(p PullProgress) {
	tag, ok := pushLineTag(p)
	if !ok {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.byTag == nil {
		f.byTag = map[string]bool{}
	}
	f.byTag[tag] = true
}

// tags 取已收到收尾行的 tag 集合（结算口；返回拷贝，调用方随便改）。
func (f *pushFacts) tags() map[string]bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]bool, len(f.byTag))
	for t := range f.byTag {
		out[t] = true
	}
	return out
}

// pushLineTag 解析推送收尾行里的 tag 名 —— isPushCompletionLine 的**带值**版本：
// 收尾行形如 "<tag>: digest: sha256:… size: N"，前缀就是 daemon 正在提交的那个 tag
// （两代 daemon 的源码同形，见 isPushCompletionLine 的依据）。
func pushLineTag(p PullProgress) (string, bool) {
	if p.ID != "" { // 带层 id 的行整场都在发，不能作完成判据
		return "", false
	}
	i := strings.Index(p.Status, pushDigestMarker)
	if i <= 0 {
		return "", false
	}
	return p.Status[:i], true
}

// tagEventWindowSlack 是 tag 事件回放窗口的**两端放宽量**：since 往前、until 往后
// 各放宽这么多。理由：窗口是「我方墙钟」与「daemon 事件时刻」两个时钟的比对
// （daemon 可能在另一台机器上），放宽是容忍时钟差；放宽的代价只是窗口里多几条
// 无关事件 —— 归属由「tag 名 + 事件指向的镜像 ID == 该 tag 现在的镜像 ID」双重
// 钉住（见 tagWrittenDuring），多收的噪声落不了判据。
const tagEventWindowSlack = 2 * time.Second

// tagWrittenDuring 报告「这个 tag 在 [started, 现在] 这个窗口里被（重新）打过」
// 这条独立于流的事实（daemon 的 image/tag 事件）。
//
// 为什么构建结算需要它：构建**全缓存命中**时产物 ID 与开工前逐字节相同（tag 此前
// 就指向同一个镜像），本机引用没有任何位移；若 daemon 的收尾行又随被中断的连接
// 丢了，就只剩这条事件能证明「tag 落地这一步真跑过」。实测（29.6.0，classic 与
// containerd 两种存储、legacy 与 BuildKit 两代 builder）：缓存命中的重打 tag
// **也会**发这条事件 —— 「产物逐字节无变化」与「tag 写入步骤没跑」在事件面上
// 是可区分的。
//
// 归属为什么要求 ActorID == 该 tag 现在的镜像 ID：事件只说「这个窗口里有人写过
// 这个 tag 名」，没有「谁写的」。加上「写进去的就是现在这个 tag 指向的镜像」，
// 就把「无关的 tag 名重打」（写进了别的镜像）挡在外面 —— 剩下的误判面只有
// 「别人在同一个窗口把同一个 tag 重打到同一个镜像」，那是与本场同样的结果。
//
// 读不到事件（回放失败/缓冲已淘汰）→ false：判据开不了口，不影响别的证据。
func (e *WriteExecutor) tagWrittenDuring(ctx context.Context, target string, started time.Time) bool {
	nowID, err := e.api.ImageRefID(ctx, target)
	if err != nil || nowID == "" {
		return false
	}
	events, err := e.api.ImageTagEvents(ctx,
		started.Add(-tagEventWindowSlack), time.Now().Add(tagEventWindowSlack))
	if err != nil {
		return false
	}
	for _, ev := range events {
		if !sameTagRef(ev.ActorName, target) {
			continue
		}
		if imageIDMatches(ev.ActorID, nowID) {
			return true
		}
	}
	return false
}
