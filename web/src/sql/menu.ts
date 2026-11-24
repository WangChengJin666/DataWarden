/**
 * SQL 编辑器的右键菜单（Navicat 同款操作：执行当前语句、格式化、选中当前语句等）。
 *
 * 为什么自己写而不是引 `codemirror-context-menu` 之类的包：
 * 需要的动作就那么六个，全部能从 EditorView 拿到，一个 200 行的文件
 * 换掉一个新依赖 + 一套自己的样式覆盖，划算。样式复用 antd 的浅色基调。
 *
 * 菜单是纯 DOM 覆盖层（position: fixed），挂在 body 上而不是编辑器内部：
 * 编辑器高度只有 220px，菜单挂在里面会被裁掉。
 */
import { EditorSelection, type EditorState } from '@codemirror/state'
import {
  cursorLineBoundaryBackward,
  cursorLineBoundaryForward,
  selectAll,
} from '@codemirror/commands'
import { openSearchPanel } from '@codemirror/search'
import type { EditorView } from '@codemirror/view'

/** 菜单项。`run` 返回 false 表示当前上下文不适用，菜单里置灰。 */
export interface SqlMenuItem {
  key: string
  label: string
  shortcut?: string
  enabled: (view: EditorView) => boolean
  run: (view: EditorView) => void
  /** 分隔线画在这一项之前 */
  separatorBefore?: boolean
}

/** 取光标（或选区起点）所在的语句文本范围。 */
export function currentStatement(view: EditorView): { from: number; to: number; text: string } {
  const doc = view.state.doc.toString()
  const pos = view.state.selection.main.head
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
  // 去掉首尾空白，避免选中整行空白后执行报「请输入 SQL」
  const raw = doc.slice(from, to)
  const lead = raw.length - raw.trimStart().length
  const trimmed = raw.trim()
  return { from: from + lead, to: from + lead + trimmed.length, text: trimmed }
}

/**
 * 选中（或取出）当前语句，并返回它的文本。
 *
 * 执行按钮也用这个：编辑器里有多条语句时，Navicat 的行为是「只执行光标所在的
 * 那一条」，而不是整篇一起丢给数据库（平台后端只允许单语句）。
 */
export function pickStatement(view: EditorView): string {
  const sel = view.state.selection.main
  if (!sel.empty) return view.state.doc.sliceString(sel.from, sel.to)

  const { from, to, text } = currentStatement(view)
  if (text) {
    view.dispatch({ selection: EditorSelection.single(from, to) })
  }
  return text
}

/**
 * 需要另起一行的复合关键字。
 *
 * 它们必须先被替换成占位符再处理单词关键字（见 formatSql 里的说明），
 * 否则 `LEFT JOIN` 会被 `JOIN` 规则切开。
 */
const COMPOUND_KEYWORDS = [
  'LEFT OUTER JOIN',
  'RIGHT OUTER JOIN',
  'FULL OUTER JOIN',
  'LEFT JOIN',
  'RIGHT JOIN',
  'FULL JOIN',
  'INNER JOIN',
  'CROSS JOIN',
  'UNION ALL',
  'GROUP BY',
  'ORDER BY',
]

/** 单词形式的主关键字，统一在前面换行。 */
const SINGLE_KEYWORDS = ['FROM', 'WHERE', 'HAVING', 'LIMIT', 'OFFSET', 'ON', 'JOIN', 'UNION']

/** 把括号外（深度 0）的逗号替换成「逗号 + 换行 + 两空格缩进」。 */
function splitTopLevelCommas(s: string): string {
  let depth = 0
  let out = ''
  for (let i = 0; i < s.length; i++) {
    const c = s[i]
    if (c === '(') depth++
    else if (c === ')') depth = Math.max(0, depth - 1)

    if (c === ',' && depth === 0) {
      // 吃掉逗号后的空格，避免出现「, \n  」
      out += ',\n  '
      while (s[i + 1] === ' ') i++
      continue
    }
    out += c
  }
  return out
}

/**
 * 极简 SQL 格式化：只做换行与缩进，不改动大小写和语义。
 *
 * 为什么不做「完整美化」：完整的 formatter 需要理解方言与嵌套，
 * 而这里的目标只是把一行挤在一起的 SQL 拆成可读的多行。
 * 主关键字前换行、子句内部按逗号缩进，已经能覆盖绝大多数场景；
 * 做不到的部分（子查询括号层级）保持原样，比硬改更安全。
 */
