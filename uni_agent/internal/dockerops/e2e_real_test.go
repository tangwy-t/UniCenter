package dockerops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── 真栈端到端（本机真 daemon + 真 registry）─────────────────────────────────
//
// **只在 UNI_AGENT_E2E=1 时运行**（CI 里没有 daemon，默认整体跳过）。价值：判据链的
// 每一环（adapter 解析 → 执行器结算 → registry 探测 → tag 事件回放 → 会话收口）都在
// 真 daemon 与真 registry 上走一遍 —— 单测的替身做不到这一点（本文件落地的两条真
// 缺陷都只有真机能看见：SDK 不认「秒.纳秒」时间戳、真截止把指令 ctx 一起作废）。
//
// 跑法：
//
//	docker run -d --name e2e-registry -p 127.0.0.1:5000:5000 registry:2
//	cd uni_agent && UNI_AGENT_E2E=1 go test -count=1 -v -run TestE2E_ ./internal/dockerops/
//	# classic 存储（嵌套 dockerd，DOCKER_HOST 指过去）：
//	DOCKER_HOST=unix:///var/run/docker-nested.sock UNI_AGENT_E2E=1 UNI_AGENT_E2E_CLASSIC=1 \
//	  go test -count=1 -v -run TestE2E_ ./internal/dockerops/
//
// 夹具自清：造出来的镜像/标签按唯一后缀命名（重跑不撞车），registry 侧的内容随
// registry 容器一起丢弃。
//
// 场景：
//   E1 build 完成瞬间取消（新构建，长流截断：收尾行与 aux 都随连接丢掉）
//   E2 build 全缓存命中 + 完成瞬间取消（#7 根除的真机验证：产物 ID 不变）
//   E3 build 在 aux 时刻取消（最狠的窗口；只钉「判成功就必须真有 tag」）
//   E4 build 真截止落在完成之后（会话仍在）
//   E5 push 完成瞬间取消（收尾行已到）
//   E6 push 掐掉收尾行（证据三端到端：整仓三 tag 靠 registry 侧探测判成功）
//   E7 push 真失败（目标不存在）→ 失败措辞 + daemon 原文
//   E8 push 真截止落在完成之后
//   E9 push 只剩证据二（关掉证据三 + 掐掉收尾行）：classic 判成功、containerd 保守
//     落失败 —— 「证据二依赖镜像存储、证据三不依赖」的实证对照

const e2eSkip = "UNI_AGENT_E2E=1 才跑（需要真 docker daemon 与 127.0.0.1:5000 的 registry）"

// e2eRegistry 是本机 registry:2 的地址（探针与推送都打它）。
const e2eRegistry = "127.0.0.1:5000"

func e2eGuard(t *testing.T) {
	t.Helper()
	if os.Getenv("UNI_AGENT_E2E") != "1" {
		t.Skip(e2eSkip)
	}
}

// e2eHookAPI 是给真 adapter 套的钩子：按条件在 emit 里做一件事（触发 cancel、
// 吞掉某些行、或挂住读循环直到 ctx 到期）。真 daemon、真流，只有客户端的读法被
// 精确控制 —— 这正是「尾数据随连接丢掉」「完成瞬间取消」这些时机的构造方式。
type e2eHookAPI struct {
	DockerAPI
	onBuildProgress func(b BuildProgress, inner func(BuildProgress))
	onPushProgress  func(p PullProgress, inner func(PullProgress))
	// policyErr 非空时 RegistryPolicy 一律失败 —— 用于「把证据三整体关掉，
	// 只剩证据二」的对照场景。
	policyErr error
	// lastBuildErr / lastPushErr 记录真 adapter 的返回错误：**非 nil 才说明结算路径
	// 真的被走到了**（nil = daemon 干完活正常收口，判据链根本没上场）——每个场景
	// 都要断言这一点，否则用例可能只是「跑赢了取消」而已。
	lastBuildErr error
	lastPushErr  error
}

func (a *e2eHookAPI) buildErr() error { return a.lastBuildErr }
func (a *e2eHookAPI) pushErr() error  { return a.lastPushErr }

func (a *e2eHookAPI) RegistryPolicy(ctx context.Context) (RegistryPolicy, error) {
	if a.policyErr != nil {
		return RegistryPolicy{}, a.policyErr
	}
	return a.DockerAPI.RegistryPolicy(ctx)
}

