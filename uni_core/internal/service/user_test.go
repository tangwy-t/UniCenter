package service

import (
	"context"
	"errors"
	"testing"

	"gorm.io/gorm"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"github.com/tangwy-t/webmanager-server/internal/pkg/contextkeys"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
)

// stubUserRepo satisfies UserRepositoryInterface for service tests.
// Only FindByID/Update/ReplaceRoles are exercised; others are no-ops.
type stubUserRepo struct {
	findByIDUser *entity.SysUser
	findByIDErr  error
	updateErr    error
	replaceErr   error

	updateUser     *entity.SysUser
	replaceCalled  bool
	replaceID      uint64
	replaceRoleIDs []uint64

	// 角色成员调整(user_role_test.go 使用)
	findRoleCode    string
	findRoleCodeErr error
	addErr          error
	removeErr       error

	addRoleID     uint64
	addUserIDs    []uint64
	removeRoleID  uint64
	removeUserIDs []uint64

	// ID 存在性校验的返回值: nil 表示"全部存在"(默认放行)。
	existingIDs     []uint64
	existingRoleIDs []uint64
}

func (m *stubUserRepo) FindByID(ctx context.Context, id uint64) (*entity.SysUser, error) {
	if m.findByIDErr != nil {
		return nil, m.findByIDErr
	}
	return m.findByIDUser, nil
}
func (m *stubUserRepo) Update(ctx context.Context, u *entity.SysUser) error {
	m.updateUser = u
	return m.updateErr
}
func (m *stubUserRepo) ReplaceRoles(ctx context.Context, id uint64, rs []uint64) error {
	m.replaceCalled = true
	m.replaceID = id
	m.replaceRoleIDs = append([]uint64(nil), rs...)
	return m.replaceErr
}
func (m *stubUserRepo) FindPage(context.Context, *request.UserQuery) ([]entity.SysUser, int64, error) {
	return nil, 0, nil
}
func (m *stubUserRepo) FindByUsername(context.Context, string) (*entity.SysUser, error) {
	return nil, nil
}
func (m *stubUserRepo) CreateWithRoles(context.Context, *entity.SysUser, []uint64) error {
	return nil
}
func (m *stubUserRepo) Delete(context.Context, uint64) error             { return nil }
func (m *stubUserRepo) UpdateStatus(context.Context, uint64, int8) error { return nil }
func (m *stubUserRepo) UpdatePassword(context.Context, uint64, string, *string) error {
	return nil
}
func (m *stubUserRepo) SetMustChangePassword(context.Context, uint64, bool) error {
	return nil
}
func (m *stubUserRepo) FindRoleCodeByID(ctx context.Context, roleID uint64) (string, error) {
	if m.findRoleCodeErr != nil {
		return "", m.findRoleCodeErr
	}
	if m.findRoleCode != "" {
		return m.findRoleCode, nil
	}
	// 默认返回良性非 admin code：AssignRoles/Create 的新守卫只拦截 "admin"，
	// 普通角色 code 应放行（与生产 FindRoleCodeByID 返回真实 code 语义一致）。
	return "dev", nil
}
func (m *stubUserRepo) AddUsersToRole(ctx context.Context, roleID uint64, userIDs []uint64) error {
	m.addRoleID = roleID
	m.addUserIDs = append([]uint64(nil), userIDs...)
	return m.addErr
}
func (m *stubUserRepo) RemoveUsersFromRole(ctx context.Context, roleID uint64, userIDs []uint64) error {
	m.removeRoleID = roleID
	m.removeUserIDs = append([]uint64(nil), userIDs...)
	return m.removeErr
}
func (m *stubUserRepo) FindExistingIDs(ctx context.Context, ids []uint64) ([]uint64, error) {
	if m.existingIDs != nil {
		return m.existingIDs, nil
	}
	return ids, nil
}
func (m *stubUserRepo) FindExistingRoleIDs(ctx context.Context, ids []uint64) ([]uint64, error) {
	if m.existingRoleIDs != nil {
		return m.existingRoleIDs, nil
	}
	return ids, nil
}

func newTestUserService(repo UserRepositoryInterface) *UserService {
	return NewUserService(repo, logger.NewNop(), nil, nil)
}

func ctxWithOperator(id uint64) context.Context {
	return contextkeys.WithUserID(context.Background(), id)
}

