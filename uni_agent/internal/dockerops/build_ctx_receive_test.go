package dockerops

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── 构建上下文上传接收器（v1.3·agent 侧）────────────────────────────────
//
// 断言面：组装（字节逐位落盘）、sha256 终验、不匹配删除、早期形态闸（gzip 与
// 未压缩 tar 都收、两者都不是则拒）、序号纪律（乱序/重复/FINAL 之后）、中止与
// 断连清理、产物目录不可用的诚实失败，以及**回收**（v1.4 修正：由定时任务派发的
// system:transfer-cleanup 驱动，超龄判定与圈定范围不变；上传路径不再清扫 ——
// 行为面与源码面各一条守卫）。全部**不起真实 socket、不碰 docker daemon** ——
// 接收器只依赖 transferDir 一个事实源（写执行器提供）。

const testSession = uint64(1790000000000000888)

func newTestReceiver(t *testing.T, transferDir string, now func() time.Time) *buildCtxReceiver {
	t.Helper()
	w := NewWriteExecutor(nil, ParseProtected(""), transferDir, agentproto.DockerComposeFlavorPlugin, nil)
	if now == nil {
		now = time.Now
	}
	return newBuildCtxReceiver(w, nopLogger{}, now)
}

// gz 给一包「gzip 魔数 + 正文」的分片流字节。
func gz(b ...byte) []byte { return append([]byte{0x1f, 0x8b}, b...) }

// tarBytes 用标准库 writer 造一份**真的**未压缩 tar（ustar 魔数落在偏移 257）——
// 早期形态闸看的是真实 tar 字节，不是手搓的魔数假体。
func tarBytes(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func shaHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestBuildCtxReceiverAssemblesAndFinishes(t *testing.T) {
	dir := t.TempDir()
	r := newTestReceiver(t, dir, nil)

	payload := gz([]byte("hello build context")...)
	if err := r.Chunk(testSession, 1, false, payload); err != nil {
		t.Fatalf("chunk1: %v", err)
	}
	if err := r.Chunk(testSession, 2, true, []byte("-tail")); err != nil {
		t.Fatalf("chunk2: %v", err)
	}
	full := append(append([]byte{}, payload...), []byte("-tail")...)

	err := r.Finish(testSession, shaHex(full), int64(len(full)), agentproto.DockerBuildCtxTransferName(testSession))
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}

	// 产物在 transferDir、推导名下、字节逐位一致。
	got, err := os.ReadFile(filepath.Join(dir, agentproto.DockerBuildCtxTransferName(testSession)))
	if err != nil {
		t.Fatalf("产物读取: %v", err)
	}
	if string(got) != string(full) {
		t.Fatalf("产物 = %q, want %q", got, full)
	}

	// 会话已结：重复完成帧是早到/重复的形态，报错而不是二次删文件。
	if err := r.Finish(testSession, shaHex(full), int64(len(full)),
		agentproto.DockerBuildCtxTransferName(testSession)); err == nil {
		t.Fatal("已结会话的重复完成帧应报错")
	}
}

