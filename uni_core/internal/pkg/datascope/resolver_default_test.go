package datascope_test

import (
	"context"
	"testing"

	"github.com/tangwy-t/webmanager-server/internal/pkg/datascope"
)

// stubRoleMenuRepo 返回固定的角色菜单 ID 集合。
type stubRoleMenuRepo struct {
	ids []uint64
	err error
}

func (s stubRoleMenuRepo) FindRoleMenuIDs(context.Context, uint64) ([]uint64, error) {
	return s.ids, s.err
}

// TestRoleResolver_UnknownLevelFailsClosed 是 P2 回归测试。
//
// role 维度由 buildUserScopes 无条件附加到每个用户的 scopes 上,而
// RoleDimensionResolver 此前只处理 ScopeAll/ScopeCustom,其余 level
// 让 AllowedIDs 保持 nil —— 在 plugin.scopeCallback 中 nil 表示
// "授予全部",于是未知 level 会被静默放大为"可见全部菜单"。
// 当前 GetUserRoleScope 只产出这两个 level,所以不可达;本测试锁定
// "未知 level 必须 fail-closed(空集)",防止上游新增 level 时静默越权。
func TestRoleResolver_UnknownLevelFailsClosed(t *testing.T) {
	r := datascope.NewRoleDimensionResolver(stubRoleMenuRepo{ids: []uint64{1, 2}})

	unknownLevels := []int8{0, 6, 7, -1, 127}
	for _, level := range unknownLevels {
		dim, err := r.Resolve(context.Background(), level, 0, 1)
		if err != nil {
			t.Fatalf("level %d: Resolve: %v", level, err)
		}
		if dim.AllowedIDs == nil {
			t.Errorf("level %d: AllowedIDs = nil(全量可见),want 空集(fail-closed)", level)
			continue
		}
		if len(dim.AllowedIDs) != 0 {
			t.Errorf("level %d: AllowedIDs = %v, want 空集", level, dim.AllowedIDs)
		}
	}
}

// TestRoleResolver_KnownLevelsUnchanged ScopeAll → nil(全量),
// ScopeCustom → 仓库返回的角色菜单集合。行为不得被 fail-closed 改动影响。
// TestRoleResolver_CustomEmptyFailsClosed 自定义范围未分配任何菜单时仓库返回 nil,
// 必须归一为空集(fail-closed)。nil 在 plugin.scopeCallback 中 = 授权全部菜单。
func TestRoleResolver_CustomEmptyFailsClosed(t *testing.T) {
	r := datascope.NewRoleDimensionResolver(stubRoleMenuRepo{ids: nil})

	dim, err := r.Resolve(context.Background(), datascope.ScopeCustom, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if dim.AllowedIDs == nil {
		t.Fatalf("ScopeCustom(空) AllowedIDs = nil(全量可见), want 空集(fail-closed)")
	}
	if len(dim.AllowedIDs) != 0 {
		t.Fatalf("ScopeCustom(空) AllowedIDs = %v, want 空集", dim.AllowedIDs)
	}
}

func TestRoleResolver_KnownLevelsUnchanged(t *testing.T) {
	r := datascope.NewRoleDimensionResolver(stubRoleMenuRepo{ids: []uint64{5, 6}})

	all, err := r.Resolve(context.Background(), datascope.ScopeAll, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if all.AllowedIDs != nil {
		t.Errorf("ScopeAll AllowedIDs = %v, want nil(全量)", all.AllowedIDs)
	}

	custom, err := r.Resolve(context.Background(), datascope.ScopeCustom, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(custom.AllowedIDs) != 2 || custom.AllowedIDs[0] != 5 {
		t.Errorf("ScopeCustom AllowedIDs = %v, want [5 6]", custom.AllowedIDs)
	}
}
