package dockerops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"strings"
	"testing"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── image:pull 进度透出（4b）──────────────────────────────────────────────

var pullRef = "1790000000000000001"

// pullCmd 编一条 image:pull 指令（Ref 与 core 侧登记会话用同一个派生函数）。
func pullCmd() *agentproto.DockerCmd {
	return &agentproto.DockerCmd{
		Ref:     pullRef,
		Action:  agentproto.DockerActionImagePull,
		Options: agentproto.DockerCmdOptions{Target: "nginx:latest"},
	}
}

// newPullFixture 造「假时钟会话管理器 + 写执行器（已注入会话）」的拉取测试环境。
func newPullFixture(api DockerAPI) (*WriteExecutor, *SessionManager, *fakeClock, *frameSink) {
	sink := &frameSink{}
	m, clk := newTestSessions(sink, streamIdleTimeout)
	w := NewWriteExecutor(api, nil, "", "", nil)
	w.SetSessions(m)
	return w, m, clk, sink
}

// pullLinesOf 解析一帧里的全部进度行。
func pullLinesOf(t *testing.T, f *agentproto.DockerFrame) []agentproto.DockerPullProgressItem {
	t.Helper()
	var out []agentproto.DockerPullProgressItem
	for _, raw := range bytes.Split(f.Data, []byte{'\n'}) {
		if len(raw) == 0 {
			continue
		}
		var p agentproto.DockerPullProgressItem
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatalf("帧 data 必须是 JSON 进度行: %v (%q)", err, raw)
		}
		if err := p.Validate(); err != nil {
			t.Fatalf("进度行必须过协议校验: %+v %v", p, err)
		}
		out = append(out, p)
	}
	return out
}

// pullResultOf 在 Do 返回后取结论（channel 已就绪时用）。
func pullResultOf(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("Do 未在时限内返回")
		return nil
	}
}

// TestPullImageProgressFoldsAndThrottles：同窗口多行合并（同层取最新、无层消息行
// 保留、行序确定），每批一帧；帧内容过协议校验、seq 单调；拉取成功后终态 Done 项
// 恰在流末、eof 帧先于 result 发出收口。
func TestPullImageProgressFoldsAndThrottles(t *testing.T) {
	ch := make(chan PullProgress, 16)
	api := &stubAPI{pullCh: ch}
	w, m, clk, sink := newPullFixture(api)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), pullCmd())
		done <- err
	}()

	// 第一个窗口：消息行 + 同层三条抖动 + 第二层。
	ch <- PullProgress{Status: "Pulling from library/nginx"}
	ch <- PullProgress{ID: "aaa", Status: "Downloading", Current: 1, Total: 100}
	ch <- PullProgress{ID: "aaa", Status: "Downloading", Current: 2, Total: 100}
	ch <- PullProgress{ID: "aaa", Status: "Downloading", Current: 3, Total: 100}
	ch <- PullProgress{ID: "bbb", Status: "Downloading", Current: 5, Total: 100}
	advanceUntil(t, clk, 50*time.Millisecond, 400*time.Millisecond, func() bool { return sink.count() >= 1 })
	if got := sink.count(); got != 1 {
		t.Fatalf("一个窗口必须折叠成一帧，实际 %d", got)
	}
	frames := sink.all()
	if frames[0].Seq != 1 {
		t.Fatalf("seq 必须从 1 起: %d", frames[0].Seq)
	}
	items := pullLinesOf(t, frames[0])
	if len(items) != 3 {
		t.Fatalf("一帧应有 3 条记录（1 消息行 + 2 层），实际 %d: %+v", len(items), items)
	}
	if items[0].ID != "" || items[0].Status != "Pulling from library/nginx" {
		t.Fatalf("消息行不符: %+v", items[0])
	}
	if items[1].ID != "aaa" || items[1].Current != 3 || items[1].Status != "Downloading" {
		t.Fatalf("同层必须只留窗口内最后一条: %+v", items[1])
	}
	if items[2].ID != "bbb" || items[2].Current != 5 {
		t.Fatalf("第二层不符: %+v", items[2])
	}

	// 第二个窗口：新值 → 新帧；消息行已清空不重复出现。
	ch <- PullProgress{ID: "aaa", Status: "Downloading", Current: 40, Total: 100}
	ch <- PullProgress{ID: "bbb", Status: "Extracting", Current: 100, Total: 100}
	advanceUntil(t, clk, 50*time.Millisecond, 400*time.Millisecond, func() bool { return sink.count() >= 2 })
	frames = sink.all()
	if len(frames) != 2 || frames[1].Seq != 2 {
		t.Fatalf("第二窗口应恰好再产一帧: %d", len(frames))
	}
	items = pullLinesOf(t, frames[1])
	if len(items) != 2 || items[0].ID != "aaa" || items[0].Current != 40 ||
		items[1].ID != "bbb" || items[1].Status != "Extracting" || items[1].Current != 100 {
		t.Fatalf("第二窗口折叠不符: %+v", items)
	}

	// 拉取结束：终态 Done 项 + eof，且 eof 帧在 Do 返回时**已经发出**（eof ≤ result）
	// —— 不靠 waitFor 补等：Do 返回本身就是「泵已把 eof 发出去」的证明（waitStreamEnded
	// 以会话收摊为界，收摊只发生在 eof 发送之后）。
	if m.count() != 1 {
		t.Fatalf("拉取期间会话必须在册: %d", m.count())
	}
	close(ch)
	if err := pullResultOf(t, done); err != nil {
		t.Fatalf("拉取成功不得返回错误: %v", err)
	}
	frames = sink.all()
	if len(frames) == 0 || !frames[len(frames)-1].EOF {
		t.Fatal("Do 返回时 eof 帧必须已发出（eof ≤ result 的时序纪律）")
	}
	waitFor(t, "槽位释放", func() bool { return m.count() == 0 })
	frames = sink.all()
	last := frames[len(frames)-1]
	if !last.EOF {
		t.Fatal("最后一帧必须带 eof")
	}
	term := pullLinesOf(t, last)
	if len(term) != 1 || !term[0].Done {
		t.Fatalf("终态项必须恰一条且 Done: %+v", term)
	}
	for _, f := range frames[:len(frames)-1] {
		for _, p := range pullLinesOf(t, f) {
			if p.Done || p.Error != "" {
				t.Fatalf("终态项必须只在流末: %+v", p)
			}
		}
	}
}