func (a *e2eHookAPI) ImageBuild(ctx context.Context, spec BuildSpec, emit func(BuildProgress)) error {
	err := a.DockerAPI.ImageBuild(ctx, spec, func(b BuildProgress) {
		a.onBuildProgress(b, emit)
	})
	a.lastBuildErr = err
	return err
}

func (a *e2eHookAPI) ImagePush(ctx context.Context, ref string, auth *ImageAuth, emit func(PullProgress)) error {
	err := a.DockerAPI.ImagePush(ctx, ref, auth, func(p PullProgress) {
		a.onPushProgress(p, emit)
	})
	a.lastPushErr = err
	return err
}

// e2eEnv 是真栈用例的环境：真 adapter + 真会话管理器（真时钟 —— 帧泵走真定时器）。
type e2eEnv struct {
	w    *WriteExecutor
	m    *SessionManager
	sink *frameSink
	hook *e2eHookAPI
	api  DockerAPI
}

func newE2EEnv(t *testing.T, execDir string) *e2eEnv {
	t.Helper()
	adapter, err := newSDKAdapter()
	if err != nil {
		t.Fatalf("真 adapter 构造失败（daemon 不在？）: %v", err)
	}
	if err := adapter.Ping(context.Background()); err != nil {
		t.Fatalf("真 daemon ping 失败: %v", err)
	}
	sink := &frameSink{}
	m := newSessionManager(sessionConfig{
		send: sink.send, log: testLogger(), now: time.Now, after: time.After,
		idleTimeout: streamIdleTimeout,
	})
	hook := &e2eHookAPI{DockerAPI: adapter}
	hook.onBuildProgress = func(b BuildProgress, inner func(BuildProgress)) { inner(b) }
	hook.onPushProgress = func(p PullProgress, inner func(PullProgress)) { inner(p) }
	w := NewWriteExecutor(hook, nil, execDir, "", nil)
	w.SetSessions(m)
	return &e2eEnv{w: w, m: m, sink: sink, hook: hook, api: adapter}
}

// cancelBuildWhen 在构建流出现某形态时下发 cancel（完成瞬间取消的构造）。
func (e *e2eEnv) cancelBuildWhen(pred func(BuildProgress) bool) {
	e.hook.onBuildProgress = func(b BuildProgress, inner func(BuildProgress)) {
		inner(b)
		if pred(b) {
			e.m.OnFrame(&agentproto.CoreDockerFrame{
				SessionID: agentproto.DockerBuildSessionID(e2eBuildRef), Op: agentproto.DockerFrameOpCancel,
			})
			e.hook.onBuildProgress = func(b BuildProgress, inner func(BuildProgress)) { inner(b) }
		}
	}
}

// cancelPushWhen 在推送流出现某形态时下发 cancel。
func (e *e2eEnv) cancelPushWhen(pred func(PullProgress) bool) {
	e.hook.onPushProgress = func(p PullProgress, inner func(PullProgress)) {
		inner(p)
		if pred(p) {
			e.m.OnFrame(&agentproto.CoreDockerFrame{
				SessionID: agentproto.DockerPushSessionID(e2ePushRef), Op: agentproto.DockerFrameOpCancel,
			})
			e.hook.onPushProgress = func(p PullProgress, inner func(PullProgress)) { inner(p) }
		}
	}
}

// suppressPushEvidence 吞掉推送收尾行（证据一缺席 —— 证据三的用武之地）。
func (e *e2eEnv) suppressPushEvidence() {
	e.hook.onPushProgress = func(p PullProgress, inner func(PullProgress)) {
		if _, ok := pushLineTag(p); ok {
			return
		}
		inner(p)
	}
}

// stallUntilCtxDone 在出现某形态后挂住读循环，直到 ctx 到期（真截止的构造：
// 会话仍在、活已干完）。
func (e *e2eEnv) stallPushUntilCtxDone(ctx context.Context, pred func(PullProgress) bool) {
	e.hook.onPushProgress = func(p PullProgress, inner func(PullProgress)) {
		inner(p)
		if pred(p) {
			e.hook.onPushProgress = func(p PullProgress, inner func(PullProgress)) { inner(p) }
			<-ctx.Done()
		}
	}
}

var (
	e2eBuildRef = "1790000000000009001"
	e2ePushRef  = "1790000000000009002"
)

