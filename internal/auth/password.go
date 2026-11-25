package auth

import (
	"crypto/rand"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// HashPassword 用 bcrypt 生成哈希，成本因子用库默认值。
func HashPassword(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// VerifyPassword 校验明文与哈希是否匹配。
func VerifyPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

// passwordAlphabet 是随机密码的字符表。
//
// 去掉了 0/O、1/l/I 这两组易混字符：初始密码要靠管理员人工转交给用户，
// 「看着像 0 其实是 O」这种误差会让用户坚信密码是错的，
// 而管理员也无从自证（明文只出现一次）。
// 62 - 5 = 57 个字符，12 位约 70 bit 熵，配合 bcrypt 足够。
const passwordAlphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// GeneratePassword 生成 n 位随机密码，用于新建用户与重置密码。
//
// 为什么用 crypto/rand 而不是 math/rand：初始密码是账号的唯一凭据，
// math/rand 的默认源可预测，攻击者拿到一组历史密码就能反推种子，
// 进而算出「下一个新同事的初始密码」。这类问题不会在测试里暴露。
//
// 明文只应通过创建/重置接口的响应返回一次，不落库、不进审计。
func GeneratePassword(n int) (string, error) {
	if n <= 0 {
		return "", fmt.Errorf("password length must be positive, got %d", n)
	}
	out := make([]byte, n)

	// 取模偏差：256 % 57 != 0，直接用 % 会让前 28 个字符概率略高。
	// 用拒绝采样把它消掉，代价只是极少数情况下多循环一次。
	limit := 256 - (256 % len(passwordAlphabet))

	for i := 0; i < n; {
		var b [1]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", fmt.Errorf("read random bytes: %w", err)
		}
		if int(b[0]) >= limit {
			continue
		}
		out[i] = passwordAlphabet[int(b[0])%len(passwordAlphabet)]
		i++
	}
	return string(out), nil
}