// TestPullImageNoChangeWindowSkipsFrame：跨窗内容逐字节相同的批不发（停滞的拉取
// 不产帧）——「状态变化才发声，发声也压着节奏」的去重半闸。
func TestPullImageNoChangeWindowSkipsFrame(t *testing.T) {
	ch := make(chan PullProgress, 8)
	api := &stubAPI{pullCh: ch}
	w, _, clk, sink := newPullFixture(api)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), pullCmd())
		done <- err
	}()

	record := PullProgress{ID: "aaa", Status: "Waiting"}
	ch <- record
	advanceUntil(t, clk, 50*time.Millisecond, 400*time.Millisecond, func() bool { return sink.count() >= 1 })

	// 同一内容再来两个窗口：不产新帧。
	ch <- record
	clk.Advance(pullFrameInterval * 2)
	time.Sleep(5 * time.Millisecond) // 让泵有机会处理（内容相同则不该有任何发送）
	if got := sink.count(); got != 1 {
		t.Fatalf("内容无变化的窗口不得产帧，实际 %d", got)
	}

	// 内容一旦变化立刻恢复产帧。
	ch <- PullProgress{ID: "aaa", Status: "Downloading", Current: 7, Total: 100}
	advanceUntil(t, clk, 50*time.Millisecond, 400*time.Millisecond, func() bool { return sink.count() >= 2 })
	items := pullLinesOf(t, sink.all()[1])
	if len(items) != 1 || items[0].Status != "Downloading" || items[0].Current != 7 {
		t.Fatalf("变化后的帧不符: %+v", items)
	}

	close(ch)
	if err := pullResultOf(t, done); err != nil {
		t.Fatalf("拉取成功不得返回错误: %v", err)
	}
}

