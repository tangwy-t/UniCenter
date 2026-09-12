package datascope

import (
	"context"
)

// RoleDimensionResolver resolves the "role" dimension data scope for menu filtering.
type RoleDimensionResolver struct {
	roleMenuRepo RoleMenuRepoInterface
}

// NewRoleDimensionResolver creates a RoleDimensionResolver.
func NewRoleDimensionResolver(roleMenuRepo RoleMenuRepoInterface) *RoleDimensionResolver {
	return &RoleDimensionResolver{roleMenuRepo: roleMenuRepo}
}

// DimensionType returns "role".
func (r *RoleDimensionResolver) DimensionType() string { return DimRole }

// Resolve resolves the accessible menu ID set based on the scope level.
//
// ScopeAll → nil(全量);ScopeCustom → 该用户角色已授权的菜单 ID。
//
// 其余 level 一律返回空集(fail-closed),**不是** nil。role 维度只由
// GetUserRoleScope 产出(非 admin 恒为 ScopeCustom),其余 level 目前
// 不可达;但此前的 default 分支让 AllowedIDs 保持 nil(= 全量可见),
// 一旦上游新增 level 或传值出错,就会静默把"未知"放大成"看到全部菜单"。
// 与 DeptDimensionResolver 的显式 default 空集保持一致:未知即无权限。
// 注意 nil(全量)与空集(无权限)在 plugin.scopeCallback 中语义相反。
func (r *RoleDimensionResolver) Resolve(ctx context.Context, level int8, selfID uint64, userID uint64) (*ResolvedDimension, error) {
	dim := &ResolvedDimension{Level: level, SelfID: selfID}
	switch level {
	case ScopeAll:
		dim.AllowedIDs = nil // nil = all
	case ScopeCustom:
		ids, err := r.roleMenuRepo.FindRoleMenuIDs(ctx, userID)
		if err != nil {
			return nil, err
		}
		// 与 DeptDimensionResolver 同理:自定义 =「仅这些菜单」,仓库返回 nil
		// (未分配任何菜单)时必须归一为空集,避免 plugin 把 nil 放大为「全量菜单」。
		if ids == nil {
			ids = []uint64{}
		}
		dim.AllowedIDs = ids
	default:
		dim.AllowedIDs = []uint64{} // 未知 level → 无可见菜单,不放大为全量
	}
	return dim, nil
}