func TestBuildCtxReceiverFinishRejectionsDeleteProduct(t *testing.T) {
	payload := gz([]byte("content")...)
	cases := []struct {
		name    string
		prepare func(r *buildCtxReceiver) error
		finish  func(r *buildCtxReceiver) error
		wantErr bool
	}{
		{
			"尺寸不符", nil,
			func(r *buildCtxReceiver) error {
				return r.Finish(testSession, shaHex(payload), int64(len(payload))+1, agentproto.DockerBuildCtxTransferName(testSession))
			}, true,
		},
		{
			"哈希不符（传输损坏的规范形态）", nil,
			func(r *buildCtxReceiver) error {
				return r.Finish(testSession, shaHex([]byte("别的字节")), int64(len(payload)), agentproto.DockerBuildCtxTransferName(testSession))
			}, true,
		},
		{
			"产物名与推导名不符", nil,
			func(r *buildCtxReceiver) error {
				return r.Finish(testSession, shaHex(payload), int64(len(payload)), "build-ctx-777.tar.gz")
			}, true,
		},
		{
			"未收到 FINAL（通道断/分片缺失）", func(r *buildCtxReceiver) error {
				return r.Chunk(testSession, 1, false, payload)
			},
			func(r *buildCtxReceiver) error {
				return r.Finish(testSession, shaHex(payload), int64(len(payload)), agentproto.DockerBuildCtxTransferName(testSession))
			}, true,
		},
		{
			"形态从未定型（短于一个校验窗口的畸形流）", func(r *buildCtxReceiver) error {
				return r.Chunk(testSession, 1, true, []byte{0x1f})
			},
			func(r *buildCtxReceiver) error {
				return r.Finish(testSession, shaHex([]byte{0x1f}), 1, agentproto.DockerBuildCtxTransferName(testSession))
			}, true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			r := newTestReceiver(t, dir, nil)
			if tc.prepare != nil {
				if err := tc.prepare(r); err != nil {
					t.Fatalf("prepare: %v", err)
				}
			} else if err := r.Chunk(testSession, 1, true, payload); err != nil {
				t.Fatalf("chunk: %v", err)
			}
			err := tc.finish(r)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Finish err = %v, wantErr = %v", err, tc.wantErr)
			}
			if _, statErr := os.Stat(filepath.Join(dir, agentproto.DockerBuildCtxTransferName(testSession))); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("终验失败必须删除产物（stat err=%v）", statErr)
			}
		})
	}
}

// TestBuildCtxReceiverAcceptsPlainTar 钉住早期形态闸的**接受面**之一：未压缩 tar
// （ustar 魔数）与 gzip 一样被放行 —— docker build/load 对明文 tar 本就合法，且
// 从浏览器上传的镜像导出物可以是 `docker save` 的原生未压缩 tar。
func TestBuildCtxReceiverAcceptsPlainTar(t *testing.T) {
	payload := tarBytes(t, "Dockerfile", []byte("FROM scratch\n"))

	t.Run("单帧整传", func(t *testing.T) {
		dir := t.TempDir()
		r := newTestReceiver(t, dir, nil)
		if err := r.Chunk(testSession, 1, true, payload); err != nil {
			t.Fatalf("明文 tar 上传必须被接受: %v", err)
		}
		if err := r.Finish(testSession, shaHex(payload), int64(len(payload)),
			agentproto.DockerBuildCtxTransferName(testSession)); err != nil {
			t.Fatalf("Finish: %v", err)
		}
		// 判读用的窗口字节是拷贝，产物必须仍是逐位一致的完整流。
		got, err := os.ReadFile(filepath.Join(dir, agentproto.DockerBuildCtxTransferName(testSession)))
		if err != nil {
			t.Fatalf("产物读取: %v", err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("产物被形态窗口截断/改写：len=%d want=%d", len(got), len(payload))
		}
	})

	t.Run("小片跨帧攒够窗口再定型", func(t *testing.T) {
		dir := t.TempDir()
		r := newTestReceiver(t, dir, nil)
		// 窗口 = 262 字节 ⇒ 40 字节的分片要跨 7 帧才够看 ustar 魔数；
		// 攒够之前**不能**下结论（否则合法的明文 tar 会在前几帧被误杀）。
		const chunk = 40
		var seq uint64
		for off := 0; off < len(payload); off += chunk {
			end := off + chunk
			if end > len(payload) {
				end = len(payload)
			}
			seq++
			if err := r.Chunk(testSession, seq, end == len(payload), payload[off:end]); err != nil {
				t.Fatalf("分片 seq=%d（off=%d）: %v", seq, off, err)
			}
		}
		if err := r.Finish(testSession, shaHex(payload), int64(len(payload)),
			agentproto.DockerBuildCtxTransferName(testSession)); err != nil {
			t.Fatalf("Finish: %v", err)
		}
	})
}

// TestBuildCtxReceiverFormGateRejectsNonArchive 钉住早期形态闸的**拒绝面**：
// 既非 gzip 也非未压缩 tar 的内容，攒够一个校验窗口就立即弃会话（深度校验仍在
// scanBuildContext —— 这里拒的是「形态根本不是归档」，不必等构建时才发现）。
func TestBuildCtxReceiverFormGateRejectsNonArchive(t *testing.T) {
	dir := t.TempDir()
	r := newTestReceiver(t, dir, nil)

	// zip 头 + 足量正文：窗口内既无 gzip 魔数也无 ustar 魔数。
	notArchive := append([]byte("PK\x03\x04zip!"), make([]byte, buildCtxMagicWindow)...)
	if err := r.Chunk(testSession, 1, false, notArchive); err == nil {
		t.Fatal("既非 gzip 也非 tar 的内容必须拒绝")
	}
	if _, statErr := os.Stat(filepath.Join(dir, agentproto.DockerBuildCtxTransferName(testSession))); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("形态拒绝必须删掉半成品（stat err=%v）", statErr)
	}
	// 作废后的会话：后续分片一律「未知会话」。
	if err := r.Chunk(testSession, 2, true, []byte("late")); err == nil {
		t.Fatal("作废会话的后续分片应报未知会话")
	}
}

