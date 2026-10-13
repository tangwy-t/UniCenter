// Package dockerstate 是 core 侧的 Docker 状态存储：资源快照、可管主机集合、指令记录。
//
// 快照**只存 Redis 不落 MySQL**（spec §2 存储决策）：docker 状态是易变的当前值，
// 历史趋势价值低（主机级趋势已有设备监控覆盖）。离线时列表仍可读并标注陈旧 ——
// 与设备监控「离线可看最后水位」同一心智。
package dockerstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// 键名与 spec §4.3.2 的清单逐字一致（键名是运维的排障入口，不得改名）。
const (
	// HostsKey 是可管主机集合（收到快照时 SADD，设备删除时 SREM）。
	HostsKey = "docker:hosts"
	// ConfigVersionKey 是 hello_ack 下发的配置版本号（单调递增计数器）。
	ConfigVersionKey = "docker:config_version"
	// CmdKeyPrefix 是指令记录（+ ref）。
	CmdKeyPrefix = "docker:cmd:"
	// CmdDeadlineKey 是按到期时刻排序的指令索引（sweep 用）。
	//
	// 它在 spec 的键清单之外：没有它就只能靠 SCAN docker:cmd:* 做扫描，而 SCAN 会
	// 遍历共享 Redis 的整个键空间；ZSET 让 sweep 变成 O(log n + k)。
	CmdDeadlineKey = "docker:cmd:deadline"
	// CmdInflightKeyPrefix 是「同 (device, action, target) 已有在飞指令」的索引（409 去重）。
	CmdInflightKeyPrefix = "docker:inflight:"
	// StreamTicketKeyPrefix 是一次性流票据（三期使用；一期先定前缀，避免两处各写一个）。
	StreamTicketKeyPrefix = "docker:stream_ticket:"

	stateKeyPrefix = "docker:state:"
)

// StateKey 返回某台设备的快照键。
func StateKey(deviceID uint64) string { return stateKeyPrefix + strconv.FormatUint(deviceID, 10) }

// InflightKey 返回某台设备的在飞索引键。
func InflightKey(deviceID uint64) string {
	return CmdInflightKeyPrefix + strconv.FormatUint(deviceID, 10)
}

// Envelope 是 core 侧存下的快照 + 元数据。
type Envelope struct {
	State agentproto.DockerState `json:"state"`
	// ReceivedAt 是 core **收到**该帧的服务端时刻（unix 毫秒）。
	//
	// 陈旧度必须用它而不是 State.T：agent 的时钟可能偏（.106 实测快 8 小时），
	// 用载荷时间戳算陈旧度会得出「数据来自未来」或「陈旧 8 小时」这种荒谬结论 ——
	// 而排障的人会照着那个结论去查一个不存在的问题。
	ReceivedAt int64 `json:"received_at"`
}

// Store 读写快照与主机集合。
type Store struct {
	rdb goredis.UniversalClient
}

// NewStore 构造存储。
func NewStore(rdb goredis.UniversalClient) *Store { return &Store{rdb: rdb} }

// Save 写入快照并登记主机集合（一次事务：两者不一致会让「集合里有、读快照为空」）。
//
// 不设 TTL：设备离线时列表仍要能显示最后已知状态（陈旧度由 ReceivedAt 计算），
// key 的生命周期由设备删除时的 Purge 负责。
func (s *Store) Save(ctx context.Context, deviceID uint64, st *agentproto.DockerState, receivedAt time.Time) error {
	b, err := json.Marshal(&Envelope{State: *st, ReceivedAt: receivedAt.UnixMilli()})
	if err != nil {
		return err
	}
	pipe := s.rdb.TxPipeline()
	pipe.Set(ctx, StateKey(deviceID), b, 0)
	pipe.SAdd(ctx, HostsKey, deviceID)
	_, err = pipe.Exec(ctx)
	return err
}

// Get 读快照；从未上报时返回 (nil, nil)（页面显示「尚未上报」，不是错误）。
func (s *Store) Get(ctx context.Context, deviceID uint64) (*Envelope, error) {
	b, err := s.rdb.Get(ctx, StateKey(deviceID)).Bytes()
	if err != nil {
		if err == goredis.Nil {
			return nil, nil
		}
		return nil, err
	}
	var env Envelope
	if err := json.Unmarshal(b, &env); err != nil {
		return nil, err
	}
	return &env, nil
}

// MGetError 是批量读的**失败名单**（设备 id → 那台读不到的原因）。
//
// 为什么把「失败的是谁」当成一等返回值：批量读把 N 次调用收成一次往返之后，
// 「这台读不到」与「这台从未上报」在结果 map 上都是缺席 —— 而这两句话在页面上是两个
// 不同结论（「读取资源快照失败」 vs 「尚未上报」）。少了这份名单，100 台里坏掉的那一台
// 会被静默降级成「零容器零镜像的正常主机」，正是本模块一直在挡的那种失败形态。
type MGetError struct {
	Failed map[uint64]error
}

// Error 只说「几台、头一台是谁」：把全部 id 拼进文案，在 Redis 整体不可达时会变成一条
// 100 个数字的长串（排障的人不会读它）；逐台的原因由调用方按台取用（Unwrap 给的是同一批）。
func (e *MGetError) Error() string {
	ids := e.sortedIDs()
	if len(ids) == 0 {
		return "批量读取快照失败"
	}
	return fmt.Sprintf("批量读取快照失败: %d 台(首个 deviceId=%d)", len(ids), ids[0])
}

