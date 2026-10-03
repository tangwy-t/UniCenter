package dockerops

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── image:build / image:push 的写执行路径（P2·分发闭环）────────────────────
//
// 选型照 4b 的 pullImage 逐条对齐（派生会话 + 折叠器 + eof ≤ result + 取消三分支
// 措辞），差异只在本文件注释点名的地方：
//   - 会话句柄由各自前缀派生（build_<ref> / push_<ref>，协议
//     DockerBuildSessionID / DockerPushSessionID），core 受理时按同一函数预登记；
//   - build 的**输入面**是 transferDir 里的上下文 tar：执行器只做「白名单文件名 →
//     目录内真实路径」的定型（复用 load 侧 transferReadPath 的符号链接纪律），
//     穿越/尺寸/条目/Dockerfile 存在性校验在 adapter 的 scanBuildContext ——
//     「检查与送出同一份字节」；
//   - push 复用 4c 的凭据面（imageAuthOf 与 pull 同一函数、同一 ImageAuth 类型）；
//   - **取消与完成的竞态照 pull 的 B4 修复扩面**（本波）：cancel 是 core 在「用户断开
//     进度流」时下发的 best-effort 动作，它到达 agent 的时刻与 daemon 干完活的时刻可以
//     只差毫秒 —— 终态必须按**构建/推送的实际结局**结算，迟到 cancel 不得把一场已经
//     完成的活改写成「已取消」。两族的完成证据见 buildImage / pushImage 的函数头
//     （pull 的三分支措辞与本文件的结算逻辑同一条纪律，实现各自持有、互不调用：
//     pull 的那份已验收，按「改动面最小」不动它）。

// newBuildProgressFramer 构造构建进度折叠器：记录行编码成 DockerBuildProgressItem
// （多一个 Stream 文本行），终态项词典与 pull 同款。折叠/去重/终态时序全是公共
// progressFramer 的（progress_framer.go —— 4b 的折叠纪律一份实现三家共用）。
func newBuildProgressFramer(sess *streamSession) *progressFramer {
	return newProgressFramer(sess, progressFramerConfig{
		encode: func(l progressLine) []byte {
			return encodeBuildItem(agentproto.DockerBuildProgressItem{
				T: sess.mgr.cfg.now().UnixMilli(), ID: l.ID, Status: l.Status, Stream: l.Stream,
			})
		},
		term: func(t int64, done bool, errMsg string) []byte {
			return encodeBuildItem(agentproto.DockerBuildProgressItem{T: t, Done: done, Error: errMsg})
		},
		threshold: pullFrameBytes,
	})
}

// newPushProgressFramer 构造推送进度折叠器：记录行编码成 DockerPushProgressItem
// （与 pull 同形的字段集 —— daemon 的 push 与 pull 是同一个 JSON 进度流）。
func newPushProgressFramer(sess *streamSession) *progressFramer {
	return newProgressFramer(sess, progressFramerConfig{
		encode: func(l progressLine) []byte {
			return encodePushItem(agentproto.DockerPushProgressItem{
				T: sess.mgr.cfg.now().UnixMilli(), ID: l.ID, Status: l.Status,
				Current: l.Current, Total: l.Total,
			})
		},
		term: func(t int64, done bool, errMsg string) []byte {
			return encodePushItem(agentproto.DockerPushProgressItem{T: t, Done: done, Error: errMsg})
		},
		threshold: pullFrameBytes,
	})
}

// buildContextPath 把「上下文 tar 文件名」定型成目录内的真实路径。
//
// 与 save/load 的 transferPath 同一纪律的两段：白名单文件名（协议
// IsDockerBuildContextFilename 之外再挡一次 filepath.Base 与反斜杠 —— 协议可被
// 伪造，agent 是最后一道）+ load 侧同款的符号链接解析与目录圈定
// （transferReadPath：EvalSymlinks 后必须仍在 transferDir 内，防符号链接逃逸）。
// 目录不可用时的 /tmp 回退与附注也照 load 的既有行为。
func (e *WriteExecutor) buildContextPath(filename string) (string, error) {
	if !agentproto.IsDockerBuildContextFilename(filename) ||
		filepath.Base(filename) != filename || strings.ContainsRune(filename, '\\') {
		return "", &ExecError{Msg: "构建上下文文件名不合法（只能填文件名，不能带路径）"}
	}
	return e.transferReadPath(e.transferDirValue(), filename)
}

