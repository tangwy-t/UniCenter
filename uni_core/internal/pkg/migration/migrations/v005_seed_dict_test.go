package migrations

import (
	"testing"
)

// ── 字典种子守卫（7c 归一后形态）──────────────────────────────────────────
//
// v017 的审计字典码（70001 执行失败）已并入 v005 的 dictDefinitions，v017 是墓碑
// —— 本文件钉住归一结果不被悄悄丢掉：70001 这行一旦从初始种子里消失，fresh 库
// 的操作日志页就会把失败任务显示成「成功」或借一个语义错位的既有码，而没有任何
// 测试变红（旧 v017 的落库路径已随墓碑删除）。
func TestDictSeedIncludesDockerAuditResultCode(t *testing.T) {
	for _, d := range dictDefinitions {
		if d.Code != "sys_opt_result_code" {
			continue
		}
		var last *dictDataDef
		for i := range d.Data {
			item := &d.Data[i]
			if item.Value == "70001" {
				if item.Label != "执行失败" || item.Class != "danger" || item.Sort != 14 {
					t.Fatalf("70001 的字典行应为 {执行失败, danger, Sort 14}，实际 %+v", *item)
				}
			}
			if last != nil && item.Sort <= last.Sort {
				t.Fatalf("sys_opt_result_code 的 Sort 必须严格递增（%s Sort=%d 排在 %s Sort=%d 之后）",
					item.Value, item.Sort, last.Value, last.Sort)
			}
			last = item
		}
		if last == nil || last.Value != "70001" {
			t.Fatalf("70001「执行失败」必须是 sys_opt_result_code 的最后一行（原 v017 种子，7c 并入），实际末行 %+v", last)
		}
		return
	}
	t.Fatal("字典种子缺 sys_opt_result_code 类型")
}

// 字典数据的身份是 (type, value)：同 type 下重复 value 会让 UI 的下拉/渲染出现
// 两个同名项，且种子的「合并历史修正」取向（文案以最后一次书写为准）会静默失效。
// 归一后全部历史批次都在 dictDefinitions 一个列表里 —— 重复在这里现形，好过在
// 线上菜单里现形。
func TestDictSeedValuesUniquePerType(t *testing.T) {
	for _, d := range dictDefinitions {
		seen := map[string]bool{}
		for _, item := range d.Data {
			if seen[item.Value] {
				t.Fatalf("字典类型 %s 里 value %q 重复（数据身份是 type+value）", d.Code, item.Value)
			}
			seen[item.Value] = true
		}
	}
}
