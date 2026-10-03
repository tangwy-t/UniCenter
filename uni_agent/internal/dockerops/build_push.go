package dockerops

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

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
//   - **结算**（取消/截止与完成的竞态）走三族共用的完成证据框架（completion.go）：
//     收口动作（完成补帧 / 取消措辞 / 失败措辞）由 settleWrite 一张表决定，
//     判据各族自己持有（build 见 buildLanded，push 见 pushLandedSet）——
//     pull 的那份判据也在同一张表上收口（它的历史措辞逐字保留）。

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
// 成功 → 终态 Done；daemon 失败 → 终态 Error + 结论句「构建镜像失败」（detail 带
// 具体原因：上下文校验的拒绝 / builder 的报错）；取消 → 不发终态项与 eof、
// 结论句「构建已取消」。收口的取舍（完成证据优先、迟到 cancel no-op、会话还在时
// 补终态帧）在 completion.go 的 settleWrite。
//
// **完成判据**（取消/截止与完成的竞态，2026 收口版；旧形态是「产物 ID 变了」一条，
// 全缓存命中时它开不了口 —— 见下方「根除」）：
//
//	产物 ID 已知（daemon 的 aux 行 / "Successfully built" 行，见 buildFacts）
//	    → 唯一判据是「目标 tag 现在指向的就是这个产物」：
//	      指向它 = 完成（**全缓存命中**时产物 ID 与开工前逐字节相同，tag 早就在
//	      那儿了 —— 产物与 tag 没有位移，只有这条对照说得清）；
//	      指向别处 = tag 没落到本场产物上（本场没完成）。
//	产物 ID 未知（收尾行随被中断的连接丢了）→ 用不依赖流的事实补判：
//	    a. legacy 的 "Successfully tagged <target>" 行到过读循环 = daemon 把 tag
//	       落完了（它是整条流的最后一行，本机真流实测）；
//	    b. 目标 tag 的本机镜像 ID 相对开工前变了（refMoved）= 产物打了上去；
//	    c. 窗口内该 tag 被（重新）打过 —— daemon 的 image/tag 事件
//	       （tagWrittenDuring；全缓存命中时唯一剩下的可观测差异）。
//
// **为什么不再按文本判「构建完成」**：构建流的完成文本随 builder 代际而变 ——
// legacy 的 "Successfully built/tagged"、BuildKit 的步骤行（其进度编码在
// moby.buildkit.trace 的 base64 protobuf 里，本域刻意不解析）。两代**同形**的只有
// 两样：aux 行携带的**产物 ID**（`{"aux":{"ID":…}}` 与
// `{"id":"moby.image.id","aux":{"ID":…}}`）与 daemon 自己记的 tag 事件。
// 判据因此钉在「产物」与「tag 的指向」这两件事上，而不是钉在某代 builder 的措辞上。
//
// **旧实现留下的保守边界（本波根除）**：构建全命中缓存时产物 ID 与基线相同
// （tag 此前就指向同一镜像，构建确实完成了却没有留下可对照的变化）—— 判据开不了口，
// 落回「取消」。现在由「产物 ID == tag 现在的指向」这条对照开口（aux 行两代都在
// 成功收口时发），行丢了还有 tag 事件兜底。
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
	// 开工前的两条对照项，都走**指令的 ctx** 而不是 buildCtx —— 它们必须在会话
	// 可能收摊之前拿到，且取不到只是让判据失效（beforeErr != nil），不影响构建本身：
	//   - 目标 tag 的本机镜像 ID（证据 b 的基线，与 pull 的拉取前基线同一时序理由）；
	//   - 开工墙钟（证据 c 的事件窗口起点；判据比的是**墙钟**，与展示用的可注入
	//     时钟无关）。
	beforeID, beforeErr := e.api.ImageRefID(ctx, spec.Tag)
	started := time.Now()
	buildCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sess.attachUpstream(func() error { cancel(); return nil })

	fr := newBuildProgressFramer(sess)
	// 完成读数：emit 是 adapter 的同步回调，逐条喂进 facts（两代 builder 的
	// 收尾形态都在 note 里解析，见 completion.go 的 buildFacts）。
	var facts buildFacts
	err = e.api.ImageBuild(buildCtx, spec, func(b BuildProgress) {
		facts.note(b, spec.Tag)
		if b.ID == "" && b.Status == "" && b.Stream == "" {
			// 纯读数（aux 行的产物 ID）：它是判据，不是进度 —— 不进帧面
			//（帧的编码只认 ID/Status/Stream，空记录会变成帧里的空行噪音）。
			return
		}
		fr.emit(progressLine{ID: b.ID, Status: b.Status, Stream: b.Stream})
	})
	if err != nil {
		// 结算判据走自己的上下文（真截止会把指令 ctx 一起作废，见 evidenceCtx）。
		ectx, ecancel := evidenceCtx(ctx)
		defer ecancel()
		return settleWrite(e.buildLanded(ectx, spec.Tag, beforeID, beforeErr, &facts, started),
			sess.isClosing(), fr, sess, "构建已取消", "构建镜像失败", err)
	}
	fr.finish(true, "")
	waitStreamEnded(sess)
	return nil
}

