package service

import (
	"context"
	"testing"
	"time"

	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
	"github.com/tangwy-t/webmanager-server/internal/pkg/session"

	"gorm.io/gorm"
)

// stubSessionStore 满足 SessionStoreInterface 的最小替身,
// 只关心 RevokePerms 的调用记录(role_update_status_test.go 复用)。
type stubSessionStore struct {
	revoked []uint64
	// revokeAll 记录 RevokeAll 的调用（角色变更/状态变更路径现在吊销会话）。
	revokeAll []uint64
	// rotateConsumed / rotateErr 供 refresh 轮换相关测试定制 CAS 结果;
	// 为 nil 时默认放行(consumed=true),既有测试无需改动。
	rotateConsumed *bool
	rotateErr      error
	// rotateCalls 记录 (old, new) 以供断言。
	rotateCalls [][2]string
}

func (s *stubSessionStore) StoreAccess(context.Context, string, uint64, time.Duration) error {
	return nil
}
func (s *stubSessionStore) StoreRefresh(context.Context, uint64, string, time.Duration) error {
	return nil
}
func (s *stubSessionStore) StorePerms(context.Context, uint64, string, []string, time.Duration) error {
	return nil
}
func (s *stubSessionStore) RevokeAll(_ context.Context, userID uint64, _ string) error {
	s.revokeAll = append(s.revokeAll, userID)
	return nil
}
func (s *stubSessionStore) GetRefresh(context.Context, uint64) (string, error) {
	return "", nil
}
func (s *stubSessionStore) DeleteRefresh(context.Context, uint64) error { return nil }

// RotateRefresh 默认放行(consumed=true)。注意默认值不能让既有测试
// 依赖"一定成功";需要验证重放拒绝的测试通过 rotateConsumed 显式指定。
func (s *stubSessionStore) RotateRefresh(_ context.Context, _ uint64, old, new string, _ time.Duration) (bool, error) {
	s.rotateCalls = append(s.rotateCalls, [2]string{old, new})
	if s.rotateConsumed != nil {
		return *s.rotateConsumed, s.rotateErr
	}
	return true, s.rotateErr
}
func (s *stubSessionStore) RevokePerms(ctx context.Context, userID uint64) error {
	s.revoked = append(s.revoked, userID)
	return nil
}
func (s *stubSessionStore) RevokeAllPerms(context.Context) error { return nil }
func (s *stubSessionStore) StoreSessionMeta(context.Context, string, *session.SessionMeta, time.Duration) error {
	return nil
}

// newTestUserServiceWithSession 构造带 sessionStore 的 UserService(角色成员测试用)
func newTestUserServiceWithSession(repo UserRepositoryInterface, ss SessionStoreInterface) *UserService {
	return NewUserService(repo, logger.NewNop(), ss, nil)
}

// ── AddRoleUsers ────────────────────────────────────────────────────

func TestAddRoleUsers_AdminRoleBlocked(t *testing.T) {
	repo := &stubUserRepo{findRoleCode: "admin"}
	svc := newTestUserServiceWithSession(repo, &stubSessionStore{})
	err := svc.AddRoleUsers(context.Background(), 1, []uint64{10})
	assertCode(t, err, apperror.CodeBadRequest)
	if len(repo.addUserIDs) != 0 {
		t.Fatalf("AddUsersToRole should not be called, got %v", repo.addUserIDs)
	}
}

func TestAddRoleUsers_RoleNotFound(t *testing.T) {
	repo := &stubUserRepo{findRoleCodeErr: gorm.ErrRecordNotFound}
	svc := newTestUserServiceWithSession(repo, &stubSessionStore{})
	err := svc.AddRoleUsers(context.Background(), 999, []uint64{10})
	assertCode(t, err, apperror.CodeNotFound)
}

func TestAddRoleUsers_Success_RevokesPerms(t *testing.T) {
	repo := &stubUserRepo{findRoleCode: "dev"}
	ss := &stubSessionStore{}
	svc := newTestUserServiceWithSession(repo, ss)
	if err := svc.AddRoleUsers(context.Background(), 2, []uint64{10, 11}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.addRoleID != 2 || len(repo.addUserIDs) != 2 || repo.addUserIDs[0] != 10 || repo.addUserIDs[1] != 11 {
		t.Fatalf("unexpected add args: role=%d users=%v", repo.addRoleID, repo.addUserIDs)
	}
	if len(ss.revoked) != 2 || ss.revoked[0] != 10 || ss.revoked[1] != 11 {
		t.Fatalf("expected RevokePerms for [10 11], got %v", ss.revoked)
	}
}

// ── RemoveRoleUsers ─────────────────────────────────────────────────

func TestRemoveRoleUsers_AdminRoleBlocked(t *testing.T) {
	repo := &stubUserRepo{findRoleCode: "admin"}
	svc := newTestUserServiceWithSession(repo, &stubSessionStore{})
	err := svc.RemoveRoleUsers(context.Background(), 1, []uint64{10})
	assertCode(t, err, apperror.CodeBadRequest)
	if len(repo.removeUserIDs) != 0 {
		t.Fatalf("RemoveUsersFromRole should not be called, got %v", repo.removeUserIDs)
	}
}

func TestRemoveRoleUsers_Success_RevokesPerms(t *testing.T) {
	repo := &stubUserRepo{findRoleCode: "dev"}
	ss := &stubSessionStore{}
	svc := newTestUserServiceWithSession(repo, ss)
	if err := svc.RemoveRoleUsers(context.Background(), 2, []uint64{11}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.removeRoleID != 2 || len(repo.removeUserIDs) != 1 || repo.removeUserIDs[0] != 11 {
		t.Fatalf("unexpected remove args: role=%d users=%v", repo.removeRoleID, repo.removeUserIDs)
	}
	if len(ss.revoked) != 1 || ss.revoked[0] != 11 {
		t.Fatalf("expected RevokePerms for [11], got %v", ss.revoked)
	}
}

// ── AddRoleUsers 无 sessionStore 时不应 panic ───────────────────────

func TestAddRoleUsers_NilSessionStore_NoPanic(t *testing.T) {
	repo := &stubUserRepo{findRoleCode: "dev"}
	svc := newTestUserServiceWithSession(repo, nil)
	if err := svc.AddRoleUsers(context.Background(), 2, []uint64{10}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
