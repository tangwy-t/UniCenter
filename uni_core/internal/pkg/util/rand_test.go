package util

import "testing"

func TestRandomHex(t *testing.T) {
	s, err := RandomHex(16)
	if err != nil {
		t.Fatalf("RandomHex(16) error: %v", err)
	}
	if len(s) != 32 {
		t.Fatalf("RandomHex(16) 长度 = %d, want 32 (hex of 16 bytes)", len(s))
	}
	// 两次生成不应相同（概率性，但 32 字节 hex 碰撞可忽略）。
	s2, _ := RandomHex(16)
	if s == s2 {
		t.Fatal("两次 RandomHex(16) 不应相同")
	}
}