export function formatSql(input: string): string {
  // 先抽走字符串字面量再规整空白。
  //
  // 顺序不能反：先 replace(/\s+/g,' ') 会把 'a  b' 里的两个空格压成一个，
  // 那是**改变查询语义**（值真的变了），不是排版。占位符用 \u0000+序号，
  // 不含空白与逗号，后续的换行规则不会碰它。
  const literals: string[] = []
  let s = input.replace(/'(?:[^']|'')*'/g, (m) => {
    literals.push(m)
    return `\u0000${literals.length - 1}\u0000`
  })

  s = s.replace(/\s+/g, ' ').trim()

  // 长复合关键字先替换成占位符，最后统一还原。
  //
  // 为什么不用「长词排前面」的 replace 顺序解决：`LEFT JOIN` 先替换后，
  // 文本里变成 `\nLEFT JOIN `，接着替换 `JOIN` 时 `\s+JOIN\s+` 依然命中
  // 其中的 `JOIN`，把它再切成一行 `LEFT` 加一行 `JOIN`。顺序挡不住，
  // 只有占位符（内容里不含 JOIN）能真正隔离。
  const compounds: string[] = []
  for (const kw of COMPOUND_KEYWORDS) {
    s = s.replace(new RegExp(`\\s+${kw.replace(/ /g, '\\s+')}\\s+`, 'gi'), () => {
      compounds.push(kw)
      return `\u0001${compounds.length - 1}\u0001`
    })
  }

  for (const kw of SINGLE_KEYWORDS) {
    s = s.replace(new RegExp(`\\s+${kw}\\s+`, 'gi'), `\n${kw} `)
  }

  s = s.replace(/\u0001(\d+)\u0001/g, (_, i) => `\n${compounds[Number(i)]} `)

  // 逗号换行只在括号外生效：`IN (1,2,3)`、函数参数这类列表保持一行，
  // 拆开会把简单的表达式排成多行，反而更难读。
  s = splitTopLevelCommas(s)

  s = s
    .split('\n')
    .map((line) => line.trimEnd())
    .join('\n')

  return s.replace(/\u0000(\d+)\u0000/g, (_, i) => literals[Number(i)])
}

/** 构造默认菜单项。`onRun` 是「执行」按钮的回调，由查询页传入。 */
export function sqlMenuItems(onRun: () => void): SqlMenuItem[] {
  return [
    {
      key: 'run-statement',
      label: '执行当前语句',
      shortcut: 'Ctrl+Enter',
      enabled: () => true,
      // 先选出语句再交给页面的执行逻辑：执行逻辑读的是编辑器里的 sql 状态，
      // 选中会让 pickStatement 只取这一段
      run: (view) => {
        pickStatement(view)
        onRun()
      },
    },
    {
      key: 'format',
      label: '格式化 SQL',
      shortcut: 'Shift+Alt+F',
      enabled: (view) => view.state.doc.length > 0,
      run: (view) => {
        const { from, to } = currentStatement(view)
        const target = from === to ? { from: 0, to: view.state.doc.length } : { from, to }
        const text = view.state.doc.sliceString(target.from, target.to)
        view.dispatch({
          changes: { from: target.from, to: target.to, insert: formatSql(text) },
        })
      },
      separatorBefore: true,
    },
    {
      key: 'select-statement',
      label: '选中当前语句',
      enabled: (view) => currentStatement(view).text.length > 0,
      run: (view) => {
        const { from, to } = currentStatement(view)
        view.dispatch({ selection: EditorSelection.single(from, to) })
      },
    },
    {
      key: 'copy',
      label: '复制',
      shortcut: 'Ctrl+C',
      enabled: (view) => !view.state.selection.main.empty,
      run: (view) => {
        const sel = view.state.selection.main
        const text = view.state.doc.sliceString(sel.from, sel.to)
        void navigator.clipboard?.writeText(text)
      },
    },
    {
      key: 'paste',
      label: '粘贴',
      shortcut: 'Ctrl+V',
      enabled: () => true,
      // 浏览器安全策略不允许脚本读剪贴板后直接写入编辑器时，
      // 退化成聚焦编辑器让用户自己按 Ctrl+V，不静默失败
      run: (view) => {
        view.focus()
        void navigator.clipboard
          ?.readText()
          .then((text) => {
            if (!text) return
            const sel = view.state.selection.main
            view.dispatch({
              changes: { from: sel.from, to: sel.to, insert: text },
              selection: { anchor: sel.from + text.length },
            })
          })
          .catch(() => {
            /* 无法读剪贴板（非 https 或未授权），保持焦点让用户手动粘贴 */
          })
      },
    },
    {
      key: 'find',
      label: '查找 / 替换',
      shortcut: 'Ctrl+F',
      enabled: () => true,
      run: (view) => openSearchPanel(view),
      separatorBefore: true,
    },
    {
      key: 'select-all',
      label: '全选',
      shortcut: 'Ctrl+A',
      enabled: () => true,
      run: (view) => selectAll(view),
    },
    {
      key: 'line-start',
      label: '光标移到行首',
      shortcut: 'Home',
      enabled: () => true,
      run: (view) => cursorLineBoundaryBackward(view),
    },
    {
      key: 'line-end',
      label: '光标移到行尾',
      shortcut: 'End',
      enabled: () => true,
      run: (view) => cursorLineBoundaryForward(view),
    },
  ]
}