// e2eLongContext 造一个**流很长**的构建上下文：一条 8KB 的 ENV 指令让 legacy
// builder 的 "Step i/N : <指令原文>" 行把流撑到 8KB 以上 —— 超过客户端缓冲区
// （约 4KB）是「取消能真的截断读循环」的前提：短流会被整段缓冲，取消之后仍把尾巴
// 读完、正常收口，结算路径根本走不到（实测过的坑）。缓存命中的重跑同样有这一行
// （指令行照打），流长不缩水。
func e2eLongContext(t *testing.T) []byte {
	t.Helper()
	df := "FROM scratch\nCOPY hello.txt /hello.txt\nENV BIG " + strings.Repeat("x", 40000) + "\n"
	return writeTar(t,
		[]string{"Dockerfile", df},
		[]string{"hello.txt", "e2e-long\n"},
	)
}

// holdBuildThenCancel 把读循环**按在**构建流上，等 ready 成立（外部观测：daemon
// 真的干完了）后下发 cancel，再放行 —— 这样 ImageBuild 必以 ctx 错误返回（结算
// 路径必被走到），而 daemon 侧的构建已经完成。
//
// 为什么必须按住：不按的话，daemon 的流一旦发完 EOF，客户端会正常收口（走的是
// 「daemon 自己说成功」的正常路径）——那时 cancel 到得再晚也不经过判据链。
//
// 前提：流要**足够长**（超过客户端缓冲区），否则取消也切不断读循环里的尾巴 ——
// 短流会被整段缓冲，取消之后仍把尾巴读完（见 e2eLongContext 的说明）。
func (e *e2eEnv) holdBuildThenCancel(ready func() bool) {
	var once sync.Once
	release := make(chan struct{})
	e.hook.onBuildProgress = func(b BuildProgress, inner func(BuildProgress)) {
		inner(b)
		once.Do(func() {
			go func() {
				deadline := time.Now().Add(2 * time.Minute)
				for time.Now().Before(deadline) && !ready() {
					time.Sleep(20 * time.Millisecond)
				}
				e.m.OnFrame(&agentproto.CoreDockerFrame{
					SessionID: agentproto.DockerBuildSessionID(e2eBuildRef), Op: agentproto.DockerFrameOpCancel,
				})
				close(release)
			}()
			<-release
		})
	}
}

// tmp -- 下面的构建指令改用长流上下文（见 e2eBuildCmd）。
func (e *e2eEnv) buildCmdLong(t *testing.T, dir, tag string) *agentproto.DockerCmd {
	t.Helper()
	name := writeContextFile(t, dir, e2eLongContext(t))
	return &agentproto.DockerCmd{
		Ref: e2eBuildRef, Action: agentproto.DockerActionImageBuild,
		Options: agentproto.DockerCmdOptions{Context: name, Tag: tag},
	}
}

// e2eBuildCmd 编一条真构建指令（上下文 tar 落在 transferDir）。
//
// 上下文与单测夹具（minimalContext）**不同**：真 daemon 的 legacy builder 对
// 「只有 FROM scratch、没有任何 COPY/指令」的 Dockerfile 会回
// 「No image was generated.」（实测），故这里用一个真能产出镜像的最小上下文。
func (e *e2eEnv) buildCmd(t *testing.T, dir, dockerfile, tag string) *agentproto.DockerCmd {
	t.Helper()
	name := writeContextFile(t, dir, writeTar(t,
		[]string{"Dockerfile", "FROM scratch\nCOPY hello.txt /hello.txt\n"},
		[]string{"hello.txt", "e2e-" + tag + "\n"},
	))
	o := agentproto.DockerCmdOptions{Context: name, Tag: tag}
	if dockerfile != "" {
		o.Dockerfile = dockerfile
	}
	return &agentproto.DockerCmd{Ref: e2eBuildRef, Action: agentproto.DockerActionImageBuild, Options: o}
}

// e2eCtxOpts 给 build/push 指令用的 target 定型（真 registry 的 host:port 要带全）。
func pushCmdAt(ref, target string) *agentproto.DockerCmd {
	return &agentproto.DockerCmd{
		Ref: e2ePushRef, Action: agentproto.DockerActionImagePush,
		Options: agentproto.DockerCmdOptions{Target: target},
	}
}

