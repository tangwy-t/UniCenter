package datascope

import (
	"context"

	"github.com/tangwy-t/webmanager-server/internal/pkg/datascope/rule"
)

// DataScopeable is re-exported from the rule package for backward compatibility.
type DataScopeable = rule.DataScopeable

// ScopeRule is re-exported from the rule package for backward compatibility.
type ScopeRule = rule.ScopeRule

// ScopeVia is re-exported from the rule package for backward compatibility.
type ScopeVia = rule.ScopeVia

// DimensionResolver resolves the accessible ID set for a given scope dimension.
type DimensionResolver interface {
	DimensionType() string
	Resolve(ctx context.Context, level int8, selfID uint64, userID uint64) (*ResolvedDimension, error)
}

type AuthRepoInterface interface {
	FindRoleDeptIDs(ctx context.Context, userID uint64) ([]uint64, error)
}

type RoleMenuRepoInterface interface {
	FindRoleMenuIDs(ctx context.Context, userID uint64) ([]uint64, error)
}

type DeptRepoInterface interface {
	FindChildDeptIDs(ctx context.Context, deptID uint64) ([]uint64, error)
}
