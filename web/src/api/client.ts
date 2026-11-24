import axios, { AxiosError } from 'axios'
import type {
  Datasource,
  DatasourceBrief,
  DatasourceInput,
  DatasourceType,
  GrantRequest,
  GrantView,
  LoginResponse,
  QueryResult,
  RedisQueryResult,
  TableColumns,
  TestConnResult,
} from './types'

const TOKEN_KEY = 'dw_token'
const USER_KEY = 'dw_user'

export function getToken(): string | null {
  return localStorage.getItem(TOKEN_KEY)
}

export function setSession(token: string, username: string, role: string) {
  localStorage.setItem(TOKEN_KEY, token)
  localStorage.setItem(USER_KEY, JSON.stringify({ username, role }))
}

export function clearSession() {
  localStorage.removeItem(TOKEN_KEY)
  localStorage.removeItem(USER_KEY)
}

export function getCurrentUser(): { username: string; role: string } | null {
  const raw = localStorage.getItem(USER_KEY)
  if (!raw) return null
  try {
    return JSON.parse(raw)
  } catch {
    return null
  }
}

const http = axios.create({
  baseURL: '/api/v1',
  timeout: 60000,
})

// 自动附加令牌
http.interceptors.request.use((config) => {
  const token = getToken()
  if (token) {
    config.headers.Authorization = `Bearer ${token}`
  }
  return config
})

// 统一错误处理：401 清会话并跳登录
http.interceptors.response.use(
  (res) => res,
  (err: AxiosError<{ error?: string }>) => {
    if (err.response?.status === 401) {
      clearSession()
      if (!location.pathname.startsWith('/login')) {
        location.href = '/login'
      }
    }
    const msg = err.response?.data?.error || err.message || '请求失败'
    return Promise.reject(new Error(msg))
  },
)

export async function login(username: string, password: string): Promise<LoginResponse> {
  const { data } = await http.post<LoginResponse>('/login', { username, password })
  setSession(data.token, username, data.role)
  return data
}

// ---------- 数据源 ----------

export async function listDatasources(): Promise<Datasource[]> {
  const { data } = await http.get<{ items: Datasource[] }>('/datasources')
  return data.items ?? []
}

export async function getDatasource(id: number): Promise<Datasource> {
  const { data } = await http.get<Datasource>(`/datasources/${id}`)
  return data
}

export async function createDatasource(input: DatasourceInput): Promise<number> {
  const { data } = await http.post<{ id: number }>('/datasources', input)
  return data.id
}

export async function updateDatasource(id: number, input: DatasourceInput): Promise<void> {
  await http.put(`/datasources/${id}`, input)
}

export async function deleteDatasource(id: number): Promise<void> {
  await http.delete(`/datasources/${id}`)
}

// ---------- 授权申请 ----------

export async function listApplyable(): Promise<Datasource[]> {
  const { data } = await http.get<{ items: Datasource[] }>('/grant/applyable')
  return data.items ?? []
}

export async function createGrantRequest(payload: {
  datasource_id: number
  scope: string
  days: number
  reason: string
}): Promise<number> {
  const { data } = await http.post<{ id: number }>('/grant/requests', payload)
  return data.id
}

export async function listMyRequests(): Promise<GrantRequest[]> {
  const { data } = await http.get<{ items: GrantRequest[] }>('/grant/requests/mine')
  return data.items ?? []
}

export async function listRequests(status = 'pending'): Promise<GrantRequest[]> {
  const { data } = await http.get<{ items: GrantRequest[] }>('/grant/requests', {
    params: { status },
  })
  return data.items ?? []
}

export async function approveRequest(id: number, comment: string): Promise<void> {
  await http.post(`/grant/requests/${id}/approve`, { comment })
}

export async function rejectRequest(id: number, comment: string): Promise<void> {
  await http.post(`/grant/requests/${id}/reject`, { comment })
}

export async function cancelRequest(id: number): Promise<void> {
  await http.post(`/grant/requests/${id}/cancel`)
}

// ---------- 已生效授权 ----------

/**
 * 列出「我能查的数据源」。
 *
 * 查询页必须用这个而不是 listDatasources()：后者走 GET /datasources，
 * 路由限 DBA，开发者调用会 403，下拉变成空的，什么都查不了。
 * 而且它返回的是一份精简视图，不含 host/port/username。
 */
