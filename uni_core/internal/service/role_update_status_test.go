package service

import (
	"context"
	"testing"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"

	"gorm.io/gorm"
)

// stubRoleRepo 满足 RoleRepositoryInterface 的最小替身。
type stubRoleRepo struct {
	findByIDRole *entity.SysRole
	findByIDErr  error

	updateStatusCalled bool
	updateStatusID     uint64
	updateStatusVal    int8
	updateStatusErr    error

	updateSortID    uint64
	updateSortVal   int
	updateSortCalls int
	updateSortErr   error

	userIDsByRole  []uint64
	findUserIDsErr error

	// ID 存在性校验的返回值: nil 表示"全部存在"(默认放行)。
	existingMenuIDs []uint64
	existingDeptIDs []uint64
}

func (m *stubRoleRepo) FindPage(context.Context, *request.RoleQuery) ([]entity.SysRole, int64, error) {
	return nil, 0, nil
}
func (m *stubRoleRepo) FindAll(context.Context) ([]entity.SysRole, error) { return nil, nil }
func (m *stubRoleRepo) FindByID(ctx context.Context, id uint64) (*entity.SysRole, error) {
	if m.findByIDErr != nil {
		return nil, m.findByIDErr
	}
	return m.findByIDRole, nil
}
func (m *stubRoleRepo) CreateWithAssociations(context.Context, *entity.SysRole, []uint64, []uint64) error {
	return nil
}
func (m *stubRoleRepo) UpdateWithAssociationsAndUserIDs(context.Context, *entity.SysRole, []uint64, []uint64) ([]uint64, error) {
	return nil, nil
}
func (m *stubRoleRepo) Delete(context.Context, uint64) error                 { return nil }
func (m *stubRoleRepo) DeleteWithAssociations(context.Context, uint64) error { return nil }
func (m *stubRoleRepo) FindUserIDsByRoleID(ctx context.Context, roleID uint64) ([]uint64, error) {
	if m.findUserIDsErr != nil {
		return nil, m.findUserIDsErr
	}
	return m.userIDsByRole, nil
}
func (m *stubRoleRepo) UpdateStatus(ctx context.Context, id uint64, status int8) error {
	m.updateStatusCalled = true
	m.updateStatusID = id
	m.updateStatusVal = status
	return m.updateStatusErr
}
func (m *stubRoleRepo) UpdateSort(ctx context.Context, id uint64, sort int) error {
	m.updateSortCalls++
	m.updateSortID = id
	m.updateSortVal = sort
	return m.updateSortErr
}
func (m *stubRoleRepo) FindExistingMenuIDs(ctx context.Context, ids []uint64) ([]uint64, error) {
	if m.existingMenuIDs != nil {
		return m.existingMenuIDs, nil
	}
	return ids, nil
}
func (m *stubRoleRepo) FindExistingDeptIDs(ctx context.Context, ids []uint64) ([]uint64, error) {
	if m.existingDeptIDs != nil {
		return m.existingDeptIDs, nil
	}
	return ids, nil
}

func newTestRoleService(repo RoleRepositoryInterface, ss SessionStoreInterface) *RoleService {
	return NewRoleService(repo, logger.NewNop(), ss)
}

// ── UpdateStatus ────────────────────────────────────────────────────

func TestRoleUpdateStatus_AdminRoleNotToggleable(t *testing.T) {
	repo := &stubRoleRepo{findByIDRole: &entity.SysRole{Code: "admin"}}
	svc := newTestRoleService(repo, &stubSessionStore{})
	err := svc.UpdateStatus(context.Background(), 1, 0)
	assertCode(t, err, apperror.CodeBadRequest)
	if repo.updateStatusCalled {
		t.Fatal("UpdateStatus should not be called for admin role")
	}
}

func TestRoleUpdateStatus_RoleNotFound(t *testing.T) {
	repo := &stubRoleRepo{findByIDErr: gorm.ErrRecordNotFound}
	svc := newTestRoleService(repo, &stubSessionStore{})
	err := svc.UpdateStatus(context.Background(), 999, 1)
	assertCode(t, err, apperror.CodeNotFound)
}

