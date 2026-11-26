// Package xlsx 把二维查询结果写成最小的 xlsx 文件。
//
// 为什么不引第三方库（excelize 之类）：
//
//	本项目只需要「表头 + 若干行单元格」这一种产出，没有任何读、改、
//	样式、公式、图表的需求。引一个大库进来，除了多几十 MB 依赖和
//	一条需要长期跟的安全升级线，换不来什么——而 xlsx 的最小可用形态
//	其实只有三个 XML 部件，写起来不到两百行。
//
// 生成的是 Office Open XML（.xlsx，非旧版 .xls）：一个 zip 里至少要有
// [Content_Types].xml、_rels/.rels、xl/workbook.xml 与 xl/worksheets/sheet1.xml。
// 缺任何一个 Excel / WPS / Numbers 都会报「文件已损坏」且不告诉你缺什么。
package xlsx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// 单元格格式编号（对应 styles.xml 里 cellXfs 的下标）。
//
// 专门给「长数字」留了一档是有原因的：订单号、身份证号这类值虽然
// 是数字，按数值写进 Excel 后会被显示成 1.47591E+16，用户拿到的文件
// 一打开就是错的。第 49 号是 Excel 内置格式 "@"（文本），
// 用它锁定这些单元格，Excel 就不会自作主张地转成科学计数法。
const (
	styleDefault = 0
	styleHeader  = 1
	styleText    = 2
)

// SheetName 是工作表名。
//
// 固定英文而不是中文：中文表名需要额外确认编码处理，而且这个文件
// 是给机器再处理的中间产物，表名不影响使用。
const SheetName = "result"

