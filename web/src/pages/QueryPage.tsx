import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  Alert,
  AutoComplete,
  Button,
  Card,
  Empty,
  Input,
  InputNumber,
  message,
  Modal,
  Popover,
  Select,
  Space,
  Spin,
  Table,
  Tabs,
  Tag,
  Tooltip,
  Typography,
} from 'antd'
import {
  CopyOutlined,
  DownloadOutlined,
  PlayCircleOutlined,
  ReloadOutlined,
} from '@ant-design/icons'
import CodeMirror from '@uiw/react-codemirror'
import { Compartment } from '@codemirror/state'
import { EditorView, keymap } from '@codemirror/view'
import { defaultKeymap, historyKeymap } from '@codemirror/commands'
import { sql as sqlLang, MySQL, PostgreSQL, type SQLDialect } from '@codemirror/lang-sql'
import {
  executeMongo,
  executeRedis,
  executeSQL,
  exportQuery,
  getCurrentUser,
  getSchema,
  listDatabases,
  listMyDatasources,
  listTables,
  saveBlob,
} from '@/api/client'
import type { DatasourceBrief, QueryResult, RedisKeyValue, RedisQueryResult } from '@/api/types'
import { buildSqlExtensions, type SchemaMap } from '@/sql/completion'
import { attachContextMenu, formatSql, pickStatement, sqlMenuItems } from '@/sql/menu'

const { Text, Paragraph } = Typography

// Redis 值的展示形态由类型决定，三种承载字段互斥：
//   string        -> value
//   hash          -> fields
//   list/set/zset -> items
// 之前只处理了 fields / value 两支，集合类型的值一律渲染成空串，
// 表现为「list、zset 查出来有 key 但值是空的」。
const REDIS_COLLECTION_TYPES = new Set(['list', 'set', 'zset'])

/** 单次查询在页面上最多展示 / 取回的行数。与 querysvc 的交互上限一致。 */
const QUERY_MAX_ROWS = 1000

/**
 * 导出允许的最大行数，必须与后端 exportMaxRowLimit 保持一致。
 *
 * 两处各写一份常量当然不理想，但跨语言的"共享常量"要么靠构建期生成
 * 类型文件，要么靠接口下发；为一个上限值引入那套机制不值得。
 * 这里用注释钉住对应关系，改动后端时 grep 这个数字能找到前端。
 */
const EXPORT_MAX_ROWS = 20000

/** 表格内每个集合 key 只铺这么多元素做预览，完整内容在弹窗里看。 */
const REDIS_PREVIEW_ITEMS = 3

/**
 * 拆分 zset 元素。后端固定输出 `member:score`，而 member 自身可能含冒号，
 * 因此必须按**最后一个**冒号切分；万一没有冒号就整体当 member。
 */
function splitZSetItem(item: string): { member: string; score?: string } {
  const idx = item.lastIndexOf(':')
  if (idx <= 0) return { member: item }
  return { member: item.slice(0, idx), score: item.slice(idx + 1) }
}

/**
 * Redis 输入的本地校验，查询与导出共用一份。
 *
 * 导出实际上是「重新执行 + 落文件」，允许导出的条件不能比允许查询更松；
 * 两处各写一遍的话，改了其中一处就会出现「查不了却能导」或反过来的空档。
 * 返回 null 表示通过，否则是给用户看的提示。
 */
