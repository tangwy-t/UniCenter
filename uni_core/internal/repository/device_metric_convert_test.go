package repository

import (
	"reflect"
	"regexp"
	"strconv"
	"testing"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agentmetrics"
)

var jsonTagRe = regexp.MustCompile(`json:"([^",]+)`)

// jsonTagName 取出字段的 json 名（不含 ,omitempty / ,string 等选项）。
// 缺 tag 即致命：两个结构体都靠 json tag 对齐，漏了 tag 就等于这一列在往返中丢掉。
func jsonTagName(t *testing.T, f reflect.StructField) string {
	t.Helper()
	m := jsonTagRe.FindStringSubmatch(string(f.Tag))
	if len(m) != 2 {
		t.Fatalf("字段 %s 缺少 json tag", f.Name)
	}
	return m[1]
}

// 守卫：agentmetrics.Wide 与 entity.DeviceMetricWide 的字段**必须逐一对应**
// （名字 + 类型 + 可空性）。转换用 JSON 往返实现，一旦两边漂移，JSON 往返会
// **静默丢字段** —— 这个守卫就是防那件事。
//
// 修正记录（Task 5 实测到的计划缺陷，探针证据见提交说明）：计划把 join key 定为
// 「Wide 字段名 → snake_case → 实体 gorm column 名」，但 column 名**不是能从 Go 字段名
// 机械推导的量**：
//  1. 计划里的朴素 snake（每个大写字母前插一个 _）把 DeviceID 变 device_i_d、TCPTotal 变
//     t_c_p_total —— 46 个字段里误报 28 个；
//  2. 换成「缩写感知」的 snake 后仍剩一个反例：CPUIOWait → cpu_io_wait，而实体列名是
//     cpu_iowait（那一列把 IO+Wait 写成单个词）。要对齐只能给这一个字段硬编码例外，
//     而 Task 5 明确禁止改 agentmetrics 的字段名。
//
// 故 join key 换成 **json tag 名**（这才是 JSON 往返真正的对齐键），并另外**逐字段要求
// 两边 Go 字段名与类型相同**（即计划原文的「名字 + 类型 + 可空性逐一对应」）。断言强度
// 不降：多了 json tag 本身的逐字比对，两个方向也都仍要求全覆盖；gorm column 与 DDL 的
// 一致性由既有的 TestMetricDDLColumnsMatchEntityTags 守。
func TestWideAndEntityFieldsMirror(t *testing.T) {
	wt := reflect.TypeOf(agentmetrics.Wide{})
	et := reflect.TypeOf(entity.DeviceMetricWide{})

	// 实体侧：Go 字段名 → 类型；json tag 名 → 类型。
	// 顺带要求每个字段都带 gorm column tag（宽表每一列都必须落库）。
	colRe := regexp.MustCompile("gorm:\"column:([a-z0-9_]+)")
	entityByName := map[string]reflect.Type{}
	entityByJSON := map[string]reflect.Type{}
	for i := 0; i < et.NumField(); i++ {
		f := et.Field(i)
		m := colRe.FindStringSubmatch(string(f.Tag))
		if len(m) != 2 {
			t.Fatalf("实体字段 %s 缺少 column tag", f.Name)
		}
		entityByName[f.Name] = f.Type
		entityByJSON[jsonTagName(t, f)] = f.Type
	}

	matched := map[string]bool{}
	for i := 0; i < wt.NumField(); i++ {
		f := wt.Field(i)
		// 1) Wide 的字段名必须在实体里有同名字段，且类型相同
		if et2, ok := entityByName[f.Name]; !ok {
			t.Errorf("agentmetrics.Wide.%s 在实体里没有同名字段（两边已漂移）", f.Name)
		} else if et2 != f.Type {
			t.Errorf("字段 %s 类型不一致: Wide=%s entity=%s", f.Name, f.Type, et2)
		}
		// 2) Wide 的 json tag 名必须在实体里存在且类型相同（JSON 往返靠它对齐）
		tag := jsonTagName(t, f)
		et2, ok := entityByJSON[tag]
		if !ok {
			t.Errorf("agentmetrics.Wide.%s 的 json tag %q 在实体里不存在（两边已漂移）", f.Name, tag)
			continue
		}
		matched[tag] = true
		if et2 != f.Type {
			t.Errorf("json tag %q 类型不一致: Wide=%s entity=%s", tag, f.Type, et2)
		}
	}
	// 反向：实体每一列都必须在 Wide 里有对应字段，否则 entity→Wide 会静默丢数据
	for tag := range entityByJSON {
		if !matched[tag] {
			t.Errorf("实体字段 json tag %q 在 agentmetrics.Wide 里没有对应字段（两边已漂移）", tag)
		}
	}
}

