package dockerops

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	timetypes "github.com/docker/docker/api/types/time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── 完成证据框架的单元测试（判据原语 + 真流夹具）────────────────────────────
//
// 结算规则（谁在什么条件下算「干完了」）的执行器级测试在 build_push_test.go /
// pull_progress_test.go；本文件钉两件事：
//   - 原语本身的形态（镜像 ID 比对、收尾行按 tag 解析、构建读数、分母口径）；
//   - **真流夹具**：本机 29.6.0 daemon 实测捕获的两代 builder 输出原样喂进来，
//     判据的读数必须与真机一致 —— 这是「判据绑在 builder 措辞上」这件事的唯一防波堤
//     （除了真流，没有别的东西能保证 BuildKit 换代时判据不悄悄瞎掉）。

// ── 真流夹具（本机真 daemon 实测捕获，未做任何裁剪）────────────────────────
//
// 采集方式：对 docker daemon 29.6.0（containerd 存储）的 /build 端点直接读原始
// JSON 流（SDK 的 ImageBuild + io.ReadAll），上下文是 `FROM scratch` + 一个 COPY，
// 目标 tag cap-b:1 / cap-a:1。
//
// legacy builder（请求 version=1；daemon 在客户端未指定代际时默认也是它）：
const buildStreamLegacyFresh = `{"stream":"Step 1/2 : FROM scratch"}
{"stream":"\n"}
{"stream":" ---> \n"}
{"stream":"Step 2/2 : COPY hello.txt /hello.txt"}
{"stream":"\n"}
{"stream":" ---> ac8fcc79148b\n"}
{"aux":{"ID":"sha256:ac8fcc79148b3e73c2efd278a4a6b91cb8d23dcaf9d932a69585108fadd58715"}}
{"stream":"Successfully built ac8fcc79148b\n"}
{"stream":"Successfully tagged cap-b:1\n"}`

// legacy builder 的**全缓存命中**形态（对比上面只多一行 "Using cache"；
// 收尾三行一字不差 —— 缓存命中同样发 aux 与两条 Successfully 行）。
const buildStreamLegacyCacheHit = `{"stream":"Step 1/2 : FROM scratch"}
{"stream":" ---> Using cache\n"}
{"stream":" ---> ac8fcc79148b\n"}
{"aux":{"ID":"sha256:ac8fcc79148b3e73c2efd278a4a6b91cb8d23dcaf9d932a69585108fadd58715"}}
{"stream":"Successfully built ac8fcc79148b\n"}
{"stream":"Successfully tagged cap-c:1\n"}`

// BuildKit（请求 version=2）：没有 stream 行 —— 进度编码在
// `{"id":"moby.buildkit.trace","aux":"<base64 protobuf>"}` 里（本域刻意不解析，
// 这里原样保留一条实测样本，钉住「解析器不得被这种形态打崩」），收尾唯一一行是
// **产物 ID 的 aux 行**。
const buildStreamBuildKit = `{"id":"moby.buildkit.trace","aux":"Cm8KR3NoYTI1NjpiYmQzNTcyN2Q5ZjA1NjA1OWM4YjRiOGYxYWIyNTMwNDI2MWZiZjdhMmQ1NmViZjlmMjE1NDU0ZDIwY2Y0MzE2GiRbaW50ZXJuYWxdIGxvYWQgcmVtb3RlIGJ1aWxkIGNvbnRleHQ="}
{"id":"moby.buildkit.trace","aux":"CnkKR3NoYTI1NjpmMWM5ZDIzNTNhOWRlZjgwOTI2OGRmMjhhMjU4YzExNTA2ODcxMWE0ZmFiYzM1MjNkOGJhZDIxOTgyMDc3MjFjGhJleHBvcnRpbmcgdG8gaW1hZ2UqDAjyqIHWBhC3jvuDAg=="}
{"id":"moby.image.id","aux":{"ID":"sha256:3b7ada8d7f83cbcfcf1f93639ecead46e139e05260b32889b79a0e9392a4d4ea"}}`

