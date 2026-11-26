import axios, { AxiosError } from 'axios'
import { message } from 'antd'
import type {
  ApplyableDatasource,
  Datasource,
  DatasourceBrief,
  DatasourceInput,
  DatasourceType,
  GrantRequest,
  GrantView,
  LoginResponse,
  QueryResult,
  RedisQueryResult,
  Role,
  TableColumns,
  TestConnResult,
  UserFilter,
  UserGroup,
  UserStatus,
  UserView,
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
    // 403 不一定是 bug：更常见的是「我的角色/状态在别处被改了，但本地
    // 缓存的 dw_user 还是旧值」——菜单照旧显示，点进去一片 403，
    // 用户会以为系统坏了。清掉会话让 ta 重新登录取最新身份，
    // 并说明原因，而不是让 ta 反复重试。
    if (err.response?.status === 403) {
      clearSession()
      if (!location.pathname.startsWith('/login')) {
        message.error('你的权限已变更，请重新登录')
        location.href = '/login'
      }
      return Promise.reject(new Error('权限不足或权限已变更，请重新登录'))
    }
    // 503 + Retry-After 是本项目「等超管集合锁超时」的信号。
    // 后端返回的中文已经可以直接展示，这里保留后端文案。
    if (err.response?.status === 503) {
      return Promise.reject(
        new Error(err.response.data?.error || '账号管理操作繁忙，请稍后重试'),
      )
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

export async function listApplyable(): Promise<ApplyableDatasource[]> {
  const { data } = await http.get<{ items: ApplyableDatasource[] }>('/grant/applyable')
  return data.items ?? []
}

/**
 * 提交授权申请。
 *
 * 不再传 scope：申请粒度就是数据源本身，审批通过即拥有该数据源下
 * 所有库表的查询权限，范围由后端固化，客户端自述的范围会被忽略。
 */
export async function createGrantRequest(payload: {
  datasource_id: number
  days: number
  reason: string
  /**
   * 是否申请「可导出查询结果」。不传等价于不要——
   * 后端不会补默认值，这个勾选是用户明确表达诉求的地方。
   */
  can_export?: boolean
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

/**
 * 开关某条授权的导出能力。
 *
 * 只改授权记录，不动申请单（申请单是「当时申请了什么」的留痕）。
 * 后端不重建 Casbin 策略：导出判定在导出接口里现查 can_export，
 * 所以改完立刻生效，没有「等 60 秒刷新」的延迟。
 */
export async function setGrantExport(id: number, canExport: boolean): Promise<void> {
  await http.post(`/grants/${id}/export`, { can_export: canExport })
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

/**
 * 导出查询结果为 Excel。
 *
 * 与 executeSQL / executeMongo 的区别：多一道「这个数据源是否批了导出」
 * 的权限闸门（授权记录上的 can_export），所以前端需要按数据源判断
 * 按钮可见性，而不是按人判断。
 *
 * 后端用同一份凭证重新执行同一条语句再生成文件，所以这里必须把查询
 * 参数原样传过去——不是把前端表格里的数据回传。
 *
 * 失败时后端回 JSON（{error}），成功时回二进制。用 responseType: 'blob'
 * 之后，错误响应也会是 blob，需要在这里读出来转成可读的 Error。
 */
export async function exportQuery(payload: {
  datasource_id: number
  type: 'sql' | 'mongo'
  database?: string
  collection?: string
  /** Mongo 原生命令，与 collection/filter 二选一 */
  command?: string
  filter?: string
  sql?: string
  row_limit?: number
  filename?: string
}): Promise<{ blob: Blob; filename: string; truncated: boolean }> {
  const res = await http.post('/query/export', payload, {
    // 超时要比后端自己的执行超时略长，但不能长太多。
    //
    // 后端 querysvc 的执行超时是 QUERY_TIMEOUT_SEC（默认 30s），
    // 超了会由后端主动中断并返回 500。若前端设 300s，用户要点完之后
    // 干等 5 分钟才见到失败——而真正的失败信息后端在 30s 时就已经给了。
    // 这里的余量只用来覆盖"查询已跑完、xlsx 生成 + 传输"这段：
    // 90s 足够 2 万行生成与下载，又不会把失败反馈拖到无法忍受。
    //
    // 若部署时把 QUERY_TIMEOUT_SEC 调到 60 以上，这里要同步放大，
    // 否则会在后端还在跑的时候就把请求判超时。
    timeout: 90_000,
    responseType: 'blob',
    // 只豁免 403，让 401 继续走全局拦截器。
    //
    // 为什么必须豁免 403：全局拦截器遇到 403 会 clearSession() 并跳 /login。
    // 而「这个库没批导出」是与登录无关的权限拒绝（开发者本人有效），
    // 走全局拦截器会把人直接踢下线，看起来像账号出问题，实际只是点错了库。
    //
    // 为什么必须保留 401：token 过期时用户必须被送回登录页。
    // 若这里写成 `() => true`（对所有状态码都放行），401 也会被吞掉，
    // 用户只会看到「导出失败（HTTP 401）」，页面却停在那里不跳转——
    // 那比原来的行为更糟。所以豁免范围只到 403。
    validateStatus: (status) => (status >= 200 && status < 300) || status === 403,
  })

  // 403：后端回的是 JSON 错误体，但 responseType 是 blob，
  // 全局拦截器读 err.response.data.error 只会拿到 Blob，
  // 提示会退化成 "[object Blob]"。所以在这里自己读出来。
  if (res.status === 403) {
    throw new Error(await readBlobError(res.data as Blob, res.status))
  }
  const blob = res.data as Blob

  return {
    blob,
    filename: filenameFromHeader(res.headers['content-disposition']),
    truncated: res.headers['x-export-truncated'] === '1',
  }
}

/**
 * 从错误响应里读出可读的错误文案。
 *
 * 导出接口出错时回的是 JSON（没挂 responseType 的普通接口能直接读 data.error），
 * 但这里 data 已经是 Blob。必须手动 text() + JSON.parse，
 * 否则用户只能看到一句 "[object Blob]"。
 */
async function readBlobError(blob: Blob | undefined, status: number): Promise<string> {
  if (!blob) return `导出失败（HTTP ${status}）`
  try {
    const text = await blob.text()
    const msg = JSON.parse(text).error
    if (typeof msg === 'string' && msg) return msg
  } catch {
    // 不是 JSON（网关返回 HTML 错误页之类），走下面的兜底
  }
  // 走到这里说明后端没有给 JSON 错误体（网关错误页之类）。
  // 401 已经交给全局拦截器，这里保留一句兜底只为兜住极端情况。
  if (status === 403) return '当前授权不包含导出权限，请到「申请授权」页申请'
  if (status === 401) return '登录已过期，请重新登录'
  return `导出失败（HTTP ${status}）`
}

/**
 * 把 blob 落到本地磁盘。
 *
 * 用一次性的 <a download> 而不是 window.open：后者会被浏览器当成弹窗拦截，
 * 而这里要的是「点了就下载」，不打开新标签页。
 * objectURL 必须回收，否则每导一次就泄漏一份完整文件的引用。
 */
export function saveBlob(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  // 立刻 revoke 在某些浏览器里会让下载中断，推迟一拍更稳。
  setTimeout(() => URL.revokeObjectURL(url), 10_000)
}

/**
 * 从 Content-Disposition 取文件名，优先用 RFC 5987 的 filename*，
 * 它才是能正确带中文的那一份（filename 被后端降级成了下划线）。
 */
function filenameFromHeader(header: unknown): string {
  const fallback = 'query_result.xlsx'
  if (typeof header !== 'string' || !header) return fallback
  const star = /filename\*=UTF-8''([^;]+)/i.exec(header)
  if (star) {
    try {
      return decodeURIComponent(star[1].trim())
    } catch {
      // 编码坏了就退回到普通 filename
    }
  }
  const plain = /filename="?([^";]+)"?/i.exec(header)
  return plain ? plain[1].trim() : fallback
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

// ---------- 组管理（仅超级管理员） ----------

export async function listGroups(): Promise<UserGroup[]> {
  const { data } = await http.get<{ items: UserGroup[] }>('/groups')
  return data.items ?? []
}

export async function createGroup(input: {
  name: string
  description: string
}): Promise<number> {
  const { data } = await http.post<{ id: number }>('/groups', input)
  return data.id
}

export async function updateGroup(
  id: number,
  input: { name: string; description: string },
): Promise<void> {
  await http.put(`/groups/${id}`, input)
}

/**
 * 删除组。
 *
 * 组员不会被删除，只会变成「未分组」——后端靠外键 ON DELETE SET NULL 保证。
 * 前端文案要如实说明，否则管理员会以为人也没了。
 */
export async function deleteGroup(id: number): Promise<void> {
  await http.delete(`/groups/${id}`)
}

// ---------- 用户管理（仅超级管理员） ----------

export async function listUsers(filter: UserFilter = {}): Promise<UserView[]> {
  const params: Record<string, string> = {}
  if (filter.keyword) params.keyword = filter.keyword
  if (filter.role) params.role = filter.role
  if (filter.status?.length) params.status = filter.status.join(',')
  if (filter.ungrouped) params.ungrouped = '1'
  else if (filter.group_id != null) params.group_id = String(filter.group_id)

  const { data } = await http.get<{ items: UserView[] }>('/users', { params })
  return data.items ?? []
}

/**
 * 新建用户。
 *
 * 密码由后端随机生成，返回体里的 password 是明文唯一一次出现的地方：
 * 调用方必须立刻展示给管理员转交，绝不能存到 localStorage / sessionStorage。
 */
export async function createUser(input: {
  username: string
  email: string
  role: Role
  status?: Exclude<UserStatus, 'deleted'>
  group_id?: number | null
}): Promise<{ id: number; password: string }> {
  const { data } = await http.post<{ id: number; password: string }>('/users', input)
  return data
}

export async function updateUser(
  id: number,
  input: { email: string; role: Role; group_id?: number | null },
): Promise<void> {
  await http.put(`/users/${id}`, input)
}

export async function setUserStatus(
  id: number,
  status: Exclude<UserStatus, 'deleted'>,
): Promise<void> {
  await http.post(`/users/${id}/status`, { status })
}

/** 重置密码，返回新的明文密码（只此一次）。 */
export async function resetUserPassword(id: number): Promise<string> {
  const { data } = await http.post<{ password: string }>(`/users/${id}/reset-password`)
  return data.password
}

/**
 * 修改自己的密码。
 *
 * 走 /me/password 而不是 /users/:id/password：后端按 token 里的身份改，
 * 路径里不带 id，前端也就没有「传错 id 改到别人」这条路径。
 *
 * 成功不改本地会话：当前 token 在有效期内继续可用，用户不必重新登录。
 * 这与「停用账号后已签发 token 仍能用满 12 小时」是同一口径的取舍，
 * 要的是「改密不打断正在进行的操作」，代价是其他设备上的旧会话不会立刻失效。
 */
export async function changeOwnPassword(oldPassword: string, newPassword: string): Promise<void> {
  await http.post('/me/password', { old_password: oldPassword, new_password: newPassword })
}

/**
 * 逻辑删除用户。
 *
 * 不是物理删除：历史授权与审批记录保留在其用户 ID 下，
 * 用户名会被改写为 `<原名>#deleted#<ID>`，原名立即可分配给新账号。
 * 不可恢复。
 */
export async function deleteUser(id: number): Promise<void> {
  await http.delete(`/users/${id}`)
}
