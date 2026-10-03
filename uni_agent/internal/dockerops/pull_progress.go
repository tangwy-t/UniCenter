package dockerops

import (
	"context"
	"strings"
	"sync/atomic"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── image:pull 的进度透出（4b）────────────────────────────────────────────
//
// 形态选型（为什么 pull **不改会话制**、也不改 result 形状）：
//   - pull 的既有语义 = 一条 15 分钟时限的写指令：受理 → 轮询 → 终态结论句
//     （「拉取镜像失败/成功」），todo 页面/在飞去重/确认档全部挂在「指令直到拉完
//     才是终态」上。改成 stats/exec 那种「受理即回会话句柄」会让老前端的轮询在
//     拉取还在跑时就看到终态 —— 结论句、409 在飞、结果载荷三处语义同时被破坏；
//   - 于是保持写指令形态，进度走**既有帧通道**：执行期间开一条 streamPull 会话
//     （不计用户槽位、豁免空闲超时），daemon 的进度行逐条折进会话；core 在受理
//     指令时就按「会话 id = pull_ + ref」预登记（两端同一派生函数，见协议
//     DockerPullSessionID），帧通道因此在指令**未终态**时就可消费 —— 这就是本切片
//     与时序有关的最微妙处，也是 id 不走随机、走派生的原因。
//   - eof 纪律：eof 帧**先于** result 发出（waitStreamEnded 钉住顺序），
//     消费端的长连接收口与轮询终态对齐；终端结果句（done/error）作为**恰一条、
//     恰在最后**的记录进帧流。
//
// 帧纪律照 sessions.go 的包注释：数据的搬运是「daemon 行 → 折叠器 → 会话缓冲」，
// 帧的发送与限速全在会话泵里 —— 本文件不碰协议帧。
//
// 折叠器的**公共实现**在 progress_framer.go（P2 起 push/build 共用同一套折叠纪律；
// 本文件只做拉取的编码接线 —— 4b 的折叠行为由既有测试逐条钉住，公共化未改拉取语义）。

// pullProgressFramer 把 daemon 的 pull 进度行折成协议 DockerPullProgressItem 的
// 折叠器：折叠与冲刷循环都在公共 progressFramer 里，本类型只补编码两个口
// （记录行 → encodePullItem；终态项 → done/error 词典）。
type pullProgressFramer struct {
	f *progressFramer
}

// newPullProgressFramer 构造折叠器并起冲刷循环。
func newPullProgressFramer(sess *streamSession) *pullProgressFramer {
	f := newProgressFramer(sess, progressFramerConfig{
		encode: func(l progressLine) []byte {
			return encodePullItem(agentproto.DockerPullProgressItem{
				T: sess.mgr.cfg.now().UnixMilli(), ID: l.ID, Status: l.Status,
				Current: l.Current, Total: l.Total,
			})
		},
		term: func(t int64, done bool, errMsg string) []byte {
			return encodePullItem(agentproto.DockerPullProgressItem{T: t, Done: done, Error: errMsg})
		},
		threshold: pullFrameBytes,
	})
	return &pullProgressFramer{f: f}
}

// emit 收一条 daemon 进度行（adapter 的同步回调，绝不阻塞）。
func (fr *pullProgressFramer) emit(p PullProgress) {
	fr.f.emit(progressLine{ID: p.ID, Status: p.Status, Current: p.Current, Total: p.Total})
}

// finish 请求终态（done/errMsg 恰其一）：残余批与终态项一并发走再 mark eof ——
// 终态项**恰一条、恰在最后**（协议契约），错误文本是 daemon 原文（错误行由
// adapter 折成返回错误，到这里原样入帧；页面看结论句，帧里留原文）。
func (fr *pullProgressFramer) finish(done bool, errMsg string) { fr.f.finish(done, errMsg) }

// imageAuthOf 把指令里的仓库认证折成 adapter 的本地形态（4c，P2 起 pull 与 push
// 共用）：auth 缺席（公共仓库/主机侧 docker login）即 nil —— 与 4b 之前逐字一致。
// 协议已经校验过 auth 只能出现在 image:pull/image:push 上、且 registry 与
// options.registry 同键、三字段非空并合尺寸闸（见协议 DockerCmd.Validate），
// 这里只做翻译不重复校验。
func imageAuthOf(cmd *agentproto.DockerCmd) *ImageAuth {
	if cmd.Auth == nil {
		return nil
	}
	return &ImageAuth{Registry: cmd.Auth.Registry, Username: cmd.Auth.Username, Password: cmd.Auth.Password}
}

// pullImage 是 image:pull 的写执行路径（4b）：进度透出 + 既有语义逐字保留。
// auth（4c）是 core 随指令注入的仓库认证（nil = 无凭据，与 4b 逐字一致）——
// 透传给 adapter，本层不做任何映射（SDK 类型不出 adapter）。
//
// 三个结局：
//   - 成功：终态项 Done → eof → result{ok}（结论句按老流程由前端说法，detail 无附注）；
//   - daemon 失败：终态项 Error（daemon 原文）→ eof → 结论句「拉取镜像失败」；
//   - 取消（core 显式取消端点 → cancel 帧 → 会话收摊 → 拉取用的 ctx 被中断）：
//     **不发**终态项与 eof（会话已消失，帧也发不出去），结论句「拉取已取消」——
//     与「失败」分开措辞：用户主动放弃不是故障，不该按故障句式弹红。
//
// **取消/截止与完成的竞态（QA B4，2026 收口版）**：cancel 只由 core 的**显式取消
// 端点**下发（POST /docker/hosts/:id/cmds/:ref/cancel → CancelProgress → cancel 帧 →
// agent 会话收摊），观看端断流（切页、关对话框、收起任务行）只停观看、不再触发。
// 它到达 agent 的时刻与 daemon 干完活的时刻可以只差毫秒。此时「ctx 被中断 → 记取消」
// 会把一场**已经成功**的拉取记成「失败·拉取已取消」（实测：任务中心显示失败，而
// `docker images` 里镜像已落地）—— 终态必须按**拉取的实际结局**结算：
//   - 已完成的证据一：daemon 的收尾行（isPullCompletionLine）已经到过我们的读循环；
//   - 已完成的证据二：拉取前后本机镜像 ID 变了（refMoved）—— 收尾行可能随中断的
//     连接一起被丢掉（ctx 取消直接关连接，缓冲里还没读到的尾数据不复存在），
//     故需要一条**独立于流**的事实补判；
//   - 两条都没有 → 拉取真被截止，才记「拉取已取消」。
//
// 证据二为什么是「ID 变了」而不是「现在有镜像」：本机本来就有的镜像，在取消后
// 依然在，单看「有」会把「重拉被截止」也说成成功（那是反向的不诚实）。
//
// 迟到 cancel 对已完成的拉取是 no-op：终态按完成结算 —— 用户放弃的是「等待」，
// 不是「结果」。**真截止（指令的执行时限到点）同此一条**：时限记的是「我们不再等」，
// 不是「它没干完」，活真干完了就该记完成（收口动作见 completion.go 的 settleWrite，
// 三族同一张表）。
func (e *WriteExecutor) pullImage(ctx context.Context, cmdRef, target string, auth *ImageAuth) error {
	// 会话管理器由构造契约保证非 nil（见 SetSessions / sessionsValue 的说明 ——
	// 7c 删掉了「未装配 = 一期黑盒」的回退分支）。
	sessions := e.sessionsValue()
	// 会话句柄由 ref **派生**（协议 DockerPullSessionID）而不是随机：core 在受理
	// 指令时就用同一个派生函数预登记了会话 —— 两端不在一把句柄上会话，帧就找不到
	// 主人。派生形态的保密度论证见 sessions.go 的 openNamed 与协议注释。
	sess, err := sessions.openNamed(streamPull, agentproto.DockerPullSessionID(cmdRef))
	if err != nil {
		// 拉取进度不占用户槽位，本分支只可能因句柄非法 —— 折叠成结论句而不是
		// 静默丢掉进度（进度是承诺的可见性，静默丢了等于没做）。
		return &ExecError{Msg: err.Error()}
	}
	// 拉取前的本机基线（证据二的对照项）：走**指令的 ctx** 而不是 pullCtx ——
	// 它必须在会话可能收摊之前拿到，且查询失败只是让证据二失效（beforeErr != nil），
	// 不影响拉取本身。
	beforeID, beforeErr := e.api.ImageRefID(ctx, target)
	// 拉取跑在**派发器的 ctx** 上（15 分钟时限照旧），cancel 的中断桥挂在会话的
	// closeUpstream：teardown 一关上游，这个 cancel 就把 SDK 的读打断 —— 会话取消
	// 与指令时限两条路都在，谁也不欠谁的上下文。
	pullCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sess.attachUpstream(func() error { cancel(); return nil })

	fr := newPullProgressFramer(sess)
	// 收尾行标记：emit 是 adapter 的**同步**回调（见 progressFramer.emit 的契约），
	// 故这里只需在 ImagePull 返回后读一个布尔。用 atomic 而不是裸 bool 是防守：
	// 将来若有 adapter 从另一个协程回调，这里也不会变成一个 race。
	var daemonDone atomic.Bool
	err = e.api.ImagePull(pullCtx, target, auth, func(p PullProgress) {
		if isPullCompletionLine(p) {
			daemonDone.Store(true)
		}
		fr.emit(p)
	})
	if err != nil {
		// 结算：证据一（收尾行）或证据二（本机引用位移）任一成立即完成 —— 迟到
		// cancel 与真截止对已完成的拉取都是 no-op。判据走自己的上下文
		//（真截止会把指令 ctx 一起作废，见 evidenceCtx）。
		ectx, ecancel := evidenceCtx(ctx)
		defer ecancel()
		return settleWrite(daemonDone.Load() || e.refMoved(ectx, target, beforeID, beforeErr),
			sess.isClosing(), fr, sess, "拉取已取消", "拉取镜像失败", err)
	}
	fr.finish(true, "")
	waitStreamEnded(sess)
	return nil
}

// isPullCompletionLine 报告一条 daemon 进度行是不是**拉取的收尾行** —— 也就是
// 「这场拉取的活已经干完了」的直接证据。
//
// daemon 只在两处写这句话，且都在整场拉取的最后一步（distribution/pull_v2.go 的
// writeStatus）："Status: Downloaded newer image for X"（有层落地）与
// "Status: Image is up to date for X"（引用已是最新，无需下载）。它们都是**消息行**
// （无层 id），并且出现在镜像已写进本地存储、引用已更新之后 —— 与 docker CLI 打印的
// 最后一行同源。
//
// 为什么必须按行判定而不是「读循环结束」：读循环结束的两种形态（正常 EOF 与会话
// 收摊导致的读中断）在这一层看起来都是「ImagePull 返回了」，只有这些行能把
// 「干完了」与「被截断」分开。也刻意不采 "Digest: …"（它在引用更新之前就发出，
// 那一小段窗口内的中断由 refMoved 兜底）。
func isPullCompletionLine(p PullProgress) bool {
	if p.ID != "" {
		return false
	}
	return strings.HasPrefix(p.Status, "Status: ")
}