func TestBuildCtxReceiverSeqDiscipline(t *testing.T) {
	t.Run("跳号", func(t *testing.T) {
		dir := t.TempDir()
		r := newTestReceiver(t, dir, nil)
		if err := r.Chunk(testSession, 1, false, gz([]byte("a")...)); err != nil {
			t.Fatal(err)
		}
		if err := r.Chunk(testSession, 3, false, []byte("c")); err == nil {
			t.Fatal("跳号（1→3）必须作废会话")
		}
		if _, err := os.Stat(filepath.Join(dir, agentproto.DockerBuildCtxTransferName(testSession))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("作废后产物必须已删除: %v", err)
		}
	})

	t.Run("重复", func(t *testing.T) {
		dir := t.TempDir()
		r := newTestReceiver(t, dir, nil)
		if err := r.Chunk(testSession, 1, false, gz([]byte("a")...)); err != nil {
			t.Fatal(err)
		}
		if err := r.Chunk(testSession, 1, false, []byte("again")); err == nil {
			t.Fatal("重复 seq 必须作废会话")
		}
	})

	t.Run("FINAL 之后仍有分片", func(t *testing.T) {
		dir := t.TempDir()
		r := newTestReceiver(t, dir, nil)
		if err := r.Chunk(testSession, 1, true, gz([]byte("done")...)); err != nil {
			t.Fatal(err)
		}
		if err := r.Chunk(testSession, 2, false, []byte("late")); err == nil {
			t.Fatal("FINAL 之后的分片必须作废会话")
		}
	})

	t.Run("未知会话的非首帧", func(t *testing.T) {
		r := newTestReceiver(t, t.TempDir(), nil)
		if err := r.Chunk(999, 5, false, []byte("x")); err == nil {
			t.Fatal("未知会话的非首帧不应重建（会话只能由 seq=1 开启）")
		}
	})
}

