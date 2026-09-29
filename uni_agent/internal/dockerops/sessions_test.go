package dockerops

import (
	"sync"
	"testing"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── 测试用的可注入时钟 ──────────────────────────────────────────────────
//
// 帧整形（100ms/50ms 窗口）、令牌回填与 10 分钟空闲超时都必须能被**确定性**驱动：
// 靠 sleep 测这些等于把「阈值是否生效」押在 CI 的调度抖动上。

type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []fakeWaiter
}

type fakeWaiter struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(1790000000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	c.mu.Lock()
	if d <= 0 {
		c.mu.Unlock()
		ch <- c.Now()
		return ch
	}
	c.waiters = append(c.waiters, fakeWaiter{at: c.now.Add(d), ch: ch})
	c.mu.Unlock()
	return ch
}

// Advance 推进时钟并唤醒全部到期者。
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now
	fire := make([]fakeWaiter, 0, len(c.waiters))
	rest := c.waiters[:0]
	for _, w := range c.waiters {
		if !w.at.After(now) {
			fire = append(fire, w)
		} else {
			rest = append(rest, w)
		}
	}
	c.waiters = rest
	c.mu.Unlock()
	for _, w := range fire {
		w.ch <- now
	}
}

// frameSink 收集会话发出去的帧。
type frameSink struct {
	mu      sync.Mutex
	frames  []*agentproto.DockerFrame
	sendErr error
	// block 非 nil 时 send 在记下帧之后阻塞（模拟「传输层卡住」：泵被拖住、
	// 生产者继续写 —— 缓冲上限与切帧逻辑正是为这种时序准备的）。
	block chan struct{}
}

func (s *frameSink) send(f *agentproto.DockerFrame) error {
	s.mu.Lock()
	if s.sendErr != nil {
		s.mu.Unlock()
		return s.sendErr
	}
	s.frames = append(s.frames, f)
	block := s.block
	s.mu.Unlock()
	if block != nil {
		<-block
	}
	return nil
}

func (s *frameSink) all() []*agentproto.DockerFrame {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*agentproto.DockerFrame(nil), s.frames...)
}

func (s *frameSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.frames)
}

func (s *frameSink) data() string {
	out := ""
	for _, f := range s.all() {
		out += string(f.Data)
	}
	return out
}

// newTestSessions 造一个假时钟驱动的会话管理器。
func newTestSessions(sink *frameSink, idle time.Duration) (*SessionManager, *fakeClock) {
	clk := newFakeClock()
	m := newSessionManager(sessionConfig{
		send:        sink.send,
		log:         testLogger(),
		now:         clk.Now,
		after:       clk.After,
		idleTimeout: idle,
	})
	return m, clk
}

// advanceUntil 小步推进假时钟直到条件成立（每次推进后把执行权让给泵的 goroutine）。
//
// 为什么不是「一次性推进到目标」：泵注册窗口定时器与测试推进时钟之间有天然竞态
// （泵还没注册，一次性推进就白推了）。小步推进 + 让出执行权让测试对调度顺序免疫。
func advanceUntil(t *testing.T, clk *fakeClock, step, total time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	elapsed := time.Duration(0)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("推进时钟 %v 后条件仍未成立", total)
		}
		clk.Advance(step)
		elapsed += step
		if elapsed > total {
			elapsed = 0
		}
		time.Sleep(time.Millisecond)
	}
}

// advanceBy 小步推进时钟到指定时长（用于「这段时间内不该发生某事」的断言）。
func advanceBy(clk *fakeClock, total time.Duration) {
	step := total / 100
	if step <= 0 {
		step = total
	}
	for elapsed := time.Duration(0); elapsed < total; elapsed += step {
		clk.Advance(step)
		time.Sleep(200 * time.Microsecond)
	}
}

