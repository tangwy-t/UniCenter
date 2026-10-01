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

// TestPullImageWithoutSessionsFallsBackToPhase1：流通道未装配（老装配/测试替身）时
// image:pull 保持一期黑盒语义 —— 成功回 ok、无载荷；失败回「拉取镜像失败」。
// 旧流程的回归钉：进度透出是增强，不是拉取的前提。
func TestPullImageWithoutSessionsFallsBackToPhase1(t *testing.T) {
	api := &stubAPI{}
	w := NewWriteExecutor(api, nil, "", "", nil) // 不注入会话
	payload, err := w.Do(context.Background(), pullCmd())
	if err != nil || payload != nil {
		t.Fatalf("无流通道的拉取成功必须照旧（空载荷、无错误）: %v %v", payload, err)
	}
	if len(api.pulled) != 1 || api.pulled[0] != "nginx:latest" {
		t.Fatalf("拉取调用不符: %v", api.pulled)
	}

	fail := &stubAPI{pullErr: errors.New("no such host")}
	w2 := NewWriteExecutor(fail, nil, "", "", nil)
	_, err = w2.Do(context.Background(), pullCmd())
	var ee *ExecError
	if !errors.As(err, &ee) || ee.Msg != "拉取镜像失败" || ee.Detail != "no such host" {
		t.Fatalf("无流通道的拉取失败结论句必须照旧: %v", err)
	}
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
