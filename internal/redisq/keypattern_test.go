package redisq

import "testing"

func TestValidatePattern_RejectMatchAll(t *testing.T) {
	bad := []string{"", "*", "*:*", "?*", "*foo", "?", "a*"}
	for _, p := range bad {
		if err := ValidatePattern(p); err == nil {
			t.Errorf("pattern %q should be rejected", p)
		}
	}
}

func TestValidatePattern_RejectShortPrefix(t *testing.T) {
	// 前缀不足 2 个确定字符
	if err := ValidatePattern("a:*"); err == nil {
		t.Error("prefix shorter than 2 chars should be rejected")
	}
}

func TestValidatePattern_AcceptGoodPrefixes(t *testing.T) {
	good := []string{"user:*", "order:1001:*", "user:1001:profile", "ab*"}
	for _, p := range good {
		if err := ValidatePattern(p); err != nil {
			t.Errorf("pattern %q should be accepted, got %v", p, err)
		}
	}
}

// 分隔符不计入有效前缀：ab:* 有 2 个有效字符，可通过
func TestValidatePattern_SeparatorNotCounted(t *testing.T) {
	if err := ValidatePattern("ab:*"); err != nil {
		t.Errorf("ab:* has 2 effective chars and should pass, got %v", err)
	}
	// a:b:* 只有 2 个有效字符，也通过
	if err := ValidatePattern("a:b:*"); err != nil {
		t.Errorf("a:b:* has 2 effective chars and should pass, got %v", err)
	}
	// 单个有效字符的必须拒绝
	for _, p := range []string{"a:*", "a::*", ":a*", ":a:*b"} {
		if err := ValidatePattern(p); err == nil {
			t.Errorf("pattern %q has <2 effective chars and should be rejected", p)
		}
	}
	// a:b?:* 的第一个通配符是 ?，前缀 a:b 有 2 个有效字符，应通过
	if err := ValidatePattern("a:b?:*"); err != nil {
		t.Errorf("a:b?:* has 2 effective chars before first wildcard, got %v", err)
	}
}

func TestIsExactKey(t *testing.T) {
	if !IsExactKey("user:1001:profile") {
		t.Error("no wildcard means exact key")
	}
	if IsExactKey("user:*") {
		t.Error("wildcard means not exact")
	}
	if IsExactKey("user:?001") {
		t.Error("question mark is a wildcard")
	}
	if IsExactKey("user:[12]001") {
		t.Error("bracket is a wildcard")
	}
}

func TestValidatePattern_ErrorIsWrapped(t *testing.T) {
	err := ValidatePattern("*")
	if err == nil {
		t.Fatal("expected error")
	}
	if !isErrPatternTooBroad(err) {
		t.Errorf("error should wrap ErrPatternTooBroad, got %v", err)
	}
}

func isErrPatternTooBroad(err error) bool {
	for err != nil {
		if err == ErrPatternTooBroad {
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