// 合帧是「先到先发」的双阈值：数据不够阈值就攒着（不早发），够 64KB 立刻发
// （不等 100ms 窗口）。
func TestSessionLogCoalescingUsesBothThresholds(t *testing.T) {
	sink := &frameSink{}
	m, clk := newTestSessions(sink, streamIdleTimeout)
	s, err := m.open(streamLog)
	if err != nil {
		t.Fatal(err)
	}

	// 三小块：不足阈值 → 攒着，一帧都不该出现。
	s.write([]byte("alpha"))
	s.write([]byte("beta"))
	s.write([]byte("gamma"))
	time.Sleep(30 * time.Millisecond)
	if got := sink.count(); got != 0 {
		t.Fatalf("不足合帧阈值不得提前发帧，实际 %d 帧", got)
	}

	// 时钟走完 100ms 窗口 → 三块合为一帧。
	advanceUntil(t, clk, 20*time.Millisecond, 400*time.Millisecond, func() bool { return sink.count() >= 1 })
	frames := sink.all()
	if len(frames) != 1 {
		t.Fatalf("窗口到期应恰好一帧，实际 %d", len(frames))
	}
	if string(frames[0].Data) != "alphabetagamma" || frames[0].Seq != 1 || frames[0].EOF {
		t.Fatalf("首帧不符: seq=%d eof=%v data=%q", frames[0].Seq, frames[0].EOF, frames[0].Data)
	}
	if !agentproto.IsDockerSessionID(frames[0].SessionID) {
		t.Fatalf("会话 id 必须过协议校验（URL 安全、≥16B）: %q", frames[0].SessionID)
	}

	// 一大块（≥64KB）立刻发：不等窗口。超上限的部分切成后续帧（单帧上限 64KB）。
	big := make([]byte, logFrameBytes+7)
	s.write(big)
	waitFor(t, "满阈值立刻冲刷", func() bool { return sink.count() >= 2 })
	advanceUntil(t, clk, 25*time.Millisecond, time.Second, func() bool {
		total := 0
		for _, f := range sink.all() {
			total += len(f.Data)
		}
		return total >= len("alphabetagamma")+len(big)
	})
	frames = sink.all()
	if frames[1].Seq != 2 {
		t.Fatalf("第二帧序号应为 2，实际 %d", frames[1].Seq)
	}
	if len(frames[1].Data) != logFrameBytes {
		t.Fatalf("第二帧应恰好是协议上限 %d 字节，实际 %d", logFrameBytes, len(frames[1].Data))
	}
	rest := ""
	for _, f := range frames[1:] {
		rest += string(f.Data)
	}
	if rest != string(big) {
		t.Fatalf("切帧不得丢数据：应 %d 字节，实际 %d", len(big), len(rest))
	}
}

// 单会话 ≤15 帧/s：超出即丢最旧并计数（日志）。这里的「旧」是**未发送**的数据 ——
// 新到的数据会留在缓冲里等下一个额度。
func TestSessionLogRateCapDropsOldestAndCounts(t *testing.T) {
	sink := &frameSink{}
	m, clk := newTestSessions(sink, streamIdleTimeout)
	s, err := m.open(streamLog)
	if err != nil {
		t.Fatal(err)
	}

	// 每块 64KB：每写一块就立刻发一帧（满阈值先发）。15 帧把本秒额度用光。
	chunk := make([]byte, logFrameBytes)
	for i := 0; i < sessionFramesPerSec; i++ {
		s.write(chunk)
		waitFor(t, "满阈值帧", func() bool { return sink.count() >= i+1 })
	}
	if got := sink.count(); got != sessionFramesPerSec {
		t.Fatalf("首次突发应是 %d 帧，实际 %d", sessionFramesPerSec, got)
	}

	// 第 16 块：额度已尽 —— 该块（最旧）被丢弃，且不会立刻多出一帧。
	s.write(chunk)
	waitFor(t, "丢弃计数", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.droppedFrames > 0
	})
	time.Sleep(20 * time.Millisecond)
	if got := sink.count(); got != sessionFramesPerSec {
		t.Fatalf("额度用尽后不得立刻再发帧，实际 %d", got)
	}

	// 时钟走过一个回填周期 → 额度恢复后**新**数据能发出去（被丢的是最旧的那块）。
	advanceBy(clk, 100*time.Millisecond)
	s.write([]byte("fresh"))
	advanceUntil(t, clk, 50*time.Millisecond, 2*time.Second, func() bool { return sink.count() > sessionFramesPerSec })
	advanceBy(clk, 200*time.Millisecond)
	frames := sink.all()
	if len(frames) != sessionFramesPerSec+1 {
		t.Fatalf("回填后应恰好多发一帧（丢一块补一块），实际 %d 帧", len(frames))
	}
	if string(frames[len(frames)-1].Data) != "fresh" {
		t.Fatalf("发出去的必须是新数据（丢最旧），实际 %q", frames[len(frames)-1].Data)
	}
}

