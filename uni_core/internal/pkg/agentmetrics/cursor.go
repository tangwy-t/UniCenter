package agentmetrics

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	goredis "github.com/redis/go-redis/v9"
)

// ─ 水位游标的唯一存取实现 ─────────────────────────────────
//
// 为什么必须收成一个类型（而不是让 flush/rollup 各写一份 readCursor/writeCursor）：
//
//  1. **键构造只有一条路径**：键名是 spec §7 的契约（flush/rollup/运维手工 DEL 共用），
//     两份实现等于两个漂移面，而漂移的症状是「静默读不到」。
//  2. **写入必须是原子的比较-写**：见 advanceCursorScript。两个后台任务（flush 推进、
//     backfill 回退）在同一条水位上并发，Go 侧的「读 → 比 → 写」在时序上必然可交错，
//     只有 Redis 侧的 Lua 才能把「比较」与「写」压成一次原子操作。
//
// 本类型不持有任何业务语义（区间怎么算、失败怎么处理都在 service 侧），只有三件事：
// 读、只前进地推进、只后退地回退。

// advanceCursorScript 是「只前进」的乐观 CAS（原子）。
//
// 三个条件全部成立才写，否则返回 0（调用方把「未写入」当作正常结果，不是错误）：
//
//	if not cur then return 0                       -- 键被并发删除 → 让位（下轮按 Bootstrap 语义重建）
//	if cur ~= ARGV[1] then return 0                -- 水位已经不是本轮读到的那个值 → 让位
//	if tonumber(ARGV[2]) <= tonumber(cur) then 0   -- 只前进：新值必须严格大于当前值
//
// 为什么第 2 个条件（期望值比对）不能省 —— 它才是真正堵住「回退被覆写」的那一条：
//
//	轮初水位 = H（H 是「已成功落库到（含）」的那个桶）
//	① flush 轮开始，读到 H；
//	② 同一时刻 backfill 把水位**回退**到 T（T ≪ H），准备重放 [T+300, H] 这段桶；
//	③ flush 轮跑完自己那段 [H+300, upper) 并想写下 lastBucketed（≈ now−300）。
//
// 只有「新值 > 当前值」这一条判断时，③ 会**通过**（lastBucketed > T），于是水位被推回
// now−300 —— 而 [T+300, H] 这段桶 flush 这一轮**根本没处理**：回退就此丢失，
// 「水位只在全成功后前移」这条不变量在实例层失效（这是本轮修复的缺陷本体）。
// 加上期望值比对后 ③ 被拒绝（当前值 T ≠ 轮初读到的 H），水位留在回退后的 T，
// backfill 紧接着的那轮重放就能真正从 T 开始补齐。
//
// 注意两个条件合起来**蕴含**用户要求的「新值 > 当前值」：cur == ARGV[1] 且
// ARGV[2] > ARGV[1] ⇒ ARGV[2] > cur。故本脚本比「只前进」更强（更强 ≠ 更松）。
//
// 代价（刻意接受）：并发者动过水位时本轮**不写**，水位留在并发者写下的值上，
// 下轮重新读水位后自然继续。代价方向是安全的 —— 最多让已成功落库的桶被**重复**处理
// （UPSERT 幂等），绝不会跳过没处理过的桶（那才是丢数据）。
const advanceCursorScript = `
local cur = redis.call("GET", KEYS[1])
if not cur then
	return 0
end
if cur ~= ARGV[1] then
	return 0
end
if tonumber(ARGV[2]) <= tonumber(cur) then
	return 0
end
redis.call("SET", KEYS[1], ARGV[2])
return 1
`

// rewindCursorScript 是「只后退」的回退（原子），backfill 专用。
//
// 为什么 backfill 用「只后退」而不是像推进那样做期望值比对：回退是**发起重放**的那一方，
// 它必须赢。两者都安全，差别只在代价方向：
//   - 回退赢（本脚本）：[target+300, upper) 被整段重放。并发推进覆盖的区间是它的子集，
//     所以最多是**多处理**一段已落库的桶（UPSERT 幂等）；「不写」才危险 ——
//     那会让本轮重放从并发者推到的高水位开始，等于这一轮回退**什么也没做**。
//   - 期望值比对赢：并发者动过水位就不回退，那正是上面要避免的。
//
// `tonumber(ARGV[1]) < tonumber(cur)` 保证**绝不写下比当前更大的值**，于是
// 「游标比回溯窗口还旧 / 目标起点比当前游标更新」时它自然什么都不做 ——
// 这条既有安全性质（回退绝不上移水位）比 Go 侧「读 → 比 → 写」更严：
// A 读到水位 X、判定 target(50) < X 该写；与此同时 B 把水位回退到 20 并准备重放
// [20, 50) 这段；A 随后那次 SET 50 相对**当前值 20** 就是一次上移，B 的重放范围
// 被吞掉一半。Lua 里的比较与写之间没有这条缝。
const rewindCursorScript = `
local cur = redis.call("GET", KEYS[1])
if not cur then
	redis.call("SET", KEYS[1], ARGV[1])
	return 1
end
if tonumber(ARGV[1]) < tonumber(cur) then
	redis.call("SET", KEYS[1], ARGV[1])
	return 1
end
return 0
`

