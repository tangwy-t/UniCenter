package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/glebarez/sqlite"
	goredis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agentmetrics"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/database"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/snowflake"
	"github.com/tangwy-t/UniCenter/uni_core/internal/repository"
)

// stubCfg 是消费方窄接口的最小实现（service 包内定义，见 AgentIngestService 的接口声明）。
type stubCfg map[string]string

func (s stubCfg) GetString(_ context.Context, key, def string) string {
	if v, ok := s[key]; ok {
		return v
	}
	return def
}
func (s stubCfg) GetInt(_ context.Context, key string, def int) int { return def }

// ingestEnv 把测试所需的全部句柄打包，便于断言「Redis 侧真的写进去了」。
type ingestEnv struct {
	svc    *AgentIngestService
	db     *gorm.DB
	raw    *agentmetrics.RawStore
	latest *agentmetrics.LatestStore
}

func newIngestTestEnv(t *testing.T) *ingestEnv {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	node, err := snowflake.New(1, logger.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	database.NewCallbacks(node).Register(db)
	if err := db.AutoMigrate(&entity.Device{}); err != nil {
		t.Fatal(err)
	}
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	raw := agentmetrics.NewRawStore(rdb, agentmetrics.RawOptions{Step: 10 * time.Second, MaxPoints: 1000})
	latest := agentmetrics.NewLatestStore(rdb)
	svc := NewAgentIngestService(repository.NewDeviceRepository(db), raw, latest,
		stubCfg{"sys.agent.enrollToken": "secret-token"}, logger.NewNop())
	return &ingestEnv{svc: svc, db: db, raw: raw, latest: latest}
}

func helloEnroll(inst string) *agentproto.Hello {
	h := &agentproto.Hello{InstanceID: inst, Hostname: "web-01", OS: "linux", Arch: "amd64",
		AgentVersion: "0.1.0", EnrollToken: "secret-token"}
	return h
}

func TestEnrollCreatesDeviceAndIssuesToken(t *testing.T) {
	env := newIngestTestEnv(t)
	svc, db := env.svc, env.db
	ctx := context.Background()

	id, token, err := svc.Enroll(ctx, helloEnroll("inst-1"))
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if id == 0 || token == "" {
		t.Fatalf("必须签发 device_id 与 agent_token: id=%d token=%q", id, token)
	}

	var d entity.Device
	if err := db.Where("instance_id = ?", "inst-1").First(&d).Error; err != nil {
		t.Fatalf("设备未落库: %v", err)
	}
	// 明文 token **绝不**落库
	if d.TokenHash == "" || d.TokenHash == token {
		t.Fatal("必须只存 sha256(token)，不得存明文")
	}
	if d.Status != entity.DeviceStatusEnabled {
		t.Fatalf("新设备应为启用态, got %d", d.Status)
	}
}

func TestEnrollIsIdempotentByInstanceID(t *testing.T) {
	env := newIngestTestEnv(t)
	svc, db := env.svc, env.db
	ctx := context.Background()

	id1, _, err := svc.Enroll(ctx, helloEnroll("inst-1"))
	if err != nil {
		t.Fatal(err)
	}
	// 同 instance_id 再次 enroll：不得新建设备，且应轮换 token
	id2, token2, err := svc.Enroll(ctx, helloEnroll("inst-1"))
	if err != nil {
		t.Fatalf("重复 enroll 必须幂等成功: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("同 instance_id 必须返回同一 device_id: %d vs %d", id1, id2)
	}
	var n int64
	db.Model(&entity.Device{}).Count(&n)
	if n != 1 {
		t.Fatalf("设备数 = %d, want 1（幂等）", n)
	}
	if token2 == "" {
		t.Fatal("重新 enroll 应轮换并返回新 token")
	}
}

func TestEnrollRejectsWrongEnrollToken(t *testing.T) {
	env := newIngestTestEnv(t)
	svc, db := env.svc, env.db
	h := helloEnroll("inst-1")
	h.EnrollToken = "wrong"
	_, _, err := svc.Enroll(context.Background(), h)
	if err == nil {
		t.Fatal("错误的 enroll token 必须被拒")
	}
	var ae *apperror.AppError
	if !errors.As(err, &ae) {
		t.Fatalf("必须是 apperror.AppError（供上层映射关闭码）: %T", err)
	}
	var n int64
	db.Model(&entity.Device{}).Count(&n)
	if n != 0 {
		t.Fatal("被拒的 enroll 不得建出设备")
	}
}

func TestAuthenticateAndIsAccepting(t *testing.T) {
	env := newIngestTestEnv(t)
	svc, db := env.svc, env.db
	ctx := context.Background()

	id, token, _ := svc.Enroll(ctx, helloEnroll("inst-1"))
	h := helloEnroll("inst-1")
	h.EnrollToken = ""
	h.AgentToken = token

	gotID, err := svc.Authenticate(ctx, h)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if gotID != id {
		t.Fatalf("鉴权应返回 enroll 时的 device_id: %d vs %d", gotID, id)
	}

	// 停用后不得再接受
	if err := db.Model(&entity.Device{}).Where("id = ?", id).Update("status", entity.DeviceStatusDisabled).Error; err != nil {
		t.Fatal(err)
	}
	ok, err := svc.IsAccepting(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("停用设备不得处于可接受状态")
	}
}

func TestAuthenticateRejectsBadToken(t *testing.T) {
	svc := newIngestTestEnv(t).svc
	h := helloEnroll("inst-1")
	h.EnrollToken = ""
	h.AgentToken = "not-a-real-token"
	if _, err := svc.Authenticate(context.Background(), h); err == nil {
		t.Fatal("非法 token 必须被拒")
	}
}

// stubAgentRepo 是可注入错误的 AgentDeviceRepository 桩（只为触发错误分支）。
// 其余方法返回零值即可 —— 被测方法只走 FindByID / FindByTokenHash。
type stubAgentRepo struct {
	findErr    error
	findDevice *entity.Device
}

func (s *stubAgentRepo) Create(context.Context, *entity.Device) error { return nil }
func (s *stubAgentRepo) FindByID(context.Context, uint64) (*entity.Device, error) {
	return s.findDevice, s.findErr
}
func (s *stubAgentRepo) FindByInstanceID(context.Context, string) (*entity.Device, error) {
	return s.findDevice, s.findErr
}
func (s *stubAgentRepo) FindByTokenHash(context.Context, string) (*entity.Device, error) {
	return s.findDevice, s.findErr
}
func (s *stubAgentRepo) UpdateEnroll(context.Context, *entity.Device) error { return nil }
func (s *stubAgentRepo) Touch(context.Context, uint64, time.Time) error     { return nil }

// stubAgentRaw / stubAgentLatest 是热层的最小桩（错误分支不写 Redis）。
type stubAgentRaw struct{}

func (stubAgentRaw) Append(context.Context, uint64, *agentproto.MetricsSample) error { return nil }

type stubAgentLatest struct{}

func (stubAgentLatest) Set(context.Context, uint64, *agentproto.MetricsSample) error { return nil }

// appErrStatusOf 把错误归一成 HTTP 状态（同 device_test.go 的 appErrStatus，
// 此处的断言跨文件共享同一个语义）。
func newIngestSvc(repo AgentDeviceRepository) *AgentIngestService {
	return NewAgentIngestService(repo, stubAgentRaw{}, stubAgentLatest{}, stubCfg{}, logger.NewNop())
}

// TestIsAcceptingPropagatesRepositoryFailure：S5 —— IsAccepting **不得把错误吞成**
// (false, nil)。
//
// 回归价值：原实现 `if err != nil || d == nil { return false, nil }` 会把 DB 故障
// 伪装成一个业务结论「该设备不接受上报」。2C 的 AgentHub 会据此拒掉连接，
// 而运维看到的现象是「所有设备都像被停用了」，真正的问题在数据库。
func TestIsAcceptingPropagatesRepositoryFailure(t *testing.T) {
	ctx := context.Background()
	dbDown := errors.New("db: connection refused")
	svc := newIngestSvc(&stubAgentRepo{findErr: dbDown})

	ok, err := svc.IsAccepting(ctx, 1001)
	if err == nil {
		t.Fatalf("仓储故障必须上抛，不得吞成 (false, nil): ok=%v", ok)
	}
	if ok {
		t.Fatal("故障时不得报告「可接受上报」")
	}
	if status := appErrStatus(err); status != 500 {
		t.Fatalf("仓储故障必须映射为 500 Internal, got status=%d err=%v", status, err)
	}
	if !errors.Is(err, dbDown) {
		t.Fatalf("Internal 必须携带原始 cause: %v", err)
	}
}

// TestIsAcceptingMissIsRejectionNotError：未命中是**合法答案**（设备不存在/已删除
// → 就是不可接受上报），不是错误；只有真故障才上抛（与上一条互补）。
func TestIsAcceptingMissIsRejectionNotError(t *testing.T) {
	ctx := context.Background()

	// 桩：未命中哨兵
	svc := newIngestSvc(&stubAgentRepo{findErr: repository.ErrNotFound})
	ok, err := svc.IsAccepting(ctx, 1001)
	if err != nil || ok {
		t.Fatalf("设备不存在应为 (false, nil), got (%v, %v)", ok, err)
	}

	// 真仓储：不存在的设备同样必须是 (false, nil) 而不是 500
	// （守卫 FindByID 的错误映射：查询路径若抛裸的 gorm.ErrRecordNotFound，
	// errors.Is(err, repository.ErrNotFound) 为假 → 会退化成 500）。
	real := newIngestTestEnv(t).svc
	ok, err = real.IsAccepting(ctx, 999999)
	if err != nil || ok {
		t.Fatalf("真仓储下不存在的设备应为 (false, nil), got (%v, %v)", ok, err)
	}
}

// TestAuthenticatePropagatesRepositoryFailure：S5 —— 鉴权也必须分辨
// 「查无此 token（400）」与「DB 故障（500）」。原实现把任何错误都当成
// 「agent token 无效」：一次数据库抖动会让所有 agent 同时以为凭据失效。
func TestAuthenticatePropagatesRepositoryFailure(t *testing.T) {
	ctx := context.Background()
	dbDown := errors.New("db: connection refused")
	svc := newIngestSvc(&stubAgentRepo{findErr: dbDown})

	h := helloEnroll("inst-1")
	h.EnrollToken = ""
	h.AgentToken = "whatever"

	_, err := svc.Authenticate(ctx, h)
	if err == nil {
		t.Fatal("仓储故障必须上抛")
	}
	if status := appErrStatus(err); status != 500 {
		t.Fatalf("仓储故障必须映射为 500（不得伪装成 400 token 无效）, got status=%d err=%v", status, err)
	}
	if !errors.Is(err, dbDown) {
		t.Fatalf("Internal 必须携带原始 cause: %v", err)
	}

	// 反向：真仓储的未命中仍然是 400（token 无效），不得变成 500
	real := newIngestTestEnv(t).svc
	h2 := helloEnroll("inst-1")
	h2.EnrollToken = ""
	h2.AgentToken = "not-a-real-token"
	if _, err := real.Authenticate(ctx, h2); appErrStatus(err) != 400 {
		t.Fatalf("查无此 token 必须仍是 400, got status=%d err=%v", appErrStatus(err), err)
	}
}

func TestIngestWritesRawAndLatest(t *testing.T) {
	env := newIngestTestEnv(t)
	ctx := context.Background()
	id, _, _ := env.svc.Enroll(ctx, helloEnroll("inst-1"))

	nowMs := time.Now().UnixMilli()
	s := &agentproto.MetricsSample{T: nowMs, CPUUsedPercent: 33}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := env.svc.Ingest(ctx, id, s); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	// ① 原始点已入滚动窗（用注入的同一个 store 校验）
	pts, err := env.raw.Bucket(ctx, id, nowMs-1000, nowMs+1000)
	if err != nil {
		t.Fatalf("Bucket: %v", err)
	}
	if len(pts) != 1 {
		t.Fatalf("原始窗内样本数 = %d, want 1", len(pts))
	}
	if pts[0].CPUUsedPercent != 33 {
		t.Fatalf("原始点内容不符: %+v", pts[0])
	}

	// ② 水位已写入
	w, err := env.latest.Get(ctx, id)
	if err != nil {
		t.Fatalf("latest.Get: %v", err)
	}
	if w == nil || w.CPUUsedPercent == nil || *w.CPUUsedPercent != 33 {
		t.Fatalf("水位未写入或内容不符: %+v", w)
	}

	// ③ 活跃设备索引里能查到该设备（flush 靠它枚举）
	ids, err := env.raw.Index(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, x := range ids {
		if x == id {
			found = true
		}
	}
	if !found {
		t.Fatalf("Index 里应含 %d, got %v", id, ids)
	}

	// ④ Touch 刷新 last_seen_at（心跳路径）
	var d entity.Device
	if err := env.db.Where("id = ?", id).First(&d).Error; err != nil {
		t.Fatal(err)
	}
	if err := env.svc.Touch(ctx, id); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	if err := env.db.Where("id = ?", id).First(&d).Error; err != nil {
		t.Fatal(err)
	}
	if d.LastSeenAt == nil {
		t.Fatal("Touch 必须刷新 last_seen_at")
	}
}

// missingOnceRepo 模拟「并发窗口」：第一次 FindByInstanceID 故意返回未命中，
// 从而强制 Enroll 走 Create 分支并撞上 instance_id 唯一约束（并发冲突路径）。
type missingOnceRepo struct {
	*repository.DeviceRepo
	missed bool
}

func (r *missingOnceRepo) FindByInstanceID(ctx context.Context, instanceID string) (*entity.Device, error) {
	if !r.missed {
		r.missed = true
		return nil, gorm.ErrRecordNotFound
	}
	return r.DeviceRepo.FindByInstanceID(ctx, instanceID)
}

// 并发 enroll 撞唯一约束时，必须回读既存设备；且**返回的明文 token 必须是可用的**
// —— 即它的 sha256 必须等于回读之后库里存的 token_hash。
//
// 回归价值：Enroll 的并发分支若只回读 device 却不落我们这次签发的 hash，
// 就会出现「返回了一个库中不存在的 token」——客户端拿它鉴权必然失败，
// 而这是最难排查的一类缺陷（enroll 成功、紧接着就 401）。
func TestEnrollConcurrentConflictRereadsAndIssuesUsableToken(t *testing.T) {
	env := newIngestTestEnv(t)
	ctx := context.Background()
	repo := repository.NewDeviceRepository(env.db)
	raw := agentmetrics.NewRawStore(
		goredis.NewClient(&goredis.Options{Addr: miniredis.RunT(t).Addr()}),
		agentmetrics.RawOptions{Step: 10 * time.Second, MaxPoints: 100})
	svc := NewAgentIngestService(&missingOnceRepo{DeviceRepo: repo}, raw, agentmetrics.NewLatestStore(
		goredis.NewClient(&goredis.Options{Addr: miniredis.RunT(t).Addr()})),
		stubCfg{"sys.agent.enrollToken": "secret-token"}, logger.NewNop())

	// 预置设备，模拟「另一个并发请求已经建好了同一台」
	seed := &entity.Device{InstanceID: "inst-1", Hostname: "first", TokenHash: "hash-first",
		Status: entity.DeviceStatusEnabled}
	if err := repo.Create(ctx, seed); err != nil {
		t.Fatal(err)
	}

	id, token, err := svc.Enroll(ctx, helloEnroll("inst-1"))
	if err != nil {
		t.Fatalf("并发冲突必须回读既存设备并成功返回，不得报错: %v", err)
	}
	if id != seed.ID {
		t.Fatalf("必须复用既存 device_id %d, got %d", seed.ID, id)
	}
	var n int64
	env.db.Model(&entity.Device{}).Count(&n)
	if n != 1 {
		t.Fatalf("并发冲突不得重复插入设备，设备数 = %d, want 1", n)
	}

	// 核心断言：返回的明文 token 必须真的能通过鉴权
	h := helloEnroll("inst-1")
	h.EnrollToken = ""
	h.AgentToken = token
	gotID, err := svc.Authenticate(ctx, h)
	if err != nil {
		t.Fatalf("并发冲突返回的 token 必须可用（库中必须存在它的 sha256）: %v", err)
	}
	if gotID != seed.ID {
		t.Fatalf("鉴权返回 device_id = %d, want %d", gotID, seed.ID)
	}
}