// TestE2E_build_cancel_at_completion：E1/E2 —— 构建完成瞬间取消（**长流**：
// 取消能真的截断读循环）。E2 是**全缓存命中**：产物 ID 与开工前逐字节相同，
// 旧判据（ID 变了）在这里开不了口 —— 判据链只能靠 tag 事件（全缓存命中时唯一
// 剩下的可观测差异）开口。
//
// 读循环被按在**第一条**进度行上（完成读数与收尾行都还没读到），等外部信号
// （daemon 的 tag 事件）成立再取消放行：这既是「完成瞬间取消」，也是「收尾行随
// 连接丢掉」——证据只剩独立于流的那几条。
func TestE2E_build_cancel_at_completion(t *testing.T) {
	e2eGuard(t)
	dir := t.TempDir()
	env := newE2EEnv(t, dir)
	tag := "e2e-build:1"

	run := func() {
		started := time.Now()
		env.holdBuildThenCancel(func() bool {
			// 外部信号：daemon 为这个 tag 发过 tag 事件 = 构建干完了（这条查询
			// 与执行器的结算无关，只是测试用来卡时刻的独立观测）。
			evs, err := env.api.ImageTagEvents(context.Background(), started.Add(-2*time.Second), time.Now())
			if err != nil {
				return false
			}
			for _, ev := range evs {
				if ev.ActorName == tag || strings.HasSuffix(ev.ActorName, "/"+tag) {
					return true
				}
			}
			return false
		})
		done := make(chan error, 1)
		go func() {
			_, err := env.w.Do(context.Background(), env.buildCmdLong(t, dir, tag))
			done <- err
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("完成瞬间取消必须按成功结算: %v", err)
			}
		case <-time.After(3 * time.Minute):
			t.Fatal("构建未在时限内返回")
		}
		if env.hook.buildErr() == nil {
			t.Fatal("本场景必须真的走到结算路径（ImageBuild 以 ctx 错误收场）")
		}
	}
	run() // E1：新构建（tag 从无到有 → 本机引用位移）
	t.Log("E1 新构建 + 完成瞬间取消（长流截断）→ 成功")
	run() // E2：全缓存命中（产物 ID 不变 → 只有 tag 事件能开口）
	t.Log("E2 全缓存命中 + 完成瞬间取消 → 成功")
}

// TestE2E_build_cancel_at_aux：E3 —— 在 aux（产物 ID）到达的瞬间取消：此刻 tag
// 还没落（真机实测：aux 之后约十余毫秒 tag 才出现），而产物已提交。
// 观察这一格的现象（判据链在产物 ID 已知时的口径）。
func TestE2E_build_cancel_at_aux(t *testing.T) {
	e2eGuard(t)
	dir := t.TempDir()
	env := newE2EEnv(t, dir)
	tag := "e2e-buildaux:1"

	done := make(chan error, 1)
	env.cancelBuildWhen(func(b BuildProgress) bool { return b.ImageID != "" })
	go func() {
		_, err := env.w.Do(context.Background(), env.buildCmd(t, dir, "", tag))
		done <- err
	}()
	var err error
	select {
	case err = <-done:
	case <-time.After(3 * time.Minute):
		t.Fatal("构建未在时限内返回")
	}
	// 结算后的真实状态：tag 指向谁、产物在不在。
	afterID, ierr := env.api.ImageRefID(context.Background(), tag)
	t.Logf("E3 aux 瞬间取消 → 执行器结论 %v；结算后 ImageRefID(%s)=%q err=%v", err, tag, afterID, ierr)
	// 本用例只钉**不撒谎**这一条（两台 daemon 上 aux 与 tag 的先后本就不同，实测：
	// containerd 上取消先到、tag 根本没落；classic 上 tag 已落、判成功）：
	//   - 判成功 ⇒ tag 必须真的指向产物（否则就是编了一条完成）；
	//   - 判取消 ⇒ 允许 tag 缺席（取消切得早时 daemon 连 tag 都不打 —— 这正是实的）。
	if err == nil && afterID == "" {
		t.Fatalf("判了成功却没有 tag（编完成）: ierr=%v", ierr)
	}
}