export async function listMyDatasources(): Promise<DatasourceBrief[]> {
  const { data } = await http.get<{ items: DatasourceBrief[] }>('/grants/mine')
  return data.items ?? []
}

export async function listActiveGrants(): Promise<GrantView[]> {
  const { data } = await http.get<{ items: GrantView[] }>('/grants')
  return data.items ?? []
}

export async function revokeGrant(id: number): Promise<void> {
  await http.post(`/grants/${id}/revoke`)
}

// ---------- 元数据（库 / 表 / 字段） ----------

/**
 * 列出数据源上当前用户可见的库。
 *
 * 后端会按授权过滤，所以返回为空有两种可能：
 *   1. 没有任何授权
 *   2. 账号只有单库权限，列不出全量库名
 * 两种情况下前端都退回「手填」，不当作错误处理。
 */
export async function listDatabases(datasourceId: number): Promise<string[]> {
  const { data } = await http.get<{ items: string[] }>(
    `/datasources/${datasourceId}/databases`,
  )
  return data.items ?? []
}

/** 列出指定库下当前用户可见的表 / 集合。 */
export async function listTables(datasourceId: number, db: string): Promise<string[]> {
  const { data } = await http.get<{ items: string[] }>(
    `/datasources/${datasourceId}/tables`,
    { params: { db } },
  )
  return data.items ?? []
}

/** 列出表的字段名，供 SQL 编辑器做列名补全。 */
export async function listColumns(
  datasourceId: number,
  db: string,
  table: string,
): Promise<string[]> {
  const { data } = await http.get<{ items: string[] }>(
    `/datasources/${datasourceId}/columns`,
    { params: { db, table } },
  )
  return data.items ?? []
}

/**
 * 一次取回整库的表结构（表名 + 字段名）。
 *
 * SQL 编辑器要提示 `别名.` 之后的字段，而别名指向哪张表得先解析语句才知道。
 * 逐表调 listColumns 会随着输入不断发请求、且每次都要等一次往返，
 * 所以这里一次性拿全库、在内存里缓存，由前端解析别名后直接查表。
 *
 * 后端只返回当前用户有读权限的表；数据源类型不支持批量读取（Mongo / Redis）
 * 时返回空数组，不报错——补全拿不到不影响查询。
 */
export async function getSchema(
  datasourceId: number,
  db: string,
): Promise<TableColumns[]> {
  const { data } = await http.get<{ items: TableColumns[] }>(
    `/datasources/${datasourceId}/schema`,
    { params: { db } },
  )
  return data.items ?? []
}

// ---------- 查询 ----------

export async function executeSQL(payload: {
  datasource_id: number
  database: string
  /** 选填，仅写入审计明细便于追溯；鉴权按库判定，不参与放行 */
  tables?: string[]
  sql: string
  row_limit?: number
}): Promise<QueryResult> {
  const { data } = await http.post<QueryResult>('/query/sql', payload)
  return data
}

export async function executeRedis(payload: {
  datasource_id: number
  key?: string
  pattern?: string
  row_limit?: number
}): Promise<RedisQueryResult> {
  const { data } = await http.post<RedisQueryResult>('/query/redis', payload)
  return data
}

/** 测试数据源连接。不写库，只验证配置能否连上。 */
export async function testDatasourceConnection(payload: {
  type: DatasourceType | string
  host: string
  port: number
  database?: string
  username?: string
  /** 编辑场景可留空，后端会用已存的密码测试 */
  password?: string
  /** 仅 MongoDB：账号所在的认证库，留空回落 admin */
  auth_source?: string
  /** 连接高级参数，必须与保存时一致的同一套 */
  options?: Record<string, string>
  datasource_id?: number
}): Promise<TestConnResult> {
  const { data } = await http.post<TestConnResult>('/datasources/test', payload)
  return data
}

/** 执行 MongoDB 查询。两种形式二选一：传 command，或传 collection+filter。 */
export async function executeMongo(payload: {
  datasource_id: number
  database?: string
  collection?: string
  filter?: string
  command?: string
  row_limit?: number
}): Promise<QueryResult> {
  const { data } = await http.post<QueryResult>('/query/mongo', payload)
  return data
}
