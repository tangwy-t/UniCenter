package service

import (
	"testing"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/dto/response"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

func pstr(s string) *string { return &s }

// CreateUserReq → SysUser(经 CopyEntity)
func TestCopyEntity_CreateUserReq_CarriesNicknameGender(t *testing.T) {
	user := &entity.SysUser{}
	util.CopyEntity(user, &request.CreateUserReq{
		Nickname: pstr("昵称A"),
		Gender:   pstr("1"),
	}, logger.NewNop())
	if user.Nickname == nil || *user.Nickname != "昵称A" {
		t.Fatalf("Nickname 未映射: %v", user.Nickname)
	}
	if user.Gender == nil || *user.Gender != "1" {
		t.Fatalf("Gender 未映射: %v", user.Gender)
	}
}

// SysUser → UserResp / UserInfoResp(经 MapEntity/CopyEntity,*string→string)
func TestMapEntity_UserResp_CarriesNicknameGender(t *testing.T) {
	user := &entity.SysUser{Nickname: pstr("昵称A"), Gender: pstr("2")}
	resp := util.MapEntity[response.UserResp](user, logger.NewNop())
	if resp.Nickname != "昵称A" || resp.Gender != "2" {
		t.Fatalf("UserResp 未映射: %+v", resp)
	}
	info := &response.UserInfoResp{}
	util.CopyEntity(info, user, logger.NewNop())
	if info.Nickname != "昵称A" || info.Gender != "2" {
		t.Fatalf("UserInfoResp 未映射: %+v", info)
	}
}
