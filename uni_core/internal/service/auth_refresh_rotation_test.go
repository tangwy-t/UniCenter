package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/jwt"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
	"github.com/tangwy-t/webmanager-server/internal/pkg/session"
)

// errStub 用于模拟 Redis 故障。
var errStub = errors.New("redis unavailable")

// ── P2-3 refresh 轮换原子性 ──────────────────────────────
//
// 改造前的实现顺序是:校验通过 → 立刻 DeleteRefresh → 查用户 → 签发新令牌
// → storeSession。它有两个独立缺陷:
//
//  1. 作废是"尽力而为":DeleteRefresh 失败只打一条 Warn,旧 refresh token
//     仍留在 Redis 中 —— 同一个 token 可被再次兑换,轮换形同虚设。
//  2. 作废时机过早:删除旧 token 之后、写入新 token 之前的任何失败
//     (DB 抖动、账号被禁用、签发失败)都会让用户既没有旧令牌也没有新令牌 ——
//     被强制登出,且无法通过重试恢复。
//
// 现在改为把"作废旧 + 写入新"合并为一次 CAS,且提交点在新令牌全部就绪之后。

// newRefreshTestService 构造 RefreshToken 所需依赖的最小 AuthService。
func newRefreshTestService(repo AuthRepositoryInterface, ss SessionStoreInterface) *AuthService {
	return NewAuthService(stubConfigProvider{}, repo, logger.NewNop(), ss, nil, nil, "", nil, nil)
}

// stubConfigProvider 提供 jwt 密钥等配置的固定值。
type stubConfigProvider struct{}

// testRefreshSecret 长度 >=32 字节:jwt 签名对短密钥直接报错,
// 而 DefaultSecretFallback 只有 24 字节,不能用于签发测试令牌。
const testRefreshSecret = "test-refresh-secret-key-0123456789abcdef"

func (stubConfigProvider) GetString(_ context.Context, key, fallback string) string {
	if key == jwt.SecretConfigKey {
		return testRefreshSecret
	}
	return fallback
}
func (stubConfigProvider) GetInt(_ context.Context, _ string, fallback int) int    { return fallback }
func (stubConfigProvider) GetBool(_ context.Context, _ string, fallback bool) bool { return fallback }

// makeValidRefreshToken 签发一个可通过 ParseRefreshToken 的 refresh token。
func makeValidRefreshToken(t *testing.T, userID uint64) string {
	t.Helper()
	tok, err := jwt.GenerateRefreshToken(userID, testRefreshSecret, 3600)
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}
	return tok
}

// recordingSessionStore 记录调用顺序,用于断言"CAS 在写 access 之后"。
type recordingSessionStore struct {
	stubSessionStore
	order        []string
	rotateResult *bool
	rotateErr    error
}

func (r *recordingSessionStore) StoreAccess(context.Context, string, uint64, time.Duration) error {
	r.order = append(r.order, "storeAccess")
	return nil
}
func (r *recordingSessionStore) StorePerms(context.Context, uint64, string, []string, time.Duration) error {
	r.order = append(r.order, "storePerms")
	return nil
}
func (r *recordingSessionStore) RotateRefresh(_ context.Context, _ uint64, old, new string, _ time.Duration) (bool, error) {
	r.order = append(r.order, "rotateRefresh")
	r.rotateCalls = append(r.rotateCalls, [2]string{old, new})
	if r.rotateResult != nil {
		return *r.rotateResult, r.rotateErr
	}
	return true, r.rotateErr
}
func (r *recordingSessionStore) GetRefresh(context.Context, uint64) (string, error) {
	return "", nil
}
func (r *recordingSessionStore) StoreRefresh(context.Context, uint64, string, time.Duration) error {
	return nil
}
func (r *recordingSessionStore) DeleteRefresh(context.Context, uint64) error {
	r.order = append(r.order, "deleteRefresh")
	return nil
}
func (r *recordingSessionStore) StoreSessionMeta(context.Context, string, *session.SessionMeta, time.Duration) error {
	return nil
}

