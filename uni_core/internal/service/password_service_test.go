package service

import (
	"context"
	"testing"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/contextkeys"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
)

type fakePwdRepo struct {
	user            *entity.SysUser
	updatedPassword string
	mustChange      *bool
}

func (f *fakePwdRepo) FindByID(ctx context.Context, id uint64) (*entity.SysUser, error) {
	return f.user, nil
}
func (f *fakePwdRepo) UpdatePassword(ctx context.Context, id uint64, pwd string, salt *string) error {
	f.updatedPassword = pwd
	return nil
}
func (f *fakePwdRepo) SetMustChangePassword(ctx context.Context, id uint64, m bool) error {
	f.mustChange = &m
	return nil
}

func TestPasswordService_ChangePassword_RejectsWeak(t *testing.T) {
	// fakePwdRepo.user 必须是密码为 "old" 的合法用户:ChangePassword 先验旧密码
	// 再查策略;user 为 nil 会空指针,而旧密码不匹配则会以「旧密码不正确」短路。
	svc := NewPasswordService(nil, &fakePwdRepo{user: newStubUser(t, "old")}, nil, logger.NewNop())
	ctx := contextkeys.WithUserID(context.Background(), 1)
	err := svc.ChangePassword(ctx, &request.ChangePasswordReq{
		OldPassword: "old",
		NewPassword: "short",
	}, "")
	if err == nil {
		t.Fatal("weak password accepted")
	}
}