func TestBuildCtxReceiverAbortAndDisconnectCleanup(t *testing.T) {
	t.Run("中止帧删半成品", func(t *testing.T) {
		dir := t.TempDir()
		r := newTestReceiver(t, dir, nil)
		if err := r.Chunk(testSession, 1, false, gz([]byte("half")...)); err != nil {
			t.Fatal(err)
		}
		r.Abort(testSession)
		if _, err := os.Stat(filepath.Join(dir, agentproto.DockerBuildCtxTransferName(testSession))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("中止后产物必须已删除: %v", err)
		}
		r.Abort(testSession) // 幂等：重复中止不 panic
	})

	t.Run("断连清理全部进行中的会话", func(t *testing.T) {
		dir := t.TempDir()
		r := newTestReceiver(t, dir, nil)
		if err := r.Chunk(111, 1, false, gz([]byte("a")...)); err != nil {
			t.Fatal(err)
		}
		if err := r.Chunk(222, 1, false, gz([]byte("b")...)); err != nil {
			t.Fatal(err)
		}
		r.AbortAll()
		for _, sess := range []uint64{111, 222} {
			if _, err := os.Stat(filepath.Join(dir, agentproto.DockerBuildCtxTransferName(sess))); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("断连清理漏了会话 %d: %v", sess, err)
			}
		}
		// 已完成产物不受断连波及（会话表里已不在）。
		if err := r.Chunk(333, 1, true, gz([]byte("c")...)); err != nil {
			t.Fatal(err)
		}
		if err := r.Finish(333, shaHex(gz([]byte("c")...)), int64(len(gz([]byte("c")...))), agentproto.DockerBuildCtxTransferName(333)); err != nil {
			t.Fatal(err)
		}
		r.AbortAll()
		if _, err := os.Stat(filepath.Join(dir, agentproto.DockerBuildCtxTransferName(333))); err != nil {
			t.Fatalf("已完成产物被断连清理误删: %v", err)
		}
	})
}

// ── 回收：定时任务驱动的 system:transfer-cleanup（v1.4 修正）─────────────
//
// 三条断言各自盯一件事，缺一不可：
//   - **动作真能清掉 ≥24h 的产物、且不动 <24h 的**（假时钟 + 临时目录，走真实
//     dispatcher → nodeExecutor → sweep 这条链，而不是直接调 sweep）；
//   - **暗线已除（行为面）**：上传路径不再清扫 —— 传完一份产物，超龄的那份必须
//     原样还在；
//   - **暗线已除（源码面）**：上传路径的函数体里不得出现 sweep 调用，且 sweep
//     函数本身仍在（回收逻辑不许被这次改写顺手删掉）。

