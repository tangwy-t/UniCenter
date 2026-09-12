package util

import "testing"

func TestParseExtSet(t *testing.T) {
	got := ParseExtSet("jpg, .PNG,  pdf , Gif,,")
	want := map[string]bool{".jpg": true, ".png": true, ".pdf": true, ".gif": true}
	if len(got) != len(want) {
		t.Fatalf("ParseExtSet 集合大小 = %d, want %d (got=%v)", len(got), len(want), got)
	}
	for k := range want {
		if !got[k] {
			t.Errorf("ParseExtSet 缺少 %q", k)
		}
	}
	if len(ParseExtSet("")) != 0 {
		t.Errorf("空配置应返回空集合")
	}
}
