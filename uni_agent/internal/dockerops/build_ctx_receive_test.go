package dockerops

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── 构建上下文上传接收器（v1.3·agent 侧）────────────────────────────────
//
// 断言面：组装（字节逐位落盘）、sha256 终验、不匹配删除、gzip 魔数拒绝、
// 序号纪律（乱序/重复/FINAL 之后）、中止与断连清理、24h 清扫的圈定范围、
// 产物目录不可用的诚实失败。全部**不起真实 socket、不碰 docker daemon** ——
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
			"魔数从未凑齐（<2 字节的畸形流）", func(r *buildCtxReceiver) error {
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

func TestBuildCtxReceiverMagicRejectsImmediately(t *testing.T) {
	dir := t.TempDir()
	r := newTestReceiver(t, dir, nil)

	err := r.Chunk(testSession, 1, false, []byte("PK\x03\x04zip!"))
	if err == nil {
		t.Fatal("非 gzip 魔数必须拒绝")
	}
	if _, statErr := os.Stat(filepath.Join(dir, agentproto.DockerBuildCtxTransferName(testSession))); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("魔数拒绝必须删掉半成品（stat err=%v）", statErr)
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

func TestBuildCtxReceiverSweepOnlyStaleUploadArtifacts(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	r := newTestReceiver(t, dir, func() time.Time { return now })

	old, fresh := agentproto.DockerBuildCtxTransferName(101), agentproto.DockerBuildCtxTransferName(202)
	write := func(name string, mtime time.Time) {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	write(old, now.Add(-buildCtxSweepAge-1*time.Hour))     // 超龄 ✓ 应删
	write(fresh, now.Add(-1*time.Hour))                    // 未超龄，保留
	write("user-image.tar", now.Add(-48*time.Hour))        // save 的自命名产物，保留
	write("build-ctx-oops.tar.gz", now.Add(-48*time.Hour)) // 非推导名（会话号非十进制），保留
	if err := os.Mkdir(filepath.Join(dir, "build-ctx-999.tar.gz"), 0o700); err != nil {
		t.Fatal(err)
	} // 同名目录：保留

	// 清扫挂在 open 上（上传开头）。
	if err := r.Chunk(testSession, 1, false, gz([]byte("new upload")...)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, old)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("超龄上传产物应被清扫: %v", err)
	}
	for _, keep := range []string{fresh, "user-image.tar", "build-ctx-oops.tar.gz"} {
		if _, err := os.Stat(filepath.Join(dir, keep)); err != nil {
			t.Fatalf("清扫误删了 %s: %v", keep, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "build-ctx-999.tar.gz")); err != nil {
		t.Fatalf("清扫碰了同名目录: %v", err)
	}
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

	// 调小闸来驱动账目触线（不必真搬 512MB：纪律同 adapter 的 maxBuildContextEntries）。
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