/**
 * 挂载右键菜单。返回销毁函数。
 *
 * 菜单节点挂在 document.body 上、position: fixed：编辑器容器有 overflow，
 * 挂在内部会被裁掉一半。打开期间监听一次全局 mousedown 用于点外部关闭。
 */
export function attachContextMenu(view: EditorView, getItems: () => SqlMenuItem[]): () => void {
  const dom = view.dom

  let menu: HTMLDivElement | null = null
  let onOutside: ((e: MouseEvent) => void) | null = null

  const close = () => {
    if (menu) {
      menu.remove()
      menu = null
    }
    if (onOutside) {
      document.removeEventListener('mousedown', onOutside, true)
      onOutside = null
    }
  }

  const open = (x: number, y: number) => {
    close()
    const items = getItems()
    menu = document.createElement('div')
    menu.className = 'dw-sql-menu'
    menu.setAttribute('role', 'menu')

    for (const item of items) {
      if (item.separatorBefore) {
        const hr = document.createElement('div')
        hr.className = 'dw-sql-menu__sep'
        menu.appendChild(hr)
      }
      const enabled = item.enabled(view)
      const row = document.createElement('div')
      row.className = `dw-sql-menu__item${enabled ? '' : ' dw-sql-menu__item--disabled'}`
      row.setAttribute('role', 'menuitem')
      row.textContent = item.label
      if (item.shortcut) {
        const kbd = document.createElement('span')
        kbd.className = 'dw-sql-menu__kbd'
        kbd.textContent = item.shortcut
        row.appendChild(kbd)
      }
      if (enabled) {
        row.addEventListener('mousedown', (e) => {
          e.preventDefault()
          e.stopPropagation()
          close()
          item.run(view)
        })
      }
      menu.appendChild(row)
    }

    document.body.appendChild(menu)

    // 贴边修正：菜单宽高已知后才能判断会不会溢出视口
    const rect = menu.getBoundingClientRect()
    const left = Math.min(x, window.innerWidth - rect.width - 8)
    const top = y + rect.height > window.innerHeight ? y - rect.height : y
    menu.style.left = `${Math.max(8, left)}px`
    menu.style.top = `${Math.max(8, top)}px`

    onOutside = (e: MouseEvent) => {
      if (menu && !menu.contains(e.target as Node)) close()
    }
    // capture 阶段监听：编辑器自己会 stopPropagation，冒泡阶段收不到
    document.addEventListener('mousedown', onOutside, true)
  }

  const onContextMenu = (e: MouseEvent) => {
    e.preventDefault()
    view.focus()
    open(e.clientX, e.clientY)
  }

  const onKeyDown = (e: KeyboardEvent) => {
    if (e.key === 'Escape') close()
  }

  dom.addEventListener('contextmenu', onContextMenu)
  document.addEventListener('keydown', onKeyDown)

  return () => {
    close()
    dom.removeEventListener('contextmenu', onContextMenu)
    document.removeEventListener('keydown', onKeyDown)
  }
}

/** 便于测试：把状态里的整段文本按语句切开。 */
export function splitStatements(state: EditorState): string[] {
  return state.doc
    .toString()
    .split(';')
    .map((s) => s.trim())
    .filter(Boolean)
}
