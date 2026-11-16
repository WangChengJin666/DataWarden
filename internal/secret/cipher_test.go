package secret

import (
	"strings"
	"testing"
)

const testKey = "0123456789abcdef0123456789abcdef" // 32 字节，AES-256

func TestCipher_RoundTrip(t *testing.T) {
	c, err := NewCipher(testKey)
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}

	cases := []string{
		"simple-password",
		"含中文的密码",
		"with special !@#$%^&*()_+\"':;,.<>",
		strings.Repeat("x", 500), // 长密码
		"",                       // 空密码
	}
	for _, plain := range cases {
		enc, err := c.Encrypt(plain)
		if err != nil {
			t.Fatalf("encrypt %q: %v", plain, err)
		}
		if plain != "" && strings.Contains(enc, plain) {
			t.Errorf("ciphertext must not contain plaintext %q", plain)
		}
		got, err := c.Decrypt(enc)
		if err != nil {
			t.Fatalf("decrypt: %v", err)
		}
		if got != plain {
			t.Errorf("round trip = %q, want %q", got, plain)
		}
	}
}

// 同一明文两次加密必须得到不同密文（随机 nonce），否则密文可比对。
func TestCipher_NonceIsRandom(t *testing.T) {
	c, _ := NewCipher(testKey)
	a, _ := c.Encrypt("same")
	b, _ := c.Encrypt("same")
	if a == b {
		t.Fatal("two encryptions of the same plaintext must differ")
	}
}

// 密文被篡改后必须解密失败，而不是返回垃圾数据。
func TestCipher_RejectsTamperedCiphertext(t *testing.T) {
	c, _ := NewCipher(testKey)
	enc, _ := c.Encrypt("secret-value")

	// 改动最后一个字符
	tampered := enc[:len(enc)-2] + "AA"
	if _, err := c.Decrypt(tampered); err == nil {
		t.Fatal("tampered ciphertext must fail to decrypt")
	}
}

// 用不同主密钥加密的密文不能被解开。
func TestCipher_RejectsWrongKey(t *testing.T) {
	c1, _ := NewCipher(testKey)
	c2, _ := NewCipher("ffffffffffffffffffffffffffffffff")

	enc, _ := c1.Encrypt("secret-value")
	if _, err := c2.Decrypt(enc); err == nil {
		t.Fatal("decrypt with wrong key must fail")
	}
}

func TestNewCipher_KeyLength(t *testing.T) {
	// AES 只接受 16 / 24 / 32 字节，其余长度必须拒绝
	for _, bad := range []string{"", "short", strings.Repeat("a", 15), strings.Repeat("a", 31), strings.Repeat("a", 48), strings.Repeat("a", 64)} {
		if _, err := NewCipher(bad); err == nil {
			t.Errorf("key of %d bytes should be rejected", len(bad))
		}
	}
	for _, ok := range []string{strings.Repeat("a", 16), strings.Repeat("a", 24), strings.Repeat("a", 32)} {
		if _, err := NewCipher(ok); err != nil {
			t.Errorf("key of %d bytes should be accepted, got %v", len(ok), err)
		}
	}
}

// 空密文（未设置密码）解密应返回空串，而不是报错，
// 这样没填密码的数据源仍然能走「无密码」连接。
func TestCipher_EmptyCiphertext(t *testing.T) {
	c, _ := NewCipher(testKey)
	got, err := c.Decrypt("")
	if err != nil {
		t.Fatalf("empty ciphertext: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
