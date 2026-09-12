package pproftext

import (
	"testing"
)

// resolveFunc 是包级变量，测试注入固定 pc → 函数映射并恢复。
func withResolveFunc(t *testing.T, m map[uint64]funcFrame) {
	t.Helper()
	orig := resolveFunc
	resolveFunc = func(pc uintptr) (string, string, int, bool) {
		f, ok := m[uint64(pc)]
		if !ok {
			return "", "", 0, false
		}
		return f.name, f.file, f.line, true
	}
	t.Cleanup(func() { resolveFunc = orig })
}

func TestParseProfileText_HeapFamily(t *testing.T) {
	text := []byte("heap profile: 40: 4000 [40: 4000] @ heap/1048576\n" +
		"10: 1000 [10: 1000] @ 0x1 0x2 0x3\n" +
		"20: 3000 [20: 3000] @ 0x1 0x2 0x4\n" +
		"#\t0x3\tsome.func+0x1\t\t/src/f.go:1\n")
	samples := parseProfileText(familyHeap, text)
	if len(samples) != 2 {
		t.Fatalf("want 2 samples, got %d", len(samples))
	}
	if len(samples[0].values) != 4 || samples[0].values[3] != 1000 {
		t.Fatalf("sample[0] values = %v (inuse 应=1000)", samples[0].values)
	}
	if len(samples[1].pcs) != 3 || samples[1].pcs[0] != 0x1 || samples[1].pcs[2] != 0x4 {
		t.Fatalf("sample[1] pcs = %v", samples[1].pcs)
	}
}

func TestParseProfileText_GoroutineZeroPC(t *testing.T) {
	text := []byte("goroutine profile: total 4\n" +
		"1 @ 0x0 0xa 0x0\n" +
		"3 @ 0xa 0xb\n")
	samples := parseProfileText(familyCount, text)
	if len(samples) != 2 {
		t.Fatalf("want 2 samples, got %d", len(samples))
	}
	if len(samples[0].pcs) != 1 || samples[0].pcs[0] != 0xa {
		t.Fatalf("0x0 pc 应被剔除, got %v", samples[0].pcs)
	}
	if samples[1].values[0] != 3 {
		t.Fatalf("sample[1] value = %d", samples[1].values[0])
	}
}

func TestParseProfileText_BlockFamily(t *testing.T) {
	text := []byte("--- contention:\ncycles/second=3563219430\n5 500 @ 0x1 0x2\n")
	samples := parseProfileText(familyBlock, text)
	if len(samples) != 1 {
		t.Fatalf("want 1 sample, got %d", len(samples))
	}
	if samples[0].values[0] != 5 || samples[0].values[1] != 500 {
		t.Fatalf("block values = %v (want [5 500])", samples[0].values)
	}
}

func TestParseProfileText_EmptyMutex(t *testing.T) {
	text := []byte("--- mutex:\ncycles/second=3563219430\nsampling period=0\n")
	samples := parseProfileText(familyBlock, text)
	if len(samples) != 0 {
		t.Fatalf("want 0 samples, got %d", len(samples))
	}
}

