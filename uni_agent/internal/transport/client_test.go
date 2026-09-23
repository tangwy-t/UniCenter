package transport

import (
	"testing"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

func mkSample(ts int64) *agentproto.MetricsSample {
	return &agentproto.MetricsSample{T: ts}
}

// TestSendDropsOldestKeepsNewest 验证积压满时「丢最旧、保最新」。
//
// 这是 spec §4.3 明确要求的行为，也是断线补发的关键：
// 运维关心「现在怎么了」，5 分钟前的样本已经过时；
// 若丢最新，则断线恢复后图上永远停在断线那一刻，最该看的最新数据反而没了。
func TestSendDropsOldestKeepsNewest(t *testing.T) {
	c := New(Config{URL: "ws://unused", HeartbeatInterval: time.Second})

	// 塞满缓冲并再多塞 5 个
	total := outboxCap + 5
	for i := 0; i < total; i++ {
		c.Send(mkSample(int64(i + 1)))
	}

	if got := c.DropCount(); got != 5 {
		t.Errorf("丢弃计数 = %d, 期望 5", got)
	}
	if got := c.Pending(); got != outboxCap {
		t.Errorf("待发送 = %d, 期望 %d（缓冲应为满）", got, outboxCap)
	}

	// 缓冲里应该是最新的 outboxCap 个：时间戳最大的那些
	newest := int64(total)
	oldestKept := int64(total - outboxCap + 1)
	for i := 0; i < outboxCap; i++ {
		s := <-c.sendCh
		want := oldestKept + int64(i)
		if s.T != want {
			t.Errorf("第 %d 个样本 T=%d, 期望 %d（应保留最新的连续一段）", i, s.T, want)
		}
	}
	_ = newest
}

// TestSendNeverBlocks 验证 Send 永不阻塞。
//
// 断线时采集不能停（spec §4.3）。若 Send 会阻塞，采集 goroutine 会被
// 卡死，恢复连接后这段数据也补不回来 —— 而这正是最需要数据的时候。
func TestSendNeverBlocks(t *testing.T) {
	c := New(Config{URL: "ws://unused", HeartbeatInterval: time.Second})

	done := make(chan struct{})
	go func() {
		// 远超缓冲容量；若实现会阻塞，这里必然超时
		for i := 0; i < outboxCap*10; i++ {
			c.Send(mkSample(int64(i + 1)))
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Send 阻塞了 —— 断线时会卡死采集循环")
	}
}

// TestSendIgnoresNil 验证 nil 样本被忽略（不占据缓冲槽位）。
func TestSendIgnoresNil(t *testing.T) {
	c := New(Config{URL: "ws://unused", HeartbeatInterval: time.Second})
	c.Send(nil)
	if got := c.Pending(); got != 0 {
		t.Errorf("nil 样本不应入队，实际待发送 %d", got)
	}
}

// TestCurrentHelloExactlyOneCredential 是本包最重要的一条回归测试。
//
// 契约要求两个 token **恰有其一**：core 明确「歧义时不暗自取优先级」，
// 两个都发会被 4001 拒连。
//
// 这个 bug 极易漏测：首次注册时 agent_token 还是空的，恰好只发一个凭据，
// 于是「首次能连上」；直到重连时才两个都带，从此**再也连不上**。
// 现象是设备永久离线，而日志只有一句 unauthorized。
func TestCurrentHelloExactlyOneCredential(t *testing.T) {
	// 情况一：**首次注册**——配置里没有 agent token，只有 enroll token。
	// 这正是「首次能连上」的那次，也是最容易被误以为已经跑通的一次。
	c := New(Config{
		URL:               "ws://unused",
		EnrollToken:       "enroll-tok",
		HeartbeatInterval: time.Second,
	})
	c.SetHello(&agentproto.Hello{InstanceID: "x", Hostname: "h", OS: "linux", Arch: "amd64", AgentVersion: "v"})
	h := c.currentHello()
	if h.EnrollToken == "" {
		t.Error("无 agent token 时应带 enroll_token")
	}
	if h.AgentToken != "" {
		t.Errorf("无 agent token 时不应带 agent_token，实际 %q", h.AgentToken)
	}

	// 情况二：已有配置里的 agent token → 只发它
	c2 := New(Config{URL: "ws://unused", AgentToken: "agent-tok", EnrollToken: "enroll-tok", HeartbeatInterval: time.Second})
	c2.SetHello(&agentproto.Hello{InstanceID: "x", Hostname: "h", OS: "linux", Arch: "amd64", AgentVersion: "v"})
	h2 := c2.currentHello()
	if h2.AgentToken != "agent-tok" {
		t.Errorf("应优先带 agent_token，实际 %q", h2.AgentToken)
	}
	if h2.EnrollToken != "" {
		t.Errorf("带了 agent_token 就**不能**再带 enroll_token（凭据歧义 → 4001），实际 %q", h2.EnrollToken)
	}

	// 情况三：enroll 成功后（运行期签发）→ 只发新的 agent token
	c.mu.Lock()
	c.agentToken = "fresh-jwt"
	c.mu.Unlock()
	h3 := c.currentHello()
	if h3.AgentToken != "fresh-jwt" {
		t.Errorf("enroll 后应带新 agent_token，实际 %q", h3.AgentToken)
	}
	if h3.EnrollToken != "" {
		t.Errorf("enroll 后不得再带 enroll_token（凭据歧义 → 4001），实际 %q", h3.EnrollToken)
	}

	// 情况四：两者都没有 → 空凭据（core 会拒，但这是调用方配置缺失，
	// 不应由 currentHello 编造）
	c4 := New(Config{URL: "ws://unused", HeartbeatInterval: time.Second})
	c4.SetHello(&agentproto.Hello{InstanceID: "x", Hostname: "h", OS: "linux", Arch: "amd64", AgentVersion: "v"})
	h4 := c4.currentHello()
	if h4.EnrollToken != "" || h4.AgentToken != "" {
		t.Error("无任何凭据时不应编造")
	}
}

// TestSetHelloAndCurrentHello 验证凭据在每次连接时现取。
//
// 关键点：enroll 成功后 agent_token 变了。若 currentHello 返回启动时的快照，
// 重连会一直拿旧 token 去鉴权，直到被 CloseUnauthorized(4001) 拒掉。
func TestSetHelloAndCurrentHello(t *testing.T) {
	base := &agentproto.Hello{
		InstanceID: "abc123",
		Hostname:   "host1",
		OS:         "linux",
		Arch:       "amd64",
	}

	c := New(Config{
		URL:               "ws://unused",
		EnrollToken:       "enroll-tok",
		AgentToken:        "",
		HeartbeatInterval: time.Second,
	})
	c.SetHello(base)

	// 初始：只有 enroll token
	h := c.currentHello()
	if h == nil {
		t.Fatal("currentHello 返回 nil")
	}
	if h.EnrollToken != "enroll-tok" {
		t.Errorf("EnrollToken = %q", h.EnrollToken)
	}
	if h.AgentToken != "" {
		t.Errorf("尚未 enroll，AgentToken 应为空，实际 %q", h.AgentToken)
	}
	if h.InstanceID != "abc123" {
		t.Errorf("InstanceID = %q（静态字段应原样带出）", h.InstanceID)
	}

	// 模拟 enroll 成功：写回新 token
	c.mu.Lock()
	c.agentToken = "fresh-jwt"
	c.mu.Unlock()

	h2 := c.currentHello()
	if h2.AgentToken != "fresh-jwt" {
		t.Errorf("enroll 后 AgentToken = %q, 期望 fresh-jwt（重连必须用新凭据）", h2.AgentToken)
	}
	// 原对象不应被就地修改（避免凭据泄漏到别处）
	if base.AgentToken != "" {
		t.Errorf("原始 hello 被就地修改了：AgentToken = %q", base.AgentToken)
	}
}

// TestAgentTokenPrefersLive 验证 AgentToken() 返回当前有效凭据。
func TestAgentTokenPrefersLive(t *testing.T) {
	c := New(Config{URL: "ws://unused", AgentToken: "old", HeartbeatInterval: time.Second})
	if got := c.AgentToken(); got != "old" {
		t.Errorf("初始应为配置里的 token，实际 %q", got)
	}
	c.mu.Lock()
	c.agentToken = "new"
	c.mu.Unlock()
	if got := c.AgentToken(); got != "new" {
		t.Errorf("应优先返回运行期签发的 token，实际 %q", got)
	}
}

// TestReportInterval 验证 core 下发的周期被采用，未下发时为 0。
func TestReportInterval(t *testing.T) {
	c := New(Config{URL: "ws://unused", HeartbeatInterval: time.Second})
	if got := c.ReportInterval(); got != 0 {
		t.Errorf("未收到 ack 时应为 0，实际 %v", got)
	}

	// 模拟收到 hello_ack 下发的 15s
	ack := &agentproto.HelloAck{Accepted: true, ReportInterval: 15}
	raw, err := agentproto.NewMessage("1", agentproto.TypeCoreHelloAck, ack)
	if err != nil {
		t.Fatal(err)
	}
	b, err := raw.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	c.handleFrame(b)

	if got := c.ReportInterval(); got != 15*time.Second {
		t.Errorf("ReportInterval = %v, 期望 15s", got)
	}
	if got := c.AgentToken(); got != "" {
		t.Errorf("ack 未带 token，不应凭空产生，实际 %q", got)
	}
}

// TestHandleFrameIgnoresUnknown 验证未知消息类型不断开连接。
//
// spec §5.4：core 将来新增消息类型时，老 agent 必须继续工作。
// 若这里报错/关闭，core 的一次无害升级会让全量 agent 掉线。
func TestHandleFrameIgnoresUnknown(t *testing.T) {
	c := New(Config{URL: "ws://unused", HeartbeatInterval: time.Second})

	// 构造一条类型合法但未注册的消息
	msg := &agentproto.Message{
		V:    agentproto.CurrentVersion,
		ID:   "123",
		Type: "core.something_new",
		TS:   time.Now().UnixMilli(),
	}
	b, err := msg.Marshal()
	if err != nil {
		t.Fatalf("构造未知消息: %v", err)
	}

	// 不应 panic，也不应改变状态
	c.handleFrame(b)
	if got := c.ReportInterval(); got != 0 {
		t.Errorf("未知消息不应改变状态，ReportInterval = %v", got)
	}
}

// TestHandleFrameRejectsMalformed 验证坏帧被丢弃而不是崩溃。
func TestHandleFrameRejectsMalformed(t *testing.T) {
	c := New(Config{URL: "ws://unused", HeartbeatInterval: time.Second})
	// 非法 JSON、以及信封字段缺失的消息
	c.handleFrame([]byte("{not json"))
	c.handleFrame([]byte(`{"v":1}`))
	c.handleFrame([]byte(`{}`))
	// 能走到这里说明没有 panic
}

// TestHandleFrameRejectedAck 验证被拒的 hello_ack 不会把状态置为 ACTIVE。
//
// 被拒说明凭据/版本有问题，重试无益；若误置 ACTIVE，
// 后续上报会因协议违反而被 core 以 4008 关闭，错误现象会变得难以理解。
func TestHandleFrameRejectedAck(t *testing.T) {
	c := New(Config{URL: "ws://unused", HeartbeatInterval: time.Second})
	c.mu.Lock()
	c.state = stateAwaitAck
	c.mu.Unlock()

	ack := &agentproto.HelloAck{Accepted: false, RejectReason: "enroll token 无效"}
	raw, err := agentproto.NewMessage("1", agentproto.TypeCoreHelloAck, ack)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := raw.Marshal()
	c.handleFrame(b)

	c.mu.Lock()
	st := c.state
	c.mu.Unlock()
	if st == stateActive {
		t.Error("被拒的 hello_ack 不应把状态置为 ACTIVE")
	}
}

// TestNewIDIsDecimal 验证信封 id 符合契约。
//
// 契约要求 id 是「非空的十进制无符号整数串」（isDecimalID 拒绝其它形态），
// 且长度不超过 MaxIDLen(64)。用 UUID 之类的带连字符形式会被直接拒掉。
func TestNewIDIsDecimal(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		id := newID()
		if id == "" {
			t.Fatal("id 为空")
		}
		if len(id) > agentproto.MaxIDLen {
			t.Fatalf("id 过长（%d > %d）: %q", len(id), agentproto.MaxIDLen, id)
		}
		for _, ch := range id {
			if ch < '0' || ch > '9' {
				t.Fatalf("id 含非十进制字符: %q", id)
			}
		}
		if seen[id] {
			t.Fatalf("id 重复: %q", id)
		}
		seen[id] = true
	}
}

// TestJitterWithinRange 验证抖动落在 [0.5d, 1.5d)。
//
// 抖动的用途是打散重连时刻：没有它，core 重启会让所有 agent
// 在同一毫秒一起重连（惊群），把刚起来的服务再打挂一次。
func TestJitterWithinRange(t *testing.T) {
	base := 10 * time.Second
	lo := base / 2
	hi := base + base/2
	for i := 0; i < 500; i++ {
		got := jitter(base)
		if got < lo || got >= hi {
			t.Fatalf("jitter(%v) = %v, 超出 [%v, %v)", base, got, lo, hi)
		}
	}
	if got := jitter(0); got != 0 {
		t.Errorf("jitter(0) = %v, 期望 0", got)
	}
}