// TestE2E_build_deadline_after_completion：E4 —— 真截止（执行时限）落在完成之后：
// 会话仍在（没有 cancel 帧），靠证据判完成，并补发终态项与 eof。
func TestE2E_build_deadline_after_completion(t *testing.T) {
	e2eGuard(t)
	dir := t.TempDir()
	env := newE2EEnv(t, dir)
	tag := "e2e-builddl:1"

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// 按在第一条进度行上直到 ctx 到期（会话仍在 = 真截止）；长流保证尾巴读不完。
	var once sync.Once
	release := make(chan struct{})
	env.hook.onBuildProgress = func(b BuildProgress, inner func(BuildProgress)) {
		inner(b)
		once.Do(func() { <-ctx.Done(); close(release) })
		<-release
	}
	done := make(chan error, 1)
	go func() {
		_, err := env.w.Do(ctx, env.buildCmdLong(t, dir, tag))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("真截止落在完成之后必须按成功结算: %v", err)
		}
	case <-time.After(3 * time.Minute):
		t.Fatal("构建未在时限内返回")
	}
	if env.hook.buildErr() == nil {
		t.Fatal("本场景必须真的走到结算路径（ImageBuild 以 ctx 错误收场）")
	}
	t.Log("E4 真截止（落在完成之后）→ 成功")
}

// TestE2E_push_cancel_at_completion：E5 —— 完成瞬间取消（收尾行已到）。
func TestE2E_push_cancel_at_completion(t *testing.T) {
	e2eGuard(t)
	dir := t.TempDir()
	env := newE2EEnv(t, dir)
	// 唯一 tag：保证这场推送的 registry 基线是「不存在」（重跑不受上次残留影响；
	// 证据三判的是**变化**，残留会让它开不了口 —— 那是设计使然，不是缺陷）。
	target := fmt.Sprintf("%s/e2e-push-%d:1", e2eRegistry, time.Now().UnixNano())

	// 先造一个本机镜像（用真构建）。
	if _, err := env.w.Do(context.Background(), env.buildCmd(t, dir, "", target)); err != nil {
		t.Fatalf("造镜像失败: %v", err)
	}
	done := make(chan error, 1)
	env.cancelPushWhen(func(p PullProgress) bool {
		_, ok := pushLineTag(p)
		return ok
	})
	go func() {
		_, err := env.w.Do(context.Background(), pushCmdAt(e2ePushRef, target))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("完成瞬间取消必须按成功结算: %v", err)
		}
	case <-time.After(3 * time.Minute):
		t.Fatal("推送未在时限内返回")
	}
	if env.hook.pushErr() == nil {
		t.Fatal("本场景必须真的走到结算路径（ImagePush 以 ctx 错误收场）")
	}
	t.Log("E5 推送完成瞬间取消 → 成功")
}

// TestE2E_push_evidence_three_without_lines：E6 —— **证据三端到端**：掐掉全部收尾行
// （含整仓多 tag 的每一个），推送真的落了 registry —— 结算必须靠 registry 侧探测
// 判成功（containerd 存储上证据二恒不开火，这是唯一能开口的那条）。
//
// 收尾方式是**真截止**（按在流上到 ctx 到期）：真截止会把指令 ctx 作废，判据因此
// 必须走 evidenceCtx（Real机实测踩到过：不作废它时证据整场开不了口）。
func TestE2E_push_evidence_three_without_lines(t *testing.T) {
	e2eGuard(t)
	dir := t.TempDir()
	env := newE2EEnv(t, dir)

	// 整仓推送：同一镜像打三个 tag（不带 tag 的 target = 推全部本地 tag）。
	// 仓库名带唯一后缀：证据三判「变化」，registry 上的残留会让它开不了口。
	repo := fmt.Sprintf("%s/e2e-multi-%d", e2eRegistry, time.Now().UnixNano())
	base := repo + ":1"
	if _, err := env.w.Do(context.Background(), env.buildCmd(t, dir, "", base)); err != nil {
		t.Fatalf("造镜像失败: %v", err)
	}
	imgID, err := env.api.ImageRefID(context.Background(), base)
	if err != nil || imgID == "" {
		t.Fatalf("取镜像 ID 失败: %v", err)
	}
	for _, tag := range []string{":2", ":3"} {
		if err := env.api.ImageTag(context.Background(), base, repo+tag); err != nil {
			t.Fatalf("打 tag 失败: %v", err)
		}
	}

	env.suppressPushEvidence()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	env.stallPushUntilCtxDone(ctx, func(PullProgress) bool { return true })
	done := make(chan error, 1)
	go func() {
		_, err := env.w.Do(ctx, pushCmdAt(e2ePushRef, repo))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("掐掉收尾行后必须靠 registry 探测判成功: %v", err)
		}
	case <-time.After(3 * time.Minute):
		t.Fatal("推送未在时限内返回")
	}
	if env.hook.pushErr() == nil {
		t.Fatal("本场景必须真的走到结算路径（ImagePush 以 ctx 错误收场）——否则测的是正常成功路径")
	}
	for _, tag := range []string{"1", "2", "3"} {
		if !env.registryHas(repo + ":" + tag) {
			t.Fatalf("整仓推送必须把三个 tag 都推到 registry（待推集合 N=3 的口径）: %s 缺席", tag)
		}
	}
	t.Log("E6 掐掉全部收尾行（整仓三 tag）+ 真截止 → 靠 registry 探测判成功")
}

