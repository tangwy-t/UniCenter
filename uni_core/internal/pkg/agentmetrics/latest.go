package agentmetrics

import (
	"context"
	"encoding/json"

	goredis "github.com/redis/go-redis/v9"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/util"
)

// LatestSummary 是设备列表页「水位卡片」所需的即时值；从最近一次原始样本投影而来。
//
// 这里的整机磁盘/网卡合计与 5min 宽表的算法**刻意不同**：宽表是对桶内样本求均值，
// 而这里是「这一刻的即时值」，无需聚合。两者数值接近但不是同一个东西。
type LatestSummary struct {
	T int64 `json:"t"` // 采样时刻 unix 毫秒

	CPUUsedPercent *float64 `json:"cpu_used_percent,omitempty"`
	Load1          *float64 `json:"load1,omitempty"`
	MemUsedPercent *float64 `json:"mem_used_percent,omitempty"`
	MemUsedMB      *float64 `json:"mem_used_mb,omitempty"`

	// 磁盘/网卡的整机合计（Σ over 明细）。
	DiskTotalGB     *float64 `json:"disk_total_gb,omitempty"`
	DiskUsedGB      *float64 `json:"disk_used_gb,omitempty"`
	DiskUsedPercent *float64 `json:"disk_used_percent,omitempty"`
	NICRXBytesSec   *float64 `json:"nic_rx_bytes_sec,omitempty"`
	NICTXBytesSec   *float64 `json:"nic_tx_bytes_sec,omitempty"`

	MaxTemperatureC *float64 `json:"max_temperature_c,omitempty"`
}

// LatestStore 读写「最新水位」单键（`agent:device:{id}:latest`，见 raw.go 的 latestKey）。
//
// 不设 TTL：设备离线时列表页仍应显示最后一次水位（配合 online 标记），
// key 的生命周期由设备删除时的 Purge 负责。
type LatestStore struct {
	rdb goredis.UniversalClient
}

func NewLatestStore(rdb goredis.UniversalClient) *LatestStore {
	return &LatestStore{rdb: rdb}
}

// Set 从样本投影水位并覆盖写入（单键常驻）。
func (s *LatestStore) Set(ctx context.Context, deviceID uint64, sample *agentproto.MetricsSample) error {
	if sample == nil {
		return nil
	}
	b, err := json.Marshal(projectLatest(sample))
	if err != nil {
		return err
	}
	return s.rdb.Set(ctx, latestKey(deviceID), b, 0).Err()
}

// Get 读取水位；未写入时返回 (nil, nil)（调用方显示「—」，不是错误）。
func (s *LatestStore) Get(ctx context.Context, deviceID uint64) (*LatestSummary, error) {
	b, err := s.rdb.Get(ctx, latestKey(deviceID)).Bytes()
	if err != nil {
		if err == goredis.Nil {
			return nil, nil
		}
		return nil, err
	}
	var out LatestSummary
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetMany 批量读取（列表页一次 pipeline 取整页，避免逐台往返）。
func (s *LatestStore) GetMany(ctx context.Context, deviceIDs []uint64) (map[uint64]*LatestSummary, error) {
	out := make(map[uint64]*LatestSummary, len(deviceIDs))
	if len(deviceIDs) == 0 {
		return out, nil
	}
	pipe := s.rdb.Pipeline()
	cmds := make([]*goredis.StringCmd, len(deviceIDs))
	for i, id := range deviceIDs {
		cmds[i] = pipe.Get(ctx, latestKey(id))
	}
	if _, err := pipe.Exec(ctx); err != nil && err != goredis.Nil {
		// Pipeline 里单个 key 未命中会返回 redis.Nil，属正常；其它错误才上报
		hasRealErr := false
		for _, c := range cmds {
			if e := c.Err(); e != nil && e != goredis.Nil {
				hasRealErr = true
				break
			}
		}
		if hasRealErr {
			return nil, err
		}
	}
	for i, id := range deviceIDs {
		b, err := cmds[i].Bytes()
		if err != nil {
			continue
		}
		var v LatestSummary
		if err := json.Unmarshal(b, &v); err != nil {
			continue
		}
		out[id] = &v
	}
	return out, nil
}

// Delete 删除水位键（设备删除时调用）。
func (s *LatestStore) Delete(ctx context.Context, deviceID uint64) error {
	return s.rdb.Del(ctx, latestKey(deviceID)).Err()
}

// projectLatest 从原始样本即时投影水位（纯函数，便于单测）。
func projectLatest(s *agentproto.MetricsSample) LatestSummary {
	out := LatestSummary{T: s.T}

	out.CPUUsedPercent = f64OrNil(s.CPUUsedPercent)
	out.Load1 = f64OrNil(s.Load1)
	out.MemUsedPercent = f64OrNil(s.MemUsedPercent)
	out.MemUsedMB = f64OrNil(s.MemUsedMB)

	// 磁盘合计：Σtotal / Σused，水位 = Σused / Σtotal × 100（total 为 0 时置 nil）
	if len(s.Disks) > 0 {
		var total, used float64
		for _, d := range s.Disks {
			total += d.TotalGB
			used += d.UsedGB
		}
		out.DiskTotalGB = f64OrNil(total)
		out.DiskUsedGB = f64OrNil(used)
		if total > 0 {
			v := util.Round2(used / total * 100)
			out.DiskUsedPercent = &v
		}
	}

	// 网卡合计：Σ rx/tx
	if len(s.NICs) > 0 {
		var rx, tx float64
		for _, n := range s.NICs {
			rx += n.RXBytesPerSec
			tx += n.TXBytesPerSec
		}
		out.NICRXBytesSec = f64OrNil(rx)
		out.NICTXBytesSec = f64OrNil(tx)
	}

	// 最高温：只在有读数的传感器里取 max（nil = 读不到，不参与）
	var maxTemp *float64
	for _, sensor := range s.Sensors {
		if sensor.TemperatureC == nil {
			continue
		}
		if maxTemp == nil || *sensor.TemperatureC > *maxTemp {
			v := *sensor.TemperatureC
			maxTemp = &v
		}
	}
	out.MaxTemperatureC = maxTemp

	return out
}

// f64OrNil 把「0 也是真实值」的浮点包成指针；本函数只做包装不做过滤
// （协议层已保证非负且有限）。
func f64OrNil(v float64) *float64 {
	out := util.Round2(v)
	return &out
}
