package agentproto

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

// ── 二进制帧：头格式钉死 / 解析拒绝 / 回环 ───────────────────────────────
//
// 这组测试是上传通道的**线上格式锁**：头布局以字节偏移逐位断言（协议演进时
// 「动了哪个字节」会立刻红），解析拒绝逐类断言哨兵（两端读循环据 errors.Is
// 决定「断连 / 弃会话 / 忽略」）。乱序/重复/中止的**状态机语义**在 agent 侧
// 组装器测试（uni_agent/internal/dockerops/build_ctx_receive_test.go）——
// 协议层只保证「每一条单帧都能被无歧义地解析或拒绝」。

func TestBinaryFrameHeaderLayoutPinned(t *testing.T) {
	payload := []byte("hello-build-context")
	frame, err := PackBinaryFrame(0x1122334455667788, 42, true, payload)
	if err != nil {
		t.Fatal(err)
	}
	if want := DockerBuildCtxFrameHeaderBytes + len(payload); len(frame) != want {
		t.Fatalf("帧长 = %d, want %d", len(frame), want)
	}
	// 头布局逐位钉死（大端序号与会话号）。
	if frame[0] != 'B' || frame[1] != 'C' {
		t.Fatalf("魔数 = %q, want \"BC\"", frame[:2])
	}
	if frame[2] != 1 {
		t.Fatalf("版本 = %d, want 1", frame[2])
	}
	if frame[3]&DockerBuildCtxFlagFinal == 0 {
		t.Fatalf("FINAL 位未置: flags=%#x", frame[3])
	}
	if got := binary.BigEndian.Uint64(frame[4:12]); got != 0x1122334455667788 {
		t.Fatalf("会话号 = %#x, want %#x（头偏移 4..12）", got, uint64(0x1122334455667788))
	}
	if got := binary.BigEndian.Uint64(frame[12:20]); got != 42 {
		t.Fatalf("序号 = %d, want 42（头偏移 12..20）", got)
	}
	if !bytes.Equal(frame[20:], payload) {
		t.Fatalf("载荷 = %q, want %q（偏移 20 起）", frame[20:], payload)
	}
}

func TestBinaryFrameRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		session, seq uint64
		final        bool
		payload      []byte
	}{
		{7, 1, false, []byte{}},
		{7, 2, false, []byte{0x00, 0xff, 0x1f, 0x8b}},
		{7, 3, true, []byte("final-chunk")},
	} {
		frame, err := PackBinaryFrame(tc.session, tc.seq, tc.final, tc.payload)
		if err != nil {
			t.Fatalf("pack: %v", err)
		}
		got, err := UnpackBinaryFrame(frame)
		if err != nil {
			t.Fatalf("unpack: %v", err)
		}
		if got.SessionID != tc.session || got.Seq != tc.seq || got.Final != tc.final {
			t.Fatalf("头回环不一致: %+v", got)
		}
		if !bytes.Equal(got.Payload, tc.payload) {
			t.Fatalf("载荷回环不一致: %q vs %q", got.Payload, tc.payload)
		}
	}
}

func TestUnpackBinaryFrameRejections(t *testing.T) {
	good, err := PackBinaryFrame(9, 1, false, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	mutate := func(fn func(b []byte) []byte) []byte { return fn(append([]byte{}, good...)) }

	cases := []struct {
		name string
		raw  []byte
		want error
	}{
		{"过短：0 字节", nil, ErrBinaryFrameTooShort},
		{"过短：19 字节", good[:19], ErrBinaryFrameTooShort},
		{"魔数错", mutate(func(b []byte) []byte { b[0] = 'X'; return b }), ErrBinaryFrameBadMagic},
		{"魔数第二字节错", mutate(func(b []byte) []byte { b[1] = 0; return b }), ErrBinaryFrameBadMagic},
		{"版本 0", mutate(func(b []byte) []byte { b[2] = 0; return b }), ErrBinaryFrameUnsupportedVersion},
		{"版本 2", mutate(func(b []byte) []byte { b[2] = 2; return b }), ErrBinaryFrameUnsupportedVersion},
		{"未知标志位", mutate(func(b []byte) []byte { b[3] = 0x40; return b }), ErrBinaryFrameBadFlags},
		{"会话号 0", mutate(func(b []byte) []byte { binary.BigEndian.PutUint64(b[4:12], 0); return b }), ErrBinaryFrameBadSession},
		{"序号 0", mutate(func(b []byte) []byte { binary.BigEndian.PutUint64(b[12:20], 0); return b }), ErrBinaryFrameBadSeq},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := UnpackBinaryFrame(tc.raw)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want errors.Is(..., %v)", err, tc.want)
			}
		})
	}
}

