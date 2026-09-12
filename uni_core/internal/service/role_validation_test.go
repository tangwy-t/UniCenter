package service

import (
	"context"
	"testing"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

func TestRoleUpdate_MissingMenuID_Returns400(t *testing.T) {
	repo := &stubRoleRepo{
		findByIDRole:    &entity.SysRole{Code: "dev"},
		existingMenuIDs: []uint64{5}, // 只有 5 存在
		existingDeptIDs: []uint64{},
	}
	svc := newTestRoleService(repo, &stubSessionStore{})

	req := &request.UpdateRoleReq{
		ID:      2,
		MenuIDs: util.JsonUint64Slice{5, 999},
		DeptIDs: util.JsonUint64Slice{},
	}
	err := svc.Update(context.Background(), req)
	assertCode(t, err, apperror.CodeBadRequest)
}

func TestRoleUpdate_MissingDeptID_Returns400(t *testing.T) {
	repo := &stubRoleRepo{
		findByIDRole:    &entity.SysRole{Code: "dev"},
		existingMenuIDs: []uint64{},
		existingDeptIDs: []uint64{7},
	}
	svc := newTestRoleService(repo, &stubSessionStore{})

	req := &request.UpdateRoleReq{
		ID:      2,
		MenuIDs: util.JsonUint64Slice{},
		DeptIDs: util.JsonUint64Slice{7, 888},
	}
	err := svc.Update(context.Background(), req)
	assertCode(t, err, apperror.CodeBadRequest)
}

func TestRoleCreate_MissingMenuID_Returns400(t *testing.T) {
	repo := &stubRoleRepo{
		existingMenuIDs: []uint64{5},
		existingDeptIDs: []uint64{},
	}
	svc := newTestRoleService(repo, &stubSessionStore{})

	req := &request.CreateRoleReq{
		Name:    "dev",
		Code:    "dev",
		MenuIDs: util.JsonUint64Slice{5, 999},
		DeptIDs: util.JsonUint64Slice{},
	}
	_, err := svc.Create(context.Background(), req)
	assertCode(t, err, apperror.CodeBadRequest)
}