// TestRefreshToken_RotationIsCommittedAfterNewTokensReady 是 P2-3 的核心断言:
// 作废旧 refresh token 的提交必须发生在新令牌写入之后。
//
// 反过来的顺序(旧实现的顺序)会在"签发/存储失败"时把用户登出:
// 旧令牌已删、新令牌没写成。本测试通过调用顺序锁定这一点。
func TestRefreshToken_RotationIsCommittedAfterNewTokensReady(t *testing.T) {
	oldTok := makeValidRefreshToken(t, 42)
	repo := &stubAuthRepo{
		findByIDUser: &entity.SysUser{},
		dataScope:    1,
	}
	ss := &recordingSessionStore{}
	svc := newRefreshTestService(repo, ss)

	// GetRefresh 需返回旧 token 以通过 double-verify。
	svc.sessionStore = &presetRefreshStore{recordingSessionStore: ss, preset: oldTok}

	_, err := svc.RefreshToken(context.Background(), &request.RefreshTokenReq{RefreshToken: oldTok}, "ip", "ua")
	if err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}

	rotateIdx, accessIdx := -1, -1
	for i, op := range ss.order {
		switch op {
		case "rotateRefresh":
			rotateIdx = i
		case "storeAccess":
			accessIdx = i
		}
	}
	if accessIdx < 0 {
		t.Fatalf("未写入 access 白名单; order=%v", ss.order)
	}
	if rotateIdx < 0 {
		t.Fatalf("未调用 RotateRefresh(CAS); order=%v", ss.order)
	}
	if rotateIdx < accessIdx {
		t.Fatalf("RotateRefresh 早于 access 写入(order=%v)—— 新令牌尚未就绪就作废旧令牌,失败会把用户登出", ss.order)
	}
	// 不得再出现"先删后建"的 DeleteRefresh。
	for _, op := range ss.order {
		if op == "deleteRefresh" {
			t.Fatalf("轮换路径不应调用 DeleteRefresh(应由 CAS 原子提交); order=%v", ss.order)
		}
	}
}

// presetRefreshStore 让 GetRefresh 返回预设值,其余委托给内嵌替身。
type presetRefreshStore struct {
	*recordingSessionStore
	preset string
}

func (p *presetRefreshStore) GetRefresh(context.Context, uint64) (string, error) {
	return p.preset, nil
}

// TestRefreshToken_RejectsReplayedToken 重放拒绝:当 CAS 报告旧令牌
// 已不是当前值(consumed=false)时必须拒绝,否则一个 refresh token
// 能换出多组有效令牌。
func TestRefreshToken_RejectsReplayedToken(t *testing.T) {
	oldTok := makeValidRefreshToken(t, 42)
	consumed := false
	ss := &recordingSessionStore{rotateResult: &consumed}
	repo := &stubAuthRepo{findByIDUser: &entity.SysUser{}, dataScope: 1}
	svc := newRefreshTestService(repo, &presetRefreshStore{recordingSessionStore: ss, preset: oldTok})

	if _, err := svc.RefreshToken(context.Background(), &request.RefreshTokenReq{RefreshToken: oldTok}, "ip", "ua"); err == nil {
		t.Fatal("CAS 报告令牌已被兑换,RefreshToken 却成功了 —— 同一 refresh token 可重复兑换")
	}
}

// TestRefreshToken_ErrorsWhenCASFails CAS 出错(Redis 不可用)时不得
// 假装成功签发令牌 —— 那样作废就没发生,重放窗口仍然敞开。
func TestRefreshToken_ErrorsWhenCASFails(t *testing.T) {
	oldTok := makeValidRefreshToken(t, 42)
	ss := &recordingSessionStore{rotateErr: errStub}
	repo := &stubAuthRepo{findByIDUser: &entity.SysUser{}, dataScope: 1}
	svc := newRefreshTestService(repo, &presetRefreshStore{recordingSessionStore: ss, preset: oldTok})

	if _, err := svc.RefreshToken(context.Background(), &request.RefreshTokenReq{RefreshToken: oldTok}, "ip", "ua"); err == nil {
		t.Fatal("CAS 失败却返回成功 —— 旧令牌未作废,可被重放")
	}
}

// TestRefreshToken_RejectsWhenStoredTokenDiffers double-verify:
// Redis 中的 refresh token 与请求不一致时必须拒绝(令牌已被轮换过)。
func TestRefreshToken_RejectsWhenStoredTokenDiffers(t *testing.T) {
	reqTok := makeValidRefreshToken(t, 42)
	ss := &recordingSessionStore{}
	repo := &stubAuthRepo{findByIDUser: &entity.SysUser{}, dataScope: 1}
	svc := newRefreshTestService(repo, &presetRefreshStore{recordingSessionStore: ss, preset: "some-other-token"})

	if _, err := svc.RefreshToken(context.Background(), &request.RefreshTokenReq{RefreshToken: reqTok}, "ip", "ua"); err == nil {
		t.Fatal("存储中的 token 与请求不一致却通过校验")
	}
}