// buildFactsFromStream 把一段真流夹具喂进 adapter 的解析器（consumeBuildStream）
// 与结算的读数盒（buildFacts）——与生产的接线同一条路（adapter 逐条 emit，
// 执行器逐条 note）。
func buildFactsFromStream(t *testing.T, stream, target string) (*buildFacts, []BuildProgress) {
	t.Helper()
	facts := &buildFacts{}
	var seen []BuildProgress
	err := consumeBuildStream(strings.NewReader(stream), func(b BuildProgress) {
		seen = append(seen, b)
		facts.note(b, target)
	})
	if err != nil {
		t.Fatalf("真流夹具必须干净解析（EOF 收口 = nil）: %v", err)
	}
	return facts, seen
}

// TestBuildStreamFixturesLegacy：legacy 两代形态（新构建 / 全缓存命中）的读数 ——
// 产物 ID（aux 的完整形态，优先于 "Successfully built" 的 12 位截断）与
// 「Successfully tagged 已到」两件事都必须取到；aux 读数**只带产物 ID**
// （没有 ID/Status/Stream = 不是进度行，执行器据此把它挡在帧面之外）。
func TestBuildStreamFixturesLegacy(t *testing.T) {
	for _, c := range []struct {
		name   string
		stream string
		target string
	}{
		{"新构建", buildStreamLegacyFresh, "cap-b:1"},
		{"全缓存命中", buildStreamLegacyCacheHit, "cap-c:1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			facts, seen := buildFactsFromStream(t, c.stream, c.target)
			builtID, tagged := facts.read()
			if builtID != "sha256:ac8fcc79148b3e73c2efd278a4a6b91cb8d23dcaf9d932a69585108fadd58715" {
				t.Fatalf("产物 ID 必须取到 aux 的完整形态: %q", builtID)
			}
			if !tagged {
				t.Fatal("legacy 的 Successfully tagged 行必须被认出来")
			}
			var auxReadings int
			for _, b := range seen {
				if b.ImageID == "" {
					continue
				}
				auxReadings++
				if b.ID != "" || b.Status != "" || b.Stream != "" {
					t.Fatalf("aux 读数只该带产物 ID（混进进度字段会让它进帧面）: %+v", b)
				}
			}
			if auxReadings != 1 {
				t.Fatalf("一场构建恰有一条 aux 产物读数，实际 %d: %+v", auxReadings, seen)
			}
		})
	}
}

// TestBuildStreamFixturesBuildKit：BuildKit 形态的读数 —— 产物 ID 来自
// moby.image.id 的 aux 行；trace 行的**字符串 aux** 不得让解析器崩（本域不解析
// protobuf，它是「有 id 无内容」的空壳记录）；没有 "Successfully tagged" 这种行，
// 那个读数必须保持 false（判据不许凭空自造）。
func TestBuildStreamFixturesBuildKit(t *testing.T) {
	facts, seen := buildFactsFromStream(t, buildStreamBuildKit, "cap-a:1")
	builtID, tagged := facts.read()
	if builtID != "sha256:3b7ada8d7f83cbcfcf1f93639ecead46e139e05260b32889b79a0e9392a4d4ea" {
		t.Fatalf("BuildKit 的产物 ID 必须从 moby.image.id 的 aux 行取到: %q", builtID)
	}
	if tagged {
		t.Fatal("BuildKit 没有 Successfully tagged 行：这个读数必须保持 false（判据不许凭空自造）")
	}
	var auxReadings int
	for _, b := range seen {
		if b.ImageID != "" {
			auxReadings++
			continue
		}
		// trace 行：有 id、无内容（它的进度编码在 base64 protobuf 里，刻意不解析）
		if b.Status != "" || b.Stream != "" {
			t.Fatalf("trace 行不该被编排出内容: %+v", b)
		}
	}
	if auxReadings != 1 {
		t.Fatalf("BuildKit 恰有一条产物 ID 读数（aux 行），实际 %d: %+v", auxReadings, seen)
	}
}