// buildSpecOf 把指令 options 折成 adapter 的定型参数：dockerfile 缺省补
// "Dockerfile"（docker build 的缺省），tag 走 defaultImageTag 补 :latest
// （与 image:tag 同一行为），args 原样交出（协议已做键值白名单）。
func buildSpecOf(ctxPath string, o *agentproto.DockerCmdOptions) BuildSpec {
	spec := BuildSpec{
		ContextPath: ctxPath,
		Dockerfile:  o.Dockerfile,
		Tag:         defaultImageTag(o.Tag),
		BuildArgs:   o.Args,
	}
	if spec.Dockerfile == "" {
		spec.Dockerfile = "Dockerfile"
	}
	return spec
}

// buildImage 是 image:build 的写执行路径（P2）。三个结局的措辞照 pullImage：
// 成功 → 终态 Done；daemon 失败（含上下文校验拒绝 —— 具体原因进 detail）→
// 终态 Error + 结论句「构建镜像失败」；取消 → 不发终态项与 eof、结论句「构建已取消」。
//
// **取消与完成的竞态（照 pull 的 B4 修复扩面）**：用户切页/关对话框 → core 下发
// cancel → 会话收摊、buildCtx 被中断，而 daemon 可能恰在同时把产物打上目标 tag ——
// 旧实现按「ctx 被中断」记「构建已取消」，把一场**已经建成**的构建记成失败。结算
// 按构建的**实际结局**走：
//   - 已完成的证据：目标 tag 的本机镜像 ID 变了（构建前后各问一次 ImageRefID；产物
//     从无到有 = 空串 → 非空，也算变）—— 这是**独立于流**的事实，正是「构建产物
//     已经存在」这件事本身；
//   - 证据不成立（ID 没变 / 构建前查不了）→ 落回「构建已取消」。
//
// 为什么构建的判据不做「收尾行」那一半（pull 与推送都有）：构建流的完成文本随
// builder 代际而变（legacy 的 "Successfully built/tagged" 行与 BuildKit 的步骤行
// 是两套形态），按文本判会把判据绑在 builder 上；而**产物存在**是两代 builder 同一件
// 事实 —— 只有它值得当判据（BuildKit 末尾的 aux 行也因此刻意不解析，见
// consumeBuildStream 的说明）。
//
// 已知的保守边界：构建**全命中缓存**时产物 ID 与基线相同（tag 此前就指向同一镜像，
// 构建确实完成了却没有留下可对照的变化）—— 判据开不了口，落回「取消」（与 pull
// 「重拉已有镜像、ID 没变」同一条纪律：宁可漏救，不编一条完成 —— 反向的不诚实更糟）。
func (e *WriteExecutor) buildImage(ctx context.Context, cmdRef string, o *agentproto.DockerCmdOptions) error {
	path, err := e.buildContextPath(o.Context)
	if err != nil {
		return err
	}
	spec := buildSpecOf(path, o)
	// 会话管理器由构造契约保证非 nil（见 write.go 的 SetSessions —— 7c 删掉了
	// 「未装配 = 黑盒」的回退分支，pull/build/push 三族同一契约）。
	sessions := e.sessionsValue()
	sess, err := sessions.openNamed(streamBuild, agentproto.DockerBuildSessionID(cmdRef))
	if err != nil {
		return &ExecError{Msg: err.Error()}
	}
	// 构建前的目标 tag 基线（完成判据的对照项）：走**指令的 ctx** 而不是 buildCtx ——
	// 它必须在会话可能收摊之前拿到，且查询失败只是让判据失效（beforeErr != nil），
	// 不影响构建本身（与 pullImage 的拉取前基线同一时序理由）。
	beforeID, beforeErr := e.api.ImageRefID(ctx, spec.Tag)
	buildCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sess.attachUpstream(func() error { cancel(); return nil })

	fr := newBuildProgressFramer(sess)
	err = e.api.ImageBuild(buildCtx, spec, func(b BuildProgress) {
		fr.emit(progressLine{ID: b.ID, Status: b.Status, Stream: b.Stream})
	})
	if err != nil {
		if sess.isClosing() {
			// 会话已收摊 = 用户取消（或断连后的 cancel）。先按**已完成**的证据结算
			// （见函数头），成立即按成功返回 —— 迟到 cancel 对已完成的构建是 no-op。
			if e.imageRefLanded(ctx, spec.Tag, beforeID, beforeErr) {
				return nil
			}
			// 终态项与 eof 无从发出（会话已消失），结论句按「取消」措辞。
			return &ExecError{Msg: "构建已取消"}
		}
		fr.finish(false, err.Error())
		waitStreamEnded(sess)
		// 上下文校验的拒绝与 daemon 失败同走结论句 + detail：结论句说「构建失败」、
		// detail 带具体原因（穿越条目 / 缺 Dockerfile / 超上限），排障一眼可查。
		return wrapDocker("构建镜像失败", err)
	}
	fr.finish(true, "")
	waitStreamEnded(sess)
	return nil
}

