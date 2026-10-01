package dockerstate

import (
	"context"
	"fmt"
	"testing"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// 最近受理索引（docker:cmd:recent）的测试：任务中心（6b）的枚举入口与容量纪律。
// 语义三点：写入与记录同事务、按受理时刻降序枚举、容量恒 ≤ RecentCmdKeep。

func recentRec(ref string, created int64) *CmdRecord {
	return &CmdRecord{Ref: ref, DeviceID: 7, UserID: 1,
		Action: "image:pull", Target: "alpine", Status: StatusPending, CreatedAt: created}
}

// TestRecentIndexOrder 钉住枚举序：受理最晚在前（ZREVRANGE），与 Create 的
// score=CreatedAt 构成「最近任务」的契约。
func TestRecentIndexOrder(t *testing.T) {
	_, mr := newTestStore(t)
	cs := NewCmdStore(mrClient(t, mr))
	ctx := context.Background()
	base := time.Now().UnixMilli()
	for i := 0; i < 5; i++ {
		if err := cs.Create(ctx, recentRec(fmt.Sprintf("r%d", i), base+int64(i)), 30*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	refs, err := cs.RecentRefs(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"r4", "r3", "r2", "r1", "r0"}
	if len(refs) != len(want) {
		t.Fatalf("枚举条目数不符, got %v", refs)
	}
	for i, r := range want {
		if refs[i] != r {
			t.Fatalf("枚举必须按受理时刻降序（最近在前）: [%d]=%s, want %s (all=%v)", i, refs[i], r, refs)
		}
	}
	if got, _ := cs.RecentRefs(ctx, 2); len(got) != 2 || got[0] != "r4" {
		t.Fatalf("limit 必须生效, got %v", got)
	}
}

// TestRecentIndexTrim 钉住容量纪律：超量 Create 后集合恒为最新的 RecentCmdKeep 条
// （裁剪与写入同事务 —— 容量账不靠读写方自觉）。
func TestRecentIndexTrim(t *testing.T) {
	_, mr := newTestStore(t)
	rdb := mrClient(t, mr)
	cs := NewCmdStore(rdb)
	ctx := context.Background()
	base := time.Now().UnixMilli()
	for i := 0; i < RecentCmdKeep+20; i++ {
		if err := cs.Create(ctx, recentRec(fmt.Sprintf("t%04d", i), base+int64(i)), 30*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	n, err := rdb.ZCard(ctx, RecentCmdKey).Result()
	if err != nil {
		t.Fatal(err)
	}
	if n != RecentCmdKeep {
		t.Fatalf("索引容量必须恒 ≤ %d, got %d", RecentCmdKeep, n)
	}
	// 最旧的报名被裁掉，最新的留下
	refs, _ := cs.RecentRefs(ctx, 1)
	if len(refs) != 1 {
		t.Fatal("RecentRefs 失败")
	}
	if score, _ := rdb.ZScore(ctx, RecentCmdKey, refs[0]).Result(); score != float64(base+RecentCmdKeep+19) {
		t.Fatalf("保留的必须是最新受理, got score %v", score)
	}
}

// TestRecentIndexSurvivesTerminalAndForget 钉住两条边界：
//   - 终态（Complete）记录**不**出索引 —— 任务中心要列的就是结果；
//   - ForgetRecent 精确剔除指定报名（读面发现记录过期时的自愈）。
func TestRecentIndexSurvivesTerminalAndForget(t *testing.T) {
	_, mr := newTestStore(t)
	cs := NewCmdStore(mrClient(t, mr))
	ctx := context.Background()
	base := time.Now().UnixMilli()
	rec := recentRec("f1", base)
	if err := cs.Create(ctx, rec, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := cs.Complete(ctx, rec, &agentproto.DockerCmdResult{Ref: rec.Ref, OK: true}); err != nil {
		t.Fatal(err)
	}
	refs, _ := cs.RecentRefs(ctx, 5)
	if len(refs) != 1 || refs[0] != "f1" {
		t.Fatalf("终态记录必须留在最近索引（任务中心列结果）: %v", refs)
	}
	if err := cs.ForgetRecent(ctx, "f1"); err != nil {
		t.Fatal(err)
	}
	if refs, _ := cs.RecentRefs(ctx, 5); len(refs) != 0 {
		t.Fatalf("ForgetRecent 必须精确剔除: %v", refs)
	}
	// 空索引/零 limit 的边界
	if err := cs.ForgetRecent(ctx, "no-such"); err != nil {
		t.Fatal(err)
	}
	if refs, _ := cs.RecentRefs(ctx, 0); refs != nil {
		t.Fatalf("limit≤0 返回 nil: %v", refs)
	}
}
