package xlsx

import (
	"archive/zip"
	"bytes"
	"io"
	"math"
	"strings"
	"testing"
)

// readZip 把生成的包读回来，按文件名索引内容。
//
// 只断言「返回了字节」是不可靠的：一个少写了 sheet1.xml 的包同样非空，
// 但 Excel 打开就是「文件已损坏」。所以直接检查部件是否齐全、能否解析。
func readZip(t *testing.T, data []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("生成的不是合法 zip: %v", err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		b, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatalf("read %s: %v", f.Name, err)
		}
		out[f.Name] = string(b)
	}
	return out
}

// xlsx 的最小部件集合，缺一个 Excel 就判定文件损坏且不提示缺什么。
func TestFromQueryResult_ContainsRequiredParts(t *testing.T) {
	data, err := FromQueryResult([]string{"id", "名字"}, [][]any{{1, "张三"}})
	if err != nil {
		t.Fatalf("FromQueryResult: %v", err)
	}
	parts := readZip(t, data)
	required := []string{
		"[Content_Types].xml",
		"_rels/.rels",
		"xl/workbook.xml",
		"xl/_rels/workbook.xml.rels",
		"xl/styles.xml",
		"xl/worksheets/sheet1.xml",
	}
	for _, name := range required {
		if _, ok := parts[name]; !ok {
			t.Errorf("缺少部件 %s", name)
		}
	}
	// 工作表名必须与 workbook.xml 里声明的一致，否则打开时提示修复
	if !strings.Contains(parts["xl/workbook.xml"], `name="`+SheetName+`"`) {
		t.Errorf("workbook.xml 未声明工作表名 %q", SheetName)
	}
	// 每个 Override / Relationship 指向的部件都得真的存在
	for _, ref := range []string{"/xl/workbook.xml", "/xl/worksheets/sheet1.xml", "/xl/styles.xml"} {
		if !strings.Contains(parts["[Content_Types].xml"], ref) {
			t.Errorf("[Content_Types].xml 缺少 %s 的声明", ref)
		}
	}
}

// 表头与数据都要在，且中文不能因为转义丢失。
func TestFromQueryResult_WritesHeaderAndRows(t *testing.T) {
	columns := []string{"订单号", "金额", "备注"}
	rows := [][]any{
		{"SO-1", 12.5, "加急"},
		{"SO-2", int64(20), nil},
	}
	data, err := FromQueryResult(columns, rows)
	if err != nil {
		t.Fatalf("FromQueryResult: %v", err)
	}
	sheet := readZip(t, data)["xl/worksheets/sheet1.xml"]

	for _, want := range []string{"订单号", "金额", "备注", "SO-1", "12.5", "加急", "SO-2", "20"} {
		if !strings.Contains(sheet, want) {
			t.Errorf("工作表里找不到 %q", want)
		}
	}
	// 单元格坐标必须按行列算对：第 2 行第 2 列是 B2
	if !strings.Contains(sheet, `r="B2"`) {
		t.Error("缺少 B2 单元格，列号计算有误")
	}
	// nil 应该被省略而不是写成空字符串单元格
	if strings.Contains(sheet, `<c r="C3"`) {
		t.Error("nil 值应省略单元格，不应写出空单元格")
	}
	// 冻结首行与自动筛选都在：导出几百行时这两项决定能不能直接用
	if !strings.Contains(sheet, `<pane ySplit="1"`) {
		t.Error("缺少冻结首行")
	}
	if !strings.Contains(sheet, `<autoFilter`) {
		t.Error("缺少自动筛选")
	}
}

// 转义是必须的：& < > 原样写进 XML 会让文件打不开，
// 而数据库里的文本随时会出现这些字符。
func TestFromQueryResult_EscapesXML(t *testing.T) {
	data, err := FromQueryResult([]string{"v"}, [][]any{{`a<b&c>"d"`}})
	if err != nil {
		t.Fatalf("FromQueryResult: %v", err)
	}
	sheet := readZip(t, data)["xl/worksheets/sheet1.xml"]
	if strings.Contains(sheet, `a<b&c>`) {
		t.Error("特殊字符未转义，生成的 XML 非法")
	}
	if !strings.Contains(sheet, "a&lt;b&amp;c&gt;&quot;d&quot;") {
		t.Errorf("转义结果不对：%s", sheet)
	}
}

// 控制字符（二进制被当成字符串返回）属于 XML 1.0 不允许的字符，
// 原样写入会让 Excel 判定文件损坏，必须替换掉。
func TestFromQueryResult_StripsIllegalControlChars(t *testing.T) {
	data, err := FromQueryResult([]string{"v"}, [][]any{{"a\x00b\x0bc"}})
	if err != nil {
		t.Fatalf("FromQueryResult: %v", err)
	}
	sheet := readZip(t, data)["xl/worksheets/sheet1.xml"]
	if strings.ContainsRune(sheet, 0x00) || strings.ContainsRune(sheet, 0x0b) {
		t.Error("非法控制字符未被清除，Excel 会报文件损坏")
	}
}

// NaN / Inf 不能写成 <v>NaN</v>：Excel 打开时判定数值非法、整份文件损坏。
func TestFromQueryResult_NonFiniteFloatsAsText(t *testing.T) {
	inf := math.Inf(1)
	data, err := FromQueryResult([]string{"v"}, [][]any{{inf}, {-inf}, {math.NaN()}})
	if err != nil {
		t.Fatalf("FromQueryResult: %v", err)
	}
	sheet := readZip(t, data)["xl/worksheets/sheet1.xml"]
	if strings.Contains(sheet, "<v>+Inf</v>") || strings.Contains(sheet, "<v>NaN</v>") {
		t.Error("非有限浮点数被写成了数值单元格")
	}
	if !strings.Contains(sheet, "inlineStr") {
		t.Error("非有限浮点数应按文本写出")
	}
}

// 列名转换：Excel 的列是 26 进制但没有 0，测试几个关键位置。
func TestColName(t *testing.T) {
	cases := map[int]string{
		1: "A", 26: "Z", 27: "AA", 52: "AZ", 53: "BA", 702: "ZZ", 703: "AAA",
	}
	for n, want := range cases {
		if got := colName(n); got != want {
			t.Errorf("colName(%d) = %q, want %q", n, got, want)
		}
	}
}

// 边界：没有列就没什么可导出的，明确报错而不是生成一个只有 sheet 的空文件。
func TestFromQueryResult_NoColumns(t *testing.T) {
	if _, err := FromQueryResult(nil, nil); err == nil {
		t.Fatal("没有列时应当报错")
	}
}

// 短行（驱动没补齐）要按列数补空，否则表格结构错位。
func TestFromQueryResult_ShortRowPadded(t *testing.T) {
	data, err := FromQueryResult([]string{"a", "b", "c"}, [][]any{{1}})
	if err != nil {
		t.Fatalf("FromQueryResult: %v", err)
	}
	sheet := readZip(t, data)["xl/worksheets/sheet1.xml"]
	if !strings.Contains(sheet, `<row r="1">`) || !strings.Contains(sheet, `<row r="2">`) {
		t.Fatal("行数不对")
	}
	// 第 2 行只应出现 A2；B2/C2 不写（等价于空）
	if strings.Contains(sheet, `r="B2"`) {
		t.Error("缺失的列不应凭空写出单元格")
	}
}