// fullWide 返回**每个字段都被设成互不相同的非零值**的 Wide。
//
// 为什么必须全覆盖：JSON 往返靠 tag 对齐，若某个 tag 拼错，该字段会在往返中
// 丢失；只设一部分字段的往返测试**抓不到**这种错误。用 reflect 自动铺满所有
// 指针字段，保证以后加列时也被覆盖。
func fullWide(t *testing.T) agentmetrics.Wide {
	t.Helper()
	var w agentmetrics.Wide
	w.DeviceID = 1001
	w.BucketTS = 1_700_000_000
	w.Samples = 30

	rv := reflect.ValueOf(&w).Elem()
	n := 0
	for i := 0; i < rv.NumField(); i++ {
		f := rv.Field(i)
		if !f.CanSet() || f.Kind() != reflect.Ptr {
			continue // 键列与 Samples 已手动设置
		}
		n++
		switch f.Type().Elem().Kind() {
		case reflect.Float64:
			v := float64(n) + 0.5
			f.Set(reflect.ValueOf(&v))
		case reflect.Int64:
			v := int64(n) * 100
			f.Set(reflect.ValueOf(&v))
		case reflect.String:
			v := "err-" + strconv.Itoa(n)
			f.Set(reflect.ValueOf(&v))
		default:
			t.Fatalf("未知指针元素类型 %s（新加列请在守卫测试里补分支）", f.Type())
		}
	}
	if n < 30 {
		t.Fatalf("覆盖到的指针字段只有 %d 个，宽表应有 40+ 列（守卫失效？）", n)
	}
	return w
}

func TestEntityFromWideRoundTrip(t *testing.T) {
	w := fullWide(t)

	e, err := EntityFromWide(w)
	if err != nil {
		t.Fatalf("EntityFromWide: %v", err)
	}
	if e.DeviceID != 1001 || e.BucketTS != 1_700_000_000 || e.Samples != 30 {
		t.Fatalf("键列丢失: %+v", e)
	}
	// 抽查两个代表性指针（float 与 string）是否真的落到了实体字段上。
	// 期望值直接取输入 w 的同名字段：fullWide 的值由「第 n 个指针字段 = n+0.5 / "err-n"」
	// 的生成规则决定（CPUUsedPercent 是第 1 个指针字段 → 1.5），把具体数字写死在断言里
	// 只会与 fullWide 的字段顺序耦合。
	if e.CPUUsedPercent == nil || *e.CPUUsedPercent != *w.CPUUsedPercent {
		t.Fatalf("cpu 丢失: %v（want %v）", e.CPUUsedPercent, *w.CPUUsedPercent)
	}
	if e.AgentLastReportError == nil || *e.AgentLastReportError != *w.AgentLastReportError {
		t.Fatalf("字符串指针丢失: %v（want %q）", e.AgentLastReportError, *w.AgentLastReportError)
	}

	back, err := WideFromEntity(e)
	if err != nil {
		t.Fatalf("WideFromEntity: %v", err)
	}
	if !reflect.DeepEqual(w, back) {
		t.Fatalf("JSON 往返必须无损（任何 tag 拼写错误都会在此暴露）:\nwant %+v\ngot  %+v", w, back)
	}
	// 外加逐字段点名，便于定位是哪个 tag 错了
	we, be := reflect.ValueOf(w), reflect.ValueOf(back)
	for i := 0; i < we.NumField(); i++ {
		if !reflect.DeepEqual(we.Field(i).Interface(), be.Field(i).Interface()) {
			t.Errorf("字段 %s 往返丢失（检查 Wide 与实体的 json tag 是否一致）", we.Type().Field(i).Name)
		}
	}
}

func TestWideFromEntityKeepsNils(t *testing.T) {
	e := &entity.DeviceMetricWide{DeviceID: 1001, BucketTS: 1}
	// 1h 行的 5min-only 列是 nil，往返后必须仍是 nil（不能变 0）
	e.TCPTimeWait = nil
	e.AgentReportSuccessCount = nil
	w, err := WideFromEntity(e)
	if err != nil {
		t.Fatal(err)
	}
	if w.TCPTimeWait != nil || w.AgentReportSuccessCount != nil {
		t.Fatal("nil 必须保持 nil（缺 ≠ 0）")
	}
	if w.Samples != 0 {
		t.Fatal("samples 非指针，零值即 0")
	}
}
