package entity

import (
	"testing"

	"github.com/tangwy-t/webmanager-server/internal/pkg/datascope/rule"
)

// dimsOf 汇总规则的维度集合。
func dimsOf(t *testing.T, e rule.DataScopeable) map[string]bool {
	t.Helper()
	rules := e.DataScopeRules()
	if len(rules) == 0 {
		t.Fatalf("%T.DataScopeRules() 为空:实体要么不注册(管理员级),要么必须声明至少一条规则", e)
	}
	dims := map[string]bool{}
	for _, r := range rules {
		if r.Column == "" {
			t.Fatalf("%T 规则列名为空", e)
		}
		dims[r.DimensionType] = true
	}
	return dims
}

// knownDimensions 是解析端(wireup)当前组装的维度全集。
// 新增维度必须同步修改 wireup.go 的 resolver 组装,并在本表登记。
var knownDimensions = map[string]bool{"dept": true, "self": true, "role": true}

// wantedScopeEntities 是注册清单的显式快照:表名 → 维度集合。
// 任何增删改都必须显式修改这里 —— 漏注册实体是"静默失去 scope 过滤"
// (IDOR 缺口),误删/误改维度同样没有运行时错误,契约测试是唯一防线。
var wantedScopeEntities = map[string]map[string]bool{
	"sys_user":          {"dept": true, "self": true},
	"sys_dept":          {"dept": true, "self": true},
	"sys_file":          {"dept": true, "self": true},
	"sys_notice_user":   {"dept": true, "self": true},
	"sys_operation_log": {"dept": true, "self": true},
	"sys_login_log":     {"dept": true, "self": true},
	"sys_menu":          {"role": true},
}

// TestScopeEntitiesRegistryContract ScopeEntities 清单完整性契约:
// 表名唯一(注册 map 键,重复即静默覆盖)、每实体规则非空、
// 维度全部位于解析端已知集合、且与显式快照逐一吻合。
func TestScopeEntitiesRegistryContract(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range ScopeEntities {
		tbl := e.(interface{ TableName() string }).TableName()
		if tbl == "" {
			t.Fatalf("%T.TableName() 为空", e)
		}
		if seen[tbl] {
			t.Fatalf("表名 %q 在 ScopeEntities 中重复(注册键覆盖会静默吞掉一个实体)", tbl)
		}
		seen[tbl] = true

		wantDims, ok := wantedScopeEntities[tbl]
		if !ok {
			t.Fatalf("表名 %q 未登记在 wantedScopeEntities 快照中:新增实体需在此显式声明并与产品确认", tbl)
		}
		gotDims := dimsOf(t, e)
		for d := range gotDims {
			if !knownDimensions[d] {
				t.Fatalf("%q.%T 使用未知维度 %q:解析端无对应 resolver,该规则永不生效", tbl, e, d)
			}
		}
		for d := range wantDims {
			if !gotDims[d] {
				t.Fatalf("%q 快照要求维度 %q,实际规则缺失", tbl, d)
			}
		}
	}
	if len(seen) != len(wantedScopeEntities) {
		t.Fatalf("ScopeEntities 有 %d 个注册实体,快照有 %d 个:两者必须相等", len(seen), len(wantedScopeEntities))
	}
}