// PTY 不丢：反压而不是丢弃（传输层被拖住 → 缓冲填满 → 写方阻塞 → exec 读循环停读
// → 容器侧阻塞）。这里用「send 被卡住」复现真实时序：泵发不出去时生产者继续写。
func TestSessionPTYBackpressuresInsteadOfDropping(t *testing.T) {
	sink := &frameSink{block: make(chan struct{})}
	m, _ := newTestSessions(sink, streamIdleTimeout)
	s, err := m.open(streamPTY)
	if err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, ptyFrameBytes)
	// 第一块腾出空间并被泵取走（此时泵卡在 send 上）。
	s.writePTY(chunk)
	waitFor(t, "泵卡在发送", func() bool { return sink.count() >= 1 })
	fit := ptyPendingBytes / ptyFrameBytes
	done := make(chan int, 1)
	go func() {
		n := 0
		for i := 0; i < fit+3; i++ {
			if !s.writePTY(chunk) {
				break
			}
			n++
		}
		done <- n
	}()
	select {
	case n := <-done:
		t.Fatalf("PTY 写方未被反压（缓冲填满后仍写完了 %d 块）", n)
	case <-time.After(50 * time.Millisecond):
	}
	close(sink.block) // 传输层恢复
	select {
	case n := <-done:
		if n < fit {
			t.Fatalf("反压解除后应写完，实际 %d 块", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("缓冲释放后写方仍未继续")
	}
	if got := m.count(); got != 1 {
		t.Fatalf("会话不该因反压被结束，实际占用 %d", got)
	}
	s.mu.Lock()
	dropped := s.droppedBytes
	s.mu.Unlock()
	if dropped != 0 {
		t.Fatalf("PTY 不得丢数据，实际丢 %d 字节", dropped)
	}
}

// 并发会话上限 3：第 4 个必须被拒（结论句直接给用户看）；取消一个立刻能再开。
func TestSessionLimitThreeAndCancelReleasesSlot(t *testing.T) {
	sink := &frameSink{}
	m, _ := newTestSessions(sink, streamIdleTimeout)
	ids := make([]string, 0, maxStreamSessions)
	for i := 0; i < maxStreamSessions; i++ {
		s, err := m.open(streamLog)
		if err != nil {
			t.Fatalf("第 %d 个会话不该被拒: %v", i+1, err)
		}
		ids = append(ids, s.ID())
	}
	if _, err := m.open(streamLog); err != errStreamLimit {
		t.Fatalf("第 4 个会话必须被拒，实际 %v", err)
	}
	m.OnFrame(&agentproto.CoreDockerFrame{SessionID: ids[0], Op: agentproto.DockerFrameOpCancel})
	if got := m.count(); got != maxStreamSessions-1 {
		t.Fatalf("取消后槽位必须立刻释放，实际占用 %d", got)
	}
	if _, err := m.open(streamLog); err != nil {
		t.Fatalf("取消后应能再开会话: %v", err)
	}
	// 未知会话的控制帧：只忽略，不得 panic（core 与 agent 的释放之间有窗口）。
	m.OnFrame(&agentproto.CoreDockerFrame{SessionID: "unknown-session-id", Op: agentproto.DockerFrameOpCancel})
}

// 数据源自然终止 → eof=true 帧（可与末帧数据同帧），随后槽位释放。
func TestSessionEOFFlushesDataAndReleasesSlot(t *testing.T) {
	sink := &frameSink{}
	m, _ := newTestSessions(sink, streamIdleTimeout)
	s, err := m.open(streamLog)
	if err != nil {
		t.Fatal(err)
	}
	s.write([]byte("last line\n"))
	s.markEOF()
	waitFor(t, "eof 帧", func() bool {
		frames := sink.all()
		return len(frames) > 0 && frames[len(frames)-1].EOF
	})
	frames := sink.all()
	if len(frames) != 1 || string(frames[0].Data) != "last line\n" {
		t.Fatalf("数据必须与 eof 同帧发出: %+v", frames)
	}
	waitFor(t, "会话槽位释放", func() bool { return m.count() == 0 })
}

// 10 分钟无数据 → 空闲超时：发 eof 收尾并释放槽位（否则用户忘了关的终端会一直占额度）。
func TestSessionIdleTimeout(t *testing.T) {
	sink := &frameSink{}
	m, clk := newTestSessions(sink, 10*time.Minute)
	s, err := m.open(streamPTY)
	if err != nil {
		t.Fatal(err)
	}
	// 还没到期：推进 9 分钟不得超时。
	advanceBy(clk, 9*time.Minute)
	if m.count() != 1 {
		t.Fatal("9 分钟内不得超时")
	}
	s.write([]byte("x")) // 有活动 → 计时从零开始
	advanceBy(clk, 9*time.Minute)
	if m.count() != 1 {
		t.Fatal("活动后重新计时：不得在满 10 分钟时就超时")
	}
	advanceUntil(t, clk, time.Minute, 3*time.Minute, func() bool { return m.count() == 0 })
	frames := sink.all()
	if len(frames) == 0 || !frames[len(frames)-1].EOF {
		t.Fatalf("空闲超时必须发 eof 收尾，实际 %+v", frames)
	}
}

// 全局护栏：软闸（滑动窗口逼近 800 帧/分时拉长合帧间隔）+ 硬闸（令牌桶兜底）。
func TestFrameGovernorSoftBrakeAndHardCap(t *testing.T) {
	clk := newFakeClock()
	g := newFrameGovernor(clk.Now)
	if got := g.scale(); got != 1 {
		t.Fatalf("空载不得降速，实际 %d", got)
	}
	for i := 0; i < dockerScaleAt; i++ {
		g.record()
	}
	if got := g.scale(); got != 2 {
		t.Fatalf("逼近 640 帧/分应 ×2 拉长，实际 %d", got)
	}
	for i := 0; i < dockerScaleMaxAt-dockerScaleAt; i++ {
		g.record()
	}
	if got := g.scale(); got != 4 {
		t.Fatalf("达到 800 帧/分应 ×4 拉长，实际 %d", got)
	}
	// 窗口滑出 60 秒 → 恢复满速。
	clk.Advance(61 * time.Second)
	if got := g.scale(); got != 1 {
		t.Fatalf("窗口滑空后应恢复，实际 %d", got)
	}
	// 硬闸：把桶抽干后必须等待，等待期满才能再取。
	for i := 0; i < dockerBurstFrames+10; i++ {
		if wait, ok := g.acquire(); !ok {
			if wait <= 0 {
				t.Fatal("额度不足时必须给出正的等待时长")
			}
			clk.Advance(wait)
		}
		g.record()
	}
	if wait, ok := g.acquire(); !ok {
		clk.Advance(wait + time.Millisecond)
		if _, ok2 := g.acquire(); !ok2 {
			t.Fatal("等待期满后必须能取到额度")
		}
	}
}

// 软闸必须真的作用到会话上：全局压力大 → 合帧间隔按倍率拉长（而不是只改个计数）。
func TestSessionIntervalFollowsGovernor(t *testing.T) {
	sink := &frameSink{}
	m, _ := newTestSessions(sink, streamIdleTimeout)
	if got := m.frameInterval(streamLog); got != logFrameInterval {
		t.Fatalf("空载日志间隔应是 %v，实际 %v", logFrameInterval, got)
	}
	if got := m.frameInterval(streamPTY); got != ptyFrameInterval {
		t.Fatalf("空载 PTY 间隔应是 %v，实际 %v", ptyFrameInterval, got)
	}
	for i := 0; i < dockerScaleAt; i++ {
		m.governor.record()
	}
	if got := m.frameInterval(streamLog); got != 2*logFrameInterval {
		t.Fatalf("逼近阈值时日志间隔应 ×2，实际 %v", got)
	}
	for i := 0; i < dockerScaleMaxAt-dockerScaleAt; i++ {
		m.governor.record()
	}
	if got := m.frameInterval(streamPTY); got != 4*ptyFrameInterval {
		t.Fatalf("满速时 PTY 间隔应 ×4，实际 %v", got)
	}
}

// 端到端护栏：三个会话同时满速灌数据，任意 60 秒窗口的 docker 帧数必须 ≤890
// （桶容量 60 + 一分钟回填 800 = 860，留出余量给断言抖动）。
func TestSessionGlobalGuardrailKeepsFramesUnderCoreLimit(t *testing.T) {
	sink := &frameSink{}
	m, clk := newTestSessions(sink, streamIdleTimeout)
	sessions := make([]*streamSession, 0, maxStreamSessions)
	for i := 0; i < maxStreamSessions; i++ {
		s, err := m.open(streamPTY)
		if err != nil {
			t.Fatal(err)
		}
		sessions = append(sessions, s)
	}
	go func() {
		for i := 0; i < 4000; i++ {
			for _, s := range sessions {
				if !s.writePTY([]byte("0123456789")) {
					return
				}
			}
		}
	}()
	// 模拟 60 秒：每 50ms 让出一次（PTY 窗口 50ms）。
	deadline := time.Now().Add(10 * time.Second)
	for elapsed := time.Duration(0); elapsed < 60*time.Second; elapsed += 50 * time.Millisecond {
		clk.Advance(50 * time.Millisecond)
		if sink.count() > 0 && time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Microsecond)
	}
	// 再给泵一点真实时间把额度用尽（假时钟不再推进，硬闸不会放行）。
	time.Sleep(100 * time.Millisecond)
	total := sink.count()
	if total == 0 {
		t.Fatal("一个会话都没发出帧（护栏把路堵死了？）")
	}
	if total > dockerBurstFrames+dockerFramesPerMin+30 {
		t.Fatalf("60 秒内发出 %d 帧，超过「860 + 余量」的硬线", total)
	}
}

