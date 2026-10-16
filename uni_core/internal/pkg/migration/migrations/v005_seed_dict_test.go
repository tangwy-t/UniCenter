package migrations

import (
	"testing"
)

// ── 字典种子守卫 ──────────────────────────────────────────────────────────
//
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
