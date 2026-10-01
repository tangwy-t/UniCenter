package service

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	"github.com/tangwy-t/UniCenter/uni_core/internal/repository"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// stats 留存读面（ContainerStatsHistory）与设备删除连带清理（PurgeDevice）的服务层测试。
// 夹具复用 docker_overview_test.go 的 fakeDeviceReader/fakeConfigGetter（同一包）。

// statsHistoryDeviceReader 是 DockerDeviceReader 的替身：未命中返回仓储哨兵
// repository.ErrNotFound（fakeDeviceReader 返回的是普通错误，会把「设备不存在」
// 伪装成「读库故障」—— 本文件要钉的恰是 404 口径）。
type statsHistoryDeviceReader struct{ devs map[uint64]*entity.Device }

func (d statsHistoryDeviceReader) FindByID(_ context.Context, id uint64) (*entity.Device, error) {
	if dev, ok := d.devs[id]; ok {
		return dev, nil
	}
	return nil, repository.ErrNotFound
}

// newStatsHistorySvc 构造带留存读面的 DockerService 与写入口（同一 miniredis）。
func newStatsHistorySvc(t *testing.T) (*DockerService, *dockerstate.StatsHistoryStore, *dockerstate.Store, goredis.UniversalClient) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	store := dockerstate.NewStore(rdb)
	sh := dockerstate.NewStatsHistoryStore(rdb)
	now := time.Now()
	svc := NewDockerService(store, statsHistoryDeviceReader{devs: map[uint64]*entity.Device{
		7: {Hostname: "uni-105", LastSeenAt: &now},
	}}, fakeConfigGetter{vals: map[string]string{"sys.docker.snapshotInterval": "30"}}, nil).
		WithStatsHistory(sh)
	return svc, sh, store, rdb
}

// TestDockerServiceStatsHistory404AndEmpty 钉住读面的两条口径：设备不存在 → 404
// （与 State 同一句话）；无历史 → 空切片非 nil（正常答案，不是错误）。
func TestDockerServiceStatsHistory404AndEmpty(t *testing.T) {
	svc, _, _, _ := newStatsHistorySvc(t)
	ctx := context.Background()

	if _, err := svc.ContainerStatsHistory(ctx, 404, "any"); err == nil {
		t.Fatal("设备不存在必须返回错误")
	} else if !isNotFound(err) {
		t.Fatalf("设备不存在必须是 404 口径, got %v", err)
	}
	resp, err := svc.ContainerStatsHistory(ctx, 7, "never-seen")
	if err != nil {
		t.Fatalf("无历史是正常答案不是错误: %v", err)
	}
	if resp.Samples == nil || len(resp.Samples) != 0 {
		t.Fatalf("无历史必须空切片非 nil: %+v", resp.Samples)
	}
}

// isNotFound 判 404 口径（apperror 的 code 面，与其他服务层测试同一判法）。
func isNotFound(err error) bool {
	ae, ok := err.(*apperror.AppError)
	return ok && ae.Code == apperror.CodeNotFound
}

// TestDockerServiceStatsHistoryMapsFields 钉住映射：留存样本 → DTO 逐字段
// （t/cpu/mem/limit），不重算任何值 —— 抽屉曲线与表格读数必须对得上。
func TestDockerServiceStatsHistoryMapsFields(t *testing.T) {
	svc, sh, _, _ := newStatsHistorySvc(t)
	ctx := context.Background()
	base := time.UnixMilli(1730000000000)
	for f := 0; f < 2; f++ {
		at := base.Add(time.Duration(f) * 30 * time.Second)
		if err := sh.Record(ctx, 7, &agentproto.DockerState{T: at.UnixMilli(), DockerOK: true,
			Containers: []agentproto.DockerContainer{{ID: "c1", Name: "alpha", State: "running",
				CPUPercent: 3.5 + float64(f), MemUsageMB: 200, MemLimitMB: 512}}}, at); err != nil {
			t.Fatal(err)
		}
	}
	resp, err := svc.ContainerStatsHistory(ctx, 7, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Samples) != 2 {
		t.Fatalf("应有 2 个样本, got %d", len(resp.Samples))
	}
	for f, sm := range resp.Samples {
		if sm.T != base.Add(time.Duration(f)*30*time.Second).UnixMilli() ||
			sm.CPUPercent != 3.5+float64(f) || sm.MemUsageMB != 200 || sm.MemLimitMB != 512 {
			t.Fatalf("样本 %d 映射不符: %+v", f, sm)
		}
	}
}

// TestDockerServicePurgeDeviceCleansStatsHistory 钉住删除设备的连带清理：
// 快照键与 stats 留存序列（桶 + 环）一并消失 —— 留存键带 TTL，不清只是晚
// 30 分钟消失，但删除是明确动作，主动清掉比等兜底快。
func TestDockerServicePurgeDeviceCleansStatsHistory(t *testing.T) {
	svc, sh, store, rdb := newStatsHistorySvc(t)
	ctx := context.Background()
	at := time.Now()
	if err := store.Save(ctx, 7, &agentproto.DockerState{T: at.UnixMilli(), DockerOK: true,
		Containers: []agentproto.DockerContainer{{ID: "c1", Name: "alpha", State: "running"}}}, at); err != nil {
		t.Fatal(err)
	}
	if err := sh.Record(ctx, 7, &agentproto.DockerState{Containers: []agentproto.DockerContainer{
		{ID: "c1", State: "running", CPUPercent: 1}}}, at); err != nil {
		t.Fatal(err)
	}
	if err := svc.PurgeDevice(ctx, 7); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{dockerstate.StateKey(7), dockerstate.StatsRingKey(7, "c1")} {
		if n, _ := rdb.Exists(ctx, key).Result(); n != 0 {
			t.Fatalf("PurgeDevice 后键 %s 必须删除", key)
		}
	}
}
