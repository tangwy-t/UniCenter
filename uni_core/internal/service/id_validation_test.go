package service

import (
	"context"
	"testing"

	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
)

// ── ID 存在性校验 ──────────────────────────────────────────────────

func TestAssignRoles_MissingRoleID_Returns400(t *testing.T) {
	repo := &stubUserRepo{
		findByIDUser:    &entity.SysUser{},
		existingRoleIDs: []uint64{1}, // 只有 1 存在
	}
	svc := newTestUserService(repo)
	err := svc.AssignRoles(context.Background(), 10, []uint64{1, 888})
	assertCode(t, err, apperror.CodeBadRequest)
	if repo.replaceCalled {
		t.Fatal("ReplaceRoles should not be called when a role ID is missing")
	}
}

func TestAssignRoles_DuplicateRoleIDs_NoFalseMissing(t *testing.T) {
	repo := &stubUserRepo{
		findByIDUser:    &entity.SysUser{},
		existingRoleIDs: []uint64{1},
	}
	svc := newTestUserService(repo)
	// 重复 ID 应先被去重, 不能因重复而误报"角色不存在"。
	err := svc.AssignRoles(context.Background(), 10, []uint64{1, 1})
	if err != nil {
		t.Fatalf("duplicate role IDs should pass validation, got %v", err)
	}
	if !repo.replaceCalled {
		t.Fatal("ReplaceRoles should be called")
	}
}

func TestAddRoleUsers_MissingUserID_Returns400(t *testing.T) {
	repo := &stubUserRepo{
		findRoleCode: "dev",
		existingIDs:  []uint64{10}, // 只有 10 存在
	}
	svc := newTestUserServiceWithSession(repo, &stubSessionStore{})
	err := svc.AddRoleUsers(context.Background(), 2, []uint64{10, 777})
	assertCode(t, err, apperror.CodeBadRequest)
	if len(repo.addUserIDs) != 0 {
		t.Fatalf("AddUsersToRole should not be called, got %v", repo.addUserIDs)
	}
}
