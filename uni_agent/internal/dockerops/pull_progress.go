package dockerops

import (
	"context"

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
//   - 取消（core 下发 cancel → 会话收摊 → 拉取用的 ctx 被中断）：**不发**终态项与
//     eof（会话已消失，帧也发不出去），结论句「拉取已取消」—— 与「失败」分开措辞：
//     用户主动放弃不是故障，不该按故障句式弹红。
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
	// 拉取跑在**派发器的 ctx** 上（15 分钟时限照旧），cancel 的中断桥挂在会话的
	// closeUpstream：teardown 一关上游，这个 cancel 就把 SDK 的读打断 —— 会话取消
	// 与指令时限两条路都在，谁也不欠谁的上下文。
	pullCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sess.attachUpstream(func() error { cancel(); return nil })

	fr := newPullProgressFramer(sess)
	err = e.api.ImagePull(pullCtx, target, auth, fr.emit)
	if err != nil {
		if sess.isClosing() {
			// 会话已收摊 = 用户取消（或断连后的 cancel）：终态项与 eof 无从发出，
			// 结论句按「取消」措辞。
			return &ExecError{Msg: "拉取已取消"}
		}
		fr.finish(false, err.Error())
		waitStreamEnded(sess)
		return wrapDocker("拉取镜像失败", err)
	}
	fr.finish(true, "")
	waitStreamEnded(sess)
	return nil
}
