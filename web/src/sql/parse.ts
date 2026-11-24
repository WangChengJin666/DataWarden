/**
 * SQL 编辑器的轻量解析工具。
 *
 * 为什么不用完整解析器：`@codemirror/lang-sql` 自带的别名识别（getAliases）
 * 只在语句语法**完整**时才生效——输入到 `SELECT t. FROM app_user t` 之前，
 * 语句处于语法错误的中间态，它拿不到别名，`t.` 也就提示不出字段。
 * 而「刚敲完 FROM 就开始写 WHERE t.」恰恰是最常见的输入路径。
 *
 * 所以这里按 Navicat 的做法做**容错扫描**：正则找 FROM / JOIN 后面的
 * 「表名 [AS] 别名」，再按游标位置找出当前正在输入的限定前缀。
 * 正则不追求覆盖全部 SQL 语法（子查询、LATERAL、函数表等），
 * 目标是「正常查询的 90% 能提示对」，解析不出来就退化成不提示，不会给出错提示。
 *
 * 文件内全部是纯函数，便于单测——这部分逻辑最容易出回归。
 */

/** 一处「表引用」：FROM / JOIN 后面的 库.表 或 表，以及它的别名。 */
export interface TableRef {
  /** 库（PostgreSQL 的 schema / MySQL 的 database），未写时为空串 */
  database: string
  table: string
  /** 别名。没写别名时**等于表名**，这样 `app_user.id` 与 `t.id` 都能命中 */
  alias: string
  /** 该表引用在文档里的起始偏移，仅用于提示排序（越靠前越可能相关） */
  from: number
}

/** 游标处的补全上下文。 */
export interface QueryContext {
  /** 限定前缀，如 `t.` 得到 ['t']，`public.app_user.` 得到 ['public','app_user'] */
  parents: string[]
  /** 前缀在文档中的起点：补全替换范围从这里开始 */
  from: number
  /** 前缀后面是否已经跟了字符（用于判断是不是空上下文） */
  empty: boolean
}

// 标识符：普通写法、双引号、反引号、方括号（MSSQL）
const IDENT = String.raw`(?:"[^"]+"|` + '`[^`]+`' + String.raw`|\[[^\]]+\]|[A-Za-z_\u4e00-\u9fa5][\w$\u4e00-\u9fa5]*)`

/** 把 `"orders"` / `` `orders` `` / `[orders]` 还原成裸名。 */
function unquote(raw: string): string {
  const s = raw.trim()
  if (s.length >= 2) {
    const head = s[0]
    if ((head === '"' && s.endsWith('"')) || (head === '`' && s.endsWith('`')) || (head === '[' && s.endsWith(']'))) {
      return s.slice(1, -1)
    }
  }
  return s
}

/**
 * 去掉注释，并把字符串字面量与注释替换成等长空白。
 *
 * 等长是关键：替换后所有下标仍与原文一致，补全范围（from）才不会错位。
 * 不去掉的话，注释里的 `from` 或字符串里的 `t.` 会污染解析结果。
 */
export function maskLiterals(sql: string): string {
  let out = ''
  let i = 0
  const n = sql.length
  const blank = (s: string) => s.replace(/[^\n]/g, ' ')

  while (i < n) {
    const c = sql[i]
    const next = sql[i + 1]

    // 行注释
    if (c === '-' && next === '-') {
      let end = sql.indexOf('\n', i)
      if (end < 0) end = n
      out += blank(sql.slice(i, end))
      i = end
      continue
    }
    // 块注释
    if (c === '/' && next === '*') {
      let end = sql.indexOf('*/', i + 2)
      end = end < 0 ? n : end + 2
      out += blank(sql.slice(i, end))
      i = end
      continue
    }
    // 字符串：'...' 与 $tag$...$tag$（PG 的美元引用）
    if (c === "'") {
      let j = i + 1
      while (j < n) {
        if (sql[j] === "'" && sql[j + 1] === "'") {
          j += 2
          continue
        }
        if (sql[j] === "'") {
          j += 1
          break
        }
        j += 1
      }
      out += blank(sql.slice(i, j))
      i = j
      continue
    }
    if (c === '$') {
      const m = /^\$[A-Za-z_]*\$/.exec(sql.slice(i))
      if (m) {
        const tag = m[0]
        const end = sql.indexOf(tag, i + tag.length)
        const j = end < 0 ? n : end + tag.length
        out += blank(sql.slice(i, j))
        i = j
        continue
      }
    }
    out += c
    i += 1
  }
  return out
}