// buildLanded 汇总一场构建的完成证据（判据链见 buildImage 的函数头）。
func (e *WriteExecutor) buildLanded(ctx context.Context, target, beforeID string,
	beforeErr error, facts *buildFacts, started time.Time) bool {
	builtID, tagged := facts.read()
	if builtID != "" {
		// 产物 ID 已钉住：只认「tag 现在指向它」这一条 —— 明确指向别处时**不再看
		// 别的证据**（那时「tag 的 ID 变了」只可能是别人重打了 tag，拿它当本场
		// 完成就是编一条完成）。
		return e.refPointsAt(ctx, target, builtID)
	}
	if tagged {
		return true
	}
	if e.refMoved(ctx, target, beforeID, beforeErr) {
		return true
	}
	return e.tagWrittenDuring(ctx, target, started)
}

// pushImage 是 image:push 的写执行路径（P2）。与 pullImage 的差别本来只有会话族与
// 结论句措辞（推送/拉取互不相混）；凭据注入与 pull 完全同源（imageAuthOf）。
//
// **完成判据（按 tag 计数收敛，2026 收口版）**：推送与拉取的关键差别是「完成了没有」
// 在本机**可能不留位移**（推的是本机镜像，落的笔在 registry 那侧），而**不带 tag 的
// target 是整仓推送**（daemon 的 all=1 语义，见 api.go 的 ImagePush 契约）——
// 「第一行 digest 到了 = 整场完成」在那种形态下是错的：首行只兑现了**一个** tag。
// 于是结算按 **M/N 计数**：
//   - N（应推集合）：带 tag = 那一个；不带 tag = 该仓库的全部本地 tag（推送前记）；
//   - M（已落集合）：逐 tag 判定，任一证据成立即算落：
//     证据一：该 tag 的收尾行（"<tag>: digest: …" = 清单已提交进 registry）到了
//     读循环（两代 daemon 的源码依据见 isPushCompletionLine）；
//     证据二：本机给这个镜像记的 canonical 引用多了一条（daemon 在清单提交成功后
//     落的笔）—— **只对单 tag 推送有归属力**（多 tag 时新增的引用按
//     manifest 摘要去重，与 tag 数不是一一对应）；
//     证据三：**registry 侧探测** —— 推送前记下每个 tag 的 manifest 现状（digest /
//     404），结算时再探一次：从无到有 / digest 变了即已落。它独立于
//     daemon 的镜像存储（classic 与 containerd 通吃）——证据二在
//     containerd 存储上恒不开火（RepoDigests 由本地 tag 合成），
//     这条正是为它补的位；收尾行随被中断的连接丢了的那部分也由它开口。
//   - 终态语义如实：M==N（且 N>0）→ 完成；M==0 → 取消/失败档；0<M<N → 失败档 +
//     结论句「部分 tag 已推送（M/N）」——「已取消」在这个场合会漏说「已经有一半
//     进了 registry」这个用户必须知道的事实（他重试时该从哪里接着看）。
//
// 收口动作（完成补帧 / 取消措辞 / 失败措辞 / 迟到 cutoff no-op）在 completion.go
// 的 settleWrite 一张表上。
func (e *WriteExecutor) pushImage(ctx context.Context, cmdRef, target string, auth *ImageAuth) error {
	// 会话管理器由构造契约保证非 nil（同 buildImage —— 三族共用 SetSessions 契约）。
	sessions := e.sessionsValue()
	sess, err := sessions.openNamed(streamPush, agentproto.DockerPushSessionID(cmdRef))
	if err != nil {
		return &ExecError{Msg: err.Error()}
	}
	// 推送前的三条对照项（都在会话可能收摊之前拿，且取不到只让对应证据开不了口）：
	//   - 应推集合（N）；
	//   - 证据二的基线（本机 canonical 引用）；
	//   - 证据三的基线（registry 侧每个 tag 的 manifest 现状）。
	tags, tagSetErr := e.pushTagSet(ctx, target)
	beforeDigests, beforeDigestErr := e.api.ImageRepoDigests(ctx, target)
	prober, base := e.pushRegistryBaseline(ctx, target, tags, auth)
	pushCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sess.attachUpstream(func() error { cancel(); return nil })

	fr := newPushProgressFramer(sess)
	// 完成读数：emit 是 adapter 的同步回调，按 tag 记名的收尾行逐条喂进 facts。
	var facts pushFacts
	err = e.api.ImagePush(pushCtx, target, auth, func(p PullProgress) {
		facts.note(p)
		fr.emit(progressLine{ID: p.ID, Status: p.Status, Current: p.Current, Total: p.Total})
	})
	if err != nil {
		// 结算判据走自己的上下文（真截止会把指令 ctx 一起作废，见 evidenceCtx）。
		ectx, ecancel := evidenceCtx(ctx)
		defer ecancel()
		pc := pushCase{
			target: target, tags: tags, facts: &facts, prober: prober, base: base,
			beforeDigests: beforeDigests, beforeDigestErr: beforeDigestErr,
		}
		var landed map[string]bool
		if tagSetErr == nil {
			landed = e.pushLandedSet(ectx, pc)
		}
		// 应推集合不可考（本地镜像清单读不到）时不结算 —— N 无从谈起，任何
		// 「M==N」都是编的（宁可漏救，不编一条完成）。此时 landed 保持空集，
		// 结论落取消/失败档。
		m := len(landed)
		n := pushDenominator(tags, landed)
		switch {
		case n > 0 && m == n:
			// 全部兑现：迟到 cancel / 真截止对已完成的推送是 no-op。
			return settleWrite(true, sess.isClosing(), fr, sess, "", "", err)
		case m > 0:
			// 部分兑现：取消档与失败档共用同一句 ——「M/N 已推送」本身就是完整结论
			//（detail 在失败档里补 daemon 原文）。
			partial := fmt.Sprintf("部分 tag 已推送（%d/%d）", m, n)
			return settleWrite(false, sess.isClosing(), fr, sess, partial, partial, err)
		default:
			return settleWrite(false, sess.isClosing(), fr, sess, "推送已取消", "推送镜像失败", err)
		}
	}
	fr.finish(true, "")
	waitStreamEnded(sess)
	return nil
}