func TestUnpackRejectsOversizePayload(t *testing.T) {
	raw := make([]byte, DockerBuildCtxFrameHeaderBytes+MaxDockerBuildCtxChunkBytes+1)
	raw[0], raw[1] = 'B', 'C'
	raw[2] = 1
	binary.BigEndian.PutUint64(raw[4:12], 5)
	binary.BigEndian.PutUint64(raw[12:20], 1)
	if _, err := UnpackBinaryFrame(raw); !errors.Is(err, ErrBinaryFramePayloadTooLarge) {
		t.Fatalf("超上限载荷 err = %v, want ErrBinaryFramePayloadTooLarge", err)
	}
}

func TestPackRejectsInvalidFrames(t *testing.T) {
	if _, err := PackBinaryFrame(0, 1, false, nil); !errors.Is(err, ErrBinaryFrameBadSession) {
		t.Fatalf("会话 0 err = %v", err)
	}
	if _, err := PackBinaryFrame(1, 0, false, nil); !errors.Is(err, ErrBinaryFrameBadSeq) {
		t.Fatalf("序号 0 err = %v", err)
	}
	big := make([]byte, MaxDockerBuildCtxChunkBytes+1)
	if _, err := PackBinaryFrame(1, 1, false, big); !errors.Is(err, ErrBinaryFramePayloadTooLarge) {
		t.Fatalf("超上限 err = %v", err)
	}
	// 上限恰好坐满：合法（分片纪律的边界）。
	exact := make([]byte, MaxDockerBuildCtxChunkBytes)
	if _, err := PackBinaryFrame(1, 1, true, exact); err != nil {
		t.Fatalf("恰好 256KB 应合法: %v", err)
	}
}

