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
  secret_ref: string
  env: string
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
  secret_ref: string
  env: string
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
