package service

import (
	"context"
	"errors"
	"testing"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// 宿主磁盘水位（Wave 4d：主机清单/总览的 diskUsedPercent/diskTotalGb）的 service 测试。
//
// 用内存替身实现 DockerHostDiskReader 而不是真库：本层要钉的是**服务语义**
//（一次批量取 / 缺数据缺省 / 与 docker 占用账目互不干扰 / 读失败降级），真 SQL 的
//「取每设备最近一条」由 repository 层测试（docker_host_disk_test.go）负责。

// fakeHostDiskReader 是 DockerHostDiskReader 的替身：按 deviceID 给预置宽表行，
// 并记录调用次数与入参（N+1 断言用）。
type fakeHostDiskReader struct {
	rows    map[uint64]entity.DeviceMetricWide
	err     error
	calls   int
	lastIDs []uint64
}

func (f *fakeHostDiskReader) LatestWideDisk(_ context.Context, ids []uint64) (map[uint64]entity.DeviceMetricWide, error) {
	f.calls++
	f.lastIDs = append([]uint64(nil), ids...)
	if f.err != nil {
		return nil, f.err
	}
	out := map[uint64]entity.DeviceMetricWide{}
	for _, id := range ids {
		if r, ok := f.rows[id]; ok {
			out[id] = r
		}
	}
	return out, nil
}

// TestDockerHostDiskWaterlineBatchNoNPlusOne 宿主磁盘水位**一次批量取**（无 N+1）：
// Hosts 一趟枚举里把全部 hostId 交给 LatestWideDisk 一次取回，逐台查是这条测试要挡的
// 失败形态。同时钉住结果等价（每台拿到自己那条水位）。
func TestDockerHostDiskWaterlineBatchNoNPlusOne(t *testing.T) {
	f := newResFixture(t)
	reader := &fakeHostDiskReader{rows: map[uint64]entity.DeviceMetricWide{
		7: {DeviceID: 7, DiskUsedPercent: f64(55), DiskTotalGB: f64(120)},
		8: {DeviceID: 8, DiskUsedPercent: f64(91.5), DiskTotalGB: f64(40)},
	}}
	f.svc.WithHostDiskMetrics(reader)
	f.saveState(t, 7, &agentproto.DockerState{})
	f.saveState(t, 8, &agentproto.DockerState{})

	resp, err := f.svc.Hosts(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	h7, h8 := hostByID(t, resp.List, 7), hostByID(t, resp.List, 8)
	if h7.DiskUsedPercent == nil || *h7.DiskUsedPercent != 55 || h7.DiskTotalGB == nil || *h7.DiskTotalGB != 120 {
		t.Fatalf("主机 7 的水位未逐字段带出: %+v", h7)
	}
	if h8.DiskUsedPercent == nil || *h8.DiskUsedPercent != 91.5 || h8.DiskTotalGB == nil || *h8.DiskTotalGB != 40 {
		t.Fatalf("主机 8 的水位未逐字段带出: %+v", h8)
	}
	// 一次批量取：多台主机只触发一次读（逐台是 N+1）。
	if reader.calls != 1 {
		t.Fatalf("批量取必须一次（无 N+1）: calls=%d", reader.calls)
	}
	// 入参覆盖枚举到的全部主机（两台都已上报快照）。
	seen := map[uint64]bool{}
	for _, id := range reader.lastIDs {
		seen[id] = true
	}
	if len(reader.lastIDs) != 2 || !seen[7] || !seen[8] {
		t.Fatalf("批量取应一次覆盖全部枚举到的主机: %+v", reader.lastIDs)
	}
}

// TestDockerHostDiskWaterlineMissingDataDefaults 缺数据一律缺省（omitempty）：
//   - 设备从未上报指标（不在 map 里）→ 两字段都 nil；
//   - 有行但该行未采集磁盘列（nil）→ 也缺省；
//
// 绝不填 0 ——「没有数据」与「磁盘 0%」是两件事。未装配（nil reader）同样缺省。
func TestDockerHostDiskWaterlineMissingDataDefaults(t *testing.T) {
	f := newResFixture(t)
	reader := &fakeHostDiskReader{rows: map[uint64]entity.DeviceMetricWide{
		7: {DeviceID: 7, DiskUsedPercent: f64(50), DiskTotalGB: f64(100)},
		// 主机 9：有行但磁盘列全 nil（未采集）—— 也应缺省。
		9: {DeviceID: 9},
	}}
	f.svc.WithHostDiskMetrics(reader)
	f.saveState(t, 7, &agentproto.DockerState{})
	f.saveState(t, 8, &agentproto.DockerState{}) // 主机 8 从未上报指标（无行）
	f.saveState(t, 9, &agentproto.DockerState{})

	resp, err := f.svc.Hosts(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if h8 := hostByID(t, resp.List, 8); h8.DiskUsedPercent != nil || h8.DiskTotalGB != nil {
		t.Fatalf("无指标行的主机必须缺省（不填 0）: %+v", h8)
	}
	if h9 := hostByID(t, resp.List, 9); h9.DiskUsedPercent != nil || h9.DiskTotalGB != nil {
		t.Fatalf("有行但未采集磁盘列也必须缺省: %+v", h9)
	}

	// 未装配读面：字段缺省且不 panic（水位是增强面，没接上不该让清单打不开）。
	bare := newResFixture(t)
	bare.saveState(t, 7, &agentproto.DockerState{})
	bresp, err := bare.svc.Hosts(bare.ctx)
	if err != nil {
		t.Fatalf("未装配磁盘水位读面不得报错: %v", err)
	}
	if h7 := hostByID(t, bresp.List, 7); h7.DiskUsedPercent != nil || h7.DiskTotalGB != nil {
		t.Fatalf("未装配时水位必须缺省: %+v", h7)
	}
}

// TestDockerHostDiskWaterlineIndependentFromDockerDiskUsage 水位与 docker 占用账目
// （Disk）**互不干扰**：两个方向都要钉 —— 有 df 账目的主机照常有水位（账目不被水位覆盖），
// 无 df 账目的主机照样有水位（水位不依赖 df）。
func TestDockerHostDiskWaterlineIndependentFromDockerDiskUsage(t *testing.T) {
	f := newResFixture(t)
	reader := &fakeHostDiskReader{rows: map[uint64]entity.DeviceMetricWide{
		7: {DeviceID: 7, DiskUsedPercent: f64(70), DiskTotalGB: f64(200)},
		8: {DeviceID: 8, DiskUsedPercent: f64(95), DiskTotalGB: f64(50)},
	}}
	f.svc.WithHostDiskMetrics(reader)
	// 主机 7 有 df 账目；主机 8 没有（旧版 agent 形态）。
	f.saveState(t, 7, &agentproto.DockerState{
		DiskUsage: &agentproto.DockerDiskUsage{ImagesTotalMB: 10, VolumesTotalMB: 2, BuildCacheMB: 1},
	})
	f.saveState(t, 8, &agentproto.DockerState{})

	resp, err := f.svc.Overview(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	h7 := hostByID(t, resp.Hosts, 7)
	if h7.Disk == nil || h7.Disk.ImagesMB != 10 || h7.Disk.VolumesMB != 2 || h7.Disk.BuildCacheMB != 1 {
		t.Fatalf("df 占用账目被水位污染或丢失: %+v", h7.Disk)
	}
	if h7.DiskUsedPercent == nil || *h7.DiskUsedPercent != 70 || h7.DiskTotalGB == nil || *h7.DiskTotalGB != 200 {
		t.Fatalf("有 df 的主机照常带宿主水位: %+v", h7)
	}
	h8 := hostByID(t, resp.Hosts, 8)
	if h8.Disk != nil {
		t.Fatalf("无 df 账目的主机 Disk 必须保持 nil（与水位无关）: %+v", h8.Disk)
	}
	if h8.DiskUsedPercent == nil || *h8.DiskUsedPercent != 95 || h8.DiskTotalGB == nil || *h8.DiskTotalGB != 50 {
		t.Fatalf("无 df 不影响宿主水位: %+v", h8)
	}
}

// TestDockerHostDiskWaterlineReadErrorDegrades 水位读失败**优雅降级**为缺省：
// 主机清单照样打得开（读面的底线是「agent 全下线也能开」，多一条硬依赖会把底线
// 换成 500）；水位缺席由条目上两字段缺省如实体现。
func TestDockerHostDiskWaterlineReadErrorDegrades(t *testing.T) {
	f := newResFixture(t)
	reader := &fakeHostDiskReader{err: errors.New("db down")}
	f.svc.WithHostDiskMetrics(reader)
	f.saveState(t, 7, &agentproto.DockerState{})

	resp, err := f.svc.Hosts(f.ctx)
	if err != nil {
		t.Fatalf("水位读失败不得让主机清单失败（降级为缺省）: %v", err)
	}
	if len(resp.List) != 1 {
		t.Fatalf("读失败时主机行照常给出: %+v", resp.List)
	}
	if h := hostByID(t, resp.List, 7); h.DiskUsedPercent != nil || h.DiskTotalGB != nil {
		t.Fatalf("读失败应降级为缺省: %+v", h)
	}
	if reader.calls != 1 {
		t.Fatalf("降级路径也应是「一次批量取」: calls=%d", reader.calls)
	}
}

// TestDockerHostDiskWaterlineOnFailedSnapshotHost 宿主水位是**宿主事实**，与 docker
// 快照能否读到无关：一台主机的 docker 快照读失败（总览里 error 字段如实标注）时，
// 它的磁盘水位照样带出 —— 「这台机器的盘快满了吗」不因 docker 读不到而消失。
func TestDockerHostDiskWaterlineOnFailedSnapshotHost(t *testing.T) {
	f := newResFixture(t)
	reader := &fakeHostDiskReader{rows: map[uint64]entity.DeviceMetricWide{
		7: {DeviceID: 7, DiskUsedPercent: f64(88), DiskTotalGB: f64(100)},
	}}
	f.svc.WithHostDiskMetrics(reader)
	f.saveState(t, 7, &agentproto.DockerState{})
	// 覆盖快照键为坏 JSON：store.Get 返回错误（读失败）而不是「从未上报」。
	if err := f.rdb.Set(f.ctx, dockerstate.StateKey(7), "{not json", 0).Err(); err != nil {
		t.Fatal(err)
	}

	resp, err := f.svc.Overview(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	h7 := hostByID(t, resp.Hosts, 7)
	if h7.Error != dockerStateReadFailed {
		t.Fatalf("坏快照必须如实标注 error: %+v", h7)
	}
	if h7.DiskUsedPercent == nil || *h7.DiskUsedPercent != 88 || h7.DiskTotalGB == nil || *h7.DiskTotalGB != 100 {
		t.Fatalf("docker 快照读失败不抹掉宿主水位（不同源）: %+v", h7)
	}
}
