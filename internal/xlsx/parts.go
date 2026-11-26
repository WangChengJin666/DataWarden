package xlsx

// 以下四个 XML 是 xlsx 的最小骨架，内容固定，不含数据，因此写成常量。
// 每个部件都在 [Content_Types].xml 或某个 .rels 里被引用——删掉任意一个，
// Excel 都会报「文件已损坏」，且不会告诉你缺的是哪个。

// contentTypesXML 声明包里各类部件的 MIME 类型。
// 按扩展名兜底（Default）而不是逐个 Override，是因为我们的部件只有
// rels / xml 两种后缀，写两条就够，新增表也不需要改这里。
const contentTypesXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>
<Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>
<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>
</Types>`

// rootRelsXML 指向工作簿。id 必须是 rId1：不是 Excel 的要求，
// 而是这份固定的关系表里只有这一条，编号换掉对不上别处的心智模型。
const rootRelsXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>
</Relationships>`

// workbookXML 只有一个工作表。SheetName 与 workbook 里的 name 必须一致，
// 否则打开时提示「修复」并丢掉表名。
const workbookXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<sheets><sheet name="` + SheetName + `" sheetId="1" r:id="rId1"/></sheets>
</workbook>`

// workbookRelsXML 把 r:id="rId1" 指向具体的表文件与样式文件。
const workbookRelsXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>
</Relationships>`

// stylesXML 定义三种单元格格式，顺序必须与代码里的常量对应：
//
//	0 默认（不加 s 属性）
//	1 表头：加粗
//	2 文本：numFmtId=49（内置的 "@"），用于长数字与二进制列，
//	  防止 Excel 把 1.4759057188135424E16 这类值改成科学计数法
//
// cellXfs 里的顺序就是单元格 s 属性的取值，改顺序等于改变所有单元格的格式。
const stylesXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
<fonts count="2">
<font><sz val="11"/><name val="Calibri"/></font>
<font><b/><sz val="11"/><name val="Calibri"/></font>
</fonts>
<fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills>
<borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders>
<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>
<cellXfs count="3">
<xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/>
<xf numFmtId="0" fontId="1" fillId="0" borderId="0" xfId="0" applyFont="1"/>
<xf numFmtId="49" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/>
</cellXfs>
</styleSheet>`