// writeArtifact 造一份产物：写入给定字节数并把 mtime 拨到指定时刻。
func writeArtifact(t *testing.T, dir, name string, size int, mtime time.Time) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, bytes.Repeat([]byte("x"), size), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func TestTransferCleanupActionRemovesOnlyStaleArtifacts(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	sink := &collectSink{}
	// 走**完整装配**（Runtime.New + 配置下发 + dispatcher）：implementedActions、
	// 参数校验、successNoter、执行体转调 sweep 全在链上 —— 直接调 sweep 会漏掉
	// 「这个动作是否真的被登记为已实现」这一层（漏登记的现象是「该操作尚未开放」）。
	r := New(Deps{
		StateDir:   t.TempDir(),
		API:        &stubAPI{},
		SendState:  func(*agentproto.DockerState) error { return nil },
		SendResult: sink.send,
		Log:        testLogger(),
		Now:        func() time.Time { return now },
	})
	r.OnConfig(&agentproto.DockerConfig{ConfigVersion: 1, TransferDir: dir})

	old, fresh := agentproto.DockerBuildCtxTransferName(101), agentproto.DockerBuildCtxTransferName(202)
	oldPath := filepath.Join(dir, old)
	writeArtifact(t, dir, old, 4096, now.Add(-buildCtxSweepAge-1*time.Hour))            // 超龄 ✓ 应删
	writeArtifact(t, dir, fresh, 128, now.Add(-1*time.Hour))                            // 未超龄，保留
	writeArtifact(t, dir, "user-image.tar", 64, now.Add(-48*time.Hour))                 // save 的自命名产物，保留
	writeArtifact(t, dir, "build-ctx-oops.tar.gz", 64, now.Add(-48*time.Hour))          // 非推导名（会话号非十进制），保留
	if err := os.Mkdir(filepath.Join(dir, "build-ctx-999.tar.gz"), 0o700); err != nil { // 同名目录：保留
		t.Fatal(err)
	}

	run := func(ref string) *agentproto.DockerCmdResult {
		t.Helper()
		before := len(sink.all())
		r.OnCmd(&agentproto.DockerCmd{Ref: ref, Action: agentproto.DockerActionTransferCleanup})
		r.dispatcher().ExecuteNow(context.Background(), ref)
		got := sink.all()
		if len(got) != before+1 {
			t.Fatalf("回收指令应恰好回一条结果，实得 %d 条", len(got)-before)
		}
		return got[len(got)-1]
	}

	res := run("1")
	if !res.OK {
		t.Fatalf("回收应成功，实得 %+v", res)
	}
	if _, err := os.Stat(oldPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("超龄上传产物应被回收: %v", err)
	}
	for _, keep := range []string{fresh, "user-image.tar", "build-ctx-oops.tar.gz"} {
		if _, err := os.Stat(filepath.Join(dir, keep)); err != nil {
			t.Fatalf("回收误删了 %s: %v", keep, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "build-ctx-999.tar.gz")); err != nil {
		t.Fatalf("回收碰了同名目录: %v", err)
	}
	// 战果（条数 + 释放字节）必须落进 detail：这是这条指令唯一的结果面
	//（core 的定时任务把 res.Detail 原文记进日志）。
	if want := fmt.Sprintf("removed=1 freed_bytes=%d", int64(4096)); res.Detail != want {
		t.Fatalf("detail = %q, want %q", res.Detail, want)
	}
	if res.Payload != nil {
		t.Fatal("回收不带结构化载荷（没有读 payload 的消费者），战果只进 detail")
	}

	// 再跑一轮：没有超龄产物 = 成功且战果为零（不是「无事可做的报错」）。
	last := run("2")
	if !last.OK || last.Detail != "removed=0 freed_bytes=0" {
		t.Fatalf("空轮应成功且战果为零，实得 %+v", last)
	}
}

// 守卫（行为面）：**暗线已除** —— 上传不再清扫超龄产物。
// 复现被打破的那条前提：载入镜像复用同一通道后产物可到 GB 级，而「传完就不再传」的
// 机器再也没有任何东西触发清扫；回收必须由定时任务驱动，不能再寄生在上传路径上。
func TestUploadDoesNotSweepStaleArtifacts(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	r := newTestReceiver(t, dir, func() time.Time { return now })

	stale := agentproto.DockerBuildCtxTransferName(101)
	writeArtifact(t, dir, stale, 4096, now.Add(-buildCtxSweepAge-1*time.Hour))

	payload := gz([]byte("new upload")...)
	if err := r.Chunk(testSession, 1, true, payload); err != nil {
		t.Fatal(err)
	}
	if err := r.Finish(testSession, shaHex(payload), int64(len(payload)),
		agentproto.DockerBuildCtxTransferName(testSession)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, stale)); err != nil {
		t.Fatalf("上传路径不得再清扫超龄产物（回收已改为定时任务 docker-transfer-cleanup）: %v", err)
	}
}

// 守卫（源码面）：与 docker_cancel_test 的源码级守卫同款 —— 上传路径的**函数体**里
// 不得出现 sweep 调用（整文件级断言会被 sweep 函数自身的定义误伤，故按函数体扫）。
//
// 行为断言只盖住「一次上传」这条正常分支；源码断言把 drop/Abort/AbortAll 这些
// 失败与清理分支也一并盖住 —— 那几处正是「顺手补一句清扫」最可能被加回去的地方。
func TestUploadPathSourceHasNoSweepCall(t *testing.T) {
	src, err := os.ReadFile("build_ctx_receive.go")
	if err != nil {
		t.Fatalf("读取 build_ctx_receive.go 失败: %v", err)
	}
	text := string(src)
	for _, sig := range []string{
		"func (r *buildCtxReceiver) open(",
		"func (r *buildCtxReceiver) Chunk(",
		"func (r *buildCtxReceiver) Finish(",
		"func (r *buildCtxReceiver) drop(",
		"func (r *buildCtxReceiver) Abort(",
		"func (r *buildCtxReceiver) AbortAll(",
	} {
		body := funcBodyOf(t, text, sig)
		if strings.Contains(body, "sweep(") {
			t.Fatalf("%s 的函数体里出现了 sweep 调用 —— 回收只由 system:transfer-cleanup 驱动（定时任务 docker-transfer-cleanup）", sig)
		}
	}
	// 回收逻辑本身必须还在（本波只换驱动力，不删逻辑）：sweep 仍是本文件的成员，
	// 且它的唯一调用点在 nodeExecutor.cleanupTransfer 里（runtime.go）。
	if !strings.Contains(text, "func (r *buildCtxReceiver) sweep(") {
		t.Fatal("sweep 函数本身必须保留（由 system:transfer-cleanup 驱动）")
	}
	rt, err := os.ReadFile("runtime.go")
	if err != nil {
		t.Fatalf("读取 runtime.go 失败: %v", err)
	}
	if !strings.Contains(string(rt), "e.upload.sweep(") {
		t.Fatal("sweep 必须由 system:transfer-cleanup 的执行体驱动（runtime.go 的 cleanupTransfer）")
	}
}