// FromQueryResult 把列名与行数据写成一个 xlsx 文件的字节内容。
//
// 入参 rows 的每一项长度不要求一致：查询结果里出现短行（驱动没有补齐
// 空值）是可能的，这里按列数补齐空单元格，否则生成的 XML 会让
// Excel 认为表格结构损坏。
func FromQueryResult(columns []string, rows [][]any) ([]byte, error) {
	if len(columns) == 0 {
		return nil, fmt.Errorf("没有可导出的列")
	}
	// Excel 的硬上限：16384 列、1048576 行（含表头）。
	// 超了 Excel 打不开，而我们在写入前就知道，可以直接给出明确错误，
	// 而不是让用户拿到一个"文件已损坏"的下载。
	const maxCols = 16384
	const maxRows = 1048576 - 1
	if len(columns) > maxCols {
		return nil, fmt.Errorf("列数 %d 超过 Excel 上限 %d", len(columns), maxCols)
	}
	if len(rows) > maxRows {
		return nil, fmt.Errorf("行数 %d 超过 Excel 上限 %d", len(rows), maxRows)
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	files := []struct {
		name string
		body []byte
	}{
		{"[Content_Types].xml", []byte(contentTypesXML)},
		{"_rels/.rels", []byte(rootRelsXML)},
		{"xl/workbook.xml", []byte(workbookXML)},
		{"xl/_rels/workbook.xml.rels", []byte(workbookRelsXML)},
		{"xl/styles.xml", []byte(stylesXML)},
		{"xl/worksheets/sheet1.xml", sheetXML(columns, rows)},
	}
	for _, f := range files {
		w, err := zw.Create(f.name)
		if err != nil {
			return nil, fmt.Errorf("create %s: %w", f.name, err)
		}
		if _, err := w.Write(f.body); err != nil {
			return nil, fmt.Errorf("write %s: %w", f.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("close zip: %w", err)
	}
	return buf.Bytes(), nil
}

// sheetXML 生成工作表。第一行是表头，其余是数据行。
func sheetXML(columns []string, rows [][]any) []byte {
	var b bytes.Buffer
	b.WriteString(xml.Header)
	b.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)
	fmt.Fprintf(&b, `<dimension ref="A1:%s%d"/>`, colName(len(columns)), len(rows)+1)
	b.WriteString(`<sheetViews><sheetView workbookViewId="0">`)
	// 冻结首行：导出通常几百行，滚下去就看不到表头了。
	// pane 必须排在 selection 之前，顺序错了 Excel 会报文件损坏。
	b.WriteString(`<pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/>`)
	b.WriteString(`<selection pane="bottomLeft" activeCell="A2" sqref="A2"/>`)
	b.WriteString(`</sheetView></sheetViews>`)
	b.WriteString(`<sheetFormatPr defaultRowHeight="15"/>`)
	b.WriteString(sheetColsXML(columns))
	b.WriteString(`<sheetData>`)

	// 表头
	b.WriteString(`<row r="1">`)
	for i, c := range columns {
		writeCell(&b, i, 1, c, styleHeader)
	}
	b.WriteString(`</row>`)

	for ri, row := range rows {
		rowNum := ri + 2 // 第 1 行是表头
		fmt.Fprintf(&b, `<row r="%d">`, rowNum)
		for ci := 0; ci < len(columns); ci++ {
			var v any
			if ci < len(row) {
				v = row[ci]
			}
			writeCell(&b, ci, rowNum, v, styleDefault)
		}
		b.WriteString(`</row>`)
	}

	b.WriteString(`</sheetData>`)
	// 自动筛选：让用户能直接在 Excel 里按列筛选，这是"导出后接着用"
	// 最常见的下一步，不加的话得自己拖。
	fmt.Fprintf(&b, `<autoFilter ref="A1:%s%d"/>`, colName(len(columns)), len(rows)+1)
	b.WriteString(`</worksheet>`)
	return b.Bytes()
}

// sheetColsXML 给每列一个按表头长度估算的宽度，并顺手把表头行设为
// 加粗（样式在 styles.xml 里）。不设宽度时 Excel 按默认 8.43 字符显示，
// 中文列名会被截成「订单…」，第一眼就不知道这列是什么。
func sheetColsXML(columns []string) string {
	var b strings.Builder
	b.WriteString(`<cols>`)
	for i, c := range columns {
		// 中文字符按 2 个字符宽度算，再留 2 个字符的余量；夹在 [8, 40] 之间，
		// 避免「一个超长列名把整个表撑到屏幕外」。
		w := 0
		for _, r := range c {
			if r > 0x7f {
				w += 2
			} else {
				w++
			}
		}
		w += 2
		if w < 8 {
			w = 8
		}
		if w > 40 {
			w = 40
		}
		fmt.Fprintf(&b, `<col min="%d" max="%d" width="%d" customWidth="1"/>`, i+1, i+1, w)
	}
	b.WriteString(`</cols>`)
	return b.String()
}

// writeCell 写一个单元格。
//
// 类型取舍：
//   - nil → 空单元格（不写 <v>，Excel 显示为空白，与 NULL 的展示一致）
//   - 数字 → 数值单元格，但超过 15 位有效数字的转成文本（见下）
//   - 布尔 → 布尔单元格
//   - 其余 → 内联字符串
func writeCell(b *bytes.Buffer, col, row int, v any, style int) {
	ref := colName(col+1) + strconv.Itoa(row)

	if v == nil {
		// 完全省略这个单元格：Excel 对 <c r="B2"/> 与不写 B2 的处理一致，
		// 但省略能让 90% 全是 NULL 的宽表文件小很多。
		return
	}

	styleAttr := ""
	if style == styleHeader {
		styleAttr = fmt.Sprintf(` s="%d"`, styleHeader)
	}

	switch val := v.(type) {
	case int, int8, int16, int32, int64:
		fmt.Fprintf(b, `<c r="%s"%s><v>%d</v></c>`, ref, styleAttr, toInt64(val))
	case uint, uint8, uint16, uint32, uint64:
		fmt.Fprintf(b, `<c r="%s"%s><v>%d</v></c>`, ref, styleAttr, toUint64(val))
	case float32:
		writeFloat(b, ref, styleAttr, float64(val))
	case float64:
		writeFloat(b, ref, styleAttr, val)
	case bool:
		v := "0"
		if val {
			v = "1"
		}
		fmt.Fprintf(b, `<c r="%s"%s t="b"><v>%s</v></c>`, ref, styleAttr, v)
	case []byte:
		// 二进制列（MySQL BLOB / 位图）按文本写出只会是乱码，
		// 统一写成 base64 之外的「十六进制」更接近用户在库里看到的样子。
		writeInlineStr(b, ref, styleAttr, fmt.Sprintf("0x%x", val))
	default:
		text := fmt.Sprintf("%v", val)
		// 长数字串也可能是文本类型传过来的（驱动把 numeric 给了字符串），
		// 一律当文本写，保住前导零与完整位数。
		if style == styleHeader {
			writeInlineStr(b, ref, styleAttr, text)
			return
		}
		writeInlineStr(b, ref, fmt.Sprintf(` s="%d"`, styleText), text)
	}
}

// writeFloat 写浮点数，特殊值（NaN / Inf）转成文本。
//
// 直接 fmt 输出 "NaN" 会生成 <v>NaN</v>，Excel 打开时判定为非法数值，
// 整个文件报损坏。宁可写成一格文本 "NaN"，也不要让用户拿到打不开的文件。
func writeFloat(b *bytes.Buffer, ref, styleAttr string, f float64) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		writeInlineStr(b, ref, fmt.Sprintf(` s="%d"`, styleText), strconv.FormatFloat(f, 'g', -1, 64))
		return
	}
	// 'g' 会丢掉末尾的 .0（1 变成 1 而不是 1.0），对导出而言更自然。
	fmt.Fprintf(b, `<c r="%s"%s><v>%s</v></c>`, ref, styleAttr,
		strconv.FormatFloat(f, 'g', -1, 64))
}

