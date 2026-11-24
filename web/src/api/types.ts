// 与后端 model 包对应的类型定义

export type Role = 'developer' | 'dba' | 'superadmin'

export type DatasourceType = 'mysql' | 'postgresql' | 'redis' | 'mongodb'

/** 已废弃：数据源不再配置可申请范围，仅兼容后端返回的旧字段。 */
export interface ApplyScope {
  database: string
  /** 为空表示该库下全部表 */
  tables: string[]
}

export interface Datasource {
  id: number
  name: string
  type: DatasourceType
  host: string
  port: number
  database: string
  /** 连接目标库使用的账号 */
  username: string
  /** 仅 MongoDB：账号所在的认证库（authSource）。留空回落 admin。 */
  auth_source: string
  /**
   * 是否已设置密码。
   * 后端永远不会返回密码本身，无论它在库里是明文还是密文。
   */
  /**
   * 连接高级参数，透传给驱动。
   * MongoDB：replicaSet / tls / tlsCAFile / tlsInsecure / readPreference 等
   * PostgreSQL：sslmode
   */
  options: Record<string, string>
  has_password: boolean
  /**
   * 连接密码明文，只在「DBA 编辑数据源」时由详情接口返回，用于直接填进编辑框。
   * 列表接口不返回该字段。没有配置密码时该字段不存在。
   */
  password?: string
  created_at: string
  apply_enabled: boolean
  max_apply_days: number
  apply_scopes: ApplyScope[]
  description: string
}

export interface DatasourceInput {
  name: string
  type: DatasourceType
  host: string
  port: number
  database: string
  username: string
  /**
   * 新建时必填；编辑时留空表示不修改密码。
   * 平台按明文保存，不会加密。
   */
  password?: string
  /** 仅 MongoDB：账号所在的认证库，留空表示 admin。 */
  auth_source?: string
  /** 连接高级参数，键值透传给驱动（见 Datasource.options）。 */
  options?: Record<string, string>
  apply_enabled: boolean
  max_apply_days: number
  apply_scopes: ApplyScope[]
  description: string
}

export type GrantRequestStatus = 'pending' | 'approved' | 'rejected' | 'canceled'

/** 已生效的授权。与 GrantRequest 不同：这是「现在真的能查」的记录。 */
export interface GrantView {
  id: number
  user_id: number
  username: string
  datasource_id: number
  datasource_name: string
  /**
   * 授权范围。"*" 表示整个数据源（所有库和表）；
   * "<库>:*" 是改口径前审批的库级授权，仍可能出现在存量记录里。
   */
  scope: string
  action: string
  expires_at: string
  created_at: string
}

/**
 * 授权范围的结构化形态。
 * 后端已经把 "<库>" / "<库>:*" / "<库>:订单,用户>" 三种写法归一，
 * 前端不需要再解析字符串。
 */
export interface ScopeView {
  /** "*" 表示整个数据源（所有库和表） */
  database: string
  /** 空数组表示整库授权（对应后端的 "shop" 或 "shop:*"） */
  tables: string[]
}

/**
 * 「我能查的数据源」——查询页下拉用。
 *
 * 与 Datasource 的区别：不含 host / port / username，
 * 这些是内网拓扑与账号信息，只该给管理员看。
 */
export interface DatasourceBrief {
  id: number
  name: string
  type: DatasourceType
  description: string
  /** 数据源配置的默认库，用于预填查询表单（Redis 下是 db 号） */
  database: string
  /** 我在这个数据源上的授权范围（已结构化，无需再 split） */
  scopes: ScopeView[]
}

export interface GrantRequest {
  id: number
  user_id: number
  username?: string
  datasource_id: number
  datasource_name?: string
  /** 申请范围由后端固化："*" 即整个数据源 */
  scope: string
  days: number
  reason: string
  status: GrantRequestStatus
  reviewer_id?: number
  review_comment: string
  reviewed_at?: string
  grant_id?: number
  created_at: string
}

export interface QueryResult {
  columns: string[]
  rows: unknown[][]
  row_count: number
  truncated: boolean
  elapsed_ms: number
}

export interface LoginResponse {
  token: string
  role: Role
}

// Redis 值的展示形态由类型决定，三种承载字段互斥：
//   string        -> value
//   hash          -> fields
//   list/set/zset -> items
//
// 注意：Redis 的 TYPE 只有 string 一种，bitmap / HyperLogLog / Geo 索引在类型上
// 都算 string。这类 key 的内容是二进制，后端不会塞进 value（JSON 会把非法
// UTF-8 换成 \ufffd，等于丢数据），而是给 encoding + hex 让前端换个方式展示。
export interface RedisKeyValue {
  key: string
  type: string
  value?: string
  fields?: Record<string, string>
  items?: string[]
  ttl: number
  /** 仅 string 类型：text / binary / hyperloglog。缺省等价于 text */
  encoding?: 'text' | 'binary' | 'hyperloglog'
  /** 二进制内容的十六进制预览 */
  hex?: string
  /** 二进制内容的原始字节数 */
  size?: number
  /** 疑似位图时置位的总数 */
  bit_count?: number
  /** 疑似位图时前若干个置位下标，形如 "0,3,8" */
  set_bits?: string
}

export interface RedisQueryResult {
  exact: boolean
  items: RedisKeyValue[]
  truncated: boolean
  elapsed_ms: number
}

/** 测试连接的结果。「连不上」用 ok:false 表示，不是 HTTP 错误。 */
export interface TestConnResult {
  ok: boolean
  message: string
  elapsed_ms?: number
}

/**
 * 整库表结构：一张表及其字段名。
 *
 * 查询页的 SQL 编辑器用它解析别名、做 `别名.` 的字段补全。
 * 字段顺序来自 information_schema 的 ordinal_position，即建表顺序，
 * 展示时不要重排——按建表顺序比按字母序更贴近表结构本身。
 */
export interface TableColumns {
  table: string
  columns: string[]
}