func TestBuildProfile_FlameAndTop(t *testing.T) {
	withResolveFunc(t, map[uint64]funcFrame{
		0x1: {name: "github.com/x/server/internal/pkg/root.Root", file: "/src/r.go", line: 1},
		0x2: {name: "github.com/x/server/internal/pkg/mid.Mid", file: "/src/m.go", line: 2},
		0x3: {name: "internal/pkg/leaf.LeafA", file: "/src/a.go", line: 3},
		0x4: {name: "internal/pkg/leaf.LeafB", file: "/src/b.go", line: 4},
	})
	def, _ := DefFor("heap")
	// 采样行 @ 后 pc 为叶在前:0x3/0x4 为叶,0x1 为根
	text := []byte("heap profile: 40: 4000 [40: 4000] @ heap/1048576\n" +
		"10: 1000 [10: 1000] @ 0x3 0x2 0x1\n" +
		"20: 3000 [20: 3000] @ 0x4 0x2 0x1\n")

	resp := BuildProfile(def, text, 2)
	if resp.TotalValue != 4000 || resp.SampleCount != 2 {
		t.Fatalf("total=%d samples=%d (want 4000/2)", resp.TotalValue, resp.SampleCount)
	}
	if resp.Flame == nil || resp.Flame.Value != 4000 {
		t.Fatal("flame root 缺失或值错误")
	}
	if len(resp.Flame.Children) != 1 || resp.Flame.Children[0].Name != "pkg/root.Root" {
		t.Fatalf("root children = %+v (want 单个 pkg/root.Root)", resp.Flame.Children)
	}
	rootFn := resp.Flame.Children[0]
	if len(rootFn.Children) != 1 || rootFn.Children[0].Name != "pkg/mid.Mid" {
		t.Fatalf("rootFn children = %+v (want 单个 pkg/mid.Mid)", rootFn.Children)
	}
	mid := rootFn.Children[0]
	if len(mid.Children) != 2 {
		t.Fatalf("mid children len = %d (want 2)", len(mid.Children))
	}
	// 子节点按 value 降序
	if mid.Children[0].Name != "pkg/leaf.LeafB" || mid.Children[0].Value != 3000 {
		t.Fatalf("mid.Children[0] = %+v (want pkg/leaf.LeafB 3000)", mid.Children[0])
	}
	if mid.Children[1].Name != "pkg/leaf.LeafA" || mid.Children[1].Value != 1000 {
		t.Fatalf("mid.Children[1] = %+v (want pkg/leaf.LeafA 1000)", mid.Children[1])
	}

	// top:flat LeafB=3000, LeafA=1000;LeafB 在前
	if len(resp.Top) != 2 {
		t.Fatalf("top len = %d (want 2)", len(resp.Top))
	}
	if resp.Top[0].Fn != "internal/pkg/leaf.LeafB" || resp.Top[0].Flat != 3000 {
		t.Fatalf("top[0] = %+v", resp.Top[0])
	}
	if resp.Top[0].Cum != 3000 || resp.Top[1].Cum != 1000 {
		t.Fatalf("cum 错误: %+v / %+v", resp.Top[0], resp.Top[1])
	}
	if resp.Top[0].File != "b.go" {
		t.Fatalf("file 应去 /src/ 前缀, got %q", resp.Top[0].File)
	}
}

func TestBuildProfile_ZeroValueSamplesSkipped(t *testing.T) {
	withResolveFunc(t, map[uint64]funcFrame{
		0x1: {name: "pkg.A", file: "a.go", line: 1},
	})
	def, _ := DefFor("heap")
	text := []byte("heap profile: 0: 0 [0: 0] @ heap/1048576\n" +
		"0: 0 [0: 0] @ 0x1\n" +
		"5: 5 [5: 5] @ 0x1\n")
	resp := BuildProfile(def, text, 10)
	if resp.TotalValue != 5 {
		t.Fatalf("total = %d (want 5,0 值采样应跳过)", resp.TotalValue)
	}
	if resp.Flame == nil || resp.Flame.Children[0].Value != 5 {
		t.Fatal("flame 树值错误")
	}
}

func TestBuildProfile_GoroutineFlatSemantics(t *testing.T) {
	withResolveFunc(t, map[uint64]funcFrame{
		0xa: {name: "pkg.A", file: "a.go", line: 1},
		0xb: {name: "pkg.B", file: "b.go", line: 2},
	})
	def, _ := DefFor("goroutine")
	// "@ 0xa 0xb":0xa 叶(A),0xb 根(B) → 样本2 flat 归 A
	text := []byte("goroutine profile: total 4\n1 @ 0xa\n3 @ 0xa 0xb\n")
	resp := BuildProfile(def, text, 10)
	if resp.TotalValue != 4 {
		t.Fatalf("total = %d (want 4)", resp.TotalValue)
	}
	byFn := map[string]int64{}
	for _, r := range resp.Top {
		byFn[r.Fn] = r.Flat
	}
	// A 是两条采样共同的叶:flat 1+3;两条栈均无 B 为叶,flat 0
	if byFn["pkg.A"] != 4 || byFn["pkg.B"] != 0 {
		t.Fatalf("flat 归属错误: A=%d B=%d (want 4/0)", byFn["pkg.A"], byFn["pkg.B"])
	}
}