// initCursorScript 是「键缺失才写」的初始化（SETNX 语义），并**返回键上最终生效的值**。
//
// 为什么返回生效值而不是布尔：初始化是「两个实例同时发现水位键缺失」的固有竞态，
// 谁先写谁赢；输的那一方必须用**赢家写下的值**继续本轮，绝不能回写自己的候选值
// （回写会让两个实例对同一段桶用不同的区间起点）。一次 Lua 调用同时完成
// 「判存在 → 写 → 回读」，中间没有可交错的缝。
const initCursorScript = `
if redis.call("EXISTS", KEYS[1]) == 1 then
	return redis.call("GET", KEYS[1])
end
redis.call("SET", KEYS[1], ARGV[1])
return ARGV[1]
`

// CursorStore 是水位游标的读写出口：键构造 + 三种原子写入（推进/回退/初始化）。
//
// 为什么不是接口而是具体类型：游标的存取**只有一种正确实现**（Lua CAS），
// 抽成接口只会让「换个实现」变得顺手，而换掉的代价是静默的丢数据。测试要注入替身时
// 注入的是 Redis 客户端（miniredis），不是这个类型。
type CursorStore struct {
	rdb goredis.Cmdable
}

// NewCursorStore 绑定游标所在的 Redis 客户端。
//
// flush 与 rollup 必须传**同一个**客户端：两族游标（cursor_5m / cursor_1h）是
// 「agent 上报链路」的键族，分裂到两个 Redis 会让水位与热层原始窗对不上。
func NewCursorStore(rdb goredis.Cmdable) *CursorStore {
	return &CursorStore{rdb: rdb}
}

// Read 读水位。
//
// 返回 (值, 键是否存在, 错误)：**「键不存在」不是错误**，它是 Bootstrap 语义的入口
// （调用方据此把水位初始化到窗口起点）；而 Redis 故障必须上抛 —— 把它当成「没有游标」
// 会让整轮从窗口起点重算并回写一个更小的水位，那是不可观测的降级。
func (s *CursorStore) Read(ctx context.Context, deviceID uint64, r Resolution) (int64, bool, error) {
	key, kerr := CursorKey(deviceID, r)
	if kerr != nil {
		return 0, false, kerr
	}
	v, err := s.rdb.Get(ctx, key).Int64()
	if err == nil {
		return v, true, nil
	}
	if errors.Is(err, goredis.Nil) {
		return 0, false, nil
	}
	return 0, false, fmt.Errorf("读水位 %s: %w", key, err)
}

// Init 在键缺失时写入 sec，并返回**键上最终生效**的水位（已有水位时就是它）。
//
// 已有水位时**绝不覆写**：水位是「已成功落库到（含）」的记账，覆写会让最近一个窗口的桶
// 被无谓地重放，也会破坏「水位只前进」这条不变量。
func (s *CursorStore) Init(ctx context.Context, deviceID uint64, r Resolution, sec int64) (int64, error) {
	key, kerr := CursorKey(deviceID, r)
	if kerr != nil {
		return 0, kerr
	}
	raw, err := s.rdb.Eval(ctx, initCursorScript, []string{key}, sec).Result()
	if err != nil {
		return 0, fmt.Errorf("初始化水位 %s: %w", key, err)
	}
	return parseCursorValue(key, raw)
}

// Advance 只前进地推进水位：仅当当前值仍是 expected（本轮读到的水位）且 next > expected 时写入。
//
// expected **必须**是本轮真正读到的值（不是推导值、不是缓冲区里的旧值）；
// next 是本轮实际成功处理到的最后一个桶。返回 false 表示「没写」—— 并发者动过水位，
// 本轮让位（下轮重新读水位后继续），这不是错误。
func (s *CursorStore) Advance(ctx context.Context, deviceID uint64, r Resolution,
	expected, next int64) (bool, error) {

	key, kerr := CursorKey(deviceID, r)
	if kerr != nil {
		return false, kerr
	}
	n, err := s.rdb.Eval(ctx, advanceCursorScript, []string{key}, expected, next).Int64()
	if err != nil {
		return false, fmt.Errorf("推进水位 %s（%d→%d）: %w", key, expected, next, err)
	}
	return n == 1, nil
}

// Rewind 只后退地回退水位（backfill 专用）：仅当 sec < 当前值（或键缺失）时写入。
//
// 返回 false 表示没写（当前水位已经不比 sec 更新 —— 无需回退），不是错误。
func (s *CursorStore) Rewind(ctx context.Context, deviceID uint64, r Resolution, sec int64) (bool, error) {
	key, kerr := CursorKey(deviceID, r)
	if kerr != nil {
		return false, kerr
	}
	n, err := s.rdb.Eval(ctx, rewindCursorScript, []string{key}, sec).Int64()
	if err != nil {
		return false, fmt.Errorf("回退水位 %s 到 %d: %w", key, sec, err)
	}
	return n == 1, nil
}

// parseCursorValue 把 Lua 返回的水位（字符串/整数）解析成 unix 秒。
//
// go-redis 在 RESP2 下把 Lua 的字符串返回成 string、数字返回成 int64，两种都要接住：
// 只接一种会让「换 Redis 协议版本」变成一条静默的解析失败。
func parseCursorValue(key string, raw any) (int64, error) {
	switch v := raw.(type) {
	case int64:
		return v, nil
	case string:
		sec, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("解析水位 %s = %q: %w", key, v, err)
		}
		return sec, nil
	default:
		return 0, fmt.Errorf("解析水位 %s: 非预期的返回值类型 %T", key, raw)
	}
}