// funcBodyOf 截出一个函数的函数体（花括号配对；找不到即判死）—— 源码级守卫用，
// 避免整文件级断言被同文件里的合法用法（sweep 的定义与它自己的注释）误伤。
func funcBodyOf(t *testing.T, src, sig string) string {
	t.Helper()
	idx := strings.Index(src, sig)
	if idx < 0 {
		t.Fatalf("源码里找不到 %s", sig)
	}
	open := strings.IndexByte(src[idx:], '{')
	if open < 0 {
		t.Fatalf("%s 之后没有函数体", sig)
	}
	depth := 0
	for i := idx + open; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[idx+open : i+1]
			}
		}
	}
	t.Fatalf("%s 的函数体不闭合", sig)
	return ""
}

func TestBuildCtxReceiverUnusableDirFailsHonestly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	r := newTestReceiver(t, dir, nil)
	if err := r.Chunk(testSession, 1, false, gz([]byte("x")...)); err == nil {
		t.Fatal("产物目录不可用必须拒绝（上传没有 /tmp 回退的附注通道）")
	}
	if r.sessions[testSession] != nil {
		t.Fatal("拒绝后不得残留会话状态")
	}
}

func TestBuildCtxReceiverSizeCapDefense(t *testing.T) {
	dir := t.TempDir()
	r := newTestReceiver(t, dir, nil)

	// 调小闸来驱动账目触线（不必真搬 GB 级字节：纪律同 adapter 的
	// maxBuildContextEntries）。
	old := maxBuildCtxUploadBytes
	maxBuildCtxUploadBytes = 16
	t.Cleanup(func() { maxBuildCtxUploadBytes = old })

	if err := r.Chunk(testSession, 1, false, gz(make([]byte, 10)...)); err != nil {
		t.Fatal(err)
	}
	if err := r.Chunk(testSession, 2, false, make([]byte, 10)); err == nil {
		t.Fatal("超尺寸账目必须作废会话（防御 core 侧被攻陷/漂移）")
	}
	if _, err := os.Stat(filepath.Join(dir, agentproto.DockerBuildCtxTransferName(testSession))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("超限作废后产物必须已删除: %v", err)
	}
}

// TestBuildCtxReceiverUploadCapIsCoarseTier 钉住 agent 侧尺寸闸的**口径**（v1.2.7）：
// 它是两档上传产物的**较大者**（协议 MaxDockerImageLoadBytes），而不是构建上下文
// 那一档 —— agent 不做用途分流（发送方是已认证的 core，见 maxBuildCtxUploadBytes
// 的取舍说明），这条闸的职责只剩「不让一次上传吃爆自己的盘」。
func TestBuildCtxReceiverUploadCapIsCoarseTier(t *testing.T) {
	if maxBuildCtxUploadBytes != int64(agentproto.MaxDockerImageLoadBytes) {
		t.Fatalf("agent 尺寸闸 = %d, want 较大者 %d",
			maxBuildCtxUploadBytes, int64(agentproto.MaxDockerImageLoadBytes))
	}
	if maxBuildCtxUploadBytes <= int64(agentproto.MaxDockerBuildContextBytes) {
		t.Fatalf("粗上界 %d 必须严格大于构建上下文档 %d（否则载入镜像的产物仍被拦）",
			maxBuildCtxUploadBytes, int64(agentproto.MaxDockerBuildContextBytes))
	}
}