func TestBuildProfile_ChildrenPruned(t *testing.T) {
	frames := map[uint64]funcFrame{0x5: {name: "pkg/leaf.Common", file: "c.go", line: 1}}
	for i := 0; i < 250; i++ {
		frames[uint64(0x100+i)] = funcFrame{name: "pkg/top.Fn" + string(rune('a'+i%26)) + string(rune('0'+i/26%10)) + string(rune('0'+i/100)), file: "t.go", line: i}
	}
	withResolveFunc(t, frames)
	def, _ := DefFor("heap")
	// 250 个互不相同的根帧:每个采样 "@ 0x5 0x<top>"(叶 Common 在前,根 FnX 在后)
	text := "heap profile: 250: 250 [250: 250] @ heap/1048576\n"
	for i := 0; i < 250; i++ {
		text += "1: 1 [1: 1] @ 0x5 0x" + hexOf(0x100+i) + "\n"
	}
	resp := BuildProfile(def, []byte(text), 5)
	if !resp.Truncated {
		t.Fatal("超过 maxFlameChildren 应置 truncated")
	}
	if len(resp.Flame.Children) != maxFlameChildren+1 {
		t.Fatalf("root children = %d (want %d 含 其它…)", len(resp.Flame.Children), maxFlameChildren+1)
	}
	last := resp.Flame.Children[len(resp.Flame.Children)-1]
	if last.Name != "其它…" || last.Value != int64(250-maxFlameChildren) {
		t.Fatalf("合并节点错误: %+v", last)
	}
}

func hexOf(n int) string {
	const digits = "0123456789abcdef"
	out := ""
	for v := n; v > 0; v >>= 4 {
		out = string(digits[v&0xf]) + out
	}
	return out
}

func TestShortenFn(t *testing.T) {
	cases := []struct{ in, want string }{
		{"github.com/tangwy-t/webmanager-server/internal/pkg/ws.(*Client).WritePump", "pkg/ws.(*Client).WritePump"},
		{"strings.(*genericReplacer).Replace", "strings.(*genericReplacer).Replace"},
		{"github.com/go-sql-driver/mysql.(*mysqlConn).startWatcher.func1", "go-sql-driver/mysql.(*mysqlConn).startWatcher.func1"},
		{"runtime.mallocgc", "runtime.mallocgc"},
	}
	for _, c := range cases {
		if got := shortenFn(c.in); got != c.want {
			t.Errorf("shortenFn(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPprofStatusEntries(t *testing.T) {
	orig := pprofProfileCount
	pprofProfileCount = func(name string) (int, bool) {
		if name == "heap" {
			return 36, true
		}
		return 0, true
	}
	t.Cleanup(func() { pprofProfileCount = orig })

	entries := StatusEntries()
	if len(entries) != 8 {
		t.Fatalf("entries len = %d (want 8)", len(entries))
	}
	byName := map[string]int{}
	for _, e := range entries {
		byName[e.Name] = e.Count
	}
	if byName["heap"] != 36 {
		t.Fatalf("heap count = %d (want 36)", byName["heap"])
	}
	// capture 类(profile/trace)不查 Lookup,count 恒 0
	for _, name := range []string{"profile", "trace"} {
		found := false
		for _, e := range entries {
			if e.Name != name {
				continue
			}
			found = true
			if e.Category != "capture" || e.Count != 0 {
				t.Fatalf("%s entry = %+v", name, e)
			}
		}
		if !found {
			t.Fatalf("缺少 %s 条目", name)
		}
	}
}
