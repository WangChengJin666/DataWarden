/**
 * 查询页 SQL 编辑器（CodeMirror 6）的扩展集合。
 *
 * 目标只有一件事：`别名.` 或 `表名.` 之后提示该表的字段——别名从当前语句的
 * FROM / JOIN 里实时解析。其余位置（SELECT 开头、WHERE、FROM 之后）不再给
 * 表名或字段候选，理由见 makeSchemaCompletion 的注释。
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

/**
 * schema 补全来源：**只有带点号的限定前缀才给候选**，即 `t.` / `app_user.` /
 * `public.app_user.` → 该表的字段。
 *
 * 为什么删掉了「SELECT 之后提示字段」「FROM 之后提示表名」这两条：
 *   - 字段候选是整库的并集（没有 FROM 时只能把库里每张表的列都列出来），
 *     候选上千条，既选不准，也把真正想要的表名挤到看不见的地方；
 *   - 表名候选同理，库表本来就由右侧的下拉负责展示与复制，
 *     编辑器再重复一份只会互相打断。
 * 只有 `前缀.` 这种「用户已经指明了是哪张表」的场景值得自动弹，
 * 目标唯一、候选有限，这是它和上面两种情况的本质区别。
 *
 * 其余位置一律返回 null（连 Ctrl+Space 也不给），让给关键字补全。
 */
export function makeSchemaCompletion(getSchema: () => SchemaMap): CompletionSource {
  return (context: CompletionContext): CompletionResult | null => {
    const schema = getSchema()
    const doc = context.state.doc.toString()
    // 没有 schema 时完全让给关键字补全，不做任何臆测
    if (Object.keys(schema).length === 0) return null

    const { parents, from } = resolveQueryContext(doc, context.pos)
    if (parents.length === 0) return null

    const ref = resolveTableByPrefix(tableRefsAt(doc, context.pos), parents)
    if (!ref) return null

    const options = columnOptions(schema, ref)
    if (options.length === 0) return null
    return { from, options, validFor: /^[\w$]*$/ }
  }
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