function validateRedisInput(mode: 'exact' | 'scan', raw: string): string | null {
  if (!raw.trim()) return '请输入 key 或 key 模式'
  if (mode === 'exact' && /[*?[]/.test(raw)) {
    return '精确查询不支持通配符，请切换到「模糊 Key」标签页'
  }
  if (mode === 'scan') {
    if (!/[*?[]/.test(raw)) {
      return '模糊查询需要带通配符（如 user:1001:*），精确 key 请用「精确 Key」'
    }
    const prefix = raw.split(/[*?[]/)[0].replace(/[:.\-_/]/g, '')
    if (prefix.length < 2) {
      return '模糊查询的前缀至少需要 2 个有效字符，否则扫描范围过宽'
    }
  }
  return null
}

/**
 * Redis 值详情弹窗。同一个入口承载两类内容，二者都不适合塞进表格一格：
 *   - 集合（list / set / zset）：元素可能上百条，需要搜索与分页；
 *   - 二进制（bitmap / HyperLogLog / 其他）：十六进制串很长，需要完整视图。
 * 内容形态由 key 自身决定——同一 key 不会既展示集合又展示二进制。
 */
function RedisValueModal({
  open,
  kv,
  onClose,
}: {
  open: boolean
  kv: RedisKeyValue | null
  onClose: () => void
}) {
  const [keyword, setKeyword] = useState('')

  const isCollection = kv ? REDIS_COLLECTION_TYPES.has(kv.type) : false
  const isBinary = kv?.encoding === 'binary' || kv?.encoding === 'hyperloglog'
  const items = useMemo(() => kv?.items ?? [], [kv])
  const isZSet = kv?.type === 'zset'

  // 过滤在弹窗内做。元素量级受后端 maxItemsPerCollection（1000）约束，
  // 前端全量过滤足够，不需要再分页拉取。
  const rows = useMemo(() => {
    const kw = keyword.trim().toLowerCase()
    return items
      .map((item, index) => {
        const { member, score } = isZSet ? splitZSetItem(item) : { member: item, score: undefined }
        return { index, member, score, raw: item }
      })
      .filter((r) => (kw ? r.member.toLowerCase().includes(kw) : true))
  }, [items, keyword, isZSet])

  const allText = rows.map((r) => r.raw).join('\n')

  // 二进制视图的内容与可复制文本
  const hex = kv?.hex ?? ''
  const setBitList = (kv?.set_bits ?? '').split(',').filter(Boolean)

  const title = (
    <Space direction="vertical" size={2} style={{ maxWidth: 620 }}>
      <Space>
        <Tag>{kv?.type}</Tag>
        {isBinary ? (
          <Tag color={kv?.encoding === 'hyperloglog' ? 'purple' : 'orange'}>
            {kv?.encoding === 'hyperloglog' ? 'HyperLogLog' : '二进制'}
          </Tag>
        ) : null}
        <Text strong>
          {isCollection ? `${items.length} 项` : isBinary ? `${kv?.size ?? 0} 字节` : ''}
        </Text>
      </Space>
      <Text code style={{ fontSize: 12, wordBreak: 'break-all' }}>
        {kv?.key}
      </Text>
    </Space>
  )

  return (
    <Modal
      open={open}
      onCancel={onClose}
      width={720}
      title={title}
      footer={[
        <Button
          key="copy"
          icon={<CopyOutlined />}
          onClick={() =>
            void copyText(isBinary ? hex : allText).then(() =>
              message.success(`已复制${isBinary ? '十六进制内容' : '内容'}`),
            )
          }
          disabled={isBinary ? !hex : rows.length === 0}
        >
          {isBinary ? '复制十六进制' : `复制${keyword ? '筛选结果' : '全部'}`}
        </Button>,
        <Button key="close" type="primary" onClick={onClose}>
          关闭
        </Button>,
      ]}
    >
      {isBinary ? (
        <Space direction="vertical" size={12} style={{ width: '100%' }}>
          <Alert
            type={kv?.encoding === 'hyperloglog' ? 'info' : 'warning'}
            showIcon
            message={
              kv?.encoding === 'hyperloglog'
                ? '这个 key 的类型是 string，但内容是一个 HyperLogLog 结构（以 HYLL 开头），不是可读文本。'
                : '这个 key 的类型是 string，但内容是二进制，按文本显示只会得到乱码，因此这里给出十六进制。'
            }
            description={
              kv?.encoding === 'hyperloglog'
                ? 'HyperLogLog 用于基数估算，原始字节无法直接解读。需要估算结果请在应用侧用 PFCOUNT 读取。'
                : '常见来源：SETBIT 位图、序列化后的 protobuf / msgpack、压缩或加密数据。'
            }
          />
          {kv?.bit_count !== undefined && kv.bit_count > 0 && setBitList.length > 0 ? (
            <Space direction="vertical" size={4} style={{ width: '100%' }}>
              <Text style={{ fontSize: 13 }}>
                若这是位图：共 <Text strong>{kv.bit_count}</Text> 个置位
              </Text>
              <Text code style={{ fontSize: 12, wordBreak: 'break-all' }}>
                {setBitList.join(', ')}
                {setBitList.length >= 64 ? ' …' : ''}
              </Text>
            </Space>
          ) : null}
          <div>
            <Text type="secondary" style={{ fontSize: 12 }}>
              十六进制（每字节两位，空格分隔，最多显示前 256 字节）
            </Text>
            <Paragraph
              copyable={false}
              style={{
                marginTop: 6,
                marginBottom: 0,
                maxHeight: 360,
                overflow: 'auto',
                background: '#fafafa',
                border: '1px solid #f0f0f0',
                borderRadius: 4,
                padding: 12,
              }}
            >
              <Text code style={{ fontSize: 12, wordBreak: 'break-all', whiteSpace: 'pre-wrap' }}>
                {hex || '（空内容）'}
              </Text>
            </Paragraph>
          </div>
        </Space>
      ) : (
        <Space direction="vertical" size={12} style={{ width: '100%' }}>
          <Space style={{ width: '100%' }} wrap>
            <Input.Search
              allowClear
              placeholder="筛选元素（支持子串匹配）"
              onChange={(e) => setKeyword(e.target.value)}
              style={{ width: 320 }}
            />
            {keyword ? (
              <Text type="secondary" style={{ fontSize: 12 }}>
                匹配 {rows.length} / {items.length} 项
              </Text>
            ) : null}
            {isZSet ? (
              <Text type="secondary" style={{ fontSize: 12 }}>
                按分数升序排列，元素格式为 member（分数）
              </Text>
            ) : null}
          </Space>
          <Table
            size="small"
            rowKey={(r) => `${r.index}-${r.raw}`}
            dataSource={rows}
            locale={{ emptyText: <Empty description={items.length === 0 ? '空集合' : '没有匹配的元素'} /> }}
            pagination={rows.length > 50 ? { pageSize: 50, showSizeChanger: false } : false}
            scroll={{ y: 420 }}
            columns={[
              {
                title: '#',
                dataIndex: 'index',
                width: 70,
                render: (v: number) => <Text type="secondary">{v}</Text>,
              },
              {
                title: isZSet ? 'member' : '元素',
                dataIndex: 'member',
                ellipsis: true,
                render: (v: string) => (
                  <Space size={4}>
                    <Text code style={{ fontSize: 12 }}>
                      {v}
                    </Text>
                    <Tooltip title="复制">
                      <CopyOutlined
                        style={{ color: '#bfbfbf', cursor: 'pointer', fontSize: 12 }}
                        onClick={() => void copyText(v).then(() => message.success('已复制'))}
                      />
                    </Tooltip>
                  </Space>
                ),
              },
              ...(isZSet
                ? [
                    {
                      title: 'score',
                      dataIndex: 'score',
                      width: 140,
                      render: (v?: string) => <Text type="secondary">{v ?? ''}</Text>,
                    },
                  ]
                : []),
            ]}
          />
        </Space>
      )}
    </Modal>
  )
}

/**
 * 表格里的集合摘要：类型说明 + 少量预览 + 「查看」入口。
 *
 * 「查看」是这里唯一的入口文案——它做的事始终是打开同一个弹窗，
 * 不随元素多少改名（早期按数量分成「查看全部 / 展开」两种叫法，
 * 用户分不清区别，且两者行为本来就一样）。
 */
function RedisCollectionCell({ kv, onOpen }: { kv: RedisKeyValue; onOpen: () => void }) {
  const items = kv.items ?? []
  if (items.length === 0) {
    return <Text type="secondary" style={{ fontSize: 13 }}>（空集合）</Text>
  }
  const preview = items.slice(0, REDIS_PREVIEW_ITEMS)
  return (
    <Space size={4} wrap>
      <Tag color="default" style={{ marginInlineEnd: 0 }}>
        {items.length} 项
      </Tag>
      {preview.map((item, i) => {
        const raw = kv.type === 'zset' ? splitZSetItem(item).member : item
        // 预览只截断显示，完整值仍在弹窗里（弹窗按列展示且可复制）
        const label = raw.length > 28 ? `${raw.slice(0, 28)}…` : raw
        return (
          <Text key={i} code style={{ fontSize: 12 }} title={raw}>
            {label}
          </Text>
        )
      })}
      <Button type="link" size="small" style={{ padding: 0, height: 'auto' }} onClick={onOpen}>
        查看
      </Button>
    </Space>
  )
}

/** 表格里的二进制摘要：标明形态 + 字节数 + 十六进制开头，详细内容看弹窗。 */
function RedisBinaryCell({ kv, onOpen }: { kv: RedisKeyValue; onOpen: () => void }) {
  const isHLL = kv.encoding === 'hyperloglog'
  const head = (kv.hex ?? '').split(' ').slice(0, 8).join(' ')
  return (
    <Space size={4} wrap>
      <Tag color={isHLL ? 'purple' : 'orange'} style={{ marginInlineEnd: 0 }}>
        {isHLL ? 'HyperLogLog' : '二进制'}
      </Tag>
      <Text type="secondary" style={{ fontSize: 12 }}>
        {kv.size ?? 0} 字节
      </Text>
      {head ? (
        <Text code style={{ fontSize: 12 }} title="十六进制预览">
          {head}
          {(kv.hex ?? '').split(' ').length > 8 || (kv.size ?? 0) > 256 ? ' …' : ''}
        </Text>
      ) : null}
      <Button type="link" size="small" style={{ padding: 0, height: 'auto' }} onClick={onOpen}>
        查看
      </Button>
    </Space>
  )
}

function renderRedisValue(kv: RedisKeyValue, onOpen: (kv: RedisKeyValue) => void) {
  if (kv.fields) {
    return (
      <Text style={{ fontSize: 13 }}>
        {Object.entries(kv.fields)
          .map(([k, val]) => `${k}=${val}`)
          .join(', ')}
      </Text>
    )
  }
  if (REDIS_COLLECTION_TYPES.has(kv.type)) {
    return <RedisCollectionCell kv={kv} onOpen={() => onOpen(kv)} />
  }
  if (kv.type === 'none') {
    return <Text type="secondary" style={{ fontSize: 13 }}>（key 不存在）</Text>
  }
  // string 但内容不是文本：bitmap / HyperLogLog / 其他二进制
  if (kv.encoding === 'binary' || kv.encoding === 'hyperloglog') {
    return <RedisBinaryCell kv={kv} onOpen={() => onOpen(kv)} />
  }
  return <Text style={{ fontSize: 13 }}>{kv.value ?? ''}</Text>
}

/** 管理角色不需要授权即可查询全部数据源（与后端 model.Role.IsPrivileged 对应）。 */
const PRIVILEGED_ROLES = ['dba', 'superadmin']

const isPrivileged = () =>
  PRIVILEGED_ROLES.includes(getCurrentUser()?.role ?? '')

/**
 * 写入剪贴板。navigator.clipboard 只在安全上下文（https / localhost）可用，
 * 内网 http 部署时会缺席，退回 execCommand 保证复制按钮不至于点了没反应。
 */
async function copyText(text: string) {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
    } else {
      const ta = document.createElement('textarea')
      ta.value = text
      ta.style.position = 'fixed'
      ta.style.opacity = '0'
      document.body.appendChild(ta)
      ta.select()
      document.execCommand('copy')
      document.body.removeChild(ta)
    }
    message.success(`已复制 ${text}`)
  } catch {
    message.warning('浏览器不允许自动复制，请手动选中复制')
  }
}

/**
 * 把查询结果补成前端可安全渲染的形状。
 *
 * 为什么需要：Go 里零行的切片是 nil，序列化出来是 `"rows": null` 而不是 `[]`。
 * 表格对 rows / columns 调 .map，拿到 null 会抛 TypeError 导致整页白屏
 * （React 没有 error boundary，一次空结果就把查询页打挂）。
 * 后端已让空结果返回 []，这里再做一层兜底——前端不能指望接口永远守约。
 */
function normalizeResult(r: QueryResult): QueryResult {
  return {
    columns: r.columns ?? [],
    rows: r.rows ?? [],
    row_count: r.row_count ?? 0,
    truncated: r.truncated ?? false,
    elapsed_ms: r.elapsed_ms ?? 0,
  }
}

/**
 * 编辑器里「表名 → 字段」的来源是整库 schema（见 schemaMap）；
 * 表清单（tableOptions）只用于展示与复制表名，与补全无关。
 */
export default function QueryPage() {
  const [datasources, setDatasources] = useState<DatasourceBrief[]>([])
  const [datasourceId, setDatasourceId] = useState<number>()
  // 管理角色：不需要授权，任何数据源的所有库表都能查。
  // 这个标记只影响「下拉怎么加载」和「执行前要不要强制选表」，
  // 真正的权限判定一律在后端——前端放行不等于能查到数据。
  const [noGrantNeeded] = useState(isPrivileged)

  // 选中表名的筛选关键字（只在表名清单的 Popover 内过滤，不请求后端）
  const [tableKeyword, setTableKeyword] = useState('')

  // 库名在 SQL 下是库/模式名，在 Mongo 下是库名，在 Redis 下是 db 号
  const [database, setDatabase] = useState('')
  // 注意：这里**没有**「已选表」状态。
  // 表清单（tableOptions）纯粹是给用户看「这个库里有哪些表」并复制的，
  // 既不参与鉴权（粒度是数据源 + 库），也不再喂给编辑器补全
  // （题注 `别名.` 的字段来自 schemaMap 的整库结构）。

  const [sql, setSql] = useState('')
  const [rowLimit, setRowLimit] = useState(100)

  /**
   * 查询请求实际带的行数上限。
   *
   * 与 rowLimit 分开：rowLimit 是输入框里的值，有导出权限时可以调到 20000，
   * 那是给导出用的；而页面上的查询结果最多翻 50 行/页，取 20000 行
   * 只是让后端多扫内存、多传几十 MB，对用户毫无用处。
   * 这里封顶到 QUERY_MAX_ROWS，保证"查询看结果"始终轻量。
   */
  const queryRowLimit = Math.min(rowLimit, QUERY_MAX_ROWS)
  const [loading, setLoading] = useState(false)
  // 导出单独一个 loading：它比查询慢（要重新执行 + 生成文件），
  // 复用查询的 loading 会让「执行」按钮在导出期间也跟着转。
  const [exporting, setExporting] = useState(false)
  const [result, setResult] = useState<QueryResult | null>(null)

  const [redisMode, setRedisMode] = useState<'exact' | 'scan'>('exact')
  const [redisKey, setRedisKey] = useState('')
  const [redisResult, setRedisResult] = useState<RedisQueryResult | null>(null)
  // Redis 值详情弹窗：集合（list/set/zset）与二进制（bitmap/HLL）共用，
  // 存被点开的那一条，null 表示关闭
  const [redisDetail, setRedisDetail] = useState<RedisKeyValue | null>(null)

  // Mongo 两种输入模式：集合+过滤条件 / 原生命令
  const [mongoMode, setMongoMode] = useState<'collection' | 'command'>('collection')
  const [mongoCollection, setMongoCollection] = useState('')
  const [mongoFilter, setMongoFilter] = useState('{}')
  const [mongoCommand, setMongoCommand] = useState('')

  // 元数据：库列表、表列表、字段缓存
  const [dbOptions, setDbOptions] = useState<string[]>([])
  const [dbLoading, setDbLoading] = useState(false)
  // 元数据接口的真故障信息（502）。空串表示正常或「确实没有数据」。
  const [dbError, setDbError] = useState('')
  const [tableOptions, setTableOptions] = useState<string[]>([])
  const [tableLoading, setTableLoading] = useState(false)
  const [tableError, setTableError] = useState('')
  // 整库表结构（表名 → 字段）。SQL 编辑器靠它把 `别名.` 解析成具体表的字段：
  // 逐表请求跟不上打字，也不能在用户改别名时重新请求，所以一次取全库缓存。
  // 与右侧的表清单（tableOptions）是两份独立数据：前者供编辑器补全，
  // 后者只是给用户看的表名速查，互不依赖。
  const [schemaMap, setSchemaMap] = useState<SchemaMap>({})
  // 库列表请求的序号，用于丢弃过期响应（见 loadDatabases）
  const dbReqSeq = useRef(0)
  // schema 请求序号，同样用于丢弃过期响应（切库时前一个库的响应可能后到）
  const schemaReqSeq = useRef(0)

  // 编辑器实例与「随状态变化需要重新配置」的 compartment。
  // 补全要用到最新的 schema，而 EditorView 是长期的——用 Compartment 热替换，
  // 比销毁重建编辑器好：后者会丢光标、丢滚动位置，打字中途尤其明显。
  const editorRef = useRef<EditorView | null>(null)
  const langCompartment = useRef(new Compartment())
  const completionCompartment = useRef(new Compartment())
  // 右键菜单的销毁函数，重建编辑器时要先解绑，否则旧节点的事件会泄漏
  const detachMenu = useRef<(() => void) | null>(null)
  // 供右键菜单里的「执行当前语句」调用的最新 run。
  // 菜单是持久化的 DOM 监听，闭包捕获的 run 会过期（它依赖 loading / sql 等状态）。
  const runRef = useRef<() => Promise<void>>(async () => {})
  // 供 reload 回调读取的最新值（闭包会捕获旧值）
  const schemaRef = useRef<SchemaMap>({})
  schemaRef.current = schemaMap
  const dialectRef = useRef<SQLDialect>(PostgreSQL)

  const current = datasources.find((d) => d.id === datasourceId)
  const isRedis = current?.type === 'redis'
  const isMongo = current?.type === 'mongodb'
  const isSQL = !isRedis && !isMongo

  // 表清单按关键字过滤（纯前端，不动后端请求）
  const filteredTables = useMemo(() => {
    const kw = tableKeyword.trim().toLowerCase()
    if (!kw) return tableOptions
    return tableOptions.filter((t) => t.toLowerCase().includes(kw))
  }, [tableOptions, tableKeyword])

  // 方言由数据源类型决定。写进 ref 让补全来源始终读到最新值：
  // EditorView 是长期的，闭包捕获的 current 会停留在首次渲染的那份。
  dialectRef.current = current?.type === 'mysql' ? MySQL : PostgreSQL

  useEffect(() => {
    void (async () => {
      try {
        // 用 listMyDatasources 而不是 listDatasources：
        // 后者走 GET /datasources，路由限 DBA，开发者调用会 403，
        // 下拉变成空、什么都查不了。这里返回的是「我拿到授权的数据源」；
        // 对 dba / superadmin 后端直接返回全量清单（他们不需要授权）。
        //
        // 需要留意的是：这个接口对开发者只返回「有授权」的数据源，
        // 所以下拉为空意味着「一条生效授权都没有」，不是接口故障。
        // 下面用空态提示把这句话说清楚——之前空下拉静默无提示，
        // 用户只会以为功能坏了。
        const list = await listMyDatasources()
        setDatasources(list)
        // 优先选 SQL 类型，没有就选第一个可查的。
        //
        // 这里只负责「选出默认数据源」，不负责拉库列表——
        // 拉列表统一由下面监听 current 的 effect 做。
        // 若在这里也拉一次，两个 effect 会双请求同一份数据，
        // 而后者返回顺序不确定，可能把先到的结果覆盖掉。
        const firstSql = list.find(
          (d) => d.type === 'postgresql' || d.type === 'mysql',
        )
        const chosen = firstSql ?? list[0]
        if (chosen) setDatasourceId(chosen.id)
      } catch (e) {
        message.error((e as Error).message)
      }
    })()
  }, [])

  // 数据源变化的唯一处理入口：清状态 + 拉库列表。
  //
  // 依赖 [current] 而不是 [datasourceId]：
  //   - datasourceId 先设置、datasources 后到位的那一帧，用 id 去
  //     datasources.find() 拿到 undefined，默认库预填必然失效
  //   - current = datasources.find(...) 返回的是数组内元素的**同一引用**，
  //     所以 [current] 不会每帧触发，只在 datasources / datasourceId
  //     真正变化时触发
  //
  // 不设「首次跳过」门闩：那种早退写在清空之后，
  // 会让首次的 setDatabase('') 覆盖掉刚预填的默认库且无人重设，
  // 是否生效变成时序巧合。首次加载与手动切换走完全相同的一条路径。
  useEffect(() => {
    setDatabase('')
    setSql('')
    setTableOptions([])
    setTableKeyword('')
    // 整库表结构也要清：留着上一个数据源的 schema 会让编辑器提示
    // 一堆根本不在当前库里的表，比没有提示更误导
    setSchemaMap({})
    // 序号自增让在途请求的结果被丢弃（否则切数据源后被旧响应覆盖）
    schemaReqSeq.current += 1
    setResult(null)
    setRedisResult(null)
    setRedisKey('')
    setMongoCollection('')
    setMongoFilter('{}')
    setMongoCommand('')

    if (!current || current.type === 'redis') {
      setDbOptions([])
      return
    }
    void loadDatabases(current)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [current])

  // loadDatabases 接收数据源对象而不是只接 id。
  //
  // 为什么：要在里面读 ds.database 作为默认库。如果只传 id 再去
  // datasources 里 find，首次加载时 datasources 还是空数组
  // （setDatasources 与 setDatasourceId 在同一批更新里，effect 拿到的是旧闭包），
  // 默认库必然取不到。传对象则调用方一定持有它，逻辑上不可能为空。
  const loadDatabases = async (ds: DatasourceBrief) => {
    // 用自增序号丢弃过期响应：用户快速切换数据源时，
    // 前一个请求可能后到，把新数据源的库列表覆盖掉。
    // 不做这层保护的现象是「切到 A 显示 B 的库」，而且无从复现。
    const seq = ++dbReqSeq.current
    setDbLoading(true)
    try {
      const list = await listDatabases(ds.id)
      if (seq !== dbReqSeq.current) return // 已有更新的请求，丢弃
      setDbOptions(list)
      setDbError('')
      // 数据源配置里有默认库时优先用它；否则只在唯一候选时自动选中
      let picked = ''
      if (ds.database && list.includes(ds.database)) {
        picked = ds.database
      } else if (list.length === 1) {
        picked = list[0]
      }
      if (picked) {
        setDatabase(picked)
        // 预填默认库时也要拉表结构与表清单：这条路径不经过 onDatabaseChange，
        // 少了这一步首次打开页面时表清单为空、编辑器也没有字段提示
        void loadTables(ds.id, picked)
        void loadSchema(ds.id, picked)
      }
    } catch (e) {
      if (seq !== dbReqSeq.current) return
      // 后端对「列不出库」已经区分了语义：
      //   - 200 + 空列表 = 确实没有可见的库（无授权），静默降级为手填是合理的
      //   - 502 = 真故障（连不上、配置错）
      // 这里收到的是异常，说明是后者，必须告诉用户，
      // 否则「下拉空着」会被误认为「没有权限」，排查方向直接跑偏。
      setDbOptions([])
      setDbError((e as Error).message)
    } finally {
      if (seq === dbReqSeq.current) setDbLoading(false)
    }
  }

  const loadTables = useCallback(
    async (id: number, db: string) => {
      if (!id || !db || isRedis) {
        setTableOptions([])
        return
      }
      setTableLoading(true)
      try {
        setTableOptions(await listTables(id, db))
        setTableError('')
      } catch (e) {
        setTableOptions([])
        // 与库列表同理：走到这里说明是故障而不是「这个库没表」
        setTableError((e as Error).message)
      } finally {
        setTableLoading(false)
      }
    },
    [isRedis],
  )

  // 库变了就重拉表清单与表结构
  const onDatabaseChange = (db: string) => {
    setDatabase(db)
    setTableKeyword('')
    // 立刻清空而不是等新 schema 回来：过渡期里旧库的表名会误导补全
    setSchemaMap({})
    if (datasourceId) {
      void loadTables(datasourceId, db)
      void loadSchema(datasourceId, db)
    }
  }

  /**
   * 拉取整库表结构。编辑器解析 `别名.` 时，别名指向哪张表只有解析完
   * 当前语句才知道，逐表请求既猜不准也跟不上输入，所以一次拿全库。
   *
   * 失败不弹提示：补全拿不到不影响查询，弹窗反而打断输入。
   * 用序号丢弃过期响应——快速切库时前一个库的响应可能后到，
   * 覆盖掉新库的结构会给出「看起来对但字段全错」的提示，比不提示糟得多。
   */
  const loadSchema = useCallback(async (id: number, db: string) => {
    const seq = ++schemaReqSeq.current
    try {
      const items = await getSchema(id, db)
      if (seq !== schemaReqSeq.current) return
      const map: SchemaMap = {}
      for (const t of items) {
        map[t.table] = t.columns
        // PostgreSQL 的「库」是 schema，SQL 里常写 schema.table，
        // 两套键都放一份，补全与时才能在两种写法下都命中
        map[`${db}.${t.table}`] = t.columns
      }
      setSchemaMap(map)
    } catch {
      if (seq !== schemaReqSeq.current) return
      setSchemaMap({})
    }
  }, [])

  /** 取编辑器里光标所在的语句；编辑器不存在时返回空串（退回整篇文本）。 */
  const pickStatementText = () => {
    const view = editorRef.current
    if (!view) return ''
    return pickStatement(view)
  }

  /** 格式化编辑器里的当前语句（没有选中也没有语句内容时格式化整篇）。 */
  const formatEditorFromView = (view: EditorView) => {
    const sel = view.state.selection.main
    const from = sel.empty ? 0 : sel.from
    const to = sel.empty ? view.state.doc.length : sel.to
    const text = view.state.doc.sliceString(from, to)
    if (!text.trim()) return
    view.dispatch({ changes: { from, to, insert: formatSql(text) } })
    view.focus()
  }

  /** 按钮版格式化：拿不到编辑器时退化为改 state（受控值同步）。 */
  const formatEditor = () => {
    const view = editorRef.current
    if (view) {
      formatEditorFromView(view)
      return
    }
    const next = formatSql(sql)
    setSql(next)
  }

  const run = async () => {
    if (!datasourceId) {
      message.warning('请先选择数据源')
      return
    }
    // 编辑器里有多条语句时只执行光标所在的那一条——
    // 后端只接受单语句（多语句一律拒绝），整篇丢过去必然报错。
    // 这也与 Navicat「Ctrl+Enter 执行当前语句」的行为一致。
    const effectiveSql = isSQL ? (pickStatementText() || sql) : sql
    if (isMongo) {
      if (!database.trim()) {
        message.warning('请选择库')
        return
      }
      if (mongoMode === 'collection') {
        if (!mongoCollection.trim()) {
          message.warning('请选择或填写集合名')
          return
        }
        if (!mongoFilter.trim().startsWith('{')) {
          message.warning('过滤条件必须是 JSON 对象，例如 {"status":"active"}')
          return
        }
      } else if (!mongoCommand.trim()) {
        message.warning('请输入 MongoDB 命令，例如 db.users.find({"a":1})')
        return
      }
    } else if (isSQL) {
      if (!database.trim()) {
        message.warning('请选择库')
        return
      }
      // 不再要求选表：授权粒度是「数据源 + 库」，有该库的权限就能查库里
      // 任何表。表下拉只是可选的补全辅助，留空合法。
      if (!effectiveSql.trim()) {
        message.warning('请输入 SQL')
        return
      }
    } else {
      // 到这里的只会是 Redis（isMongo / isSQL 已在前面分支处理）
      const redisError = validateRedisInput(redisMode, redisKey)
      if (redisError) {
        message.warning(redisError)
        return
      }
    }

    setLoading(true)
    setResult(null)
    setRedisResult(null)
    // 关掉可能还开着的值详情弹窗：它指向的是上一次查询的结果
    setRedisDetail(null)
    try {
      if (isRedis) {
        // 带上限：与导出用同一个「限 N 行」，页面上看到多少个 key、
        // 导出就是多少个（后端 Redis 服务另有扫描上限，超出会被截断）。
        const base = { datasource_id: datasourceId, row_limit: queryRowLimit }
        const payload =
          redisMode === 'exact'
            ? { ...base, key: redisKey.trim() }
            : { ...base, pattern: redisKey.trim() }
        setRedisResult(await executeRedis(payload))
        return
      }
      if (isMongo) {
        const payload =
          mongoMode === 'collection'
            ? {
                datasource_id: datasourceId,
                database: database.trim(),
                collection: mongoCollection.trim(),
                filter: mongoFilter.trim(),
                // 查询用 queryRowLimit，不用导出用的 rowLimit：
                // 页面表格翻页也只显示 50 行，取 20000 行纯属浪费后端
                // 内存与传输。两个上限分开，用户调大"限 N 行"是为了导出全量。
                row_limit: queryRowLimit,
              }
            : {
                datasource_id: datasourceId,
                database: database.trim(),
                command: mongoCommand.trim(),
                row_limit: queryRowLimit,
              }
        setResult(normalizeResult(await executeMongo(payload)))
        return
      }
      setResult(
        normalizeResult(
          await executeSQL({
            datasource_id: datasourceId,
            database: database.trim(),
            // 不再提交 tables：表清单只是展示与复制用，本身不参与鉴权，
            // 而编辑器补全也不再依赖它。审计里的 tables 保持缺省，
            // 「这次查了哪些表」以 SQL 本身为准（已随审计一起落库）。
            sql: effectiveSql,
            row_limit: queryRowLimit,
          }),
        ),
      )
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setLoading(false)
    }
  }

  // 让右键菜单的「执行当前语句」始终调用最新的 run（它依赖 loading / sql 等状态，
  // 直接闭包捕获会拿到首帧的旧值）
  runRef.current = run

  /**
   * 当前数据源是否允许导出。
   *
   * 判定粒度是**数据源**，不是人：后端在 /grants/mine 里为每个数据源
   * 单独算好 can_export（多条授权取「或」，管理角色恒为 true）。
   * 于是「A 数据源批了导出、B 数据源没批」时，切到 B 按钮就消失，
   * 不需要用户在点击后才收到一个 403。
   */
  const canExport = current?.can_export === true

  /**
   * 把当前查询结果导出成 Excel。SQL / Mongo / Redis 三种数据源都支持。
   *
   * 为什么重新发一次请求而不是把页面上这一份表格转成 CSV：
   *   - 页面上的数据是脱敏、限行、且只渲染了当前分页的；
   *   - 后端按同一份凭证重新执行同一条语句，导出的内容可复现，
   *     审计里也能对上「他导走的到底是什么」。
   *
   * 参数与 run() 里发的那次完全一致：用户看到什么就导出什么。
   * 行数上限交给后端（默认 5000，可在输入框里给到 20000），
   * 前端只负责把用户填的值传过去。
   */
  const doExport = async () => {
    if (!datasourceId) {
      message.warning('请先选择数据源')
      return
    }
    // 不在这里用 canExport 提前拦截。
    //
    // canExport 来自页面加载时的那份 /grants/mine 快照，可能已经陈旧：
    // DBA 刚批了导出而用户没刷新页面时它是 false，本地拦截会把本来
    // 该成功的导出挡掉；反过来快照说 true、授权刚被收紧时，
    // 拦截也拦不住（那种情况只有后端知道）。两头的正确答案都在后端。
    //
    // 所以交给 /query/export 判定：它是权威源，且改完即时生效。
    // 被拒时会带回明确文案（"没批导出" 还是 "该库没有查询权限"），
    // 比这里一句笼统的提示更有指导性。按钮本身的显隐仍按 canExport，
    // 那是为了"没批导出时不要显出一个点了必然失败的按钮"。

    // 校验与 run() 保持一致：导出实际上就是"执行 + 落文件"，
    // 允许导出的条件不能比允许查询更松。
    const effectiveSql = isSQL ? (pickStatementText() || sql) : sql
    let payload: Parameters<typeof exportQuery>[0]
    if (isRedis) {
      // 与 run() 共用同一份校验：能导出的前提是这条输入本身能查。
      const redisError = validateRedisInput(redisMode, redisKey)
      if (redisError) {
        message.warning(redisError)
        return
      }
      const subject = redisKey.trim()
      // 文件名带的是 key / 模式本身，模式里的 * 会被后端清洗成下划线。
      const filename = `${current?.name ?? 'redis'}_${subject}`
      payload =
        redisMode === 'exact'
          ? { datasource_id: datasourceId, type: 'redis', key: subject, row_limit: rowLimit, filename }
          : { datasource_id: datasourceId, type: 'redis', pattern: subject, row_limit: rowLimit, filename }
    } else if (isMongo) {
      if (!database.trim()) {
        message.warning('请选择库')
        return
      }
      if (mongoMode === 'collection') {
        if (!mongoCollection.trim()) {
          message.warning('请选择或填写集合名')
          return
        }
        if (!mongoFilter.trim().startsWith('{')) {
          message.warning('过滤条件必须是 JSON 对象，例如 {"status":"active"}')
          return
        }
        payload = {
          datasource_id: datasourceId,
          type: 'mongo',
          database: database.trim(),
          collection: mongoCollection.trim(),
          filter: mongoFilter.trim(),
          row_limit: rowLimit,
          filename: `${current?.name ?? 'mongo'}_${mongoCollection.trim()}`,
        }
      } else {
        if (!mongoCommand.trim()) {
          message.warning('请输入 MongoDB 命令')
          return
        }
        payload = {
          datasource_id: datasourceId,
          type: 'mongo',
          database: database.trim(),
          command: mongoCommand.trim(),
          row_limit: rowLimit,
          filename: `${current?.name ?? 'mongo'}_导出`,
        }
      }
    } else {
      if (!database.trim()) {
        message.warning('请选择库')
        return
      }
      if (!effectiveSql.trim()) {
        message.warning('请输入 SQL')
        return
      }
      payload = {
        datasource_id: datasourceId,
        type: 'sql',
        database: database.trim(),
        sql: effectiveSql,
        row_limit: rowLimit,
        filename: `${current?.name ?? 'query'}_${database.trim()}`,
      }
    }

    setExporting(true)
    try {
      const { blob, filename, truncated } = await exportQuery(payload)
      saveBlob(blob, filename)
      if (truncated) {
        // 截断必须说清楚：用户拿到文件时看不到「还有更多行」，
        // 不说就可能拿一份不完整的数据去做结论。
        message.warning(`已导出（结果达到行数上限被截断，可调大「限 N 行」后重导）：${filename}`)
      } else {
        message.success(`已导出：${filename}`)
      }
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setExporting(false)
    }
  }

  // 编辑器扩展只构造一次：schema 与方言都通过 ref 读取实时值，
  // 避免每次 schemaMap 变化就重建扩展（会重置补全面板与选区）。
  const extensions = useMemo(
    () => [
      // 语言支持随方言切换（MySQL / PostgreSQL 的关键字与标识符引号不同），
      // 放在 Compartment 里由下面的 effect 热替换
      langCompartment.current.of(sqlLang({ dialect: dialectRef.current, upperCaseKeywords: true })),
      // 补全 + 悬停提示（自定义来源，见 sql/completion.ts）
      completionCompartment.current.of(
        buildSqlExtensions({
          getSchema: () => schemaRef.current,
          getDialect: () => dialectRef.current,
        }),
      ),
      // 快捷键：Ctrl/Cmd+Enter 执行、Shift+Alt+F 格式化、Ctrl+Space 唤出补全
      //（Ctrl+Space 已由补全扩展自带，这里不重复绑定）
      keymap.of([
        {
          key: 'Mod-Enter',
          run: () => {
            void runRef.current()
            return true
          },
        },
        {
          key: 'Shift-Alt-f',
          run: (view) => {
            formatEditorFromView(view)
            return true
          },
        },
        // 编辑器内建按键（移动、选中、撤销等）。basicSetup 已关，这里显式带上，
        // 不然后续覆盖 keymap 时会把 Ctrl+Z 之类的默认行为一起丢掉。
        ...defaultKeymap,
        ...historyKeymap,
      ]),
      EditorView.contentAttributes.of({ 'aria-label': 'SQL 编辑器' }),
    ],
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [],
  )

  // 数据源类型变化时热替换方言与补全配置。
  // 不销毁编辑器：重建会丢光标、丢滚动位置，切换数据源后用户往往还要接着改 SQL。
  useEffect(() => {
    const view = editorRef.current
    if (!view) return
    view.dispatch({
      effects: [
        langCompartment.current.reconfigure(
          sqlLang({ dialect: dialectRef.current, upperCaseKeywords: true }),
        ),
        completionCompartment.current.reconfigure(
          buildSqlExtensions({
            getSchema: () => schemaRef.current,
            getDialect: () => dialectRef.current,
          }),
        ),
      ],
    })
  }, [current?.type])

  // schema 到位后强制刷新一次补全候选：补全来源虽然是惰性的（每次按键都读 ref），
  // 但编辑器在 schema 为空时不会缓存任何内容，这里只是让已打开的补全面板及时更新。
  useEffect(() => {
    const view = editorRef.current
    if (!view) return
    // 触发一次 reconfigure，让 autocompletion 的缓存失效
    view.dispatch({
      effects: completionCompartment.current.reconfigure(
        buildSqlExtensions({
          getSchema: () => schemaRef.current,
          getDialect: () => dialectRef.current,
        }),
      ),
    })
  }, [schemaMap])

  // Redis 结果：key / 类型 / 值 / TTL
  const redisTable = redisResult ? (
    <Space direction="vertical" style={{ width: '100%' }}>
      <Space wrap>
        <Text type="secondary">
          返回 {redisResult.items.length} 个 key，耗时 {redisResult.elapsed_ms} ms
        </Text>
        <Tag color={redisResult.exact ? 'blue' : 'default'}>
          {redisResult.exact ? '精确查询' : 'SCAN 模糊查询'}
        </Tag>
        {redisResult.truncated ? <Tag color="warning">结果已截断</Tag> : null}
      </Space>
      <Table
        size="small"
        rowKey={(r) => r.key}
        dataSource={redisResult.items}
        locale={{ emptyText: '没有匹配的 key' }}
        columns={[
          {
            title: 'Key',
            dataIndex: 'key',
            width: 260,
            ellipsis: true,
            render: (v: string) => <Text code>{v}</Text>,
          },
          {
            title: '类型',
            dataIndex: 'type',
            width: 100,
            render: (v: string) => <Tag>{v}</Tag>,
          },
          {
            title: '值',
            // 不加 ellipsis：这一列里放的是「N 项 / 预览 / 查看」组合控件，
            // 裁切会把按钮吃掉。元素本身已在预览里限长，不会撑爆列宽。
            width: 420,
            render: (_, r) => renderRedisValue(r, setRedisDetail),
          },
          {
            title: 'TTL',
            dataIndex: 'ttl',
            width: 100,
            render: (v: number) =>
              v > 0 ? <Text type="secondary">{v}s</Text> : <Text type="secondary">永久</Text>,
          },
        ]}
        scroll={{ x: 'max-content', y: 420 }}
        pagination={{ pageSize: 50, showSizeChanger: false }}
      />
      <Text type="secondary" style={{ fontSize: 12 }}>
        提示：Redis 结果不做脱敏。bitmap、HyperLogLog 等二进制内容的类型也是 string，
        这类 key 会标注形态并以十六进制展示，而不是当作文本渲染。
      </Text>
      <RedisValueModal
        open={redisDetail !== null}
        kv={redisDetail}
        onClose={() => setRedisDetail(null)}
      />
    </Space>
  ) : null

  const resultTable = result ? (
    <Space direction="vertical" style={{ width: '100%' }}>
      <Space>
        <Text type="secondary">
          返回 {result.row_count} 行，耗时 {result.elapsed_ms} ms
        </Text>
        {result.truncated ? <Tag color="warning">结果已截断</Tag> : null}
      </Space>
      <Table
        size="small"
        rowKey={(_, i) => String(i)}
        dataSource={result.rows.map((row, i) => ({ __idx: i, row }))}
        columns={result.columns.map((col, ci) => ({
          // key 用下标而不是列名：联表查询（SELECT * FROM a JOIN b）里
          // 同名列完全合法，用列名做 key 会让 antd 把两列当成同一个，
          // 表现为其中一列渲染出错或串值。后端已对重名列加位置前缀，
          // 这里再用下标兜一层，任何情况下都不会撞。
          key: ci,
          title: col,
          dataIndex: ['row', ci],
          ellipsis: true,
          render: (v: unknown) =>
            v === null || v === undefined ? (
              <Text type="secondary">NULL</Text>
            ) : (
              String(v)
            ),
        }))}
        scroll={{ x: 'max-content', y: 420 }}
        pagination={{ pageSize: 50, showSizeChanger: false }}
      />
    </Space>
  ) : null

  /**
   * 「导出 Excel」按钮。
   *
   * 只在当前数据源批了导出时出现——没批的时候不显示（而不是显示成禁用），
   * 因为这个按钮代表的是"这个数据源没有这个能力"，而不是"暂时不可用"；
   * 灰按钮会让人反复去看权限，而权限根本没给这个数据源。
   * 管理角色免授权，后端恒返回 can_export=true，所以他们总能看到。
   */
  const exportButton = () => {
    if (!canExport) return null
    // Redis 与 SQL 一样要求先查出结果：导出的是「你看到的这一份」重新执行
    // 后的结果，还没查过就导，用户不知道自己会导走什么。Mongo 的命令行模式
    // 不产生页面表格，所以对它不设这个门槛。
    const hasResult = isRedis ? redisResult !== null : result !== null || isMongo
    return (
      <Tooltip
        title={
          isRedis
            ? '按当前 key / key 模式重新查询并导出为 Excel；hash / list / set / zset 逐元素展开成多行'
            : '按当前查询条件重新执行并导出为 Excel（受「限 N 行」约束）'
        }
      >
        <Button
          icon={<DownloadOutlined />}
          onClick={() => void doExport()}
          loading={exporting}
          disabled={!hasResult}
        >
          导出 Excel
        </Button>
      </Tooltip>
    )
  }

  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      <Card title="数据查询">
        {/* 管理角色的提示放在规则最前面：他们看到的下拉是全量数据源，
            跟开发者看到的不是一回事。不说明的话，DBA 会以为
            「没申请过授权的数据源怎么也能选」——看着像权限泄漏。 */}
        {noGrantNeeded ? (
          <Alert
            type="success"
            showIcon
            style={{ marginBottom: 16 }}
            message="管理角色：免授权查询"
            description={
              <span>
                当前账号（{getCurrentUser()?.role}）不需要授权即可查询全部数据源的所有库表，
                因此下拉里是完整数据源清单。所有查询仍会记入审计日志。
              </span>
            }
          />
        ) : null}
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 16 }}
          message="查询规则"
          description={
            isMongo ? (
              <span>
                {noGrantNeeded
                  ? '管理角色免授权，任意集合都可查询。'
                  : '授权范围按库判定：有该库的权限即可查询库下任意集合。'}
                只允许查询操作，
                写操作与会写库的聚合阶段（$out、$merge）一律拦截。结果中的手机号、身份证、邮箱等字段会自动脱敏。
                {canExport
                  ? ' 当前数据源已批导出权限，可把结果导出为 Excel；导出的内容同样经过脱敏，并单独记入审计日志。'
                  : ''}
              </span>
            ) : isRedis ? (
              <span>
                {noGrantNeeded
                  ? '管理角色免授权，任意 key 与前缀都可查询。'
                  : '授权范围按 key 前缀判定，前缀授权会自动覆盖其下所有子 key。'}
                模糊查询走 SCAN，绝不使用 KEYS。
                {canExport
                  ? ' 当前数据源已批导出权限，可把结果导出为 Excel（集合类型逐元素展开成多行）；Redis 结果不做脱敏，导出内容同样不脱敏，并单独记入审计日志。'
                  : ''}
              </span>
            ) : (
              <span>
                只能执行只读语句（SELECT / SHOW / DESC / EXPLAIN）。
                {noGrantNeeded
                  ? '管理角色免授权，任意数据源、任意库表都可查询。'
                  : '授权范围按库判定：选好数据源和库，该库下所有表都可以查，不需要逐表申明。'}
                结果中的手机号、身份证、邮箱等字段会自动脱敏。
                {canExport
                  ? ' 当前数据源已批导出权限，可把结果导出为 Excel；导出的内容同样经过脱敏，并单独记入审计日志。'
                  : ''}
              </span>
            )
          }
        />

        {/* 开发者一条生效授权都没有时，下拉是空的且没有任何解释——
            这是最常见的困惑来源：「数据源管理里明明有数据，这里怎么是空的」。
            原因在下拉的数据来源上：它列的是「我有授权的数据源」，
            不是数据源表本身。把这句话直接写出来，比让用户自己猜强。 */}
        {!noGrantNeeded && datasources.length === 0 ? (
          <Alert
            type="warning"
            showIcon
            style={{ marginBottom: 12 }}
            message="没有任何可查询的数据源"
            description={
              <span>
                这个下拉列的是「你已获得授权的数据源」，不是平台上的全部数据源。
                请先到「申请授权」页提交申请，DBA 审批通过后即可在这里查询。
                若你确认已经有授权，请检查授权是否已到期。
              </span>
            }
          />
        ) : null}

        <Space wrap style={{ marginBottom: 12 }}>
          <Select
            style={{ width: 240 }}
            placeholder="选择数据源"
            value={datasourceId}
            onChange={setDatasourceId}
            showSearch
            optionFilterProp="label"
            options={datasources.map((d) => ({
              value: d.id,
              label: `${d.name} (${d.type})`,
            }))}
          />

          {isRedis ? null : (
            <>
              {/* 库名从下拉选：之前是纯 Input，用户得自己记住库名，
                  填错就是一次 403，还看不出是填错还是没权限。 */}
              <AutoComplete
                style={{ width: 200 }}
                placeholder={isMongo ? '选择库，如 test' : '选择库'}
                value={database}
                onChange={setDatabase}
                onSelect={onDatabaseChange}
                options={dbOptions.map((d) => ({ value: d, label: d }))}
                notFoundContent={
                  dbLoading ? <Spin size="small" /> : '无法列出库，可直接输入'
                }
                allowClear
              >
                <Input
                  // 有真故障时把输入框标红：用户一眼能看出「这里不对」，
                  // 而不是以为「就是没有库，只能自己打」。
                  status={dbError ? 'error' : undefined}
                  suffix={
                    <ReloadOutlined
                      style={{ color: '#999', cursor: 'pointer' }}
                      onClick={() => {
                        // current 一定存在：这段渲染在 isRedis===false 的分支里，
                        // 而 current 为 undefined 时也走不到这里（依赖 datasourceId）。
                        if (current) void loadDatabases(current)
                      }}
                    />
                  }
                />
              </AutoComplete>

              {isSQL ? (
                // 表清单只是一个「有哪些表」的速查表：只读展示 + 一键复制表名。
                //
                // 为什么不做成多选：授权粒度是数据源 + 库，选不选表都不影响
                // 能否执行；编辑器补全也早已改成读整库结构（schemaMap），
                // 不再需要用户先勾表。留着多选只会让人误以为「漏选会被拒」。
                <Popover
                  trigger="click"
                  placement="bottomLeft"
                  onOpenChange={(open) => {
                    if (open && datasourceId && database) {
                      void loadTables(datasourceId, database)
                    }
                  }}
                  title={
                    <Space size={8}>
                      <span>表（{tableOptions.length}）</span>
                      <Text type="secondary" style={{ fontSize: 12, fontWeight: 'normal' }}>
                        点击表名即可复制，无需选择
                      </Text>
                    </Space>
                  }
                  content={
                    <div style={{ width: 320 }}>
                      <Input
                        size="small"
                        allowClear
                        placeholder="筛选表名"
                        value={tableKeyword}
                        onChange={(e) => setTableKeyword(e.target.value)}
                        style={{ marginBottom: 8 }}
                      />
                      <div style={{ maxHeight: 320, overflow: 'auto' }}>
                        {tableLoading ? (
                          <div style={{ padding: '16px 0', textAlign: 'center' }}>
                            <Spin size="small" />
                          </div>
                        ) : filteredTables.length === 0 ? (
                          <Text type="secondary" style={{ fontSize: 12 }}>
                            {tableError
                              ? `无法列出表：${tableError}`
                              : database
                                ? '这个库里没有可见的表'
                                : '先选择库'}
                          </Text>
                        ) : (
                          filteredTables.map((t) => (
                            <div
                              key={t}
                              className="dw-table-pick"
                              title="点击复制表名"
                              onClick={() => void copyText(t)}
                            >
                              <Text code style={{ fontSize: 12 }}>
                                {t}
                              </Text>
                              <CopyOutlined style={{ color: '#bfbfbf', fontSize: 12 }} />
                            </div>
                          ))
                        )}
                      </div>
                    </div>
                  }
                >
                  <Button style={{ minWidth: 140 }} loading={tableLoading}>
                    表清单{tableOptions.length > 0 ? `（${tableOptions.length}）` : ''}
                  </Button>
                </Popover>
              ) : null}

              {/* 上限按能力分开：查询走 querysvc 的单次上限（1000，再多
                  也没法在页面上看），有导出权限时放宽到后端的导出上限
                  （20000）——导出的用途就是拿全量，卡在 1000 等于让人
                  导出个截图。两个值都与后端的 clamp 范围对齐，超出会被
                  后端夹回去，但界面先给出正确的范围，避免用户以为
                  "填了 20000 却只拿到 1000 行"是 bug。 */}
              <InputNumber
                min={1}
                max={canExport ? EXPORT_MAX_ROWS : QUERY_MAX_ROWS}
                value={rowLimit}
                onChange={(v) => setRowLimit(v ?? 100)}
                addonBefore="限"
                addonAfter="行"
                style={{ width: 170 }}
              />
            </>
          )}
        </Space>

        {/* 元数据接口的真故障提示。
            放在输入行下面而不是用 message 弹窗：弹窗一闪而过，
            用户没看清就消失了；这里的错误往往需要照着提示去查配置。 */}
        {dbError ? (
          <Alert
            type="error"
            showIcon
            style={{ marginBottom: 12 }}
            message="无法获取库列表"
            description={
              <span>
                {dbError}。可以直接手动输入库名继续查询；
                若手动输入也无法连接，请让 DBA 检查数据源的连接配置。
              </span>
            }
            closable
            onClose={() => setDbError('')}
          />
        ) : null}
        {tableError ? (
          <Alert
            type="error"
            showIcon
            style={{ marginBottom: 12 }}
            message="无法获取表列表"
            description={
              <span>
                {tableError}。表清单只用于展示与复制表名，不影响能否执行查询，
                可以直接手写 SQL 继续。
              </span>
            }
            closable
            onClose={() => setTableError('')}
          />
        ) : null}

        {isMongo ? (
          <Card size="small" type="inner" title="MongoDB 查询">
            <Tabs
              activeKey={mongoMode}
              onChange={(k) => setMongoMode(k as 'collection' | 'command')}
              items={[
                {
                  key: 'collection',
                  label: '集合查询',
                  children: (
                    <Space direction="vertical" style={{ width: '100%' }}>
                      <AutoComplete
                        style={{ width: '100%' }}
                        placeholder="选择或填写集合名，如 users"
                        value={mongoCollection}
                        onChange={setMongoCollection}
                        options={tableOptions.map((t) => ({ value: t, label: t }))}
                        notFoundContent="先选择库再选择集合"
                      />
                      <Input.TextArea
                        rows={5}
                        placeholder='过滤条件，如 {"status":"active"} 或 {"projectId":NumberLong("14759057188135424")}。留空查全部'
                        value={mongoFilter}
                        onChange={(e) => setMongoFilter(e.target.value)}
                        style={{ fontFamily: 'Menlo, Consolas, monospace', fontSize: 13 }}
                      />
                      <Text type="secondary" style={{ fontSize: 12 }}>
                        集合名用于定位查询目标，必须填写准确。权限按库校验，库下所有集合都可查。查询固定为 find（只读）。
                      </Text>
                    </Space>
                  ),
                },
                {
                  key: 'command',
                  label: '原生命令',
                  children: (
                    <Space direction="vertical" style={{ width: '100%' }}>
                      <Input.TextArea
                        rows={5}
                        placeholder='db.users.find({"status":"active"})'
                        value={mongoCommand}
                        onChange={(e) => setMongoCommand(e.target.value)}
                        style={{ fontFamily: 'Menlo, Consolas, monospace', fontSize: 13 }}
                      />
                      <Text type="secondary" style={{ fontSize: 12 }}>
                        支持 find / findOne / countDocuments / distinct / aggregate，
                        也支持 db.getCollection("名字").find(...) 写法。
                        可以直接粘贴 mongosh 里的写法：NumberLong("...")、ObjectId("...")、
                        ISODate("...")、裸键名（{'{ $match: {...} }'}）都能识别。
                        写操作（insert / update / delete / drop）以及会写库的聚合阶段（$out、$merge）一律拒绝。
                      </Text>
                    </Space>
                  ),
                },
              ]}
            />
          </Card>
        ) : isRedis ? (
          <Card size="small" type="inner" title="Redis 查询">
            <Tabs
              activeKey={redisMode}
              onChange={(k) => setRedisMode(k as 'exact' | 'scan')}
              items={[
                {
                  key: 'exact',
                  label: '精确 Key',
                  children: (
                    <Input
                      placeholder="完整 key，如 user:1001:profile"
                      value={redisKey}
                      onChange={(e) => setRedisKey(e.target.value)}
                    />
                  ),
                },
                {
                  key: 'scan',
                  label: '模糊 Key',
                  children: (
                    <Space direction="vertical" style={{ width: '100%' }}>
                      <Input
                        placeholder="带前缀的模式，如 user:1001:*"
                        value={redisKey}
                        onChange={(e) => setRedisKey(e.target.value)}
                      />
                      <Text type="secondary" style={{ fontSize: 12 }}>
                        必须带至少 2 个字符的前缀。平台使用 SCAN 游标遍历，绝不使用 KEYS 命令。
                      </Text>
                    </Space>
                  ),
                },
              ]}
            />
          </Card>
        ) : (
          <Space direction="vertical" style={{ width: '100%' }}>
            <CodeMirror
              value={sql}
              onChange={setSql}
              // extensions 用 useMemo 固定引用，但其中的 schema 通过 ref 读取实时值；
              // 方言与补全配置分别放在两个 Compartment 里，切换数据源时热替换，
              // 不销毁编辑器（销毁会丢光标与滚动位置）。
              extensions={extensions}
              onCreateEditor={(view) => {
                editorRef.current = view
                detachMenu.current?.()
                detachMenu.current = attachContextMenu(view, () => sqlMenuItems(() => void runRef.current()))
              }}
              height="260px"
              placeholder={
                "SELECT u.id, u.phone FROM app_user u WHERE u.created_at > now() - interval '1 day' LIMIT 100"
              }
              basicSetup={{
                lineNumbers: true,
                foldGutter: false,
                highlightActiveLine: true,
                highlightSelectionMatches: true,
                // 补全由自定义的 autocompletion 扩展接管（见 sql/completion.ts）
                autocompletion: false,
                // 搜索面板的快捷键保留（Ctrl+F），右键菜单里也有入口
                searchKeymap: true,
              }}
              style={{ border: '1px solid #d9d9d9', borderRadius: 6, fontSize: 13 }}
            />
            <Space size={4} wrap>
              <Text type="secondary" style={{ fontSize: 12 }}>
                <Text code>别名.</Text> 或 <Text code>表名.</Text> 自动提示该表的字段
                （仅限这句语句里 FROM / JOIN 过的表）；
                <Text code>Ctrl+Enter</Text> 执行光标所在语句，
                <Text code>Shift+Alt+F</Text> 格式化。
                表名可从上方「表清单」点选复制。
              </Text>
            </Space>
          </Space>
        )}

        <div style={{ marginTop: 16 }}>
          <Space>
            <Button type="primary" icon={<PlayCircleOutlined />} onClick={run} loading={loading}>
              执行
            </Button>
            {isSQL ? (
              <Tooltip title="只格式化编辑器里的内容，不会执行">
                <Button onClick={formatEditor}>格式化</Button>
              </Tooltip>
            ) : null}
            <Tooltip title="重新拉取库、表与字段结构，编辑器补全会随之更新">
              <Button
                icon={<ReloadOutlined />}
                onClick={() => {
                  if (!datasourceId || !database) return
                  void loadTables(datasourceId, database)
                  void loadSchema(datasourceId, database)
                  message.success('已刷新表结构')
                }}
                disabled={!datasourceId || !database}
              >
                刷新表结构
              </Button>
            </Tooltip>
          </Space>
        </div>
      </Card>

      {result ? (
        <Card title="查询结果" extra={exportButton()}>
          {resultTable}
        </Card>
      ) : null}
      {redisResult ? <Card title="Redis 查询结果">{redisTable}</Card> : null}

      <Paragraph type="secondary" style={{ fontSize: 12 }}>
        提示：所有查询都会记录审计日志，包含 SQL、涉及表、返回行数与耗时。
      </Paragraph>
    </Space>
  )
}