// TestE2E_push_denied_stays_failed：E7 —— 真失败（不存在的仓库/无权限）：
// 结论句是失败档（不是取消），detail 带 daemon 原文。
func TestE2E_push_denied_stays_failed(t *testing.T) {
	e2eGuard(t)
	dir := t.TempDir()
	env := newE2EEnv(t, dir)
	target := "127.0.0.1:1/nowhere:1" // 端口没人听：推送必失败

	_, err := env.w.Do(context.Background(), pushCmdAt(e2ePushRef, target))
	var ee *ExecError
	if !errors.As(err, &ee) || ee.Msg != "推送镜像失败" || ee.Detail == "" {
		t.Fatalf("真失败必须记失败档（含 daemon 原文）: %v", err)
	}
	t.Logf("E7 真失败 → %s（detail: %s）", ee.Msg, ee.Detail)
}

// TestE2E_push_deadline_after_completion：E8 —— 真截止落在完成之后（会话仍在）。
func TestE2E_push_deadline_after_completion(t *testing.T) {
	e2eGuard(t)
	dir := t.TempDir()
	env := newE2EEnv(t, dir)
	target := fmt.Sprintf("%s/e2e-dl-%d:1", e2eRegistry, time.Now().UnixNano())

	if _, err := env.w.Do(context.Background(), env.buildCmd(t, dir, "", target)); err != nil {
		t.Fatalf("造镜像失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	env.stallPushUntilCtxDone(ctx, func(p PullProgress) bool {
		_, ok := pushLineTag(p)
		return ok
	})
	done := make(chan error, 1)
	go func() {
		_, err := env.w.Do(ctx, pushCmdAt(e2ePushRef, target))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("真截止落在完成之后必须按成功结算: %v", err)
		}
	case <-time.After(3 * time.Minute):
		t.Fatal("推送未在时限内返回")
	}
	if env.hook.pushErr() == nil {
		t.Fatal("本场景必须真的走到结算路径（ImagePush 以 ctx 错误收场）")
	}
	t.Log("E8 推送真截止（落在完成之后）→ 成功")
}

