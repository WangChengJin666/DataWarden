package redisq

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// Encoding 是 string 类型 key 内容的实际编码形态。
//
// 背景：Redis 的 TYPE 只有 string 一种，但 bitmap、HyperLogLog、Geo 索引
// 内部都存成 string。直接 GET 出来是一段二进制，前端当文本渲染只会得到乱码。
// 这里按内容特征把「看着像什么」标出来，让前端能给出有意义的展示。
type Encoding string

const (
	// EncodingText 是可正常显示为文本的内容。
	EncodingText Encoding = "text"
	// EncodingBinary 是无法归类为文本、也不符合已知结构的二进制。
	EncodingBinary Encoding = "binary"
	// EncodingHyperLogLog 是 PFADD 产生的 HyperLogLog（固定以 "HYLL" 开头）。
	EncodingHyperLogLog Encoding = "hyperloglog"
)

// hllMagic 是 HyperLogLog 的固定文件头（Redis 源码 hyperloglog.c 中的 hdr）。
// 有它就能百分百确定是 HLL，不用猜。
const hllMagic = "HYLL"

// detectStringEncoding 判断 string 内容的编码形态。
//
// 顺序有讲究：
//  1. HLL 有魔数，最先判，判定是确定的；
//  2. 合法 UTF-8 且不含控制字符 -> text，绝大多数业务数据走这条；
//  3. 其余一律 binary。
//
// **不尝试识别 bitmap**：SETBIT 只写出实际置位的字节，短位图可能只有 1~2 字节，
// 与普通二进制无法可靠区分。宁可统一报 binary 并给十六进制 + 位图参考，
// 也不做会误判的启发式猜测。
func detectStringEncoding(raw []byte) Encoding {
	if len(raw) == 0 {
		return EncodingText
	}
	if strings.HasPrefix(string(raw), hllMagic) {
		return EncodingHyperLogLog
	}
	if utf8.Valid(raw) && !hasControlCharsExceptWhitespace(raw) {
		return EncodingText
	}
	// 到这里已确定不是可读文本，统一归为 binary，由前端按十六进制展示。
	return EncodingBinary
}

// hasControlCharsExceptWhitespace 判断是否含除常见空白外的控制字符。
// 判定为 text 的内容要能安全展示，\t \n \r 之外的 C0 控制字符一律算二进制。
func hasControlCharsExceptWhitespace(raw []byte) bool {
	for _, b := range raw {
		if b < 0x20 && b != '\t' && b != '\n' && b != '\r' {
			return true
		}
		if b == 0x7f {
			return true
		}
	}
	return false
}

// FormatBytes 把二进制内容转成可读的十六进制串，形如 "14 80"。
// 超过 maxBytes 的部分截断并用省略号标记，避免大位图撑爆响应。
func FormatBytes(raw []byte, maxBytes int) (hex string, truncated bool) {
	if maxBytes <= 0 {
		maxBytes = 256
	}
	n := len(raw)
	if n > maxBytes {
		n = maxBytes
		truncated = true
	}
	var sb strings.Builder
	sb.Grow(n * 3)
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteByte(' ')
		}
		const hexDigits = "0123456789abcdef"
		sb.WriteByte(hexDigits[raw[i]>>4])
		sb.WriteByte(hexDigits[raw[i]&0x0f])
	}
	return sb.String(), truncated
}

// bitmapSetBits 统计位图中置位的个数，并在同时算出最常见的置位下标清单（上限 limit）。
// 位图按 Redis SETBIT 的语义解释：字节 i 的第 bit 位（从高位起）对应下标 i*8+bit。
// 元素极多时只取前 limit 个，继续计数但不记录，保证响应量可控。
func bitmapSetBits(raw []byte, limit int) (count int, positions []int) {
	if limit <= 0 {
		limit = 256
	}
	for byteIdx, b := range raw {
		if b == 0 {
			continue
		}
		for bit := 0; bit < 8; bit++ {
			// 最高位对应 offset 最小的下标
			if b&(0x80>>uint(bit)) != 0 {
				count++
				if len(positions) < limit {
					positions = append(positions, byteIdx*8+bit)
				}
			}
		}
	}
	return count, positions
}

// joinInts 把下标清单拼成 "0,3,8" 形式，便于前端直接展示。
func joinInts(nums []int) string {
	if len(nums) == 0 {
		return ""
	}
	parts := make([]string, len(nums))
	for i, n := range nums {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ",")
}