// TestPullImageErrorMapsToErrorItemAndConclusion：daemon 失败 → 终态 Error 项
// （daemon 原文）挂在流末 + eof；结论句照旧是「拉取镜像失败」（旧轮询语义不变）。
func TestPullImageErrorMapsToErrorItemAndConclusion(t *testing.T) {
	ch := make(chan PullProgress, 4)
	api := &stubAPI{pullCh: ch, pullErr: errors.New("denied: requested access to the resource is denied")}
	w, m, clk, sink := newPullFixture(api)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), pullCmd())
		done <- err
	}()

	ch <- PullProgress{ID: "aaa", Status: "Downloading", Current: 1, Total: 100}
	advanceUntil(t, clk, 50*time.Millisecond, 400*time.Millisecond, func() bool { return sink.count() >= 1 })
	close(ch)

	var ee *ExecError
	err := pullResultOf(t, done)
	if !errors.As(err, &ee) || ee.Msg != "拉取镜像失败" || ee.Detail != "denied: requested access to the resource is denied" {
		t.Fatalf("失败结论句不符: %v", err)
	}
	waitFor(t, "eof 与槽位释放", func() bool {
		fs := sink.all()
		return len(fs) > 0 && fs[len(fs)-1].EOF && m.count() == 0
	})
	term := pullLinesOf(t, sink.all()[len(sink.all())-1])
	if len(term) != 1 || term[0].Error != "denied: requested access to the resource is denied" || term[0].Done {
		t.Fatalf("终态 Error 项不符: %+v", term)
	}
}

// TestPullImageCancelStops：cancel 控制帧（core 按 pull_<ref> 句柄下发）→ 拉取即停，
// 结论句「拉取已取消」（与失败分措辞 —— 用户主动放弃不是故障），不再产帧、槽位
// 立刻释放。
func TestPullImageCancelStops(t *testing.T) {
	ch := make(chan PullProgress, 8)
	api := &stubAPI{pullCh: ch}
	w, m, clk, sink := newPullFixture(api)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), pullCmd())
		done <- err
	}()

	ch <- PullProgress{ID: "aaa", Status: "Downloading", Current: 1, Total: 100}
	advanceUntil(t, clk, 50*time.Millisecond, 400*time.Millisecond, func() bool { return sink.count() >= 1 })
	before := sink.count()

	m.OnFrame(&agentproto.CoreDockerFrame{
		SessionID: agentproto.DockerPullSessionID(pullRef),
		Op:        agentproto.DockerFrameOpCancel,
	})

	var ee *ExecError
	err := pullResultOf(t, done)
	if !errors.As(err, &ee) || ee.Msg != "拉取已取消" {
		t.Fatalf("取消结论句不符: %v", err)
	}
	if got := m.count(); got != 0 {
		t.Fatalf("取消必须立刻释放槽位: %d", got)
	}
	// 取消后：不产新帧、也不发 eof（会话已消失 —— 与 stats/logs 同纪律）。
	time.Sleep(50 * time.Millisecond)
	if got := sink.count(); got != before {
		t.Fatalf("取消后不得再产帧: %d -> %d", before, got)
	}
	last := sink.all()[len(sink.all())-1]
	if last.EOF {
		t.Fatal("取消路径不发 eof")
	}
}

// ── 取消/完成的竞态（B4 守卫）──────────────────────────────────────────────
//
// 背景（QA 实测；P2 起「观看/执行」解耦 —— 断流只停观看、不再下发 cancel，cancel
// 只由显式取消端点下发：POST /cmds/:ref/cancel → CancelProgress → cancel 帧 → 会话
// 收摊）：用户在拉取进行中经该端点下发取消；若 daemon 恰在同时把活干完，旧实现按
// 「ctx 被中断」把这场**已经成功**的拉取记成「失败·拉取已取消」（任务中心红字，而
// `docker images` 里镜像已经落地）。四条用例把结算规则钉死：完成的事实优先于迟到的
// 取消，且**只有**「完成」的两条独立证据能翻案（防止顺手把真取消也放行）。