// TestBuildFactsShortIDFallback：aux 行丢失时退回 "Successfully built" 的 12 位
// 截断形态；已有完整 ID 时不被截断形态覆盖（aux 是权威读数）。
func TestBuildFactsShortIDFallback(t *testing.T) {
	var f buildFacts
	f.note(BuildProgress{Stream: "Successfully built ac8fcc79148b"}, "cap:1")
	builtID, _ := f.read()
	if builtID != "ac8fcc79148b" {
		t.Fatalf("截断形态必须兜底: %q", builtID)
	}
	f.note(BuildProgress{ImageID: "sha256:ac8fcc79148b3e73c2efd278a4a6b91cb8d23dcaf9d932a69585108fadd58715"}, "cap:1")
	builtID, _ = f.read()
	if builtID != "sha256:ac8fcc79148b3e73c2efd278a4a6b91cb8d23dcaf9d932a69585108fadd58715" {
		t.Fatalf("aux 的完整形态必须成为权威读数: %q", builtID)
	}
	// 反过来：完整 ID 已在，截断行不得把它覆盖回短形态。
	f.note(BuildProgress{Stream: "Successfully built cafebabe1234"}, "cap:1")
	if got, _ := f.read(); got != "sha256:ac8fcc79148b3e73c2efd278a4a6b91cb8d23dcaf9d932a69585108fadd58715" {
		t.Fatalf("aux 读数不得被后到的截断形态覆盖: %q", got)
	}
}

// TestBuildFactsTaggedTargetMatching：Successfully tagged 的引用名要与 target 归一
// 比对（daemon 打印的是它收到的原样引用，target 可能是另一种写法）。
func TestBuildFactsTaggedTargetMatching(t *testing.T) {
	var f buildFacts
	f.note(BuildProgress{Stream: "Successfully tagged app:1"}, "docker.io/library/app:1")
	if _, tagged := f.read(); !tagged {
		t.Fatal("同一引用的两种写法必须算命中")
	}
	var g buildFacts
	g.note(BuildProgress{Stream: "Successfully tagged other:1"}, "app:1")
	if _, tagged := g.read(); tagged {
		t.Fatal("别的仓库/别的 tag 的收尾行不得算命中")
	}
}

// TestImageIDMatches：镜像 ID 形态归一的四条（带不带 sha256:、12 位截断前缀、
// 不匹配、空值）。
func TestImageIDMatches(t *testing.T) {
	long := "sha256:ac8fcc79148b3e73c2efd278a4a6b91cb8d23dcaf9d932a69585108fadd58715"
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"同形", long, long, true},
		{"前缀形态不同", long, strings.TrimPrefix(long, "sha256:"), true},
		{"12 位截断", "sha256:ac8fcc79148b", long, true},
		{"截断但短于 12 位不认", "sha256:ac8fcc", long, false},
		{"不同产物", "sha256:deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef", long, false},
		{"空值", "", long, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := imageIDMatches(c.a, c.b); got != c.want {
				t.Fatalf("imageIDMatches(%q,%q) = %v want %v", c.a, c.b, got, c.want)
			}
		})
	}
}

// TestPushLineTag：收尾行按 tag 记名的解析 —— 形态取真机实测的
// "<tag>: digest: sha256:… size: N"；带层 id 的行、起手消息行、pull 的大写 Digest
// 行都不算；没有骨架的行也不算。
func TestPushLineTag(t *testing.T) {
	cases := []struct {
		name   string
		in     PullProgress
		tag    string
		wantOK bool
	}{
		{"真机收尾行", PullProgress{Status: "latest: digest: sha256:abc size: 476"}, "latest", true},
		{"整仓推送里按 tag 记名", PullProgress{Status: "2: digest: sha256:abc size: 9"}, "2", true},
		{"层行不算（有 id）", PullProgress{ID: "aaa", Status: "latest: digest: sha256:abc size: 1"}, "", false},
		{"起手消息行不算", PullProgress{Status: "The push refers to repository [h/a]"}, "", false},
		{"pull 的大写 Digest 行不算", PullProgress{Status: "Digest: sha256:abc"}, "", false},
		{"骨架在开头（没有 tag 前缀）不算", PullProgress{Status: ": digest: sha256:abc"}, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tag, ok := pushLineTag(c.in)
			if ok != c.wantOK || tag != c.tag {
				t.Fatalf("pushLineTag(%+v) = (%q,%v), want (%q,%v)", c.in, tag, ok, c.tag, c.wantOK)
			}
		})
	}
}