func TestRoleUpdateStatus_Success_RevokesUsersSessions(t *testing.T) {
	repo := &stubRoleRepo{
		findByIDRole:  &entity.SysRole{Code: "dev"},
		userIDsByRole: []uint64{10, 11},
	}
	ss := &stubSessionStore{}
	svc := newTestRoleService(repo, ss)
	if err := svc.UpdateStatus(context.Background(), 2, 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !repo.updateStatusCalled || repo.updateStatusID != 2 || repo.updateStatusVal != 0 {
		t.Fatalf("unexpected update args: called=%v id=%d status=%d",
			repo.updateStatusCalled, repo.updateStatusID, repo.updateStatusVal)
	}
	// 安全修复（#3）：停用角色须 RevokeAll 吊销受影响用户的全部会话。
	if len(ss.revokeAll) != 2 || ss.revokeAll[0] != 10 || ss.revokeAll[1] != 11 {
		t.Fatalf("expected RevokeAll for [10 11], got %v", ss.revokeAll)
	}
}

func TestRoleUpdateStatus_NilSessionStore_NoPanic(t *testing.T) {
	repo := &stubRoleRepo{
		findByIDRole:  &entity.SysRole{Code: "dev"},
		userIDsByRole: []uint64{10},
	}
	svc := newTestRoleService(repo, nil)
	// 无 sessionStore(内部调用)时仅更新状态,不依赖缓存
	if err := svc.UpdateStatus(context.Background(), 2, 1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !repo.updateStatusCalled {
		t.Fatal("UpdateStatus should be called")
	}
}

// ── UpdateSort ──────────────────────────────────────────────────────

func TestRoleUpdateSort_BatchOverwrite(t *testing.T) {
	repo := &stubRoleRepo{findByIDRole: &entity.SysRole{Code: "dev"}}
	svc := newTestRoleService(repo, &stubSessionStore{})
	req := &request.UpdateRoleSortReq{
		Items: []request.RoleSortItem{
			{ID: 10, Sort: 3},
			{ID: 11, Sort: 1},
			{ID: 12, Sort: 2},
		},
	}
	if err := svc.UpdateSort(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.updateSortCalls != 3 {
		t.Fatalf("expected 3 UpdateSort calls, got %d", repo.updateSortCalls)
	}
	// 最后一个调用应为 ID=12,Sort=2
	if repo.updateSortID != 12 || repo.updateSortVal != 2 {
		t.Fatalf("unexpected last UpdateSort args: id=%d sort=%d", repo.updateSortID, repo.updateSortVal)
	}
}

func TestRoleUpdateSort_RoleNotFound(t *testing.T) {
	repo := &stubRoleRepo{findByIDErr: gorm.ErrRecordNotFound}
	svc := newTestRoleService(repo, &stubSessionStore{})
	req := &request.UpdateRoleSortReq{
		Items: []request.RoleSortItem{{ID: 999, Sort: 1}},
	}
	err := svc.UpdateSort(context.Background(), req)
	assertCode(t, err, apperror.CodeNotFound)
	if repo.updateSortCalls != 0 {
		t.Fatal("UpdateSort should not be called when role not found")
	}
}

func TestRoleUpdateSort_AdminRoleAllowed(t *testing.T) {
	// 排序是纯展示字段,内置 admin 角色也允许调整显示顺序
	repo := &stubRoleRepo{findByIDRole: &entity.SysRole{Code: "admin"}}
	svc := newTestRoleService(repo, &stubSessionStore{})
	req := &request.UpdateRoleSortReq{
		Items: []request.RoleSortItem{{ID: 1, Sort: 0}},
	}
	if err := svc.UpdateSort(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.updateSortCalls != 1 {
		t.Fatal("UpdateSort should be called for admin role")
	}
}