func assertCode(t *testing.T, err error, want int) {
	t.Helper()
	var ae *apperror.AppError
	if !errors.As(err, &ae) {
		t.Fatalf("expected AppError(code=%d), got %v", want, err)
	}
	if ae.Code != want {
		t.Fatalf("expected code=%d, got %d (%v)", want, ae.Code, err)
	}
}

// --- AssignRoles 自保护（安全关键）---

func TestAssignRoles_RejectsSelfModify(t *testing.T) {
	svc := newTestUserService(&stubUserRepo{findByIDUser: &entity.SysUser{}})
	err := svc.AssignRoles(ctxWithOperator(5), 5, []uint64{1, 2})
	assertCode(t, err, apperror.CodeBadRequest)
}

func TestAssignRoles_RejectsSelfModify_AdminNotExempt(t *testing.T) {
	svc := newTestUserService(&stubUserRepo{findByIDUser: &entity.SysUser{}})
	// 操作者即目标，admin 角色不被豁免
	err := svc.AssignRoles(ctxWithOperator(1), 1, []uint64{1})
	assertCode(t, err, apperror.CodeBadRequest)
}

func TestAssignRoles_NoOperator_AllowsOther(t *testing.T) {
	// ok=false（无 operator）时不触发自保护，按他人处理
	repo := &stubUserRepo{findByIDUser: &entity.SysUser{}}
	svc := newTestUserService(repo)
	if err := svc.AssignRoles(context.Background(), 7, []uint64{1}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !repo.replaceCalled || repo.replaceID != 7 {
		t.Fatalf("expected ReplaceRoles(id=7), got %+v", repo)
	}
}

// --- AssignRoles 正常与边界 ---

func TestAssignRoles_ReplaceSuccess(t *testing.T) {
	repo := &stubUserRepo{findByIDUser: &entity.SysUser{}}
	svc := newTestUserService(repo)
	if err := svc.AssignRoles(ctxWithOperator(9), 7, []uint64{3, 4}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repo.replaceRoleIDs) != 2 || repo.replaceRoleIDs[0] != 3 || repo.replaceRoleIDs[1] != 4 {
		t.Fatalf("unexpected roleIDs: %v", repo.replaceRoleIDs)
	}
}

func TestAssignRoles_EmptyClearsRoles(t *testing.T) {
	repo := &stubUserRepo{findByIDUser: &entity.SysUser{}}
	svc := newTestUserService(repo)
	if err := svc.AssignRoles(ctxWithOperator(9), 7, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !repo.replaceCalled || len(repo.replaceRoleIDs) != 0 {
		t.Fatalf("expected empty roleIDs clear, got %+v", repo)
	}
}

func TestAssignRoles_NotFound(t *testing.T) {
	repo := &stubUserRepo{findByIDErr: gorm.ErrRecordNotFound}
	svc := newTestUserService(repo)
	err := svc.AssignRoles(ctxWithOperator(9), 7, []uint64{1})
	assertCode(t, err, apperror.CodeNotFound)
	if repo.replaceCalled {
		t.Fatal("ReplaceRoles must not be called for missing user")
	}
}

// --- UpdateUserInfo ---

func TestUpdateUserInfo_UpdatesInfoOnly(t *testing.T) {
	repo := &stubUserRepo{findByIDUser: &entity.SysUser{BaseEntity: entity.BaseEntity{ID: 7}}}
	svc := newTestUserService(repo)
	name := "alice"
	if err := svc.UpdateUserInfo(context.Background(), &request.UpdateUserReq{ID: 7, RealName: &name}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.updateUser == nil || repo.updateUser.ID != 7 {
		t.Fatalf("Update not called with id=7: %+v", repo)
	}
	if repo.updateUser.RealName == nil || *repo.updateUser.RealName != name {
		t.Fatalf("RealName not propagated: %v", repo.updateUser.RealName)
	}
	if repo.replaceCalled {
		t.Fatal("ReplaceRoles must not be called from UpdateUserInfo")
	}
}

func TestUpdateUserInfo_NotFound(t *testing.T) {
	repo := &stubUserRepo{findByIDErr: gorm.ErrRecordNotFound}
	svc := newTestUserService(repo)
	err := svc.UpdateUserInfo(context.Background(), &request.UpdateUserReq{ID: 7})
	assertCode(t, err, apperror.CodeNotFound)
}
