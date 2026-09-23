package agentproto

import "testing"

func TestParseSemverValid(t *testing.T) {
	cases := []struct {
		in                  string
		major, minor, patch int
		pre                 string
	}{
		{"0.1.0", 0, 1, 0, ""},
		{"1.2.3", 1, 2, 3, ""},
		{"10.20.30", 10, 20, 30, ""},
		{"1.2.3-rc.1", 1, 2, 3, "rc.1"},
		{"1.2.3+build.5", 1, 2, 3, ""},
		{"1.2.3-rc.1+build.5", 1, 2, 3, "rc.1"},
		{"2.0.0-alpha", 2, 0, 0, "alpha"},
	}
	for _, c := range cases {
		v, err := ParseSemver(c.in)
		if err != nil {
			t.Fatalf("%q 应可解析，实际 %v", c.in, err)
		}
		if v.Major != c.major || v.Minor != c.minor || v.Patch != c.patch || v.Pre != c.pre {
			t.Fatalf("%q 解析为 %+v，期望 %d.%d.%d-%q", c.in, v, c.major, c.minor, c.patch, c.pre)
		}
	}
}

// 「不是 semver」必须是可拒绝的显式结果，而不是被宽松解析成某个版本号。
//
// 这些输入含两类：拼写错误（缺段、前导零、带空白）与环境里真实存在的非版本自述
// （`dev`）。宽松解析会让它们变成合法目标版本，从而出现「怎么点升级都升不上去」
// 而日志一切正常的现象。
func TestParseSemverRejects(t *testing.T) {
	for _, in := range []string{
		"", "1", "1.2", "1.2.3.4", "01.2.3", "1.02.3", "1.2.03",
		"1.2.3-", "1.2.3 ", " 1.2.3", "1.2.3-rc..1", "v1.2.3", "dev",
		"1.2.x", "1.2.3+", "1.2.3-rc.1 ", "1.2.3-rc.1/2", "1.2.3+bad/1",
	} {
		if _, err := ParseSemver(in); err == nil {
			t.Fatalf("%q 应被拒绝，实际解析成功", in)
		}
		if IsSemver(in) {
			t.Fatalf("IsSemver(%q) 应为 false", in)
		}
	}
}

func TestCompareSemver(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.1.0", "0.1.0", 0},
		{"0.1.0", "0.1.1", -1},
		{"0.2.0", "0.1.9", 1},
		{"1.0.0", "0.9.9", 1},
		// 预发布：有标识的一律小于同号正式版 —— 这条决定了「先发 rc 再发正式版」
		// 能否走通（目标写 0.2.0 时，设备上的 0.2.0-rc.1 必须被判为不同）。
		{"0.2.0-rc.1", "0.2.0", -1},
		{"0.2.0", "0.2.0-rc.1", 1},
		// 语义化版本规范给出的标准升序链
		{"1.0.0-alpha", "1.0.0-alpha.1", -1},
		{"1.0.0-alpha.1", "1.0.0-alpha.beta", -1},
		{"1.0.0-alpha.beta", "1.0.0-beta", -1},
		{"1.0.0-beta", "1.0.0-beta.2", -1},
		{"1.0.0-beta.2", "1.0.0-beta.11", -1}, // 数字标识按数值比，不是字典序
		{"1.0.0-beta.11", "1.0.0-rc.1", -1},
		{"1.0.0-rc.1", "1.0.0", -1},
		{"1.0.0-2", "1.0.0-10", -1},
	}
	for _, c := range cases {
		a, err1 := ParseSemver(c.a)
		b, err2 := ParseSemver(c.b)
		if err1 != nil || err2 != nil {
			t.Fatalf("%q / %q 解析失败: %v %v", c.a, c.b, err1, err2)
		}
		if got := CompareSemver(a, b); got != c.want {
			t.Fatalf("Compare(%q,%q) = %d，期望 %d", c.a, c.b, got, c.want)
		}
		// 反对称性
		if got := CompareSemver(b, a); got != -c.want {
			t.Fatalf("Compare(%q,%q) = %d，期望 %d（反对称性）", c.b, c.a, got, -c.want)
		}
	}
}

func TestSemverStringRoundTrip(t *testing.T) {
	for _, in := range []string{"0.1.0", "1.2.3-rc.1", "10.20.30"} {
		v, err := ParseSemver(in)
		if err != nil {
			t.Fatal(err)
		}
		if v.String() != in {
			t.Fatalf("String() = %q，期望 %q", v.String(), in)
		}
	}
	// build 元数据不参与往返（被丢弃是刻意的：它不参与比较）
	v, err := ParseSemver("1.2.3+build.5")
	if err != nil {
		t.Fatal(err)
	}
	if v.String() != "1.2.3" {
		t.Fatalf("build 元数据应被丢弃，实际 %q", v.String())
	}
}
