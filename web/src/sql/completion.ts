/**
 * 查询页 SQL 编辑器（CodeMirror 6）的扩展集合。
 *
 * 目标是接近 Navicat 的输入体验，核心是四件事：
 *   1. `别名.` 之后提示字段——别名从当前语句的 FROM / JOIN 里实时解析
 *   2. SELECT 列表、WHERE 等「该写列的位置」直接提示字段名
 *   3. 鼠标悬停在表名 / 别名上，显示它的字段清单
 *   4. Ctrl+Enter 执行、Ctrl+Space 手动唤出补全等常用快捷键
 *
 * 为什么不用 `@codemirror/lang-sql` 自带的 schema 补全：它只在语句语法
 * 完整时才认得出别名，而用户敲到一半（`SELECT t. FROM app_user t` 还在写）
 * 正是最需要提示的时刻——那时 lang-sql 直接返回 null，什么也不提示。
 * 这里用 parse.ts 的容错扫描接管补全来源。
 */
import {
  autocompletion,
  type Completion,
  type CompletionContext,
  type CompletionResult,
  type CompletionSource,
} from '@codemirror/autocomplete'
import { keywordCompletionSource, type SQLDialect } from '@codemirror/lang-sql'
import { hoverTooltip, type EditorView, type Tooltip } from '@codemirror/view'
import {
  maskLiterals,
  parseTableRefs,
  resolveQueryContext,
  resolveTableByPrefix,
  type TableRef,
} from './parse'

/**
 * 编辑器使用的 schema：表名 → 字段名数组。
 *
 * PostgreSQL 下同一张表可能同时以 `app_user` 和 `public.app_user` 作键，
 * 因为用户在 SQL 里两种写法都会用，补全得都认。
 */
export type SchemaMap = Record<string, string[]>

/** 候选里最多列多少字段：几千条的下拉反而没法用。 */
const MAX_COLUMN_OPTIONS = 300

/** 一段 SQL 语句的范围。别名解析必须限制在当前语句内。 */
function statementRange(doc: string, pos: number): { from: number; to: number } {
  let from = 0
  for (let i = 0; i < pos; i++) {
    if (doc[i] === ';') from = i + 1
  }
  let to = doc.length
  for (let i = pos; i < doc.length; i++) {
    if (doc[i] === ';') {
      to = i
      break
    }
  }
  return { from, to }
}

/** 解析游标所在语句里的 FROM / JOIN 表引用。 */
export function tableRefsAt(doc: string, pos: number): TableRef[] {
  const { from, to } = statementRange(doc, pos)
  const stmt = doc.slice(from, to)
  // 位置修正成文档绝对偏移，悬停提示与排序都依赖它
  return parseTableRefs(maskLiterals(stmt)).map((r) => ({ ...r, from: r.from + from }))
}

/** 生成字段候选。detail 里带表名，从下拉里一眼能看出字段属于哪张表。 */
function columnOptions(schema: SchemaMap, ref: TableRef): Completion[] {
  const cols = schema[ref.table] ?? []
  if (cols.length === 0) return []
  return cols.slice(0, MAX_COLUMN_OPTIONS).map((name, i) => ({
    label: name,
    type: 'property',
    detail: ref.table,
    info: `${ref.table}.${name}`,
    // 保持建表顺序：boost 递减，靠前的字段排得越前。主键与常用列通常在表头，
    // 这与 Navicat 的排序直觉一致（按字母序反而难找）。
    boost: Math.max(0, 100 - i) / 100,
  }))
}

/** 表候选：schema 里的全部表 + 当前语句已有的别名。 */
function tableOptions(schema: SchemaMap, refs: TableRef[]): Completion[] {
  const out: Completion[] = []
  const seen = new Set<string>()

  for (const ref of refs) {
    // 有别名时，`别名.` 才是用户在写的写法，权重给高
    if (ref.alias !== ref.table) {
      out.push({
        label: ref.alias,
        type: 'variable',
        detail: `${ref.table} 的别名`,
        boost: 90,
      })
      seen.add(ref.alias)
    }
  }
  for (const name of Object.keys(schema)) {
    // PostgreSQL 下同一张表有裸名与带 schema 两份，候选里只展示裸名
    if (name.includes('.') || seen.has(name)) continue
    out.push({ label: name, type: 'type', detail: '表', boost: 60 })
  }
  return out
}

/** 光标前 40 个字符，用来判断「当前位置该不该出现字段 / 表名」。 */
function contextTail(doc: string, pos: number): string {
  return maskLiterals(doc.slice(Math.max(0, pos - 40), pos))
}

/**
 * schema 补全来源。
 *
 * 三种情况：
 *   - `前缀.`：前缀是别名 / 表名 / 库.表名 → 只提示该表的字段
 *   - 顶层且处于「该写表名」的位置（FROM / JOIN / INTO）→ 表名与别名
 *   - 顶层且处于「该写字段」的位置（SELECT 列表、WHERE、ON、ORDER BY…）→ 字段
 *   - 其余（前缀解析不到、位置判断不出）→ 返回 null，让给关键字补全。
 *     **宁可不提示，也不提示错表的字段**——错提示比没提示更误导人。
 */
