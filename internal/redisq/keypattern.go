package redisq

import (
	"errors"
	"fmt"
	"strings"
)

// MinLiteralPrefix 是模糊查询允许的最小字面量前缀长度。
// 少于该长度的模式会让 SCAN 退化成接近全库扫描，生产环境必须拒绝。
const MinLiteralPrefix = 2

// ErrPatternTooBroad 表示 key 模式过于宽泛，可能拖垮 Redis。
var ErrPatternTooBroad = errors.New("key pattern too broad")

// wildcardChars 是 Redis glob 的通配符集合。
const wildcardChars = "*?["

// separatorChars 是 key 命名中的分隔符。它们不构成业务含义的前缀，
// 因此在计算"有效前缀长度"时不计入。
const separatorChars = ":._-/"

// IsExactKey 判断是否是不含任何通配符的精确 key。
func IsExactKey(pattern string) bool {
	return !strings.ContainsAny(pattern, wildcardChars)
}

// effectivePrefixLen 计算前缀中真正有约束力的字符数（排除分隔符）。
// 例如 "a:*" 的有效前缀只有 1 个字符，扫描范围仍然极宽，必须拒绝。
func effectivePrefixLen(prefix string) int {
	n := 0
	for _, r := range prefix {
		if !strings.ContainsRune(separatorChars, r) {
			n++
		}
	}
	return n
}

// ValidatePattern 校验查询模式。精确 key 直接通过，模糊模式要求足够长的有效前缀。
func ValidatePattern(pattern string) error {
	if pattern == "" {
		return fmt.Errorf("%w: pattern is empty", ErrPatternTooBroad)
	}
	if IsExactKey(pattern) {
		return nil
	}

	idx := strings.IndexAny(pattern, wildcardChars)
	prefix := pattern[:idx]
	effective := effectivePrefixLen(prefix)

	if effective < MinLiteralPrefix {
		return fmt.Errorf(
			"%w: prefix %q has %d effective chars, need at least %d",
			ErrPatternTooBroad, prefix, effective, MinLiteralPrefix,
		)
	}
	return nil
}
