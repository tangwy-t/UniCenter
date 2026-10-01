package dockerstate

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// 指令状态（spec §4.1 的状态机）。
const (
	StatusPending   = "pending"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusTimeout   = "timeout"
)

const (
	// ResultTTL 是终态记录的存活窗口（spec §13：10 分钟）。
	//
	// TTL 从**结果写入**起算而不是从派发起算：15 分钟的 save 若从派发起算，
	// 会在结果落盘前就过期 —— 用户拿到 404 而指令其实正在跑。
	ResultTTL = 10 * time.Minute
	// pendingSafetyTTL 是 pending 记录的兜底存活窗口。
	//
	// 正常路径由 sweep 终结（终结时改成 ResultTTL）；这条兜底只为「sweep 长期失效」
	// 这种异常准备 —— 它必须大于最长指令超时（15min），故取 2 小时。
	pendingSafetyTTL = 2 * time.Hour
	// RecentCmdKey 是「最近受理的指令」索引（ZSET：score=受理时刻 CreatedAt 毫秒，
	// member=ref），任务中心（GET /docker/tasks，6b）的枚举入口。
	//
	// 它是 6b 补进 CmdStore 数据面的**唯一新键**（任务要求「只读不新建存储，缺什么补
	// 什么」—— 任务中心不建新表，缺的正是枚举能力：没有它只能 SCAN docker:cmd:*，
	// 那是对共享 Redis 的全键空间遍历）。与 CmdDeadlineKey 同族：索引只是加速器，
	// **判定以记录为准**（读面逐条 Get，索引成员对应的记录已过期则剔除 —— 与
	// Inflight 的陈旧索引自愈同一纪律）。
	//
	// 键名落在 spec §4.3.2 的清单之外，与 CmdDeadlineKey 一样要写清理由；运维排障
	// 时按 CmdKeyPrefix 族定位。
	RecentCmdKey = "docker:cmd:recent"
	// RecentCmdKeep 是最近索引的容量上限（保留最新的 N 条，超出者随每次 Create 裁剪）。
	//
	// 取 300 而不是端点的 N=100：终态记录 TTL 仅 10 分钟（pending 兜底 2 小时），
	// 索引容量只有覆盖住「一次高吞吐窗口 + 过滤余量」才有意义 —— 任务中心按
	// hostId/status/action 过滤后仍应拿得到 100 条；300 条的最新窗口在高吞吐
	// 下也稳超过滤后 100 条的需求，而裁剪开销只剩一次 O(log n) 的 ZREMRANGEBYRANK。
	RecentCmdKeep = 300
)

