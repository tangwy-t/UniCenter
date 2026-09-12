package datascope

import (
	"context"
)

// DeptDimensionResolver resolves the "dept" dimension data scope.
type DeptDimensionResolver struct {
	authRepo AuthRepoInterface
	deptRepo DeptRepoInterface
}

// NewDeptDimensionResolver creates a DeptDimensionResolver.
func NewDeptDimensionResolver(authRepo AuthRepoInterface, deptRepo DeptRepoInterface) *DeptDimensionResolver {
	return &DeptDimensionResolver{authRepo: authRepo, deptRepo: deptRepo}
}

// DimensionType returns "dept".
func (r *DeptDimensionResolver) DimensionType() string { return "dept" }

// Resolve resolves the accessible department ID set based on the scope level.
func (r *DeptDimensionResolver) Resolve(ctx context.Context, level int8, selfID uint64, userID uint64) (*ResolvedDimension, error) {
	dim := &ResolvedDimension{Level: level, SelfID: selfID}
	switch level {
	case ScopeAll:
		dim.AllowedIDs = nil // nil = all
	case ScopeCustom:
		ids, err := r.authRepo.FindRoleDeptIDs(ctx, userID)
		if err != nil {
			return nil, err
		}
		// 自定义范围 =「仅这些部门」。未勾选任何部门时仓库返回 nil,而
		// plugin.scopeCallback 把 nil 读作「授权全部」、空集才表示「无可见项」。
		// 这里把 nil 归一为空集,防止自定义范围未配置部门时 fail-open 放大为全量。
		if ids == nil {
			ids = []uint64{}
		}
		dim.AllowedIDs = ids
	case ScopeDept:
		dim.AllowedIDs = []uint64{selfID}
	case ScopeDeptAndBelow:
		childIDs, err := r.deptRepo.FindChildDeptIDs(ctx, selfID)
		if err != nil {
			return nil, err
		}
		dim.AllowedIDs = append([]uint64{selfID}, childIDs...)
	case ScopeSelf:
		dim.AllowedIDs = nil // dept dimension does not apply for self-only scope
	default:
		dim.AllowedIDs = []uint64{}
	}
	return dim, nil
}