export function makeSchemaCompletion(getSchema: () => SchemaMap): CompletionSource {
  return (context: CompletionContext): CompletionResult | null => {
    const schema = getSchema()
    const doc = context.state.doc.toString()
    // 没有 schema 时完全让给关键字补全，不做任何臆测
    if (Object.keys(schema).length === 0) return null

    const { parents, from, empty } = resolveQueryContext(doc, context.pos)
    if (empty && !context.explicit) return null

    const refs = tableRefsAt(doc, context.pos)

    if (parents.length > 0) {
      const ref = resolveTableByPrefix(refs, parents)
      if (!ref) return null
      const options = columnOptions(schema, ref)
      if (options.length === 0) return null
      return { from, options, validFor: /^[\w$]*$/ }
    }

    const tail = contextTail(doc, context.pos)
    const wantsTable = /(?:\bfrom|\bjoin|\binto)\s+\w*$/i.test(tail)
    // 字段位置：SELECT 之后、WHERE / ON / HAVING / ORDER BY / GROUP BY /
    // SET / 逗号之后。判断不出就只在手动唤出时给候选。
    const wantsColumn = /(?:\bselect|\bwhere|\bon|\bhaving|\bby|\band|\bor|,)\s+\w*$/i.test(tail)

    if (!context.explicit && !wantsTable && !wantsColumn) return null

    const options = wantsTable
      ? tableOptions(schema, refs)
      : columnOptionsForQuery(schema, refs)

    if (options.length === 0) return null
    return { from, options, validFor: /^[\w$]*$/ }
  }
}

/**
 * 无前缀时的字段候选：把当前语句涉及的所有表的字段并起来。
 *
 * 为什么不是只给单表字段：`SELECT |` 时用户还没写 FROM，或者写了多表 JOIN，
 * 任何单表都不完整。并集再按表排序是唯一不误导的选择。
 * 语句里还没写 FROM 时退化成「整个库的全部字段」——用户此刻确实还没限定范围，
 * 由 SELECT 之后的输入逐步收敛。
 */
function columnOptionsForQuery(schema: SchemaMap, refs: TableRef[]): Completion[] {
  const options: Completion[] = []
  const seen = new Set<string>()
  const add = (name: string, table: string, i: number) => {
    if (seen.has(name)) return
    seen.add(name)
    options.push({
      label: name,
      type: 'property',
      detail: table,
      boost: Math.max(0, 100 - i) / 100,
    })
  }

  if (refs.length > 0) {
    for (const ref of refs) {
      const cols = schema[ref.table] ?? []
      cols.slice(0, MAX_COLUMN_OPTIONS).forEach((c, i) => add(c, ref.table, i))
    }
  } else {
    for (const [table, cols] of Object.entries(schema)) {
      if (table.includes('.')) continue
      cols.slice(0, MAX_COLUMN_OPTIONS).forEach((c, i) => add(c, table, i))
    }
  }
  return options
}

/**
 * 悬停提示：把鼠标放在表名或别名上，显示它的字段清单。
 *
 * 与补全共用同一份 schema 与解析结果，所以「能补全的表」也「能悬停」，
 * 不会出现补全拿得到字段、悬停却一片空白这种不一致。
 */
export function makeHoverTooltip(getSchema: () => SchemaMap) {
  return hoverTooltip(
    (view, pos, side): Tooltip | null => {
      const schema = getSchema()
      if (Object.keys(schema).length === 0) return null

      const doc = view.state.doc.toString()
      const refs = tableRefsAt(doc, pos)
      if (refs.length === 0) return null

      // 找出光标所在位置的标识符
      const line = view.state.doc.lineAt(pos)
      const text = line.text
      let start = pos - line.from
      let end = start
      while (start > 0 && /[\w$]/.test(text[start - 1])) start--
      while (end < text.length && /[\w$]/.test(text[end])) end++
      const word = text.slice(start, end)
      if (!word) return null

      const lower = word.toLowerCase()
      const ref = refs.find((r) => r.alias.toLowerCase() === lower) ?? refs.find((r) => r.table.toLowerCase() === lower)
      if (!ref) return null

      const cols = schema[ref.table] ?? []
      const from = line.from + start
      const to = line.from + end
      if (pos < from || pos > to + 1 || side < 0) return null

      return {
        pos: from,
        end: to,
        above: true,
        create: () => {
          const dom = document.createElement('div')
          dom.className = 'dw-sql-hover'
          const head = document.createElement('div')
          head.className = 'dw-sql-hover__head'
          head.textContent = `${ref.table}${ref.alias !== ref.table ? `（别名 ${ref.alias}）` : ''} · ${cols.length} 个字段`
          dom.appendChild(head)
          if (cols.length === 0) {
            const empty = document.createElement('div')
            empty.className = 'dw-sql-hover__empty'
            empty.textContent = '未取到字段，可能是表不存在或没有读取权限'
            dom.appendChild(empty)
          } else {
            const list = document.createElement('div')
            list.className = 'dw-sql-hover__cols'
            for (const c of cols.slice(0, 120)) {
              const item = document.createElement('span')
              item.textContent = c
              list.appendChild(item)
            }
            dom.appendChild(list)
          }
          return { dom }
        },
      }
    },
    { hoverTime: 250 },
  )
}

/**
 * 组装编辑器扩展。
 *
 * override 两个来源：schema 补全在前（更具体），关键字补全在后。
 * 关键字补全用大写，与 `upperCaseKeywords: true` 的惯例一致，
 * 避免同一份 SQL 里 `select` 和 `SELECT` 混着出现。
 */
export function buildSqlExtensions(opts: {
  getSchema: () => SchemaMap
  getDialect: () => SQLDialect
}) {
  return [
    autocompletion({
      override: [
        makeSchemaCompletion(opts.getSchema),
        keywordCompletionSource(opts.getDialect(), true),
      ],
      activateOnTyping: true,
      closeOnBlur: true,
    }),
    makeHoverTooltip(opts.getSchema),
  ]
}

export type { EditorView }
