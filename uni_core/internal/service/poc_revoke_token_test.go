package service

import (
	"context"
	"testing"
	"time"

	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/session"
)

// 本文件是「角色/数据范围变更只清权限缓存、不吊销 token」（评审报告 #3）
// 修复后的回归验证。
//
// 修复内容：AssignRoles 现在调用 RevokeAll 吊销目标用户的全部已签发 access
// token（与禁用/删除/改密一致），使降权/降职立即生效，而非等 accessExpire。

// pocRevokeSessionStore 同时记录 RevokePerms 与 RevokeAll 的调用。
type pocRevokeSessionStore struct {
	revokePerms []uint64
	revokeAll   []uint64
}

func (s *pocRevokeSessionStore) StoreAccess(context.Context, string, uint64, time.Duration) error {
	return nil
}
func (s *pocRevokeSessionStore) StoreRefresh(context.Context, uint64, string, time.Duration) error {
	return nil
}
func (s *pocRevokeSessionStore) StorePerms(context.Context, uint64, string, []string, time.Duration) error {
	return nil
}
func (s *pocRevokeSessionStore) RevokeAll(_ context.Context, uid uint64, _ string) error {
	s.revokeAll = append(s.revokeAll, uid)
	return nil
}
func (s *pocRevokeSessionStore) GetRefresh(context.Context, uint64) (string, error) { return "", nil }
func (s *pocRevokeSessionStore) DeleteRefresh(context.Context, uint64) error        { return nil }
func (s *pocRevokeSessionStore) RotateRefresh(context.Context, uint64, string, string, time.Duration) (bool, error) {
	return true, nil
}
func (s *pocRevokeSessionStore) RevokePerms(_ context.Context, uid uint64) error {
	s.revokePerms = append(s.revokePerms, uid)
	return nil
}
func (s *pocRevokeSessionStore) RevokeAllPerms(context.Context) error { return nil }
func (s *pocRevokeSessionStore) StoreSessionMeta(context.Context, string, *session.SessionMeta, time.Duration) error {
	return nil
}

// 角色变更必须吊销目标用户的全部会话（RevokeAll），使旧 scope 立即失效。
func TestPoc_AssignRoles_RevokesAllSessions(t *testing.T) {
	ss := &pocRevokeSessionStore{}
	repo := &stubUserRepo{findByIDUser: &entity.SysUser{}}
	svc := newTestUserServiceWithSession(repo, ss)

	if err := svc.AssignRoles(context.Background(), 7, []uint64{2, 3}); err != nil {
		t.Fatalf("AssignRoles 失败: %v", err)
	}

	if len(ss.revokeAll) == 0 || ss.revokeAll[0] != 7 {
		t.Fatalf("角色变更应 RevokeAll(7) 吊销全部会话，实际 %v —— 降权仍不立即生效", ss.revokeAll)
	}
}
