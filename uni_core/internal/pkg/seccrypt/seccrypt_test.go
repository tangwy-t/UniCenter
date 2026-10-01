package seccrypt

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

// TestEncryptDecryptRoundTrip：合法密钥与明文往返无损。
func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte("k"), KeyMinBytes)
	plain := []byte("s3(p)@ss!wörld-镜像密码")
	box, err := Encrypt(key, plain)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if strings.Contains(box, string(plain)) {
		t.Fatal("密文盒不得包含明文字面量")
	}
	got, err := Decrypt(key, box)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("roundtrip 不相等: got %q want %q", got, plain)
	}
}

// TestEncryptNonceUniqueness：同一明文同一密钥的两次加密产物必须不同
// （GCM 的 nonce 纪律 —— 同一 nonce 重用在认证加密里等于密钥失守）。
func TestEncryptNonceUniqueness(t *testing.T) {
	key := bytes.Repeat([]byte("n"), KeyMinBytes)
	var boxes []string
	for i := 0; i < 16; i++ {
		box, err := Encrypt(key, []byte("same password"))
		if err != nil {
			t.Fatalf("Encrypt: %v", err)
		}
		for _, b := range boxes {
			if b == box {
				t.Fatalf("第 %d 次加密产物与既有密文重复 —— nonce 没有真正随机", i)
			}
		}
		boxes = append(boxes, box)
	}
}

// TestKeyLengthEnforced：短密钥必须被拒（fail-closed：宁可不加密，
// 也不拿着弱密钥把明文写进库）。
func TestKeyLengthEnforced(t *testing.T) {
	short := bytes.Repeat([]byte("s"), KeyMinBytes-1)
	if _, err := Encrypt(short, []byte("x")); err != ErrInvalidKey {
		t.Fatalf("短密钥加密应报 ErrInvalidKey，实际 %v", err)
	}
	if _, err := Decrypt(short, "AAAA"); err != ErrInvalidKey {
		t.Fatalf("短密钥解密应报 ErrInvalidKey，实际 %v", err)
	}
}

// TestTamperDetected：密文盒任一位翻转/截断/错密钥都必须解密失败
// （GCM 的 tag 是整条链路的完整性保证 —— 这也是「删除即失效」的前提：
// 库里的密文坏了，绝不可能被静默退回一个旧明文）。
func TestTamperDetected(t *testing.T) {
	key := bytes.Repeat([]byte("t"), KeyMinBytes)
	box, err := Encrypt(key, []byte("top-secret"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(box)
	if err != nil {
		t.Fatal(err)
	}
	flip := append([]byte(nil), raw...)
	flip[len(flip)/2] ^= 0x01
	if _, err := Decrypt(key, base64.StdEncoding.EncodeToString(flip)); err != ErrDecrypt {
		t.Fatalf("翻转密文必须解密失败，实际 %v", err)
	}
	other := bytes.Repeat([]byte("o"), KeyMinBytes)
	if _, err := Decrypt(other, box); err != ErrDecrypt {
		t.Fatalf("错密钥必须解密失败，实际 %v", err)
	}
	if _, err := Decrypt(key, box[:len(box)-3]); err != ErrDecrypt {
		t.Fatalf("截断密文盒必须解密失败，实际 %v", err)
	}
	if _, err := Decrypt(key, "not-base64!"); err != ErrDecrypt {
		t.Fatalf("非本包产物必须解密失败，实际 %v", err)
	}
}

// TestSizeLimits：空明文与超长明文拒绝；超长密文盒拒绝。
func TestSizeLimits(t *testing.T) {
	key := bytes.Repeat([]byte("z"), KeyMinBytes)
	if _, err := Encrypt(key, nil); err != ErrTooLarge {
		t.Fatalf("空明文应报 ErrTooLarge，实际 %v", err)
	}
	if _, err := Encrypt(key, bytes.Repeat([]byte("x"), MaxPlaintextBytes+1)); err != ErrTooLarge {
		t.Fatalf("超长明文应报 ErrTooLarge，实际 %v", err)
	}
	if _, err := Decrypt(key, strings.Repeat("A", MaxBoxBytes()+1)); err != ErrTooLarge {
		t.Fatalf("超长密文盒应报 ErrTooLarge，实际 %v", err)
	}
}