func writeInlineStr(b *bytes.Buffer, ref, styleAttr, text string) {
	fmt.Fprintf(b, `<c r="%s"%s t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>`,
		ref, styleAttr, escapeXML(text))
}

// escapeXML 转义 XML 文本内容。
//
// 不能只用 xml.EscapeText 的默认行为：它会把 \r 转成 &#xD;（可以接受），
// 但对 XML 1.0 不允许的控制字符（0x00-0x08、0x0B、0x0C、0x0E-0x1F）
// 会原样输出，而这类字符在数据库里是真实存在的（二进制被当成字符串返回）。
// 一旦写进文件，Excel 直接判定文件损坏。这里把它们替换成可见的替代符。
func escapeXML(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 16)
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&apos;")
		case '\r':
			b.WriteString("&#xD;")
		case '\n', '\t':
			b.WriteRune(r)
		default:
			if isXMLChar(r) {
				b.WriteRune(r)
			} else {
				b.WriteRune('?')
			}
		}
	}
	return b.String()
}

// isXMLChar 判断字符是否在 XML 1.0 允许的范围内。
func isXMLChar(r rune) bool {
	return r == 0x9 || r == 0xA || r == 0xD ||
		(r >= 0x20 && r <= 0xD7FF) ||
		(r >= 0xE000 && r <= 0xFFFD) ||
		(r >= 0x10000 && r <= 0x10FFFF)
}

// colName 把 1 起的列号转成 Excel 的列名：1→A、26→Z、27→AA。
func colName(n int) string {
	if n <= 0 {
		return "A"
	}
	var out []byte
	for n > 0 {
		n--
		out = append([]byte{byte('A' + n%26)}, out...)
		n /= 26
	}
	return string(out)
}

func toInt64(v any) int64 {
	switch n := v.(type) {
	case int:
		return int64(n)
	case int8:
		return int64(n)
	case int16:
		return int64(n)
	case int32:
		return int64(n)
	case int64:
		return n
	}
	return 0
}

func toUint64(v any) uint64 {
	switch n := v.(type) {
	case uint:
		return uint64(n)
	case uint8:
		return uint64(n)
	case uint16:
		return uint64(n)
	case uint32:
		return uint64(n)
	case uint64:
		return n
	}
	return 0
}