// TestBuildCtxReceiverDiskRoomPrecheck 钉住**盘量预检**（v1.2.7）：盘不够时早失败、
// 结论句说得清「需多少、剩多少」，半成品删干净；探测本身失败则**放行**（读不到盘量
// 不是拒绝上传的理由）。全程用**可注入的假 statfs**，不碰真实磁盘。
func TestBuildCtxReceiverDiskRoomPrecheck(t *testing.T) {
	// setFree 注入固定/可变的可用字节数（err 非 nil = 模拟探测失败）。
	setFree := func(t *testing.T, free *int64, probeErr error) {
		t.Helper()
		old := buildCtxFreeBytes
		buildCtxFreeBytes = func(string) (int64, error) {
			if probeErr != nil {
				return 0, probeErr
			}
			return *free, nil
		}
		t.Cleanup(func() { buildCtxFreeBytes = old })
	}

	t.Run("开局即不足（连预留都占不出）→ 不建会话、不落文件", func(t *testing.T) {
		free := int64(1 << 20) // 1MB，低于 64MB 预留
		setFree(t, &free, nil)
		dir := t.TempDir()
		r := newTestReceiver(t, dir, nil)

		err := r.Chunk(testSession, 1, false, gz([]byte("hi")...))
		if err == nil {
			t.Fatal("盘量不足必须在开局就早失败")
		}
		if want := "可用空间不足：需 64MB，剩 1MB"; !strings.Contains(err.Error(), want) {
			t.Fatalf("结论句 = %q，必须含 %q（差多少要说得清）", err.Error(), want)
		}
		if _, statErr := os.Stat(filepath.Join(dir, agentproto.DockerBuildCtxTransferName(testSession))); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("盘量不足不得留下产物文件（stat err=%v）", statErr)
		}
		if r.sessions[testSession] != nil {
			t.Fatal("拒绝后不得残留会话状态")
		}
	})

	t.Run("组装期间盘被吃光 → 早于 ENOSPC 失败并删半成品", func(t *testing.T) {
		free := int64(buildCtxDiskReserveBytes) * 2
		setFree(t, &free, nil)
		dir := t.TempDir()
		r := newTestReceiver(t, dir, nil)

		first := gz(make([]byte, 4096)...)
		if err := r.Chunk(testSession, 1, false, first); err != nil {
			t.Fatalf("开局盘够时必须能开始: %v", err)
		}
		// 别的写入把盘吃掉了（只剩不到预留）——下一片必须在**写之前**被拒。
		free = buildCtxDiskReserveBytes - 1
		second := make([]byte, 4096)
		err := r.Chunk(testSession, 2, false, second)
		if err == nil {
			t.Fatal("盘量不足必须早失败（不等 ENOSPC）")
		}
		if !strings.Contains(err.Error(), diskShortText(int64(len(second))+buildCtxDiskReserveBytes, free)) {
			t.Fatalf("结论句 = %q，必须含 %q", err.Error(),
				diskShortText(int64(len(second))+buildCtxDiskReserveBytes, free))
		}
		if _, statErr := os.Stat(filepath.Join(dir, agentproto.DockerBuildCtxTransferName(testSession))); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("盘量不足作废后半成品必须删除（stat err=%v）", statErr)
		}
	})

	t.Run("探测失败放行（读不到盘量不是拒绝上传的理由）", func(t *testing.T) {
		free := int64(0)
		setFree(t, &free, errors.New("statfs 不可用"))
		dir := t.TempDir()
		r := newTestReceiver(t, dir, nil)

		payload := gz([]byte("no-probe")...)
		if err := r.Chunk(testSession, 1, true, payload); err != nil {
			t.Fatalf("探测失败必须放行（写失败仍是最后兜底）: %v", err)
		}
		if err := r.Finish(testSession, shaHex(payload), int64(len(payload)),
			agentproto.DockerBuildCtxTransferName(testSession)); err != nil {
			t.Fatalf("Finish: %v", err)
		}
	})

	t.Run("humanBytes 的可读形态", func(t *testing.T) {
		for _, tc := range []struct {
			n    int64
			want string
		}{
			{8 << 30, "8GB"},
			{512 << 20, "512MB"},
			{64 << 20, "64MB"},
			{3 << 20, "3MB"},
			{1500, "1.5KB"},
			{512, "512B"},
		} {
			if got := humanBytes(tc.n); got != tc.want {
				t.Fatalf("humanBytes(%d) = %q, want %q", tc.n, got, tc.want)
			}
		}
	})
}