// pushCase 是一场推送的结算上下文：应推集合、流内事实、三层证据的对照项。
type pushCase struct {
	target string
	// tags 是应推集合（带 tag 时一个；不带 tag 时该仓库的全部本地 tag）。
	tags []string
	// facts 是流内事实（收尾行，按 tag 记名）。
	facts *pushFacts
	// prober / base 是证据三（registry 侧观测）的探针与基线；prober 为 nil =
	// 探测不可用（该证据整场开不了口）。
	prober *registryProber
	base   manifestBaseline
	// beforeDigests / beforeDigestErr 是证据二（本机 canonical 引用）的对照项。
	beforeDigests   []string
	beforeDigestErr error
}

// pushLandedSet 逐 tag 结算「已落 registry」的集合（M 的来源，判据见 pushImage 头）。
func (e *WriteExecutor) pushLandedSet(ctx context.Context, pc pushCase) map[string]bool {
	landed := pc.facts.tags() // 证据一：已到读循环的收尾行
	// 证据三：registry 侧逐 tag 探测。只探**有基线**的 tag（基线取不到的 tag，
	// 探测结果无法与开工前对照 —— 开不了口）；已靠收尾行确认的不再探（直接事实
	// 不需要旁证，省一次往返）。
	if pc.prober != nil {
		// 与基线同一取向：整趟结算探测共用一个总时限，不随 tag 数线性膨胀。
		probeCtx, cancel := context.WithTimeout(ctx, registryProbeTimeout)
		defer cancel()
		for tag := range pc.base {
			if landed[tag] {
				continue
			}
			now, err := pc.prober.manifest(probeCtx, tag)
			if err != nil {
				continue
			}
			if pc.base.landed(tag, now) {
				landed[tag] = true
			}
		}
	}
	// 证据二：本机 canonical 引用多了一条。单 tag 推送才有归属力（见 pushImage 头）：
	// 多 tag 时它证明的是「这个仓库被写过」，不是「哪几个 tag 落了」。
	if len(pc.tags) == 1 && !landed[pc.tags[0]] &&
		e.repoDigestGrew(ctx, pc.target, pc.beforeDigests, pc.beforeDigestErr) {
		landed[pc.tags[0]] = true
	}
	return landed
}