// TestPushDenominator：终态分母的并集口径 —— 应推集合之外**确实落地**的 tag
// （daemon 在推送那一刻多枚举到的）必须进分母，否则会出现 M > N 的自相矛盾结论。
func TestPushDenominator(t *testing.T) {
	tags := []string{"1", "2"}
	if n := pushDenominator(tags, map[string]bool{"1": true}); n != 2 {
		t.Fatalf("常规情形 N = 应推数: %d", n)
	}
	if n := pushDenominator(tags, map[string]bool{"1": true, "3": true}); n != 3 {
		t.Fatalf("多出来的已落 tag 必须进分母: %d", n)
	}
	if n := pushDenominator(nil, map[string]bool{"9": true}); n != 1 {
		t.Fatalf("应推集合缺席时 N = 已落数: %d", n)
	}
}

// TestPushAllTagsDecision：adapter 的 all=1（整仓推送）判定 —— 不带 tag 的仓库
// 引用走它；带 tag / digest 形态不走（SDK 对无 tag 引用会补 :latest，只推一个）。
//
// 由来（真机实测）：SDK 在 All=false 时对 "127.0.0.1:5000/multi" 发 tag=latest，
// daemon 只推 latest（仓库里另外两个 tag 一个不动）——与「不带 tag = 推全部本地 tag」
// 的契约相悖，正是这一判定把它纠正回来。
func TestPushAllTagsDecision(t *testing.T) {
	cases := []struct {
		ref  string
		want bool
	}{
		{"app:1", false},
		{"harbor.example.com/app:1", false},
		{"127.0.0.1:5000/ns/app:v1", false},
		{"app", true},
		{"harbor.example.com/app", true},
		{"127.0.0.1:5000/multi", true},
		{"app@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", false},
		{"", false}, // 解不开：交给 SDK 报错
	}
	for _, c := range cases {
		if got := pushAllTags(c.ref); got != c.want {
			t.Fatalf("pushAllTags(%q) = %v, want %v", c.ref, got, c.want)
		}
		// 契约的另一半：这个判定必须真的被 adapter 用到（**走 pushOptions 这条
		// 生产路径**）—— 只测判定函数会让「判定写对了但没接上」悄悄溜过去。
		opts, err := pushOptions(c.ref, nil)
		if err != nil {
			t.Fatalf("pushOptions(%q) 出错: %v", c.ref, err)
		}
		if opts.All != c.want {
			t.Fatalf("adapter 的推送选项必须带上这个判定: pushOptions(%q).All = %v, want %v", c.ref, opts.All, c.want)
		}
	}
}

// TestBuildJSONLineToleratesKitTraceAux：aux 键的两种形态都不得让整行解析失败 ——
// 对象（产物 ID）与字符串（BuildKit 的 base64 trace）走同一个字段。
func TestBuildJSONLineToleratesKitTraceAux(t *testing.T) {
	var line buildJSONLine
	if err := json.NewDecoder(bytes.NewReader([]byte(
		`{"id":"moby.buildkit.trace","aux":"Q2FwdHVyZWQ="}`))).Decode(&line); err != nil {
		t.Fatalf("字符串形态的 aux 不得让解析崩掉: %v", err)
	}
	if auxImageID(line.Aux) != "" {
		t.Fatal("字符串形态的 aux 取不出产物 ID（本域刻意不解析 protobuf）")
	}
	if err := json.NewDecoder(bytes.NewReader([]byte(
		`{"aux":{"ID":"sha256:abc"}}`))).Decode(&line); err != nil {
		t.Fatalf("对象形态的 aux 必须能解析: %v", err)
	}
	if got := auxImageID(line.Aux); got != "sha256:abc" {
		t.Fatalf("对象形态的 aux 必须取到 ID: %q", got)
	}
}

