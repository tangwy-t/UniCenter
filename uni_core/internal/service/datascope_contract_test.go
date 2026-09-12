package service

import (
	"testing"

	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/datascope"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
)

// TestDimensionResolverCoverage 注册端(entity.ScopeEntities 规则维度)
// 与解析端(wireup 组装的 resolver 维度)必须精确匹配:
// 规则引用无 resolver 的维度 = 该规则永不生效;resolver 无规则引用 =
// wireup 冗余(或规则遗漏)。wireup 组装清单见 wireup/wireup.go。
func TestDimensionResolverCoverage(t *testing.T) {
	// 解析端:与 wireup 相同的三个维度 resolver。
	resolver := datascope.NewScopeResolver([]datascope.DimensionResolver{
		datascope.NewDeptDimensionResolver(nil, nil),
		datascope.NewSelfDimensionResolver(),
		datascope.NewRoleDimensionResolver(nil),
	}, logger.NewNop())

	resolvedDims := map[string]bool{}
	for d := range resolver.Resolvers {
		resolvedDims[d] = true
	}

	// 注册端:汇总 ScopeEntities 全部规则引用的维度。
	usedDims := map[string]bool{}
	for _, e := range entity.ScopeEntities {
		for _, r := range e.DataScopeRules() {
			usedDims[r.DimensionType] = true
		}
	}

	for d := range usedDims {
		if !resolvedDims[d] {
			t.Fatalf("实体规则引用维度 %q,但解析端缺少该 resolver(规则永不生效)", d)
		}
	}
	for d := range resolvedDims {
		if !usedDims[d] {
			t.Fatalf("解析端注册维度 %q,但没有任何实体规则引用(冗余或实体漏声明)", d)
		}
	}
}