// Unwrap 摊平逐台原因（Go 1.20+ 的多错误解包）：errors.Is/As 能穿透到原始错误
// （连接错误、反序列化错误），打印出来也不丢原因。
//
// 顺序是确定的（device id 升序）——否则「首条原因」会随 map 迭代次序抖动。
func (e *MGetError) Unwrap() []error {
	ids := e.sortedIDs()
	out := make([]error, 0, len(ids))
	for _, id := range ids {
		out = append(out, e.Failed[id])
	}
	return out
}

// sortedIDs 返回失败设备的 id 升序（报错文案与解包顺序都要确定）。
func (e *MGetError) sortedIDs() []uint64 {
	ids := make([]uint64, 0, len(e.Failed))
	for id := range e.Failed {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// MGet 批量读快照：一次 pipeline 把「N 台 = N 次往返」压成一次往返（主机枚举的 N+1 闸）。
//
// 逐台结论与逐台调用 Get **一一对应**，只是并成了一批：
//   - 读到了 → map[id] = 信封；
//   - 键不存在（从未上报）→ id 既不在 map、也不在失败名单里：不是错误
//     （与 Get 的 (nil, nil) 是同一句话）；
//   - 读失败（键值损坏 / 该条命令报错；整批不可执行时每台都是读失败）→ id 缺席于 map，
//     但**在失败名单里**（返回的 *MGetError）。成功的那 99 台照常返回 —— 一批里坏一台
//     不该让另外 99 台看不见，而「坏的是谁」必须说得出（见 MGetError）。
//
// 为什么是 pipeline 而不是 Redis 的 MGET：MGET 是一条命令、一个（整体的）错误，
// 拿不到「哪一台读不出来」；pipeline 逐条给结果与错误，与上面那三种逐台结论一一对应。
func (s *Store) MGet(ctx context.Context, ids []uint64) (map[uint64]*Envelope, error) {
	out := make(map[uint64]*Envelope, len(ids))
	if len(ids) == 0 {
		return out, nil // 无键可读：不发一条空 pipeline（一次纯浪费的往返）
	}
	cmds := make([]*goredis.StringCmd, len(ids))
	pipe := s.rdb.Pipeline()
	for i, id := range ids {
		cmds[i] = pipe.Get(ctx, StateKey(id))
	}
	// Exec 的返回值**不看**：它给的是「第一条错误」，而键不存在（最常见的一种）也是错误
	// （redis.Nil）。逐条裁决由各 cmd 自己给出 —— 整批不可执行时（连接断开 / ctx 取消）
	// 连接错误会被逐条设到每个 cmd 上，于是每台都如实落进失败名单。
	_, _ = pipe.Exec(ctx)

	var failed map[uint64]error
	fail := func(id uint64, err error) {
		if failed == nil {
			failed = make(map[uint64]error, len(ids))
		}
		failed[id] = err
	}
	for i, id := range ids {
		b, err := cmds[i].Bytes()
		if err != nil {
			if errors.Is(err, goredis.Nil) {
				continue // 从未上报：既不是数据、也不是错误
			}
			fail(id, err)
			continue
		}
		var env Envelope
		if err := json.Unmarshal(b, &env); err != nil {
			// 键在、值读不出来：与 Get 同一句话（读失败），不是「从未上报」。
			fail(id, err)
			continue
		}
		out[id] = &env
	}
	if failed != nil {
		return out, &MGetError{Failed: failed}
	}
	return out, nil
}

// Hosts 返回全部上报过快照的设备 id（升序）。
func (s *Store) Hosts(ctx context.Context) ([]uint64, error) {
	members, err := s.rdb.SMembers(ctx, HostsKey).Result()
	if err != nil {
		return nil, err
	}
	out := make([]uint64, 0, len(members))
	for _, m := range members {
		if id, err := strconv.ParseUint(m, 10, 64); err == nil {
			out = append(out, id)
		}
	}
	return out, nil
}

// Purge 删除设备时连带清理（复用指标键清理先例：否则键永久泄漏，
// 页面还会列出一台已经删掉的主机）。
func (s *Store) Purge(ctx context.Context, deviceID uint64) error {
	pipe := s.rdb.TxPipeline()
	pipe.Del(ctx, StateKey(deviceID))
	pipe.SRem(ctx, HostsKey, deviceID)
	pipe.Del(ctx, InflightKey(deviceID))
	_, err := pipe.Exec(ctx)
	return err
}

// StaleThreshold 是「多久没更新算陈旧」：max(3×interval, 90s)（spec §13 的时间常数推导）。
func StaleThreshold(intervalSec int) time.Duration {
	if intervalSec <= 0 {
		intervalSec = 30
	}
	d := 3 * time.Duration(intervalSec) * time.Second
	if d < 90*time.Second {
		return 90 * time.Second
	}
	return d
}

// Stale 计算陈旧结论与年龄（毫秒）。env 为 nil 时返回 (false, 0) —— 「从未上报」
// 与「陈旧」在页面上是两句不同的话，不能混为一谈。
func Stale(env *Envelope, now time.Time, intervalSec int) (bool, int64) {
	if env == nil {
		return false, 0
	}
	age := now.Sub(time.UnixMilli(env.ReceivedAt))
	if age < 0 {
		// 未来时刻（服务端时钟回拨/数据损坏）：按「刚刚」处理，不给出负年龄。
		age = 0
	}
	return age > StaleThreshold(intervalSec), age.Milliseconds()
}