// 证据一：daemon 的收尾行已经到达读循环（"Status: …" = 镜像已写进本地存储、
// 引用已更新）—— 此后到达的 cancel 是 no-op，终态按完成结算。
func TestPullImageCancelAfterDaemonCompletedStaysSucceeded(t *testing.T) {
	ch := make(chan PullProgress, 8)
	api := &stubAPI{pullCh: ch}
	w, m, clk, sink := newPullFixture(api)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), pullCmd())
		done <- err
	}()

	ch <- PullProgress{ID: "aaa", Status: "Pull complete"}
	ch <- PullProgress{Status: "Status: Downloaded newer image for nginx:latest"}
	advanceUntil(t, clk, 50*time.Millisecond, 400*time.Millisecond, func() bool { return sink.count() >= 1 })

	// 用户经显式取消端点下发 cancel（core 端点 → cancel 帧）→ 会话收摊 → 拉取用的
	// ctx 被中断，ImagePull 以 ctx 错误返回（替身与 SDK 同款）。
	m.OnFrame(&agentproto.CoreDockerFrame{
		SessionID: agentproto.DockerPullSessionID(pullRef),
		Op:        agentproto.DockerFrameOpCancel,
	})

	if err := pullResultOf(t, done); err != nil {
		t.Fatalf("daemon 已完成的拉取在迟到 cancel 后必须按成功结算，实际: %v", err)
	}
	if got := m.count(); got != 0 {
		t.Fatalf("取消必须立刻释放槽位: %d", got)
	}
}

// 证据二：收尾行随中断的连接一起丢了（ctx 取消直接关连接，缓冲里的尾数据不复存在）
// —— 用独立于流的事实补判：拉取前本机没有这个引用、现在有了 = 镜像落地 = 完成。
func TestPullImageCancelAfterImageLandedStaysSucceeded(t *testing.T) {
	ch := make(chan PullProgress, 8)
	// 观测序列：拉取前（第一问）= 本机没有；取消结算时（第二问）= 已经落地。
	api := &stubAPI{pullCh: ch, imageRefIDs: []string{"", "sha256:landed"}}
	w, m, clk, sink := newPullFixture(api)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), pullCmd())
		done <- err
	}()

	ch <- PullProgress{ID: "aaa", Status: "Extracting", Current: 90, Total: 100}
	advanceUntil(t, clk, 50*time.Millisecond, 400*time.Millisecond, func() bool { return sink.count() >= 1 })

	m.OnFrame(&agentproto.CoreDockerFrame{
		SessionID: agentproto.DockerPullSessionID(pullRef),
		Op:        agentproto.DockerFrameOpCancel,
	})

	if err := pullResultOf(t, done); err != nil {
		t.Fatalf("镜像已落地（引用从无到有）必须按成功结算，实际: %v", err)
	}
	if len(api.imageRefCalls) != 2 {
		t.Fatalf("完成判据必须做拉取前后两次对照，实际 %d 次: %v", len(api.imageRefCalls), api.imageRefCalls)
	}
}

// 反向守卫：重拉一个**本机已有**的镜像、被取消且 ID 没变 —— 不得因为「现在本机有
// 这个镜像」就说成功（那是反向的不诚实：重拉的更新意图并没有兑现）。
func TestPullImageCancelWithPreexistingImageStaysCanceled(t *testing.T) {
	ch := make(chan PullProgress, 8)
	api := &stubAPI{pullCh: ch, imageRefIDs: []string{"sha256:old1", "sha256:old1"}}
	w, m, clk, sink := newPullFixture(api)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), pullCmd())
		done <- err
	}()

	ch <- PullProgress{ID: "aaa", Status: "Downloading", Current: 10, Total: 100}
	advanceUntil(t, clk, 50*time.Millisecond, 400*time.Millisecond, func() bool { return sink.count() >= 1 })

	m.OnFrame(&agentproto.CoreDockerFrame{
		SessionID: agentproto.DockerPullSessionID(pullRef),
		Op:        agentproto.DockerFrameOpCancel,
	})

	var ee *ExecError
	if err := pullResultOf(t, done); !errors.As(err, &ee) || ee.Msg != "拉取已取消" {
		t.Fatalf("引用没变（旧镜像仍在）时必须记取消，实际: %v", err)
	}
}

// 反向守卫：拉取前查不了（daemon 抖动）= 没有对照基线 —— 单看「现在有镜像」不可作
// 判据（那可能是本来就有的），落回「取消」而不是编一条完成。
func TestPullImageCancelWithUnknownBaselineStaysCanceled(t *testing.T) {
	ch := make(chan PullProgress, 8)
	api := &stubAPI{pullCh: ch, imageRefErr: errors.New("daemon busy"), imageRefIDs: []string{"sha256:any"}}
	w, m, clk, sink := newPullFixture(api)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), pullCmd())
		done <- err
	}()

	ch <- PullProgress{ID: "aaa", Status: "Downloading", Current: 10, Total: 100}
	advanceUntil(t, clk, 50*time.Millisecond, 400*time.Millisecond, func() bool { return sink.count() >= 1 })

	m.OnFrame(&agentproto.CoreDockerFrame{
		SessionID: agentproto.DockerPullSessionID(pullRef),
		Op:        agentproto.DockerFrameOpCancel,
	})

	var ee *ExecError
	if err := pullResultOf(t, done); !errors.As(err, &ee) || ee.Msg != "拉取已取消" {
		t.Fatalf("没有拉取前基线时不得凭「现在有镜像」判完成，实际: %v", err)
	}
}

