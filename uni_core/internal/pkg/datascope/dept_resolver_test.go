package datascope_test

import (
	"context"
	"testing"

	"github.com/tangwy-t/webmanager-server/internal/pkg/datascope"
)

type stubAuthRepo struct {
	deptIDs []uint64
	err     error
}

func (s stubAuthRepo) FindRoleDeptIDs(context.Context, uint64) ([]uint64, error) {
	return s.deptIDs, s.err
}

type stubDeptRepo struct{}

func (stubDeptRepo) FindChildDeptIDs(context.Context, uint64) ([]uint64, error) {
	return nil, nil
}

// TestDeptResolver_CustomEmptyFailsClosed 自定义范围未配置任何部门时仓库返回 nil,
// 必须归一为空集(fail-closed)。plugin.scopeCallback 中 nil = 授权全部,若原样透传,
// 自定义范围会越权看到全量部门/用户/文件等按 dept 维度过滤的数据。
func TestDeptResolver_CustomEmptyFailsClosed(t *testing.T) {
	r := datascope.NewDeptDimensionResolver(stubAuthRepo{deptIDs: nil}, stubDeptRepo{})

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

// TestDeptResolver_CustomExplicitList 自定义范围配置了部门时,原样返回该集合。
func TestDeptResolver_CustomExplicitList(t *testing.T) {
	r := datascope.NewDeptDimensionResolver(stubAuthRepo{deptIDs: []uint64{5, 6}}, stubDeptRepo{})

	dim, err := r.Resolve(context.Background(), datascope.ScopeCustom, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(dim.AllowedIDs) != 2 || dim.AllowedIDs[0] != 5 || dim.AllowedIDs[1] != 6 {
		t.Fatalf("ScopeCustom AllowedIDs = %v, want [5 6]", dim.AllowedIDs)
	}
}