// TestRuntimeBuildCtxHooksDelegateToReceiver 钉住 Runtime 侧四个 hook 的接线：
// OnBinaryFrame/OnBuildCtxFinish/OnBuildCtxAbort/OnDisconnected 是传输层与
// 接收器之间的**唯一**接口 —— 接线断了，传输层路由（transport 测试）与接收器
// 状态机（上方测试）各自全绿也毫无意义。
func TestRuntimeBuildCtxHooksDelegateToReceiver(t *testing.T) {
	dir := t.TempDir()
	var sink stateSink
	r := New(Deps{
		StateDir: t.TempDir(), SendState: sink.state, SendResult: sink.result,
		Log: nopLogger{}, Now: time.Now, Interval: 30 * time.Second,
	})
	// transferDir 走配置下发（与生产同路径：hello_ack → OnConfig）。
	r.OnConfig(&agentproto.DockerConfig{ConfigVersion: 1, TransferDir: dir})

	sess := uint64(4242)
	payload := gz([]byte("runtime-hook")...)
	// 分片 + 完成：产物应落盘。
	r.OnBinaryFrame(agentproto.BinaryFrame{SessionID: sess, Seq: 1, Final: true, Payload: payload})
	r.OnBuildCtxFinish(&agentproto.CoreDockerBuildCtxFinish{
		SessionID: sess, Name: agentproto.DockerBuildCtxTransferName(sess),
		SHA256Hex: shaHex(payload), SizeBytes: int64(len(payload)),
	})
	if _, err := os.Stat(filepath.Join(dir, agentproto.DockerBuildCtxTransferName(sess))); err != nil {
		t.Fatalf("钩子链产物未落盘: %v", err)
	}

	// 中止钩子：半成品删除。
	sess2 := uint64(4343)
	r.OnBinaryFrame(agentproto.BinaryFrame{SessionID: sess2, Seq: 1, Final: false, Payload: gz([]byte("half")...)})
	r.OnBuildCtxAbort(&agentproto.CoreDockerBuildCtxAbort{SessionID: sess2})
	if _, err := os.Stat(filepath.Join(dir, agentproto.DockerBuildCtxTransferName(sess2))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("中止钩子未删半成品: %v", err)
	}

	// 断连钩子：进行中的会话全清；已完成产物（sess）不动。
	sess3 := uint64(4646)
	r.OnBinaryFrame(agentproto.BinaryFrame{SessionID: sess3, Seq: 1, Final: false, Payload: gz([]byte("dropped")...)})
	r.OnDisconnected()
	if _, err := os.Stat(filepath.Join(dir, agentproto.DockerBuildCtxTransferName(sess3))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("断连钩子未删进行中的半成品: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, agentproto.DockerBuildCtxTransferName(sess))); err != nil {
		t.Fatalf("断连钩子误删已完成产物: %v", err)
	}

	// nil 载荷防御：四个钩子都不能被 nil 打崩（读循环里直接回调）。
	r.OnBuildCtxFinish(nil)
	r.OnBuildCtxAbort(nil)
}