// isPullCompletionLine 是完成判据的**唯一**解析口（纯函数，逐条钉住形态）：
// 只认无层 id 的 "Status: …" 消息行 —— 带 id 的层行（"Pull complete" 也是层行）
// 与 daemon 的其它消息行都不算完成，Digest 行也刻意不算（它在引用更新之前发出，
// 那一小段窗口由 pullLanded 兜底）。
func TestIsPullCompletionLine(t *testing.T) {
	cases := []struct {
		name string
		in   PullProgress
		want bool
	}{
		{"下载完成的新镜像", PullProgress{Status: "Status: Downloaded newer image for nginx:latest"}, true},
		{"引用已是最新", PullProgress{Status: "Status: Image is up to date for nginx:latest"}, true},
		{"层行的 Pull complete 不算", PullProgress{ID: "aaa", Status: "Pull complete"}, false},
		{"Digest 行不算（在引用更新之前发）", PullProgress{Status: "Digest: sha256:abc"}, false},
		{"起手消息行不算", PullProgress{Status: "Pulling from library/nginx"}, false},
		{"层行状态与收尾同形也不算（有 id）", PullProgress{ID: "aaa", Status: "Status: x"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isPullCompletionLine(c.in); got != c.want {
				t.Fatalf("isPullCompletionLine(%+v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// TestSetSessionsNilFailsFast：会话管理器的构造契约 —— SetSessions(nil) 与
// 「从未注入」都必须 panic（fail fast）。
//
// 7c 删掉了「nil = 一期黑盒回退」分支后，这条测试取代了原先的
// TestPullImageWithoutSessionsFallsBackToPhase1（旧流程回归钉）：那时 nil 是
// 合法形态（进度透出只是增强，拉取必须照旧）；现在进度透出是写路径的常设依赖，
// 生产 Runtime.New 永远注入，nil 只可能是装配缺陷 —— 旧的守卫对象已不存在，
// 新的守卫对象是「缺陷必须立刻现形，而不是静默黑盒」。
func TestSetSessionsNilFailsFast(t *testing.T) {
	api := &stubAPI{}
	w := NewWriteExecutor(api, nil, "", "", nil)
	m, _ := newTestSessions(&frameSink{}, streamIdleTimeout)
	w.SetSessions(m) // 正常注入不 panic
	func() {
		defer func() { recover() }()
		w.SetSessions(nil) // 显式注入 nil = 装配缺陷
		t.Fatal("SetSessions(nil) 必须 panic")
	}()
	// 从未注入：执行器直接构造（不接替身）时，拉取路径必须 fail fast 而不是
	// 静默走黑盒 —— 这里用独立的执行器（上面那个已注入过）。
	w2 := NewWriteExecutor(api, nil, "", "", nil)
	func() {
		defer func() { recover() }()
		_, _ = w2.Do(context.Background(), pullCmd())
		t.Fatal("未注入会话管理器的拉取必须 panic")
	}()
}

// TestPullImageSessionIDMatchesCoreRegistration：agent 开的进度会话句柄必须等于
// core 在受理时预登记的同一个派生值（两端在同一把句柄上会面 —— 帧才找得到主人）。
func TestPullImageSessionIDMatchesCoreRegistration(t *testing.T) {
	ch := make(chan PullProgress, 4)
	api := &stubAPI{pullCh: ch}
	w, m, clk, sink := newPullFixture(api)
	_ = clk
	_ = sink

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), pullCmd())
		done <- err
	}()
	// 让 Do 走到开会话那一步（喂一条进度可见）。
	ch <- PullProgress{ID: "aaa", Status: "Downloading", Current: 1, Total: 100}
	waitFor(t, "会话建立", func() bool {
		return m.lookup(agentproto.DockerPullSessionID(pullRef)) != nil
	})
	if m.lookup(agentproto.DockerPullSessionID(pullRef)).kind != streamPull {
		t.Fatal("进度会话的 kind 必须是 streamPull")
	}
	close(ch)
	if err := pullResultOf(t, done); err != nil {
		t.Fatalf("拉取成功不得返回错误: %v", err)
	}
}

// ── adapter：JSON 进度流的逐行解析 ────────────────────────────────────────

// TestConsumePullStreamParsesProgressLines：多层多行的 daemon JSON 流逐行折成
// 本域记录（字段逐项保真，progressDetail 缺席 = 0）；错误行折成**读满后的返回错误**
// 而不作为进度行；EOF 正常收口返回 nil。
func TestConsumePullStreamParsesProgressLines(t *testing.T) {
	stream := strings.Join([]string{
		`{"status":"Pulling from library/nginx"}`,
		`{"id":"aaa","status":"Pulling fs layer","progressDetail":{"current":4096,"total":8192},` +
			`"progress":"[====>  ] 4kB/8kB"}`,
		`{"id":"bbb","status":"Downloading"}`,
		`{"status":"Digest: sha256:abc"}`,
		``,
	}, "\n")

	var got []PullProgress
	if err := consumePullStream(strings.NewReader(stream), func(p PullProgress) { got = append(got, p) }); err != nil {
		t.Fatalf("EOF 收口必须是 nil 错误: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("应折出 4 条记录，实际 %d: %+v", len(got), got)
	}
	if got[0].ID != "" || got[0].Status != "Pulling from library/nginx" {
		t.Fatalf("消息行不符: %+v", got[0])
	}
	if got[1].ID != "aaa" || got[1].Status != "Pulling fs layer" ||
		got[1].Current != 4096 || got[1].Total != 8192 {
		t.Fatalf("progressDetail 必须逐项保真: %+v", got[1])
	}
	if got[2].ID != "bbb" || got[2].Current != 0 || got[2].Total != 0 {
		t.Fatalf("progressDetail 缺席必须折叠成 0: %+v", got[2])
	}
	if got[3].Status != "Digest: sha256:abc" {
		t.Fatalf("Digest 行不符: %+v", got[3])
	}
}

// TestConsumePullStreamReturnsDaemonErrorLine：daemon 的错误行（errorDetail.message）
// 不产进度记录，读满后成为返回错误 —— 与 docker CLI 同口径（拉取的实际工作要到
// 流结束才落定，半途 abort 会把 daemon 的并发拉取槽位挂死）。
func TestConsumePullStreamReturnsDaemonErrorLine(t *testing.T) {
	stream := strings.Join([]string{
		`{"id":"aaa","status":"Downloading"}`,
		`{"errorDetail":{"code":1,"message":"denied: requested access to the resource is denied"},"error":"denied"}`,
		``,
	}, "\n")

	var emitted int
	err := consumePullStream(strings.NewReader(stream), func(PullProgress) { emitted++ })
	if err == nil || err.Error() != "denied: requested access to the resource is denied" {
		t.Fatalf("错误行必须折成返回错误（detail 原文优先）: %v", err)
	}
	if emitted != 1 {
		t.Fatalf("错误行不得作为进度记录产出，实际 %d 条", emitted)
	}
}

// TestConsumePullStreamBrokenLineIsError：半截损坏的流（每行都应是合法 JSON，
// 解不开 = 传输层坏了）返回解码错误而不是装作成功。
func TestConsumePullStreamBrokenLineIsError(t *testing.T) {
	err := consumePullStream(strings.NewReader(`{"id":"aaa","status":"Downloading"}`+"\nnot-json\n"), func(PullProgress) {})
	if err == nil {
		t.Fatal("损坏流必须返回错误")
	}
}

// ── 真截止（执行时限到点）的结算（2026 收口：与迟到 cancel 同一张表）───────────

// TestPullImageDeadlineAfterImageLandedStaysSucceeded：时限到点（会话仍在）落在
// 镜像落地**之后** —— 时限记的是「我们不再等」，不是「它没干完」：完成的事实优先，
// 终态 Done 项与 eof 照发（页面不会看到一条没有终态的流）。
func TestPullImageDeadlineAfterImageLandedStaysSucceeded(t *testing.T) {
	ch := make(chan PullProgress, 8)
	api := &stubAPI{
		pullCh:      ch,
		pullErr:     errors.New("context deadline exceeded"),
		imageRefIDs: []string{"", "sha256:landed"},
	}
	w, _, _, sink := newPullFixture(api)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), pullCmd())
		done <- err
	}()
	ch <- PullProgress{ID: "aaa", Status: "Extracting", Current: 90, Total: 100}
	close(ch) // 会话还在、以 pullErr 收场（真截止）

	if err := pullResultOf(t, done); err != nil {
		t.Fatalf("镜像已落地时真截止也必须按完成结算: %v", err)
	}
	fs := sink.all()
	if len(fs) == 0 || !fs[len(fs)-1].EOF {
		t.Fatal("完成档必须补发终态项与 eof")
	}
	term := pullLinesOf(t, fs[len(fs)-1])
	if last := term[len(term)-1]; !last.Done {
		t.Fatalf("终态项必须恰在流末且 Done: %+v", term)
	}
}

// 反向守卫：真截止且没有落地证据 —— 结论句仍是失败（这是真失败/真截止的措辞，
// 与取消分档），终态 Error 项挂流末。
func TestPullImageDeadlineWithoutLandingStaysFailed(t *testing.T) {
	ch := make(chan PullProgress, 8)
	api := &stubAPI{
		pullCh:      ch,
		pullErr:     errors.New("context deadline exceeded"),
		imageRefIDs: []string{"sha256:old", "sha256:old"},
	}
	w, _, _, sink := newPullFixture(api)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), pullCmd())
		done <- err
	}()
	ch <- PullProgress{ID: "aaa", Status: "Downloading", Current: 10, Total: 100}
	close(ch)

	var ee *ExecError
	if err := pullResultOf(t, done); !errors.As(err, &ee) || ee.Msg != "拉取镜像失败" ||
		ee.Detail != "context deadline exceeded" {
		t.Fatalf("无落地证据的真截止必须记失败（含 daemon 原文）: %v", err)
	}
	fs := sink.all()
	if len(fs) == 0 || !fs[len(fs)-1].EOF {
		t.Fatal("失败档的终态项与 eof 照发")
	}
	term := pullLinesOf(t, fs[len(fs)-1])
	if last := term[len(term)-1]; last.Done || last.Error == "" {
		t.Fatalf("终态项必须是 Error 档: %+v", term)
	}
}

