package agentmetrics

import (
	"context"
	"testing"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

func TestLatestStoreSetGetDelete(t *testing.T) {
	_, rdb := newTestRedis(t)
	store := NewLatestStore(rdb)
	ctx := context.Background()

	// 未写入时返回 nil, nil（列表页据此显示「—」）
	got, err := store.Get(ctx, 1001)
	if err != nil {
		t.Fatalf("Get 未命中不应报错: %v", err)
	}
	if got != nil {
		t.Fatalf("未写入时应为 nil, got %+v", got)
	}

	s := sample(t, 1_700_000_000_000, 42.5)
	s.Disks = []agentproto.DiskMetric{{Mountpoint: "/", TotalGB: 500, UsedGB: 300}}
	if err := store.Set(ctx, 1001, s); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err = store.Get(ctx, 1001)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("写入后必须能读回")
	}
	if got.CPUUsedPercent == nil || *got.CPUUsedPercent != 42.5 {
		t.Fatalf("cpu = %v, want 42.5", got.CPUUsedPercent)
	}
	if got.T != 1_700_000_000_000 {
		t.Fatalf("T = %d, want 采样时刻毫秒", got.T)
	}
	// 整机磁盘合计：Σused / Σtotal
	if got.DiskUsedPercent == nil {
		t.Fatal("有磁盘明细时应算出整机磁盘水位")
	}
	if *got.DiskTotalGB != 500 || *got.DiskUsedGB != 300 {
		t.Fatalf("磁盘合计不符: total=%v used=%v", *got.DiskTotalGB, *got.DiskUsedGB)
	}

	// 批量取：列表页用
	many, err := store.GetMany(ctx, []uint64{1001, 1002})
	if err != nil {
		t.Fatal(err)
	}
	if len(many) != 1 {
		t.Fatalf("GetMany 应只返回命中的 1 台, got %d", len(many))
	}

	if err := store.Delete(ctx, 1001); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got, _ := store.Get(ctx, 1001); got != nil {
		t.Fatal("Delete 后必须读不到")
	}
}

// TestLatestStorePurgedByRawStore 锁定「设备删除时的 Redis 连带清理」：
// RawStore.Purge 必须同时删掉原始窗与水位键（latestKey 定义在 raw.go，
// 与 Purge 的 Del 是同一条 key 约定的两半）。
//
// 先断言 Purge 前能读到，否则本测试会因 Set 失效而**空转通过**（vacuous）。
func TestLatestStorePurgedByRawStore(t *testing.T) {
	_, rdb := newTestRedis(t)
	latest := NewLatestStore(rdb)
	raw := NewRawStore(rdb, RawOptions{Step: 10 * time.Second, MaxPoints: 100})
	ctx := context.Background()

	if err := latest.Set(ctx, 1001, sample(t, 1_700_000_000_000, 42.5)); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got, err := latest.Get(ctx, 1001); err != nil || got == nil {
		t.Fatalf("Purge 前必须能读到水位（err=%v, got=%+v）", err, got)
	}

	if err := raw.Purge(ctx, 1001); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	got, err := latest.Get(ctx, 1001)
	if err != nil {
		t.Fatalf("Purge 后 Get 不应报错: %v", err)
	}
	if got != nil {
		t.Fatalf("Purge 必须连带删除水位键, got %+v", got)
	}
}
