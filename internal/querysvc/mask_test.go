package querysvc

import "testing"

func TestMaskValue_Phone(t *testing.T) {
	r := DefaultRules()
	if got := r.Mask("phone", "13812345678"); got != "138****5678" {
		t.Errorf("phone mask = %q", got)
	}
}

func TestMaskValue_IDCard(t *testing.T) {
	r := DefaultRules()
	got := r.Mask("id_card", "310101199001011234")
	if got != "310101********1234" {
		t.Errorf("id card mask = %q", got)
	}
}

func TestMaskValue_Email(t *testing.T) {
	r := DefaultRules()
	if got := r.Mask("email", "alice@example.com"); got != "al***@example.com" {
		t.Errorf("email mask = %q", got)
	}
}

func TestMaskValue_BankCard(t *testing.T) {
	r := DefaultRules()
	got := r.Mask("bank_card_no", "6222021234567890123")
	if got != "6222***********0123" {
		t.Errorf("bank card mask = %q", got)
	}
}

func TestMaskValue_UnmatchedColumnUnchanged(t *testing.T) {
	r := DefaultRules()
	if got := r.Mask("order_no", "SO20261113001"); got != "SO20261113001" {
		t.Errorf("unmatched column should be unchanged, got %q", got)
	}
}

func TestMaskValue_ShortValueSafe(t *testing.T) {
	r := DefaultRules()
	if got := r.Mask("phone", "123"); got != "***" {
		t.Errorf("short value should be fully masked, got %q", got)
	}
}

func TestMaskValue_EmptyStaysEmpty(t *testing.T) {
	r := DefaultRules()
	if got := r.Mask("phone", ""); got != "" {
		t.Errorf("empty value should stay empty, got %q", got)
	}
}

func TestMaskResult(t *testing.T) {
	r := DefaultRules()
	res := &QueryResult{
		Columns: []string{"id", "phone", "email"},
		Rows: [][]interface{}{
			{int64(1), "13812345678", "bob@test.com"},
		},
	}
	r.MaskResult(res)
	if got := res.Rows[0][1]; got != "138****5678" {
		t.Errorf("masked phone = %v", got)
	}
	if got := res.Rows[0][2]; got != "bo***@test.com" {
		t.Errorf("masked email = %v", got)
	}
	// 非字符串与未匹配列必须保持原样
	if got := res.Rows[0][0]; got != int64(1) {
		t.Errorf("id must stay unchanged, got %v", got)
	}
}

func TestMaskResult_HandlesShortRow(t *testing.T) {
	r := DefaultRules()
	res := &QueryResult{
		Columns: []string{"a", "phone", "c"},
		Rows:    [][]interface{}{{"x"}}, // 行比列短，不能 panic
	}
	r.MaskResult(res)
	if len(res.Rows[0]) != 1 {
		t.Errorf("row length should not change")
	}
}

func TestMaskResult_NilResult(t *testing.T) {
	r := DefaultRules()
	r.MaskResult(nil) // 不应 panic
}