// TestSettleWriteTable：结算收口表的四条分支（完成 / 完成但会话已收摊 /
// 证据不成立且会话已收摊 / 证据不成立且会话还在）—— 三族共用这一张表，
// 措辞由调用方给（各族自己的两句）。
//
// 每条分支都在**真会话**上跑（假时钟 + 帧泵）：收口动作里有帧时序（终态项与 eof
// 的顺序），只有走真会话才测得到。
func TestSettleWriteTable(t *testing.T) {
	daemonErr := errors.New("ctx deadline exceeded")

	// fixture 造一套「写执行器 + 真会话 + 折叠器」：settleWrite 只碰会话与折叠器，
	// 执行器只是拿会话管理器的入口。
	fixture := func() (*SessionManager, *fakeClock, *frameSink, *streamSession, *progressFramer) {
		sink := &frameSink{}
		m, clk := newTestSessions(sink, streamIdleTimeout)
		sess, err := m.openNamed(streamPush, agentproto.DockerPushSessionID(pushRef))
		if err != nil {
			t.Fatal(err)
		}
		fr := newProgressFramer(sess, progressFramerConfig{
			encode: func(progressLine) []byte { return []byte("x\n") },
			term:   func(int64, bool, string) []byte { return []byte("term\n") },
		})
		return m, clk, sink, sess, fr
	}

	// ① 完成 + 会话还在：补终态 Done 项与 eof，返回 nil。
	_, clk, sink, sess, fr := fixture()
	done := make(chan error, 1)
	go func() { done <- settleWrite(true, false, fr, sess, "推送已取消", "推送镜像失败", daemonErr) }()
	advanceUntil(t, clk, 50*time.Millisecond, 400*time.Millisecond, func() bool {
		fs := sink.all()
		return len(fs) > 0 && fs[len(fs)-1].EOF
	})
	if err := <-done; err != nil {
		t.Fatalf("完成证据成立必须按成功收口: %v", err)
	}

	// ② 完成 + 会话已收摊：不发帧、不改终态，返回 nil（迟到 cancel 是 no-op）。
	_, _, sink2, sess2, fr2 := fixture()
	sess2.cancelBy("test")
	if err := settleWrite(true, true, fr2, sess2, "推送已取消", "推送镜像失败", daemonErr); err != nil {
		t.Fatalf("会话已收摊但完成事实成立时也必须成功: %v", err)
	}
	if len(sink2.all()) != 0 {
		t.Fatal("会话已收摊时无从发帧（也不该有帧）")
	}

	// ③ 证据不成立 + 会话已收摊：取消措辞。
	_, _, _, sess3, fr3 := fixture()
	sess3.cancelBy("test")
	var ee *ExecError
	if err := settleWrite(false, true, fr3, sess3, "推送已取消", "推送镜像失败", daemonErr); !errors.As(err, &ee) || ee.Msg != "推送已取消" {
		t.Fatalf("取消措辞不符: %v", err)
	}

	// ④ 证据不成立 + 会话还在：失败措辞 + daemon 原文进 detail（终态 Error 项照发）。
	m4, clk4, sink4, sess4, fr4 := fixture()
	done4 := make(chan error, 1)
	go func() {
		done4 <- settleWrite(false, false, fr4, sess4, "推送已取消", "推送镜像失败", daemonErr)
	}()
	advanceUntil(t, clk4, 50*time.Millisecond, 400*time.Millisecond, func() bool {
		fs := sink4.all()
		return len(fs) > 0 && fs[len(fs)-1].EOF
	})
	if err := <-done4; !errors.As(err, &ee) || ee.Msg != "推送镜像失败" || ee.Detail != "ctx deadline exceeded" {
		t.Fatalf("失败措辞与 detail 不符: %v", err)
	}
	if m4.count() != 0 {
		t.Fatalf("收口后槽位必须释放: %d", m4.count())
	}
}

// TestEventStampIsSdkParseable：事件窗口的时间戳形态必须过 **SDK 自己的解析器**
// （timetypes.GetTimestamp —— client.Events 在发请求前会拿它过一遍 since/until）。
//
// 由来（真机实测踩到、单测的替身看不见）：SDK 只认 RFC3339、纯 unix 秒整数与时长
// 串；「秒.纳秒」这种复合形态会被它**判错并让整个事件订阅失败** —— 症状是判据
// 静默失效（构建结算退回保守），而所有替身测试全绿。这条守卫直接调 SDK 的解析器，
// 把「形态必须被它接受」钉死在单元层。
func TestEventStampIsSdkParseable(t *testing.T) {
	now := time.Date(2026, 10, 3, 9, 30, 15, 123456789, time.UTC)
	s := eventStamp(now)
	if _, err := timetypes.GetTimestamp(s, time.Now()); err != nil {
		t.Fatalf("eventStamp 给的时间戳必须被 SDK 接受（RFC3339 族）: %q err=%v", s, err)
	}
	// 往返：解析回来仍是同一时刻（纳秒不丢，窗口边界才不会被磨掉）。
	back, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || !back.Equal(now) {
		t.Fatalf("时间戳往返不等: %v vs %v（err=%v）", back, now, err)
	}
}
