package service

import (
	"context"
	"testing"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
)

// 本文件是「AssignRoles / Create 缺"admin 角色不可授"守卫」（评审报告 #5）
// 修复后的回归验证。
//
// 修复内容：AssignRoles 与 Create 在授予角色时，对目标角色逐一校验 code，
// 拦截内置 admin 角色（与 AddRoleUsers/RemoveRoleUsers 的
// guardRoleMembershipEditable 对齐）。

// AssignRoles 授予 admin 角色应被拒绝。
func TestPoc_AssignRoles_RejectsAdminRole(t *testing.T) {
	repo := &stubUserRepo{
		findByIDUser:    &entity.SysUser{},
		existingRoleIDs: []uint64{1},
		findRoleCode:    "admin", // 角色 1 = admin
	}
	svc := newTestUserServiceWithSession(repo, &stubSessionStore{})

	err := svc.AssignRoles(context.Background(), 7, []uint64{1})
	assertCode(t, err, apperror.CodeBadRequest)
	if repo.replaceCalled {
		t.Fatal("admin 角色不应被授予（ReplaceRoles 不应被调用）")
	}
}

// AssignRoles 授予普通角色仍应放行。
func TestPoc_AssignRoles_AllowsNonAdminRole(t *testing.T) {
	repo := &stubUserRepo{
		findByIDUser:    &entity.SysUser{},
		existingRoleIDs: []uint64{2},
		findRoleCode:    "dev",
	}
	svc := newTestUserServiceWithSession(repo, &stubSessionStore{})

	if err := svc.AssignRoles(context.Background(), 7, []uint64{2}); err != nil {
		t.Fatalf("授予普通角色应放行，实际 %v", err)
	}
	if !repo.replaceCalled || repo.replaceID != 7 {
		t.Fatal("普通角色应正常 ReplaceRoles")
	}
}

// Create 新建账号时授予 admin 角色应被拒绝。
func TestPoc_CreateUser_RejectsAdminRole(t *testing.T) {
	repo := &stubUserRepo{
		existingRoleIDs: []uint64{1},
		findRoleCode:    "admin", // 角色 1 = admin
	}
	svc := newTestUserServiceWithSession(repo, &stubSessionStore{})

	req := &request.CreateUserReq{
		Username: "newadmin",
		Password: "password123",
		RoleIDs:  []uint64{1},
	}
	if _, err := svc.Create(context.Background(), req); err == nil {
		t.Fatal("Create 授予 admin 角色应被拒绝")
	}
}