// TestSettlementEvidenceSurvivesExpiredCommandCtx：**真截止**（指令 ctx 就此作废）
// 时结算判据必须仍然开得了口 —— 判据查询走 evidenceCtx（摘掉取消信号、只留自己的
// 时限），而不是那条已经被时限作废的 ctx。
//
// 由来（本机真机 e2e 才暴露的缺陷）：真截止与结算发生在同一刻，判据的每一次查询
// （ImageRefID / RepoDigests / tag 事件 / registry 探测）若沿用指令 ctx 会全部立刻
// 失败，一场「时限到点时其实已经干完」的活被记成失败 —— 与迟到 cancel 那类不诚实
// 正好反向。替身**尊重 ctx**（见 snapshot_test.go 的 stubAPI 说明）是这条守卫能成立
// 的前提：不尊重 ctx 的替身让缺陷在单测里隐身。
func TestSettlementEvidenceSurvivesExpiredCommandCtx(t *testing.T) {
	ch := make(chan PullProgress, 8)
	api := &stubAPI{
		pullCh:      ch,
		pullErr:     errors.New("context deadline exceeded"),
		imageRefIDs: []string{"", "sha256:landed"},
	}
	w, _, _, _ := newPullFixture(api)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := w.Do(ctx, pullCmd())
		done <- err
	}()
	<-ctx.Done() // 时限到点：指令 ctx 就此作废
	close(ch)    // 拉取以 ctx 错误收场，会话仍在（真截止）

	if err := pullResultOf(t, done); err != nil {
		t.Fatalf("真截止落在落地之后必须按成功结算（判据不能被作废的 ctx 掐死）: %v", err)
	}
	if len(api.imageRefCalls) != 2 {
		t.Fatalf("判据必须真的查到两次（开工前 + 结算），实际 %d: %v", len(api.imageRefCalls), api.imageRefCalls)
	}
}