// TestE2E_push_evidence_two_only：**证据二的边界实证** —— registry 探测整体关掉
// （策略读不到 → 探针 nil）+ 收尾行全掐，只剩「本机 canonical 引用多了一条」。
//
// 两台 daemon 上跑同一段代码给出两种结果，正是「证据二依赖镜像存储」的实证：
//   - classic 存储（UNI_AGENT_E2E_CLASSIC=1）：推送成功会在本机落一笔 digest 引用
//     → 判成功；
//   - containerd 存储：RepoDigests 由本地 tag 合成，推送不改变它 → 判据开不了口
//     → 保守落失败档（这时能开口的是证据三，本用例把它关掉了）。
//
// 收尾方式是真截止（会话仍在）——保守档的措辞因此是「推送镜像失败」而不是
// 「推送已取消」：截止不是用户取消，两者在结果里的档位本就不同。
func TestE2E_push_evidence_two_only(t *testing.T) {
	e2eGuard(t)
	dir := t.TempDir()
	env := newE2EEnv(t, dir)
	target := fmt.Sprintf("%s/e2e-ev2-%d:1", e2eRegistry, time.Now().UnixNano())

	if _, err := env.w.Do(context.Background(), env.buildCmd(t, dir, "", target)); err != nil {
		t.Fatalf("造镜像失败: %v", err)
	}
	env.hook.policyErr = errors.New("e2e: 探测面关闭（本用例只看证据二）")
	env.suppressPushEvidence()
	// 收尾方式用真截止（与 E6 同款）：按在流上到 ctx 到期 —— 保证走到结算路径。
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	env.stallPushUntilCtxDone(ctx, func(PullProgress) bool { return true })

	done := make(chan error, 1)
	go func() {
		_, err := env.w.Do(ctx, pushCmdAt(e2ePushRef, target))
		done <- err
	}()
	var err error
	select {
	case err = <-done:
	case <-time.After(3 * time.Minute):
		t.Fatal("推送未在时限内返回")
	}
	if env.hook.pushErr() == nil {
		t.Fatal("本场景必须真的走到结算路径（ImagePush 以 ctx 错误收场）")
	}
	classic := os.Getenv("UNI_AGENT_E2E_CLASSIC") == "1"
	if classic {
		if err != nil {
			t.Fatalf("classic 存储上证据二必须开口（推送成功会落 digest 引用）: %v", err)
		}
		t.Log("E9 classic 存储：掐掉收尾行 + 关掉证据三 → 证据二判成功")
		return
	}
	var ee *ExecError
	if !errors.As(err, &ee) || ee.Msg != "推送镜像失败" {
		t.Fatalf("containerd 存储上证据二恒不开火，必须保守落失败档，实际: %v", err)
	}
	t.Log("E9 containerd 存储：同样的证据面 → 保守落「推送镜像失败」（判据不许凭空开口）")
}

// registryHas 直接问一次 registry：这个 tag 落没落（测试自用的独立观测，
// 不经过被测的执行器判据）。
func (e *e2eEnv) registryHas(target string) bool {
	_, _, ref, err := registryTargetOf(target)
	if err != nil {
		return false
	}
	p, err := newRegistryProber(context.Background(), e.api, target, nil)
	if err != nil {
		return false
	}
	st, err := p.manifest(context.Background(), ref)
	return err == nil && st.exists
}

// TestE2E_tag_events_replay：tag 事件回放的真机守卫（E4 判据的证据面）。
//
// 由来：这条路径在真机上踩过两个只有真机能看见的坑 —— 时间戳形态被 SDK 拒（整场
// 回放失败）与「流读完的 io.EOF 也走错误通道」（正常的关流被误报成证据不可用）；
// 单测替身不经过 SDK，两个坑都隐身。本用例把「一次真构建之后，窗口内能读到这个 tag
// 的事件，且事件指向的镜像就是该 tag 现在的镜像」钉在真机上。
func TestE2E_tag_events_replay(t *testing.T) {
	e2eGuard(t)
	dir := t.TempDir()
	env := newE2EEnv(t, dir)
	tag := fmt.Sprintf("e2e-ev-%d:1", time.Now().UnixNano())

	started := time.Now()
	if _, err := env.w.Do(context.Background(), env.buildCmd(t, dir, "", tag)); err != nil {
		t.Fatalf("造镜像失败: %v", err)
	}
	nowID, err := env.api.ImageRefID(context.Background(), tag)
	if err != nil || nowID == "" {
		t.Fatalf("取镜像 ID 失败: %v", err)
	}
	// 两端都给松弛（与 tagWrittenDuring 同口径）；until 落在未来时 SDK 会把流挂到
	// 那一刻 —— 适配层的静默判完机制负责不傻等（见 adapter.ImageTagEvents）。
	evs, err := env.api.ImageTagEvents(context.Background(), started.Add(-2*time.Second), time.Now().Add(2*time.Second))
	if err != nil {
		t.Fatalf("tag 事件回放必须成功（时间戳形态与 EOF 处理错了都会在这里现形）: %v", err)
	}
	for _, ev := range evs {
		if ev.Type == "image" && ev.Action == "tag" && ev.ActorName == tag {
			if !imageIDMatches(ev.ActorID, nowID) {
				t.Fatalf("事件指向的镜像必须与 tag 现在的镜像一致: %q vs %q", ev.ActorID, nowID)
			}
			t.Logf("E10 tag 事件回放：%s → %s", ev.ActorName, ev.ActorID)
			return
		}
	}
	t.Fatalf("窗口内必须能读到这个 tag 的 tag 事件（本场构建刚打过它），实际 %d 条事件", len(evs))
}