// pushImage 是 image:push 的写执行路径（P2）。与 pullImage 的差别本来只有会话族与
// 结论句措辞（推送/拉取互不相混）；凭据注入与 pull 完全同源（imageAuthOf）。
//
// **取消与完成的竞态（照 pull 的 B4 修复扩面）**：结算必须按推送的**实际结局**走，
// 迟到 cancel 对已完成的推送是 no-op。推送与拉取的关键差别是「完成了没有」在**本机
// 不留位移**（推的是本机镜像，落的笔在 registry 那侧），故证据链两半分别是：
//   - 证据一（直接）：daemon 的收尾行（isPushCompletionLine，即 "<tag>: digest: …"）
//     已经到过我们的读循环 —— 它只在**清单提交进 registry 之后**发出（两代 daemon
//     的源码依据见 isPushCompletionLine）；
//   - 证据二（独立于流）：本机给这个镜像记的 canonical 引用多了一条（推送前后各问
//     一次 ImageRepoDigests）—— daemon 把清单交出去之后在本机落的 push 痕迹
//     （addDigestReference → RepoDigests），连接被中断也带不走它；
//   - 两条都没有 → 推送真被截止，才记「推送已取消」。
//
// 证据二的口径边界：它依赖镜像存储实现 —— classic 存储（生产 26.1.x）在推送成功后
// 落这笔 digest 引用；containerd 存储的 RepoDigests 由本地 tag **合成**（推送不改变
// 它），在那里这条判据恒不开火（保守方向：宁可漏救，也不编一条完成）。详见
// DockerAPI.ImageRepoDigests 的契约注释与代码引用。
func (e *WriteExecutor) pushImage(ctx context.Context, cmdRef, target string, auth *ImageAuth) error {
	// 会话管理器由构造契约保证非 nil（同 buildImage —— 三族共用 SetSessions 契约）。
	sessions := e.sessionsValue()
	sess, err := sessions.openNamed(streamPush, agentproto.DockerPushSessionID(cmdRef))
	if err != nil {
		return &ExecError{Msg: err.Error()}
	}
	// 推送前的本机 digest 基线（证据二的对照项）：与 pullImage 的拉取前基线同一时序
	// 理由 —— 走指令的 ctx，必须在会话可能收摊之前拿到；查询失败只是让证据二失效
	// （beforeErr != nil），不影响推送本身。
	beforeDigests, beforeErr := e.api.ImageRepoDigests(ctx, target)
	pushCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sess.attachUpstream(func() error { cancel(); return nil })

	fr := newPushProgressFramer(sess)
	// 收尾行标记：与 pullImage 同款（emit 是 adapter 的同步回调，用 atomic 防守
	// 「将来若有 adapter 从另一个协程回调」的形态）。
	var daemonDone atomic.Bool
	err = e.api.ImagePush(pushCtx, target, auth, func(p PullProgress) {
		if isPushCompletionLine(p) {
			daemonDone.Store(true)
		}
		fr.emit(progressLine{ID: p.ID, Status: p.Status, Current: p.Current, Total: p.Total})
	})
	if err != nil {
		if sess.isClosing() {
			// 会话已收摊 = 用户取消（或断连后的 cancel）。先按**已完成**的两种证据
			// 结算（见函数头），都没有才落到「取消」。
			if daemonDone.Load() || e.pushLanded(ctx, target, beforeDigests, beforeErr) {
				return nil
			}
			// 终态项与 eof 无从发出（会话已消失），结论句按「取消」措辞。
			return &ExecError{Msg: "推送已取消"}
		}
		fr.finish(false, err.Error())
		waitStreamEnded(sess)
		return wrapDocker("推送镜像失败", err)
	}
	fr.finish(true, "")
	waitStreamEnded(sess)
	return nil
}