// 单帧绝不超过协议上限（MaxDockerFrameDataBytes）：日志缓冲可以攒到 256KB，
// 而 256KB 的整帧会被 core 的载荷校验直接拒掉 —— 用户少一段日志且 agent 毫无察觉。
// 故冲刷时必须切成多帧，且 eof 只能跟着最后一帧。
func TestSessionLogFlushSplitsOversizedFrames(t *testing.T) {
	sink := &frameSink{block: make(chan struct{})}
	m, clk := newTestSessions(sink, streamIdleTimeout)
	s, err := m.open(streamLog)
	if err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, logFrameBytes) // 64KB = 协议上限
	s.write(chunk)
	waitFor(t, "第一帧进入发送", func() bool { return sink.count() >= 1 })
	// 泵被传输层拖住时生产者继续写：pending 攒到 3×64KB。
	s.write(chunk)
	s.write(chunk)
	s.write(chunk)
	close(sink.block)
	waitFor(t, "残留数据全部发出", func() bool { return sink.count() >= 4 })
	advanceBy(clk, 200*time.Millisecond)

	total := 0
	for _, f := range sink.all() {
		if len(f.Data) > agentproto.MaxDockerFrameDataBytes {
			t.Fatalf("帧 %d 超协议上限: %d 字节", f.Seq, len(f.Data))
		}
		total += len(f.Data)
	}
	if total != 4*logFrameBytes {
		t.Fatalf("切帧不得丢数据：应 %d 字节，实际 %d", 4*logFrameBytes, total)
	}
	if got := sink.count(); got != 4 {
		t.Fatalf("4×64KB 应恰好切成 4 帧，实际 %d 帧", got)
	}
}

// eof 必须落在**最后一帧**上：切帧时若把 eof 挂在中间帧，消费端会提前收流丢尾巴。
func TestSessionEOFFollowsLastSplitFrame(t *testing.T) {
	sink := &frameSink{block: make(chan struct{})}
	m, _ := newTestSessions(sink, streamIdleTimeout)
	s, err := m.open(streamLog)
	if err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, logFrameBytes)
	s.write(chunk)
	waitFor(t, "第一帧进入发送", func() bool { return sink.count() >= 1 })
	s.write(chunk)
	s.write(chunk)
	s.markEOF()
	close(sink.block)
	waitFor(t, "全部帧发出", func() bool { return sink.count() >= 3 })
	waitFor(t, "eof 后释放槽位", func() bool { return m.count() == 0 })
	frames := sink.all()
	for i, f := range frames {
		if f.EOF && i != len(frames)-1 {
			t.Fatalf("eof 出现在第 %d/%d 帧（必须只挂最后一帧）", i+1, len(frames))
		}
	}
	if !frames[len(frames)-1].EOF {
		t.Fatalf("最后一帧必须带 eof，实际 %+v", frames[len(frames)-1])
	}
}
