import axios, { AxiosError } from 'axios'
import type {
  Datasource,
  DatasourceInput,
  DatasourceType,
  GrantRequest,
  LoginResponse,
  QueryResult,
  RedisQueryResult,
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

// ---------- 查询 ----------

export async function executeSQL(payload: {
  datasource_id: number
  database: string
  tables: string[]
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
  datasource_id?: number
}): Promise<TestConnResult> {
  const { data } = await http.post<TestConnResult>('/datasources/test', payload)
  return data
}