// isPushCompletionLine 报告一条 daemon 进度行是不是**推送的收尾行** —— 也就是
// 「这场推送的活已经干完了（清单已带 tag 提交进 registry）」的直接证据。
//
// 形态与依据（两代 daemon 的源码同形，都是无层 id 的消息行 + "<tag>: digest: … size: …"）：
//   - classic 存储（graphdriver）：distribution/push_v2.go 的 pushTag —— 层上传完之后
//     才 `manSvc.Put(清单, WithTag(tag))`（**真正把镜像带 tag 写进 registry 的那一步**），
//     Put 成功返回之后才 progress.Messagef(out, "", "%s: digest: %s size: %d", …)；
//   - containerd 存储：daemon/containerd/image_push.go —— remotes.PushContent 成功后，
//     在「只在成功才发这句话」的 defer 里 progress.Messagef(out, "", "%s: digest: %s
//     size: %d", tagged.Tag(), …)。
//
// 两处都是 `ref.Tag()`/`tagged.Tag()` 前缀 + 小写 "digest:"，即形如
// "latest: digest: sha256:… size: 1234"。为什么判据取这个小写骨架而不是别的：
//   - pull 的 "Digest: sha256:…" 行是大写 D 且没有 "<tag>: " 前缀，不会误命中；
//   - 带 id 的层行（"Pushed" / "Layer already exists" / "Preparing" 等）整场都在发，
//     不能作完成判据 —— 只有这一句落在「清单已提交」之后；
//   - 引用名里不可能出现 ": digest: " 这样的「冒号+空格」组合，故这个骨架只可能
//     来自上面两处 Messagef。
//
// 为什么不采 aux（PushResult{Tag,Digest,Size}）：它同样是清单提交之后才发，但它是
// 另一个键（aux 而非 status），本域的 JSON 行结构刻意不解析它；用形态稳定的 digest
// 消息行，判据与帧面（进度流）同源同一份数据。
//
// 已知边界：target 不带 tag 时（协议允许的形态）daemon 会推该仓库的**全部本地 tag**、
// 逐个发 digest 行 —— 此时第一行到达即判完成，若后续 tag 的推送被取消，结论会偏
// 「成功」（多 tag 场景可能只兑现了一部分）。带 tag 的推送（页面的常态形态）没有这个
// 歧义。
func isPushCompletionLine(p PullProgress) bool {
	if p.ID != "" {
		return false
	}
	return strings.Contains(p.Status, ": digest: ")
}

// imageRefLanded 报告「一场构建把产物落到目标 tag 上了」这条**独立于流**的事实：
// 前后同一个引用的本机镜像 ID 变了（此前没有 = 空串，现在有了，也算变）。
//
// 判据与 pull_progress.go 的 pullLanded 逐字同款（那份已验收、按最小改动面不动它；
// 这里独立成函数是因为调用点是 build 语义，借用一个 pull 专属的名字会误导读者）。
//
// 两处刻意不判：构建前查不了（beforeErr != nil）或现在查不了 —— 没有对照基线时
// 「现在有这个 tag」无法区分「这场构建出来的」与「本来就有的」，宁可开不了口
// （落回「取消」），也不编一条完成。
func (e *WriteExecutor) imageRefLanded(ctx context.Context, ref, beforeID string, beforeErr error) bool {
	if beforeErr != nil {
		return false
	}
	afterID, err := e.api.ImageRefID(ctx, ref)
	if err != nil {
		return false
	}
	return afterID != "" && afterID != beforeID
}

// pushLanded 报告「这场推送把镜像交到 registry 了」这条**独立于流**的事实：
// 本机给这个镜像记的 canonical 引用（repo@sha256:…）多了一条 —— daemon 在清单提交
// 成功之后在本机落的笔（classic 的 addDigestReference，经 image_inspect 的 repoDigests
// 露出；依据与口径边界见 DockerAPI.ImageRepoDigests）。
//
// 为什么是「多了一条」而不是「有 digest 引用」：本机本来就有的 digest 引用（上次
// 拉取/推送留下的）在取消之后依然在，单看「有」会把「重推被截止」也说成成功（那是
// 反向的不诚实）；同理，重推**同一份内容**时 daemon 不会再落一笔（digest 引用已存在，
// addDigestReference 直接返回）—— 判据开不了口，落回「取消」，由收尾行那半兜底。
//
// 两处刻意不判：推送前查不了（beforeErr != nil）或现在查不了 —— 没有对照基线时
// 「现在有 digest 引用」无法区分新旧，宁可开不了口，也不编一条完成。
func (e *WriteExecutor) pushLanded(ctx context.Context, target string, before []string, beforeErr error) bool {
	if beforeErr != nil {
		return false
	}
	after, err := e.api.ImageRepoDigests(ctx, target)
	if err != nil {
		return false
	}
	for _, d := range after {
		if !slices.Contains(before, d) {
			return true
		}
	}
	return false
}
