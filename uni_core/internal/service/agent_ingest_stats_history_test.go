package service

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// 快照 ingest 的 stats 留存挂钩（P2）测试：观察者纪律（写失败不阻塞主链、
// 未装配零影响）与投影正确性（多容器多帧、running 过滤、t 与 ReceivedAt 同源）。
//
// SaveDockerState 不碰设备表与指标热层，故直接用 miniredis 上的两个 store 构造
// ingest 服务（repo/raw/latest 传 nil —— 本文件不触达它们）。

// newIngestWithStats 构造带 stats 留存挂钩的 ingest 服务（返回它写入的两个 store）。
func newIngestWithStats(t *testing.T) (*AgentIngestService, *dockerstate.StatsHistoryStore, *dockerstate.Store) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	store := dockerstate.NewStore(rdb)
	sh := dockerstate.NewStatsHistoryStore(rdb)
	svc := NewAgentIngestService(nil, nil, nil, store, nil, stubCfg{}, logger.NewNop()).
		WithDockerStatsHistory(sh)
	return svc, sh, store
}

// statsSnap 造一帧快照（cpu 用帧标识，cids 各自一个 running 容器）。
func statsSnap(cpu float64, cids ...string) *agentproto.DockerState {
	st := &agentproto.DockerState{T: time.Now().UnixMilli(), DockerOK: true}
	for _, cid := range cids {
		st.Containers = append(st.Containers, agentproto.DockerContainer{
			ID: cid, Name: "n-" + cid, State: "running",
			CPUPercent: cpu, MemUsageMB: 128, MemLimitMB: 512,
		})
	}
	return st
}

// TestSaveDockerStateRecordsStatsHistory 钉住挂钩的投影语义：
//   - 多容器各自成序、多帧按帧序追加（ingest 顺序 = 时间轴顺序）；
//   - 快照与留存**同一帧**落库（信封在、序列在），t 与 ReceivedAt 同源；
//   - 非运行容器不入序（agent 对它们本就没有读数，写进去只是零线噪音）。
func TestSaveDockerStateRecordsStatsHistory(t *testing.T) {
	svc, sh, store := newIngestWithStats(t)
	ctx := context.Background()

	for f := 0; f < 3; f++ {
		st := statsSnap(float64(f), "aaa", "bbb")
		st.Containers = append(st.Containers, agentproto.DockerContainer{
			ID: "stopped", Name: "n-stopped", State: "exited",
		})
		if err := svc.SaveDockerState(ctx, 7, st); err != nil {
			t.Fatal(err)
		}
	}

	for _, cid := range []string{"aaa", "bbb"} {
		got, err := sh.History(ctx, 7, cid)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 3 {
			t.Fatalf("容器 %s 应有 3 帧留存, got %d", cid, len(got))
		}
		for f, sm := range got {
			if sm.CPUPercent != float64(f) {
				t.Fatalf("帧序必须保留（样本 %d cpu got %v）—— 异步挂钩会让相邻帧赛跑", f, sm.CPUPercent)
			}
		}
	}
	// 停止容器不入序。
	if got, _ := sh.History(ctx, 7, "stopped"); len(got) != 0 {
		t.Fatalf("非运行容器不得入序, got %d", len(got))
	}

	// 快照主链与留存同帧落库，t 与 ReceivedAt 逐字对齐（页面的 lastSync 与曲线
	// 最后一格不出现一帧之差）。
	env, err := store.Get(ctx, 7)
	if err != nil || env == nil {
		t.Fatalf("快照必须照常落库: %v %+v", err, env)
	}
	got, _ := sh.History(ctx, 7, "aaa")
	if last := got[len(got)-1]; last.T != env.ReceivedAt {
		t.Fatalf("留存样本的 t 必须与 ReceivedAt 同源: %d vs %d", last.T, env.ReceivedAt)
	}
}

// TestSaveDockerStateStatsHookFailureNotBlocking 钉住观察者纪律：留存写失败
// （Redis 不可用）只告警，快照落库与 SaveDockerState 的返回值**零影响** ——
// 主链的答案永远是「快照是否落库成功」。
func TestSaveDockerStateStatsHookFailureNotBlocking(t *testing.T) {
	ctx := context.Background()

	// 留存指向死地址、快照指向活实例：两条路径分开验证「失败只影响挂钩自己」。
	dead := miniredis.RunT(t)
	deadAddr := dead.Addr()
	dead.Close()
	deadRdb := goredis.NewClient(&goredis.Options{Addr: deadAddr})
	t.Cleanup(func() { _ = deadRdb.Close() })

	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	store := dockerstate.NewStore(rdb)
	svc := NewAgentIngestService(nil, nil, nil, store, nil, stubCfg{}, logger.NewNop()).
		WithDockerStatsHistory(dockerstate.NewStatsHistoryStore(deadRdb))

	if err := svc.SaveDockerState(ctx, 9, statsSnap(2, "aaa")); err != nil {
		t.Fatalf("留存失败不得阻塞快照主链: %v", err)
	}
	env, err := store.Get(ctx, 9)
	if err != nil || env == nil {
		t.Fatalf("快照必须照常落库: %v %+v", err, env)
	}

	// 未装配（nil 挂钩）= 留存关闭：快照照常落库，行为与 P2 之前逐字一致。
	mr2 := miniredis.RunT(t)
	rdb2 := goredis.NewClient(&goredis.Options{Addr: mr2.Addr()})
	t.Cleanup(func() { _ = rdb2.Close() })
	store2 := dockerstate.NewStore(rdb2)
	sh2 := dockerstate.NewStatsHistoryStore(rdb2)
	svc2 := NewAgentIngestService(nil, nil, nil, store2, nil, stubCfg{}, logger.NewNop())
	if err := svc2.SaveDockerState(ctx, 7, statsSnap(3, "aaa")); err != nil {
		t.Fatal(err)
	}
	if env, err := store2.Get(ctx, 7); err != nil || env == nil {
		t.Fatalf("nil 挂钩下快照必须照常落库: %v %+v", err, env)
	}
	if got, _ := sh2.History(ctx, 7, "aaa"); len(got) != 0 {
		t.Fatalf("未装配的挂钩不得写入, got %d", len(got))
	}
}
