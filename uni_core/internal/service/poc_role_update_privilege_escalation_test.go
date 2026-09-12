package service

import (
	"context"
	"testing"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
)

// 本文件是「角色 Update 缺 admin 守卫 + 全量批量赋值 → 竖直提权」
// （评审报告 #4）修复后的回归验证。
//
// 修复内容：Update 增加 admin 哨兵守卫 ——
//  1. 内置 admin 角色的 Code 不允许被改成其他值（防止释放 uk_role_code）。
//  2. 任意非 admin 角色的 Code 不允许被改成 "admin"（防止伪造第二个哨兵）。

// 内置 admin 角色的 Code 不允许被改名。
func TestPoc_RoleUpdate_AdminCodeImmutable(t *testing.T) {
	role := &entity.SysRole{Code: "admin"}
	repo := &stubRoleRepo{findByIDRole: role}
	svc := newTestRoleService(repo, &stubSessionStore{})

	err := svc.Update(context.Background(), &request.UpdateRoleReq{ID: 1, Name: "x", Code: "hacked"})
	assertCode(t, err, apperror.CodeBadRequest)
	if role.Code != "admin" {
		t.Fatal("admin 角色 Code 被改动 —— 守卫失效")
	}
}

// 任意非 admin 角色不允许把 Code 改成 "admin"（竖直提权路径被封堵）。
func TestPoc_RoleUpdate_CannotRenameToAdmin(t *testing.T) {
	role := &entity.SysRole{Code: "dev"}
	repo := &stubRoleRepo{findByIDRole: role}
	svc := newTestRoleService(repo, &stubSessionStore{})

	scopeAll := int8(1)
	err := svc.Update(context.Background(), &request.UpdateRoleReq{
		ID: 2, Name: "dev", Code: "admin", DataScope: &scopeAll,
	})
	assertCode(t, err, apperror.CodeBadRequest)
	if role.Code == "admin" {
		t.Fatal("非 admin 角色被批量赋值成 admin —— 竖直提权路径未封堵")
	}
}

// admin 角色改非 Code 字段（如 Name/Remark）仍应放行，避免过度收紧。
func TestPoc_RoleUpdate_AdminCanEditNonCodeFields(t *testing.T) {
	role := &entity.SysRole{Code: "admin", Name: "超级管理员"}
	repo := &stubRoleRepo{findByIDRole: role}
	svc := newTestRoleService(repo, &stubSessionStore{})

	err := svc.Update(context.Background(), &request.UpdateRoleReq{ID: 1, Name: "管理员", Code: "admin"})
	if err != nil {
		t.Fatalf("admin 角色改 Name（Code 不变）应放行，实际 %v", err)
	}
	if role.Name != "管理员" {
		t.Fatalf("Name 未生效，实际 %q", role.Name)
	}
}