func TestUnpackCopiesPayload(t *testing.T) {
	frame, err := PackBinaryFrame(3, 1, false, []byte("abc"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := UnpackBinaryFrame(frame)
	if err != nil {
		t.Fatal(err)
	}
	frame[20] = 'X' // 改原缓冲：解包结果必须不受影响（读循环缓冲复用安全）
	if string(got.Payload) != "abc" {
		t.Fatalf("解包载荷被原缓冲污染: %q", got.Payload)
	}
}

// ── 产物文件名推导 ────────────────────────────────────────────────────────

func TestBuildCtxTransferNameRoundTrip(t *testing.T) {
	for _, id := range []uint64{1, 42, 1790000000000000888, ^uint64(0)} {
		name := DockerBuildCtxTransferName(id)
		if !IsDockerBuildContextFilename(name) {
			t.Fatalf("推导名 %q 应过 build 上下文文件名的契约白名单", name)
		}
		got, ok := ParseDockerBuildCtxTransferName(name)
		if !ok || got != id {
			t.Fatalf("Parse(%q) = (%d, %v), want (%d, true)", name, got, ok, id)
		}
	}
}

func TestParseBuildCtxTransferNameRejectsForeignShapes(t *testing.T) {
	for _, bad := range []string{
		"", "build-ctx-", "build-ctx-.tar.gz", "build-ctx-0.tar.gz", "build-ctx-abc.tar.gz",
		"build-ctx-1.tar", "build-ctx-1.gz", "xbuild-ctx-1.tar.gz", "build-ctx-1.tar.gz/..",
		"../build-ctx-1.tar.gz", "/build-ctx-1.tar.gz", "build-ctx-1\\stuff.tar.gz",
		"myproject.tar.gz", "build-ctx-1.tar.gz2", "build-context-1.tar.gz",
	} {
		if id, ok := ParseDockerBuildCtxTransferName(bad); ok {
			t.Fatalf("外来形态 %q 被误认成上传产物（id=%d）—— agent 必须只落推导名", bad, id)
		}
	}
}

// ── 控制消息的语义校验 ────────────────────────────────────────────────────

func TestBuildCtxControlValidate(t *testing.T) {
	sess := uint64(1001)
	fin := func() *CoreDockerBuildCtxFinish {
		return &CoreDockerBuildCtxFinish{
			SessionID: sess,
			Name:      DockerBuildCtxTransferName(sess),
			SHA256Hex: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			SizeBytes: 0,
		}
	}
	if err := fin().Validate(); err != nil {
		t.Fatalf("合法完成帧被拒: %v", err)
	}
	bad := []struct {
		name   string
		mutate func(f *CoreDockerBuildCtxFinish)
	}{
		{"会话 0", func(f *CoreDockerBuildCtxFinish) { f.SessionID = 0 }},
		{"名字非推导名", func(f *CoreDockerBuildCtxFinish) { f.Name = "whatever.tar.gz" }},
		{"哈希非 64hex", func(f *CoreDockerBuildCtxFinish) { f.SHA256Hex = "abc" }},
		{"哈希大写", func(f *CoreDockerBuildCtxFinish) { f.SHA256Hex = "E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855" }},
		{"大小为负", func(f *CoreDockerBuildCtxFinish) { f.SizeBytes = -1 }},
	}
	for _, tc := range bad {
		t.Run("完成帧/"+tc.name, func(t *testing.T) {
			f := fin()
			tc.mutate(f)
			if err := f.Validate(); err == nil {
				t.Fatalf("非法完成帧被放行: %s", tc.name)
			}
		})
	}

	if err := (&CoreDockerBuildCtxAbort{SessionID: sess}).Validate(); err != nil {
		t.Fatalf("合法中止帧被拒: %v", err)
	}
	if err := (&CoreDockerBuildCtxAbort{}).Validate(); err == nil {
		t.Fatal("会话 0 的中止帧被放行")
	}
}

func TestHexSHA256(t *testing.T) {
	h := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if !IsHexSHA256(h) {
		t.Fatal("合法 sha256 被拒")
	}
	for _, bad := range []string{"", "x" + h[1:], h[:63], h + "0", "zz" + h[2:]} {
		if IsHexSHA256(bad) {
			t.Fatalf("非法形态 %q 被放行", bad)
		}
	}
}

func TestNewMessageForBuildCtxControls(t *testing.T) {
	// 两条控制消息走信封通路（组 → 码 → 解码 → 载荷校验）一遍过：
	// 它们必须是**登记过的**类型，编解码链路与既有消息零差别。
	fin, err := NewMessage("1001", TypeCoreDockerBuildCtxFinish, &CoreDockerBuildCtxFinish{
		SessionID: 1001, Name: DockerBuildCtxTransferName(1001),
		SHA256Hex: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
	})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeTypedFor(fin, DirCoreToAgent)
	if err != nil {
		t.Fatalf("完成帧解码失败: %v", err)
	}
	if _, ok := decoded.(*CoreDockerBuildCtxFinish); !ok {
		t.Fatalf("类型不符: %T", decoded)
	}
	if _, err := DecodeTypedFor(fin, DirAgentToCore); !errors.Is(err, ErrWrongDirection) {
		t.Fatalf("完成帧在 agent→core 方向应报 ErrWrongDirection: %v", err)
	}

	abort, err := NewMessage("1002", TypeCoreDockerBuildCtxAbort, &CoreDockerBuildCtxAbort{SessionID: 1001})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeTypedFor(abort, DirCoreToAgent); err != nil {
		t.Fatalf("中止帧解码失败: %v", err)
	}
}

// FuzzUnpackBinaryFrame 断言解包不变量：随机字节要么干净拒绝（typed error），
// 要么解出与打包一致的帧 —— 绝不 panic、绝不返回半成品头。
func FuzzUnpackBinaryFrame(f *testing.F) {
	f.Add(uint64(1), uint64(1), false, []byte("seed"))
	f.Add(uint64(0), uint64(0), true, []byte{})
	f.Fuzz(func(t *testing.T, session, seq uint64, finalP bool, payload []byte) {
		if len(payload) > 64 {
			payload = payload[:64] // seed 截到小分片：覆盖合法打包 + Fuzz 自由段
		}
		frame, err := PackBinaryFrame(session, seq, finalP, payload)
		if err != nil {
			if !errors.Is(err, ErrBinaryFrameBadSession) &&
				!errors.Is(err, ErrBinaryFrameBadSeq) &&
				!errors.Is(err, ErrBinaryFramePayloadTooLarge) {
				t.Fatalf("打包拒绝必须是 typed error: %v", err)
			}
			return
		}
		got, err := UnpackBinaryFrame(frame)
		if err != nil {
			t.Fatalf("自产帧解包失败: %v", err)
		}
		if got.Final != finalP {
			t.Fatalf("FINAL 位回环不一致: %v vs %v", got.Final, finalP)
		}
	})
}

// 垃圾字节流：解包要么 typed 拒绝，要么给出一帧字段全部合法（无哨兵值）的结果。
func FuzzUnpackBinaryFrameGarbage(f *testing.F) {
	f.Add([]byte("not a frame at all"))
	f.Add(make([]byte, 100))
	f.Fuzz(func(t *testing.T, raw []byte) {
		got, err := UnpackBinaryFrame(raw)
		if err != nil {
			return
		}
		if got.SessionID == 0 || got.Seq == 0 {
			t.Fatal("解包成功却带着哨兵值字段")
		}
		if len(got.Payload) > MaxDockerBuildCtxChunkBytes {
			t.Fatal("解包成功却超单帧上限")
		}
	})
}