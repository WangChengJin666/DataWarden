package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
)

// Cipher 用 AES-GCM 加解密数据源密码。
//
// 为什么需要它：密码要直接存进 datasource 表，明文落库意味着任何能读该表的人
// （包括 DBA、pg_dump 备份文件、误发的导出）都能直接拿到生产库密码。
// 用主密钥加密后，即使数据库被完整拖走，没有主密钥也解不开。
//
// 主密钥来自环境变量 DATAWARDEN_MASTER_KEY，必须与数据库分开保管。
// 这也是本方案的边界：主密钥和数据库在同一个 .env.local 里，本机被完全攻破时
// 两者会一起泄露。要防这一层，得把主密钥移到 KMS 或独立密钥服务器。
type Cipher struct {
	aead cipher.AEAD
}

// NewCipher 用给定主密钥构造 Cipher。
// 密钥长度必须是 AES 支持的 16 / 24 / 32 字节（AES-128 / 192 / 256）。
func NewCipher(masterKey string) (*Cipher, error) {
	key := []byte(masterKey)
	switch len(key) {
	case 16, 24, 32:
	default:
		return nil, fmt.Errorf("master key must be 16, 24 or 32 bytes, got %d", len(key))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("new aes cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("new gcm: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// Encrypt 加密明文，返回 base64 编码的密文。
// 每次调用都用新的随机 nonce，所以同一明文两次加密结果不同，
// 避免攻击者通过比对密文判断两个数据源是否用了同一个密码。
//
// 空明文返回空密文，表示「未设置密码」。
func (c *Cipher) Encrypt(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}

	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	// nonce 前置于密文，解密时按固定长度切分
	sealed := c.aead.Seal(nonce, nonce, []byte(plain), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt 解密 Encrypt 产出的 base64 密文。
// 空密文返回空串；密文被篡改或用错主密钥时返回错误。
func (c *Cipher) Decrypt(encoded string) (string, error) {
	if encoded == "" {
		return "", nil
	}

	sealed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decode ciphertext: %w", err)
	}

	ns := c.aead.NonceSize()
	if len(sealed) < ns {
		return "", fmt.Errorf("ciphertext too short")
	}

	plain, err := c.aead.Open(nil, sealed[:ns], sealed[ns:], nil)
	if err != nil {
		// 这里不返回底层错误细节，避免泄露加密实现信息
		return "", fmt.Errorf("decrypt failed: wrong master key or corrupted data")
	}
	return string(plain), nil
}