// CmdRecord 是一条指令在服务端的完整记录（Redis `docker:cmd:<ref>`，JSON）。
type CmdRecord struct {
	Ref      string `json:"ref"`
	DeviceID uint64 `json:"device_id"`
	Action   string `json:"action"`
	Target   string `json:"target"`
	// UserID 是发起人：轮询与流接入都必须校验归属（防同权限用户 B 劫持用户 A 的会话）。
	UserID uint64 `json:"user_id"`
	// Perm 是受理时校验的权限码：轮询时按它再校验一次（同码同人才能看结果）。
	Perm   string `json:"perm"`
	Status string `json:"status"`
	// Confirm 是受理时的确认值（审计与排障用；不随轮询响应回显）。
	Confirm    string `json:"confirm,omitempty"`
	CreatedAt  int64  `json:"created_at"`
	FinishedAt int64  `json:"finished_at,omitempty"`
	// Error 是终态结论句（页面显示它）。
	Error string `json:"error,omitempty"`
	// Detail 是排障细节（落库不渲染）。
	Detail        string `json:"detail,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
	AlreadyExists bool   `json:"already_exists,omitempty"`
	// Payload 是成功结果的数据面（一期：日志文本 / inspect 数据 / yml 全文）。
	Payload json.RawMessage `json:"payload,omitempty"`
}

// CmdStore 读写指令记录、在飞索引与到期索引。
type CmdStore struct {
	rdb goredis.UniversalClient
}

// NewCmdStore 构造指令存储。
func NewCmdStore(rdb goredis.UniversalClient) *CmdStore { return &CmdStore{rdb: rdb} }

// timeoutScript 仅当记录仍是 pending 时写入 timeout，返回 1 表示写入成功。
//
// 为什么必须原子：sweep 与「结果恰好此刻到达」是并发路径 —— GET+SET 两步写会在两者
// 之间插入对方的写入，结果是「一条已经成功的指令被服务端的推断覆盖成超时」。
// 事实（agent 上报的结果）永远优先于推断（服务端的超时），故判定与写入收成一个原子操作。
var timeoutScript = goredis.NewScript(`
local cur = redis.call('GET', KEYS[1])
if not cur then return 0 end
local rec = cjson.decode(cur)
if rec.status ~= 'pending' then return 0 end
rec.status = 'timeout'
rec.error = ARGV[1]
rec.finished_at = tonumber(ARGV[2])
redis.call('SET', KEYS[1], cjson.encode(rec), 'EX', tonumber(ARGV[3]))
return 1
`)

// recentAddScriptSrc 原子地「报名 + 裁剪」最近索引：ZADD 后当场把容量压回 RecentCmdKeep。
//
// 为什么必须脚本而不是把 `ZREMRANGEBYRANK 0 -(keep+1)` 写进事务：过负的 stop 在两套
// Redis 实现里的越界语义不一致 —— 真 Redis 对「小于集合长度的负 rank」按「从 start
// 一直删到底」钳制（集合不足 keep+1 条时会**整锅清空**，刚 ZADD 进去的那条也活不了），
// miniredis 则按区间取交（不动）；同一个测试在两套实现下会给两个答案，坑的是测试
// 永远绿、生产悄悄空。脚本里用 ZCARD 的**当场读数**算正索引，两边语义一致且原子
// （与 timeoutScript 同一纪律：判定与写入收成一个操作，事实不被并发撕裂）。
//
// 注意：事务内走 pipe.Eval 时传的是**脚本原文**而不是 *Script（TxPipeline 的
// Cmdable.Eval 只收 string；*Script.Run 的 EVALSHA 缓存路径不能嵌进 MULTI/EXEC）。
const recentAddScriptSrc = `
redis.call('ZADD', KEYS[1], ARGV[1], ARGV[2])
local n = redis.call('ZCARD', KEYS[1])
local keep = tonumber(ARGV[3])
if n > keep then
    redis.call('ZREMRANGEBYRANK', KEYS[1], 0, n - keep - 1)
end
return n
`

// Create 建立 pending 记录，并登记在飞索引、到期索引与最近受理索引。
//
// 四个键一次事务写完：只写一半（例如有记录但没在飞索引）会让 409 去重失效，
// 而失效的表现是「同一条指令并发执行两次」——compose 类的操作会互踩。
// 最近索引的报名+裁剪也收在同一事务里（脚本见 recentAddScriptSrc）：写一半会让
// 索引与记录不同步，且容量约束必须原子 —— 两个并发 Create 各做一次裁剪后仍
// 保证 |set| ≤ RecentCmdKeep。
func (s *CmdStore) Create(ctx context.Context, rec *CmdRecord, timeout time.Duration) error {
	rec.Status = StatusPending
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	deadline := float64(time.Now().Add(timeout).UnixMilli())
	pipe := s.rdb.TxPipeline()
	pipe.Set(ctx, CmdKeyPrefix+rec.Ref, b, pendingSafetyTTL)
	pipe.ZAdd(ctx, CmdDeadlineKey, goredis.Z{Score: deadline, Member: rec.Ref})
	pipe.HSet(ctx, InflightKey(rec.DeviceID), inflightField(rec.Action, rec.Target), rec.Ref)
	pipe.Eval(ctx, recentAddScriptSrc, []string{RecentCmdKey}, rec.CreatedAt, rec.Ref, RecentCmdKeep)
	_, err = pipe.Exec(ctx)
	return err
}

// inflightField 是在飞索引的哈希字段（`action|target`）。
//
// 用哈希而不是「一个 key 一目标」：哈希让同设备的在飞项聚在一起、清理只需一次 HDEL，
// 而在飞项的规模是「同一设备的并发指令数」（个位数）。
func inflightField(action, target string) string { return action + "|" + target }

// Get 读指令记录；不存在（或已过期）时返回 (nil, nil)。
func (s *CmdStore) Get(ctx context.Context, ref string) (*CmdRecord, error) {
	b, err := s.rdb.Get(ctx, CmdKeyPrefix+ref).Bytes()
	if err != nil {
		if err == goredis.Nil {
			return nil, nil
		}
		return nil, err
	}
	var rec CmdRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

// Inflight 返回同 (device, action, target) 的在飞 ref；没有则空串。
//
// 命中后还要**回查记录**：记录可能已终结（结果回来了但索引没清干净，例如进程被杀在
// 两次写之间）。索引只是加速器，判定以记录为准 —— 否则一条早已完成的指令会永远
// 挡住同目标的后续指令（用户看到 409 但列表里没有任何在跑的东西）。
func (s *CmdStore) Inflight(ctx context.Context, deviceID uint64, action, target string) (string, error) {
	ref, err := s.rdb.HGet(ctx, InflightKey(deviceID), inflightField(action, target)).Result()
	if err != nil {
		if err == goredis.Nil {
			return "", nil
		}
		return "", err
	}
	rec, err := s.Get(ctx, ref)
	if err != nil {
		return "", err
	}
	if rec == nil || rec.Status != StatusPending {
		// 陈旧索引：顺手清掉（下一次调用就干净了）。
		_ = s.rdb.HDel(ctx, InflightKey(deviceID), inflightField(action, target)).Err()
		return "", nil
	}
	return ref, nil
}

// RecentRefs 返回最近受理的 ref（按受理时刻**降序**，至多 limit 条）。
//
// 只做枚举（索引是加速器）：成员的记录可能已过期（TTL 到而成员还在），
// 读面必须逐条 Get 并自行剔除 ——「判定以记录为准」与 Inflight 同一句纪律。
func (s *CmdStore) RecentRefs(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}
	return s.rdb.ZRevRange(ctx, RecentCmdKey, 0, int64(limit)-1).Result()
}

// ForgetRecent 从最近索引剔除一条 ref（读面发现记录已过期时的自愈；失败只影响
// 下一次读的效率，不遮住任何事实 —— 索引成员留着也只是被跳过）。
func (s *CmdStore) ForgetRecent(ctx context.Context, ref string) error {
	return s.rdb.ZRem(ctx, RecentCmdKey, ref).Err()
}

// Complete 写入 agent 上报的结果（终态）。
//
// 迟到的结果**覆盖** timeout：事实优先于推断（一条实际执行成功的指令不该在页面上
// 永远显示「超时」）。反向由 timeoutScript 保证（推断不得覆盖事实）。
func (s *CmdStore) Complete(ctx context.Context, rec *CmdRecord, res *agentproto.DockerCmdResult) error {
	rec.Status = StatusSucceeded
	if !res.OK {
		rec.Status = StatusFailed
	}
	rec.Error = res.Error
	rec.Detail = res.Detail
	rec.SessionID = res.SessionID
	rec.AlreadyExists = res.AlreadyExists
	rec.Payload = res.Payload
	rec.FinishedAt = time.Now().UnixMilli()

	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	pipe := s.rdb.TxPipeline()
	pipe.Set(ctx, CmdKeyPrefix+rec.Ref, b, ResultTTL)
	pipe.ZRem(ctx, CmdDeadlineKey, rec.Ref)
	pipe.HDel(ctx, InflightKey(rec.DeviceID), inflightField(rec.Action, rec.Target))
	_, err = pipe.Exec(ctx)
	return err
}

// Timeout 由 sweep 调用：仅当记录仍是 pending 时写入终态 timeout（原子，见 timeoutScript）。
func (s *CmdStore) Timeout(ctx context.Context, rec *CmdRecord) error {
	if rec == nil {
		return nil
	}
	msg := "指令超时未完成"
	_, err := timeoutScript.Run(ctx, s.rdb, []string{CmdKeyPrefix + rec.Ref},
		msg, time.Now().UnixMilli(), int(ResultTTL/time.Second)).Result()
	if err != nil && err != goredis.Nil {
		return err
	}
	// 在飞索引与到期索引一并清理：sweep 是终态的唯一保证路径，清不干净就永久泄漏。
	pipe := s.rdb.TxPipeline()
	pipe.ZRem(ctx, CmdDeadlineKey, rec.Ref)
	pipe.HDel(ctx, InflightKey(rec.DeviceID), inflightField(rec.Action, rec.Target))
	_, err = pipe.Exec(ctx)
	return err
}

// DueRefs 返回到期时刻早于 now 的 ref（最多 limit 条，升序）。
func (s *CmdStore) DueRefs(ctx context.Context, now time.Time, limit int) ([]string, error) {
	return s.rdb.ZRangeByScore(ctx, CmdDeadlineKey, &goredis.ZRangeBy{
		Min:    "-inf",
		Max:    strconv.FormatInt(now.UnixMilli(), 10),
		Offset: 0,
		Count:  int64(limit),
	}).Result()
}

// Sweep 终结所有已到期的 pending 指令，返回终结条数（由 wireup 的后台任务周期调用）。
func (s *CmdStore) Sweep(ctx context.Context, now time.Time, limit int) (int, error) {
	refs, err := s.DueRefs(ctx, now, limit)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, ref := range refs {
		rec, err := s.Get(ctx, ref)
		if err != nil {
			continue
		}
		if rec == nil {
			// 记录已过期（TTL 到）而索引还在：清索引，不计数。
			_ = s.rdb.ZRem(ctx, CmdDeadlineKey, ref).Err()
			continue
		}
		if rec.Status != StatusPending {
			_ = s.rdb.ZRem(ctx, CmdDeadlineKey, ref).Err()
			continue
		}
		if err := s.Timeout(ctx, rec); err == nil {
			n++
		}
	}
	return n, nil
}
