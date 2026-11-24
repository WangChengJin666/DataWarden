package redisq

import "testing"

// 这三种形态是「TYPE 都是 string，但内容完全不是文本」的全部已知情形。
// 之前一律走 GET + String()，bitmap / HLL 到前端就变成 \ufffd 乱码。
func TestDetectStringEncoding(t *testing.T) {
	cases := []struct {
		name string
		raw  []byte
		want Encoding
	}{
		{"纯文本", []byte("hello redis"), EncodingText},
		{"中文文本", []byte("数据审计工单平台"), EncodingText},
		{"带换行制表的文本", []byte("a\tb\nc\r\n"), EncodingText},
		{"JSON", []byte(`{"a":1}`), EncodingText},
		{"空内容", nil, EncodingText},
		// HYLL 魔数：HyperLogLog 的确定判据
		{"HyperLogLog", []byte("HYLL\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x80G\xa9\x84B"), EncodingHyperLogLog},
		// SETBIT 0/3 后字节是 1001 0000 = 0x90（位 0、3 置位），
		// 再 SETBIT 8 得第二个字节 1000 0000 = 0x80
		{"位图", []byte{0x90, 0x80}, EncodingBinary},
		// 非法 UTF-8：不能当文本渲染
		{"非法utf8", []byte{0xff, 0xfe, 0x00}, EncodingBinary},
		// NUL 字节：utf8.Valid 认为是合法，但展示出来是截断的，必须算二进制
		{"含NUL", []byte("abc\x00def"), EncodingBinary},
		{"含控制字符", []byte("abc\x01def"), EncodingBinary},
	}
	for _, tc := range cases {
		if got := detectStringEncoding(tc.raw); got != tc.want {
			t.Errorf("%s: detectStringEncoding(%q) = %q, want %q", tc.name, tc.raw, got, tc.want)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	hex, truncated := FormatBytes([]byte{0x00, 0x0f, 0x90, 0xff}, 256)
	if hex != "00 0f 90 ff" {
		t.Errorf("hex = %q, want %q", hex, "00 0f 90 ff")
	}
	if truncated {
		t.Error("should not be truncated")
	}

	hex, truncated = FormatBytes([]byte{0x01, 0x02, 0x03}, 2)
	if hex != "01 02" {
		t.Errorf("hex = %q, want %q", hex, "01 02")
	}
	if !truncated {
		t.Error("expected truncated=true")
	}
}

// SETBIT 的下标换算容易写反：字节内是高位对应小下标。
func TestBitmapSetBits(t *testing.T) {
	// SETBIT key 0 1 / 3 1 / 8 1 -> 1001 0000, 1000 0000 = 0x90, 0x80
	count, positions := bitmapSetBits([]byte{0x90, 0x80}, 64)
	if count != 3 {
		t.Errorf("count = %d, want 3", count)
	}
	if joinInts(positions) != "0,3,8" {
		t.Errorf("positions = %q, want %q", joinInts(positions), "0,3,8")
	}

	// 全零不应产生任何置位
	count, positions = bitmapSetBits([]byte{0x00, 0x00}, 64)
	if count != 0 || len(positions) != 0 {
		t.Errorf("zeros should have no bits set, got count=%d positions=%v", count, positions)
	}

	// 超过 limit 时只记录 limit 个，但计数仍要完整
	count, positions = bitmapSetBits([]byte{0xff, 0xff}, 4)
	if count != 16 {
		t.Errorf("count = %d, want 16", count)
	}
	if len(positions) != 4 {
		t.Errorf("len(positions) = %d, want 4 (limited)", len(positions))
	}
}
