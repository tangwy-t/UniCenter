package util

import "testing"

func TestSanitizeFilename(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"report.pdf", "report.pdf"},
		{"evil\r\nname.pdf", "evilname.pdf"},  // CR/LF 剥离
		{`a"b.pdf`, "ab.pdf"},                 // 引号剥离
		{`..\..\etc\passwd`, "....etcpasswd"}, // 反斜杠剥离
		{"a/b/c.pdf", "abc.pdf"},              // 正斜杠剥离
		{`""`, "download"},                    // 清空后回退
		{"\r\n", "download"},                  // 仅 CR/LF → 回退
	}
	for _, c := range cases {
		if got := SanitizeFilename(c.in); got != c.want {
			t.Errorf("SanitizeFilename(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
