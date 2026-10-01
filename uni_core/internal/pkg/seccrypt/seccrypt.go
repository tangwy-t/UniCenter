// Package seccrypt 是敏感字段的 AES-256-GCM 加解密（4c 仓库凭据密码）。
//
// 选型理由（本仓库没有加密先例，这是第一处，因此写清边界）：
//   - **AES-256-GCM**：认证加密 —— 密文被篡改时解密必然失败（tag 校验），
//     不需要在 GCM 之外再叠一层完整性哈希；且 Go 标准库实现就已具备，
//     不引入第三方密码学依赖（依赖越少，审计面越小）。
//   - **每条记录独立随机 12 字节 nonce**：同一明文两次加密产物不同
//     （测试钉住「同一密文不重复」）；nonce 必须唯一由调用方保证 —— 本包
//     每次 Encrypt 都取新的随机 nonce，同一密钥下 96 位随机 nonce 的碰撞
//     概率可忽略，符合 GCM 的 nonce 纪律（碰撞 = 该密钥下彻底失守）。
//
// 密文形态：base64(nonce ‖ ciphertext)。盒前缀 nonce 与 GCM 的 tag
// 粘连在尾部，一个字段就是一个自包含的密文盒，无需旁挂 nonce 列。
//
// 密钥管理（主密钥从配置读取，**绝不明文落库**）：
//   - 主密钥存 sys_config 键 `sys.docker.registry.secret`（与 sys.jwt.secret
//     同一通道先例）；键名含 secret，配置 API 的敏感键掩码自动生效；
//     长度至少 32 字节（hex 形式 64 字符），不足即拒绝加解密。
//   - 密钥不在代码、不在配置文件：由部署侧经配置管理 API 写入随机 hex
//     （可用 util.RandomHex(48) 生成）。**没有不安全默认值** —— 密钥缺失时
//     凭据写路径与拉取注入都显式报错（fail-closed），而不是回退到弱密钥。
//   - 轮换流程：rot 旧密钥后旧密文不可解 —— 轮换步骤是「暂停受理拉取 →
//     记录旧密钥 → 写新密钥 → 凭据逐条重输密码」，中途旧密文解密失败
//     按「凭据不可用」结论句处理，不判死功能。
package seccrypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

// KeyMinBytes 是主密钥的最短长度（32 字节 = AES-256）。
const KeyMinBytes = 32

// MaxPlaintextBytes 是单条明文的上限（与协议的 maxDockerRegistrySecretBytes 同档：
// 4096）。加解密两侧都校验 —— 越界的明文是没有存储价值的数据，越界的密文
// 盒是畸形输入，都不该进入内存展开。
const MaxPlaintextBytes = 4096

// maxBoxBytes 是密文盒的字节上限：4KB 明文 + GCM 开销的保守上界。
var maxBoxBytes = 32 + base64.StdEncoding.EncodedLen(MaxPlaintextBytes+16)

// MaxBoxBytes 是 maxBoxBytes 的导出读口（解密侧的尺寸闸，测试用它记上限口径）。
func MaxBoxBytes() int { return maxBoxBytes }

var (
	// ErrInvalidKey 表示主密钥长度不足（安全下限 = KeyMinBytes）。
	ErrInvalidKey = errors.New("seccrypt: key must be at least 32 bytes")
	// ErrTooLarge 表示明文/密文盒超过尺寸上限。
	ErrTooLarge = errors.New("seccrypt: input exceeds size limit")
	// ErrDecrypt 表示密文无法解出（tag 校验失败 / 非本包产物 / 编码损坏）。
	ErrDecrypt = errors.New("seccrypt: cannot decrypt box")
)

// nonceSize 是 GCM 标准 nonce 长度（12 字节）。
const nonceSize = 12

// Encrypt 用 key 加密 plaintext，返回自包含的 base64 密文盒。
// 每次调用取新的随机 nonce —— 同一明文同一密钥的两次加密产物必然不同。
func Encrypt(key []byte, plaintext []byte) (string, error) {
	if len(key) < KeyMinBytes {
		return "", ErrInvalidKey
	}
	if len(plaintext) == 0 || len(plaintext) > MaxPlaintextBytes {
		return "", ErrTooLarge
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("seccrypt: new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("seccrypt: new gcm: %w", err)
	}
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("seccrypt: read nonce: %w", err)
	}
	sealed := gcm.Seal(nil, nonce, plaintext, nil)
	box := append(nonce, sealed...)
	return base64.StdEncoding.EncodeToString(box), nil
}

// Decrypt 用 key 解开 box，返回明文。密文盒非本包产物 / tag 校验失败 /
// 编码损坏一律返回 ErrDecrypt（不区分具体原因：不向调用方泄露密文情报）。
func Decrypt(key []byte, box string) ([]byte, error) {
	if len(key) < KeyMinBytes {
		return nil, ErrInvalidKey
	}
	if len(box) > maxBoxBytes {
		return nil, ErrTooLarge
	}
	raw, err := base64.StdEncoding.DecodeString(box)
	if err != nil || len(raw) <= nonceSize+16 {
		return nil, ErrDecrypt
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("seccrypt: new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("seccrypt: new gcm: %w", err)
	}
	plain, err := gcm.Open(nil, raw[:nonceSize], raw[nonceSize:], nil)
	if err != nil {
		return nil, ErrDecrypt
	}
	return plain, nil
}