// pushDenominator 是终态里的 N：应推集合 ∪ 已落集合。
//
// 为什么是并集：daemon 在**推送那一刻**枚举整仓的 tag，而我方的应推集合是推送前
// 最近一次本机快照 —— 期间新打上的 tag 会被 daemon 推掉，收尾行也会报它。
// 已确认落地的 tag 就该进分母（否则 M > N 这种自相矛盾的结论会漏出去）。
func pushDenominator(tags []string, landed map[string]bool) int {
	n := len(tags)
	for tag := range landed {
		if !slices.Contains(tags, tag) {
			n++
		}
	}
	return n
}

// pushTagSet 返回一场推送的**应推集合**（tag 名列表）：
//   - target 带 tag（或 digest 形态）→ 就那一个，N=1；
//   - target 不带 tag → 该仓库在当前本机上的**全部 tag**（daemon 的 all=1 语义会
//     逐个推它们，见 api.go 的 ImagePush 契约）。
//
// 「不带 tag」这一支必须问本机镜像清单（daemon 侧在推送那一刻自己枚举仓库 tag，
// agent 侧只能拿同一时刻最近的一次枚举当 N）；两次枚举若有偏差，结算侧的并集口径
// （pushDenominator）兜住「多出来的 tag」那一半。
func (e *WriteExecutor) pushTagSet(ctx context.Context, target string) ([]string, error) {
	_, _, ref, err := registryTargetOf(target)
	if err != nil {
		return nil, err
	}
	if ref != "" {
		return []string{ref}, nil
	}
	images, err := e.api.Images(ctx)
	if err != nil {
		return nil, err
	}
	return localTagsOf(images, target), nil
}

// pushRegistryBaseline 取证据三的基线：推送前对每个待推 tag 各探一次 registry。
//
// 探针构造失败（daemon 策略读不到 / 引用解不开 / 自签 CA 配不齐）→ 返回 nil 探针，
// 这条证据**整场开不了口**（其余证据照旧）—— 不猜、不降级（见 registry_probe.go 的
// 能力边界）。单个 tag 探测失败只让**那个 tag** 的基线缺席。
func (e *WriteExecutor) pushRegistryBaseline(ctx context.Context, target string,
	tags []string, auth *ImageAuth) (*registryProber, manifestBaseline) {
	if len(tags) == 0 {
		return nil, nil
	}
	prober, err := newRegistryProber(ctx, e.api, target, auth)
	if err != nil {
		return nil, nil
	}
	// 整趟基线的**总**时限（不是每 tag 各给 registryProbeTimeout）：整仓推送时
	// 一个不可达的 registry 不该让推送启动前的等待随 tag 数线性增长。
	baseCtx, cancel := context.WithTimeout(ctx, registryProbeTimeout)
	defer cancel()
	base := manifestBaseline{}
	for _, tag := range tags {
		st, err := prober.manifest(baseCtx, tag)
		if err != nil {
			continue
		}
		base[tag] = st
	}
	return prober, base
}

// repoDigestGrew 报告「本机给这个仓库记的 canonical 引用多了一条」——daemon 在
// 清单提交成功之后落的笔（classic 的 addDigestReference，经 image_inspect 的
// repoDigests 露出；依据与口径边界见 DockerAPI.ImageRepoDigests）。
//
// 为什么是「多了一条」而不是「有 digest 引用」：本机本来就有的 digest 引用
// （上次拉取/推送留下的）在取消之后依然在，单看「有」会把「重推被截止」也说成
// 成功（那是反向的不诚实）；同理，重推**同一份内容**时 daemon 不会再落一笔
// （digest 引用已存在，addDigestReference 直接返回）—— 判据开不了口，由收尾行
// 与证据三那两层兜底。
//
// 两处刻意不判：推送前查不了（beforeErr != nil）或现在查不了 —— 没有对照基线时
// 「现在有 digest 引用」无法区分新旧，宁可开不了口，也不编一条完成。
func (e *WriteExecutor) repoDigestGrew(ctx context.Context, target string, before []string, beforeErr error) bool {
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
// 判据的**带值**形态是 pushLineTag（收尾行的前缀 = 正在提交的那个 tag）——
// 2026 收口起结算按 tag 逐个记账（整仓推送会逐 tag 发收尾行，第一行只兑现一个），
// 本函数只剩「是不是收尾行」这一个问题的答案（表测试与可读性用）。
func isPushCompletionLine(p PullProgress) bool {
	_, ok := pushLineTag(p)
	return ok
}

// pushDigestMarker 是收尾行的中段骨架（daemon 两代实现的 Messagef 格式串里都有它）。
//
// 引用名里不可能出现 ": digest: " 这样的「冒号+空格」组合，故这个骨架只可能来自
// 那两处 Messagef；pull 的 "Digest: sha256:…" 是大写 D 且无 "<tag>: " 前缀，
// 不会误命中。
const pushDigestMarker = ": digest: "