/**
 * FROM / JOIN 后面的表引用。
 *
 * 匹配形态（`t1` 为库或表名）：
 *   FROM app_user t
 *   FROM app_user AS t
 *   FROM public.app_user AS t
 *   JOIN "AppUser" u ON ...
 *   FROM app_user            → alias 取 app_user
 *
 * 不匹配子查询 `FROM (SELECT ...)`：那需要真正解析括号层级，
 * 子查询里的别名由内层自己的 FROM 贡献，顶层拿不到也不该拿。
 */
const TABLE_REF = new RegExp(
  String.raw`\b(?:from|join)\s+(${IDENT}(?:\s*\.\s*${IDENT})?)(?:\s+(?:as\s+)?(${IDENT}))?`,
  'gi',
)

const KEYWORDS_AFTER_TABLE = new Set(
  (
    'where group order having limit offset union intersect except on using ' +
    'inner left right full cross join natural straight_join window fetch for ' +
    'returning lateral sets'
  ).split(' '),
)

/** 从（已 mask 的）SQL 中提取全部表引用及其别名。 */
export function parseTableRefs(masked: string): TableRef[] {
  const refs: TableRef[] = []
  TABLE_REF.lastIndex = 0
  let m: RegExpExecArray | null
  while ((m = TABLE_REF.exec(masked)) !== null) {
    const full = unquote(m[1]).replace(/\s+/g, '')
    const parts = full.split('.')
    const table = parts[parts.length - 1]
    let alias = table

    const candidate = m[2] ? unquote(m[2]) : ''
    // 别名位置上的可能是子句关键字（`FROM t WHERE x`）或另一个表名
    // （`FROM a, b` 的逗号写法不会被这里匹配到，但 `FROM t natural join` 会）
    if (candidate && !KEYWORDS_AFTER_TABLE.has(candidate.toLowerCase())) {
      alias = candidate
    }

    refs.push({
      database: parts.length > 1 ? parts.slice(0, -1).join('.') : '',
      table,
      alias,
      from: m.index,
    })
  }
  return refs
}

/**
 * 判断游标处正在输入什么，返回限定前缀与替换起点。
 *
 * 例：
 *   `SELECT t.|`            → parents ['t'],     from 指向 `t.` 之后
 *   `SELECT t.na|`          → parents ['t'],     from 指向 `na` 起点
 *   `SELECT * FROM a WHERE |` → parents [],     from 为游标
 *   `SELECT 'x.|'`          → parents []（字符串内不提示）
 */
export function resolveQueryContext(doc: string, pos: number): QueryContext {
  const masked = maskLiterals(doc)
  const before = masked.slice(0, pos)

  // 匹配「[限定链] + 当前正在输入的标识符片段」
  // 结尾允许零个字符（刚敲完点号），所以片段部分用 * 而不是 +
  const m = new RegExp(
    String.raw`((?:(?:${IDENT})\s*\.\s*)*)((?:${IDENT})?)$`,
  ).exec(before)

  if (!m) {
    return { parents: [], from: pos, empty: true }
  }

  const chain = m[1] ?? ''
  const partial = m[2] ?? ''
  const parents = chain
    ? chain
        .split('.')
        .map((s) => unquote(s))
        .filter((s) => s !== '')
    : []

  return {
    parents,
    from: pos - partial.length,
    empty: partial.length === 0 && parents.length === 0,
  }
}

/**
 * 把限定前缀（如 ['t'] 或 ['public','app_user']）解析成具体的表。
 *
 * 匹配优先级：别名 → 表名 → 库名.表名。返回 null 表示「前缀指向的东西
 * 不在 FROM/JOIN 里」，此时不该给出任何列提示——否则用户会拿到
 * 一张根本没被查询的表的字段，比不提示更糟。
 */
export function resolveTableByPrefix(refs: TableRef[], parents: string[]): TableRef | null {
  if (parents.length === 0) return null
  const last = parents[parents.length - 1].toLowerCase()
  const parentDb = parents.length > 1 ? parents.slice(0, -1).join('.').toLowerCase() : ''

  const byAlias = refs.find((r) => r.alias.toLowerCase() === last)
  if (byAlias) return byAlias

  const byTable = refs.find(
    (r) => r.table.toLowerCase() === last && (!parentDb || r.database.toLowerCase() === parentDb),
  )
  return byTable ?? null
}
