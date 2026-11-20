// 与后端 model 包对应的类型定义

export type Role = 'developer' | 'dba' | 'superadmin'

export type DatasourceType = 'mysql' | 'postgresql' | 'redis' | 'mongodb'

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
  /**
   * 仅 MongoDB：账号所在的认证库（authSource）。
   * 留空表示与 database 相同。建在 admin 库的管理员账号需填 admin。
   */
  auth_source: string
  /**
   * 是否已设置密码。
   * 后端永远不会返回密码本身，无论它在库里是明文还是密文。
   */
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
  /** 仅 MongoDB：账号所在的认证库，留空表示与 database 相同。 */
  auth_source?: string
  apply_enabled: boolean
  max_apply_days: number
  apply_scopes: ApplyScope[]
  description: string
}

export type GrantRequestStatus = 'pending' | 'approved' | 'rejected' | 'canceled'

export interface GrantRequest {
  id: number
  user_id: number
  username?: string
  datasource_id: number
  datasource_name?: string
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

// Redis 查询结果。与 SQL 结果结构不同：Redis 是键值对，hash 类型带 fields。
export interface RedisKeyValue {
  key: string
  type: string
  value?: string
  fields?: Record<string, string>
  ttl: number
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
