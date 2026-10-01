package dockerops

import (
	"context"
	"path/filepath"
	"strings"

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
//   - push 复用 4c 的凭据面（imageAuthOf 与 pull 同一函数、同一 ImageAuth 类型）。

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
	buildCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sess.attachUpstream(func() error { cancel(); return nil })

	fr := newBuildProgressFramer(sess)
	err = e.api.ImageBuild(buildCtx, spec, func(b BuildProgress) {
		fr.emit(progressLine{ID: b.ID, Status: b.Status, Stream: b.Stream})
	})
	if err != nil {
		if sess.isClosing() {
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

// pushImage 是 image:push 的写执行路径（P2）。与 pullImage 的差别只有会话族与
// 结论句措辞（推送/拉取互不相混）；凭据注入与 pull 完全同源（imageAuthOf）。
func (e *WriteExecutor) pushImage(ctx context.Context, cmdRef, target string, auth *ImageAuth) error {
	// 会话管理器由构造契约保证非 nil（同 buildImage —— 三族共用 SetSessions 契约）。
	sessions := e.sessionsValue()
	sess, err := sessions.openNamed(streamPush, agentproto.DockerPushSessionID(cmdRef))
	if err != nil {
		return &ExecError{Msg: err.Error()}
	}
	pushCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sess.attachUpstream(func() error { cancel(); return nil })

	fr := newPushProgressFramer(sess)
	err = e.api.ImagePush(pushCtx, target, auth, func(p PullProgress) {
		fr.emit(progressLine{ID: p.ID, Status: p.Status, Current: p.Current, Total: p.Total})
	})
	if err != nil {
		if sess.isClosing() {
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
