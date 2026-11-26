# DataWarden

面向研发团队的数据审计工单平台。为开发人员提供生产数据库的受控只读查询能力，支持 MySQL、PostgreSQL、Redis、MongoDB 四类数据源，所有操作留痕可审计。

## 产品目标与当前状态

完整产品规划包含只读查询、变更工单（执行前生成行镜像、支持回滚）、可直连的临时账号三类能力。

**当前只交付了只读查询这一条线**：变更工单、回滚、临时账号均未开始实现，
代码里也没有相关接口。README 中标注「规划」的内容请以此为准，不要当成已有能力。

| 阶段 | 内容 | 状态 |
|---|---|---|
| 阶段一 | 地基 + 只读查询（四类数据源）+ 授权申请审批 + 账号管理 | ✅ 已完成 |
| 阶段二 | 变更工单、回滚、临时账号 | ⬜ 未开始 |

## 已实现能力

| 能力 | 状态 | 说明 |
|---|---|---|
| 用户登录与 JWT 鉴权 | ✅ | bcrypt 密码哈希，令牌含角色 |
| 自助修改密码 | ✅ | 登录后右上角「修改密码」，需校验当前密码；校验与写入在同一事务内完成 |
| 角色权限（developer / dba / superadmin） | ✅ | Casbin 策略，数据源级 `ds:<id>:*:*` 通配 |
| 用户管理 | ✅ | 仅 superadmin：新建账号（随机初始密码一次性展示）、分配角色与组、重置密码、停用启用、逻辑删除（释放用户名） |
| 组管理 | ✅ | 仅 superadmin：组只做归类（类似部门），**不承载任何数据权限**；删组时成员转为未分组 |
| 数据源管理 | ✅ | 增删改查，仅 DBA / superadmin 可访问（列表与详情同样限权） |
| 可申请配置 | ✅ | 按数据源设置是否开放申请与最长天数（不再配置库表范围） |
| 授权申请与审批 | ✅ | 提交 → DBA 审批 → 授权即时生效；禁止自审 |
| PostgreSQL / MySQL 受控查询 | ✅ | 只读白名单、强制限行、超时中断 |
| MongoDB 受控查询 | ✅ | 集合+过滤条件或原生命令，写操作与 `$out`/`$merge` 在解析阶段拒绝 |
| Redis 查询通道 | ✅ | 精确 key + `SCAN` 模糊 key，绝不使用 `KEYS` |
| 授权期限与回收 | ✅ | 授权带过期时间，定时重建 Casbin 策略；支持立即收回 |
| 结果脱敏 | ✅ | 手机号、身份证、邮箱、银行卡按列名关键字匹配 |
| 导出 Excel | ✅ | 按数据源单独审批；未批导出的数据源只能在线查看（见下文「导出权限」） |
| 全链路审计 | ✅ | 查询、数据源变更、审批、账号操作全部留痕，表级触发器禁止篡改 |
| Web 界面 | ✅ | React 18 + Ant Design + CodeMirror 6，八个页面，菜单按角色动态渲染 |

## 界面

前端为 React 18 + Ant Design + CodeMirror 6，共**八个页面**：

| 页面 | 可见角色 | 功能 |
|---|---|---|
| 数据查询 | 全部 | 选择数据源执行只读查询，结果自动脱敏；SQL 编辑器带表名/字段名补全 |
| 申请授权 | 全部 | 查看可申请的数据源、提交申请、查看自己的申请进度 |
| 数据源管理 | DBA / superadmin | 登记数据源，配置「是否开放申请 / 最长天数 / 高级连接参数」 |
| 授权审批 | DBA / superadmin | 审批申请，通过后授权立即生效 |
| 授权管理 | DBA / superadmin | 查看生效中的授权，可立即收回 |
| 用户管理 | superadmin | 账号增删改查、重置密码、停用启用、查看已删除账号 |
| 组管理 | superadmin | 组的增删改查，查看成员数 |

侧边栏菜单按角色动态渲染：开发者只有「数据查询」和「申请授权」；
dba 额外看到数据源管理、授权审批、授权管理三页；用户管理与组管理只对 superadmin 出现。
直接访问越权路径会被重定向，接口层同样返回 403。

### SQL 编辑器的补全

编辑器是 CodeMirror 6 + `@codemirror/lang-sql`，补全候选来自元数据接口：

1. **库**：进入页面或切换数据源时按 `GET /datasources/:id/databases` 拉取（后端按授权过滤）
2. **表**：选定库后按 `GET /datasources/:id/tables?db=` 拉取，页面上是**只读的表名清单**
   （点击表名即复制）。它不参与鉴权（授权粒度是数据源，见下文「申请的两道校验」），
   也不再需要用户勾选
3. **字段**：选定库后按 `GET /datasources/:id/schema?db=` 一次取回整库结构，
   输入 `别名.` / `表名.` 时直接查表给出该表字段

编辑器只在**带点号的限定前缀**之后给候选（`t.`、`app_user.`、`public.app_user.`），
`SELECT` 之后或 `FROM` 之后不再自动弹表名 / 字段：没有 FROM 时字段候选只能是整库并集，
上千条无从选择，表名则由右侧清单负责。Ctrl+Space 也只在限定前缀处有作用。

字段接口对 MongoDB 走「采样一条文档取键名」——Mongo 没有列定义，只能采样；
空集合取不到字段，界面上会显示成「未采样」而不是报错。

**库表未授权时补全退回手填：** 账号只有单库权限时 `ListDatabases` 会失败，
前端不报错，只把下拉变成可自由输入，不阻断查询。

## 导出权限

申请授权时可以勾选「可导出查询结果」。审批通过后，这个勾选会**复制**到授权记录
（`grant_record.can_export`），查询页才会出现「导出 Excel」按钮。

关键是判定粒度：**跟着单条授权、单个数据源走**，不是跟着人走。

- 同一个人 A 数据源批了导出、B 数据源没批，是两次独立审批各自的决定：
  查 A 时能导出、切到 B 按钮直接消失，B 的导出请求也会被后端 403；
- 同一数据源上有多条生效授权时取「或」（任一条批过就允许）；
- 已撤销、已过期、账号已删除的授权一律不算（与查询侧同一套过滤条件）；
- 审批时没勾就默认 false，**存量授权也一律为 false**——它们当初的审批里没有
  「导出」这一项，事后自动放开等于把没人审查过的能力发给全体持权人；
- dba / superadmin 免授权查询，导出也不拦（他们本来就能拿到全部数据）。

几个实现上的取舍：

| 决定 | 原因 |
|---|---|
| 导出**不进 Casbin** | `act` 段目前只有 `read`。把导出做成 `act=export` 要为每条授权多写一条策略，而判定只需要一个布尔值——两套事实源迟早对不上。判定放在 `httpapi.ExportHandler` |
| 后端**重新执行**语句，而不是接收前端回传的表格 | 前端手里的那份是当前分页、且可被篡改；重新执行后导出的内容可复现，审计才说得清。脱敏规则同样生效 |
| 导出单独记一条审计（`export.sql` / `export.mongo`） | 数据外带必须能回答「谁、什么时候、导走了哪些数据、多少行」 |
| Redis 不支持导出 | 它的形态是 key→值（hash / list / zset / 二进制），摊平成二维表没有合理语义 |
| DBA 可以单独关掉某条授权的导出 | 「先别让他导数据」远多于「让他完全不能查」；否则只能整条收回，再走一遍审批 |

行数上限：默认 5000 行，最大 20000 行（`exportMaxRowLimit`）。不设上限时一条
`SELECT` 就能把整库拉进内存转 xlsx，任何拿到授权的账号都能触发。达到上限时响应头
`X-Export-Truncated: 1`，前端会提示「结果被截断」。

Excel 生成在 `internal/xlsx`，只写「表头 + 单元格」这一种最小 xlsx（六个 zip 部件），
不引第三方库。长数字一律按文本写（`numFmtId=49`），否则订单号、身份证号打开就是
`1.47591E+16`；非法控制字符与 NaN/Inf 也做了处理，否则 Excel 直接判文件损坏。

## API

所有接口挂在 `/api/v1` 下。除 `POST /login` 外均需 `Authorization: Bearer <token>`。

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| POST | `/login` | 公开 | 换取 JWT |
| POST | `/me/password` | 已登录 | 修改自己的密码（需当前密码） |
| GET | `/datasources` | dba / superadmin | 数据源列表（只返回 `has_password`） |
| GET | `/datasources/:id` | dba / superadmin | 数据源详情（含明文 `password`，供编辑框直接回显） |
| POST | `/datasources` | dba / superadmin | 登记数据源（含可申请配置） |
| PUT | `/datasources/:id` | dba / superadmin | 修改数据源与可申请配置 |
| DELETE | `/datasources/:id` | dba / superadmin | 删除数据源 |
| POST | `/datasources/test` | dba / superadmin | 测试连接（不写库） |
| POST | `/query/sql` | 需库级授权 | 执行只读 SQL |
| POST | `/query/redis` | 需 key 前缀授权 | 执行 Redis 查询（精确 key 或 SCAN 模式） |
| POST | `/query/mongo` | 需库级授权 | 执行 MongoDB 查询（集合+过滤 或 原生命令） |
| POST | `/query/export` | 需库级授权 **且** 该数据源批了导出 | 重新执行查询并导出 xlsx |
| POST | `/grants/:id/export` | dba / superadmin | 单独开关某条授权的导出能力 |
| GET | `/datasources/:id/databases` | 已登录 | 列出可见的库（按授权过滤） |
| GET | `/datasources/:id/tables?db=` | 已登录 | 列出库下可见的表（按授权过滤） |
| GET | `/datasources/:id/columns?db=&table=` | 已登录 | 列出表的字段（供编辑器补全） |
| GET | `/datasources/:id/schema?db=` | 已登录 | 一次返回整库表结构（供编辑器补全，逐表过滤权限） |
| GET | `/grants` | dba / superadmin | 列出生效中的授权 |
| POST | `/grants/:id/revoke` | dba / superadmin | 立即收回一条授权 |
| GET | `/grants/mine` | 已登录 | 自己当前生效的数据源（开发者查可用数据源走这里） |
| GET | `/grant/applyable` | 已登录 | 列出开放申请的数据源 |
| POST | `/grant/requests` | 已登录 | 提交授权申请 |
| GET | `/grant/requests/mine` | 已登录 | 我的申请列表 |
| GET | `/grant/requests` | dba / superadmin | 按状态列出申请（默认 pending） |
| POST | `/grant/requests/:id/approve` | dba / superadmin | 审批通过 |
| POST | `/grant/requests/:id/reject` | dba / superadmin | 驳回（必须填原因） |
| POST | `/grant/requests/:id/cancel` | 申请人本人 | 撤销待审申请 |
| GET | `/users` | **superadmin** | 用户列表（默认在用 + 已停用，可筛选已删除） |
| POST | `/users` | **superadmin** | 新建用户，返回一次性初始密码 |
| PUT | `/users/:id` | **superadmin** | 改邮箱 / 角色 / 所属组（用户名不可改） |
| POST | `/users/:id/status` | **superadmin** | 停用 / 启用 |
| POST | `/users/:id/reset-password` | **superadmin** | 重置密码，返回一次性新密码 |
| DELETE | `/users/:id` | **superadmin** | 逻辑删除，释放用户名 |
| GET | `/groups` | **superadmin** | 组列表（含成员数） |
| POST | `/groups` | **superadmin** | 新建组 |
| PUT | `/groups/:id` | **superadmin** | 改组名与说明 |
| DELETE | `/groups/:id` | **superadmin** | 删组（成员转为未分组，不删用户） |

**开发者与 DBA 拿数据源的接口是两套**：`GET /datasources` 只对 DBA 开放（会带 host/port/username），
开发者用 `GET /grants/mine` 看自己能查哪些数据源。

**登录失败会区分原因**：用户名不存在回「用户不存在」，账号停用回「账号已停用」，
密码错误回「密码错误」，数据库故障回 500 而不是伪装成凭据错误。
这是有意的取舍——用户能分清该找回密码还是该找管理员，代价是用户名可被枚举。
若要收紧为黑盒，只需把 `auth_handler.go` 里三处文案改成同一句，前端无需改动。

### Redis 查询

Redis 没有表的概念，鉴权用 key 前缀。授权粒度同样是数据源——
审批通过后策略为 `ds:<数据源ID>:*:*`，对任意 db 的任意 key 有效。

> 历史遗留：早期是按库里手填范围申请（形如 `37:user:1001` → 鉴权对象
> `ds:<数据源ID>:37:user:1001`）。这类库级授权仍然兼容，鉴权侧对两种写法一视同仁。

**前缀授权自动向下覆盖。** 授权 `user:1001` 后可以查 `user:1001:name`、`user:1001:profile`
等所有子 key，但查不到 `user:1002:name`。实现方式是对目标 key 逐级向上生成候选
（`user:1001:name` → `user:1001:*` → `user:1001` → `user:*` → `user` → `*`），
任意一个命中即放行。

请求示例：

```json
// 精确查询
{ "datasource_id": 2, "key": "user:1001:name" }

// 模糊查询（必须带至少 2 个有效字符的前缀）
{ "datasource_id": 2, "pattern": "user:1001:*" }
```

模糊查询走 `SCAN` 游标遍历，**绝不使用 `KEYS`**（`internal/redisq/safety_test.go`
通过解析源码强制这条规则，有人写 `Keys()` 会直接测试失败）。前缀去掉分隔符后不足 2 个字符
的模式会被拒绝，避免退化成接近全库扫描。

数据源配置里 `database` 字段填 Redis 的 **db 号**（如 `37`）。

## 数据源凭据

每个数据源在界面上单独填写 **连接账号** 和 **连接密码**，存入 `datasource.password`。

> ⚠️ **密码以明文保存，不做加密。**

这是一个明确的取舍，不是遗漏。平台必须把明文密码交给驱动去连目标库，
应用层加密只增加一步解密，挡不住拿到整机权限的人（密钥通常和数据库在同一台机器）。
加密仅在「数据库被单独拖走」这一种场景下有防护价值，而代价是密钥管理复杂度。

所以这里如实明文存储，把保护责任放到平台外部：

- **平台元数据库必须做访问控制与网络隔离**
- **备份与 dump 文件要加密**——`pg_dump` 出来的文件里就有明文密码
- **每个数据源使用只读账号**，绝不用业务主账号
- 列表接口不返回密码（只有 `has_password`）；编辑单条数据源时详情接口返回明文，直接填进编辑框

**残余风险，部署前请确认能接受：**

- 任何能读 `datasource` 表的人（含 DBA、能拿到备份的人）都能直接看到生产库密码
- 审计日志能证明「谁在何时用了某数据源」，但无法证明「密码没有被读走」
- 如果平台库密码与业务库复用，一次平台库泄露等于业务库一起泄露

**如果将来要改为加密存储**：把 `password` 的编解码收拢到一层 `CredentialCodec`
接口（`Encode`/`Decode`），当前实现是恒等变换。切到 AES 只需替换实现，
并在列值上加版本前缀（如 `v2:<base64>`）以区分新旧格式，可逐行迁移。

### 密码回显只发生在单条详情接口

| 接口 | 是否返回密码 |
|---|---|
| `GET /api/v1/datasources`（列表） | **否**，只有 `has_password: true/false` |
| `GET /api/v1/datasources/:id`（详情） | **是**，明文 `password` 字段 |

分开的理由很简单：列表一次返回全部数据源，回显密码等于一次响应就把全库凭据带到浏览器；
而编辑只需要一条，所以只在详情接口回显。

两个接口都限定 `dba` / `superadmin`。能拿到明文的人本来就有权改掉这条密码，
所以回显并不放宽权限边界——它只是省去了管理员翻密码本这一步。

模型层用单独的序列化字段 `PlainPassword`（`json:"password,omitempty"`）承载回显，
`Password` 本身永久 `json:"-"`。只有显式调用 `WithPlainPassword()` 的接口才会带出密码，
「哪个接口会吐密码」在代码里一眼可查。

编辑时密码框里是当前明文：
- 直接改 → 新密码写入
- **清空 → 保留原密码**，不会把已配好的密码抹掉
- 没有配置密码的数据源，详情响应里就没有 `password` 字段

### 测试连接

新增和编辑数据源时，弹窗底部有「测试连接」按钮，会用当前表单值实际连一次目标库。

- **不写库**：不创建记录、不产生授权，只回答「这套配置能否连上」
- **编辑时可留空密码**：没重填密码就用库里已存的密码解密后测试，不用每次重新输
- **限时 10 秒**：目标不可达时不会让前端一直转圈
- **错误已翻译**：认证失败、连接超时、端口不通、库不存在会给出中文说明，
  而不是把驱动原文直接抛出来
- **仅 DBA 可用**：否则等于给所有登录用户开了一个内网端口探测器

**MongoDB 的探测额外执行一次 `listCollections`。** 只做 `Ping` 是不够的：在开启认证的实例上，
未认证的 `Ping` 依然返回成功，直到执行真实命令才报 `Unauthorized`，那样「测试连接」会给出错误的通过结论。

### 各类型对连接字段的要求

| 类型 | 账号 | 密码 | 库名 / db 号 | 认证库 |
|---|---|---|---|---|
| PostgreSQL | 必填 | 必填 | **必填** | — |
| MySQL | 必填 | 必填 | 可选 | — |
| MongoDB | 必填 | 必填 | **必填** | 留空 = `admin`（与 mongosh 一致）；业务库账号填该库名 |
| Redis | **可选** | 视实例而定 | 可选（默认 db 0） | — |

**为什么只有 PostgreSQL 必须填库名：** 它的连接串是
`postgres://user:pw@host:port/DBNAME`，库名是连接的一部分，留空会连到与用户同名的库
并直接失败。
MySQL 不一样，DSN 为 `user:pw@tcp(host:port)/DBNAME`，库名留空表示「连接但不选库」，
查询时写 `库名.表名` 完全可用，所以不该强制填写。

**Redis 支持 ACL 账号。** 早期版本只传密码，填了账号也会被静默丢掉，
遇到 Redis 6+ ACL 或云托管实例时表现为「测试连接失败，且报错和账号毫无关系」。
现在会同时传 username；只有 `requirepass` 的老实例留空即可。

**MongoDB 的「认证库」不是「默认库」。** MongoDB 的账号归属于某个库：

- 建在 `admin` 的账号是全局管理员 → **认证库留空即可**（平台按 `admin` 认证）
- 建在业务库的账号只对该库有效 → 认证库填该库名

默认值是 `admin`，与 MongoDB 官方工具一致：驱动的 URI 不带 `authSource` 时
用 `admin`，`mongosh --authenticationDatabase` 的默认值也是 `admin`。
连接串路径里的库只决定「连上后默认切到哪个库」，不参与认证。

两者混淆的代价很大：认证库填错时服务端**一律返回 `AuthenticationFailed`**，
表现就是「认证失败，请检查账号和密码」，而密码其实完全正确。
现在"测试连接"失败时会把**实际使用的认证库**写进提示，直接对照即可：

> 认证失败：账号或密码不正确。当前未指定认证库，平台按默认的 admin 认证（与 mongosh 一致）。
> 若该账号是建在业务库里的专属用户，请把「认证库」填成那个库名

### 高级连接参数

云实例、副本集、TLS 这些场景，光有 host/port/user/password 是不够的。
表单底部有「高级连接参数」折叠面板，键值以白名单方式透传给驱动：

| 类型 | 支持的参数 |
|---|---|
| MongoDB | `replicaSet`、`directConnection`、`tls`、`tlsCAFile`、`tlsInsecure`、`readPreference`、`authMechanism`、`appName` |
| PostgreSQL | `sslmode`（`disable` / `allow` / `prefer` / `require` / `verify-ca` / `verify-full`） |
| MySQL | `tls`（`true` / `false`） |

**白名单而不是任意透传：** 这些参数会改变连接语义，未知键直接报错而不是静默忽略——
用户填了 `replicaSet` 却没生效，比明确报「不支持该参数」更难排查。

**MongoDB 的 host 支持多个成员**，逗号分隔（`10.0.0.5,10.0.0.6`），
端口统一用「端口」字段；单独指定某个成员的端口可写 `10.0.0.5:27018`。
IPv6 必须加方括号（`[::1]` 或 `[::1]:27017`）：裸写 `::1:27017` 无法区分
最后一段是端口还是地址，驱动会当成非法地址。

**PostgreSQL 的 `sslmode` 之前硬编码成 `disable`**，云厂商的 PG（RDS / ApsaraDB / TencentDB）
基本强制 SSL，一连接就被拒。现在默认仍是 `disable`（内网直连最常见），但可以覆盖。

数据库类数据源请配置**只读账号**，不要用业务主账号。平台虽然只允许 `SELECT` 类语句，
但一个拥有写权限的账号一旦出现绕过，代价是全库级的。

### MongoDB 查询

MongoDB 不支持 SQL，查询接口是 `POST /api/v1/query/mongo`，支持两种输入形式：

**一、集合 + 过滤条件**（推荐，非技术人员也能用）

```json
{
  "datasource_id": 3,
  "database": "shop",
  "collection": "users",
  "filter": "{\"status\":\"active\"}"
}
```

**二、原生命令**

```json
{
  "datasource_id": 3,
  "database": "shop",
  "command": "db.users.find({\"status\":\"active\"}).limit(20)"
}
```

允许的命令只有 `find`、`findOne`、`countDocuments`、`count`、`distinct`、`aggregate`。
写入类操作（`insert*`、`update*`、`replace*`、`delete*`、`remove`、`drop`、`createIndex` 等）
在**解析阶段**就被拒绝，不会到达数据库；`aggregate` 中的 `$out`、`$merge` 同样拦截——
这两个阶段会把结果写回集合，本质是写操作。

**鉴权按库判定**，object 形如 `ds:<数据源ID>:<库>:<集合>`。而授权粒度是数据源：
审批通过后该数据源下任意库、任意集合都可查，因此不需要为每个集合单独申请。

**类型采用白名单而不是黑名单。** MongoDB 的命令会随版本增加，黑名单漏掉一个就是一次写权限泄漏。

### 申请的两道校验

提交申请时后端会依次校验，任何一条不满足都拒绝：

1. 数据源已开启 `apply_enabled`
2. 申请天数在 `1 ~ max_apply_days` 之间

**申请时不需要选库**：申请粒度就是数据源本身。审批通过即拥有该数据源下
所有库、所有表的查询权限，落库 scope 固定为 `*`，生成的策略是 `ds:<id>:*:*`
（与 `dba` / `superadmin` 的内置策略 `ds:*:*:*` 同构，只是数据源被钉死）。

不再逐库申请的理由：库里有什么表由数据库决定，用户填的库名可能拼错，
而拼错的表现是「审批通过后换个库就 403」——双方都看不出原因；
逐库申请还会让查两个库的人多跑一遍审批，而它给不出任何更强的安全保证。

`datasource.apply_scopes` 列已废弃（保留历史值，读取侧忽略）。
改口径之前审批的库级授权（`<库>:*`）仍然兼容，鉴权侧不需要区分；
迁移 `0008` 会把当前生效的授权统一提升为 `*`（已撤销与已过期的不动，
历史申请单的审批留痕也不改）。

因为申请粒度是整个数据源，`days` 决定的有效期要按实际需要批——早期版本里
审批只给 7 天，导致授权很快全部过期，表现为「数据源管理里有数据、查询页却为空」，
排查时容易误判成权限失效。

**审批人不能审批自己的申请**，前后端都有拦截。单人 DBA 团队需要另一个 DBA 或 superadmin 来批。

### 授权什么时候失效

授权记录带 `expires_at`，但 Casbin 的策略在**内存里**。早期只在启动和「审批通过」两个
时刻重建策略，于是：

- 一条 7 天的授权，过期后**依然可用**，直到下次重启或有人审批 —— 这是真实的越权窗口
- 吊销更是完全没有入口：`GrantStore.Revoke` 写了，但没有 HTTP 路由

现在两条路径都补上了：

| 场景 | 触发方式 | 生效延迟 |
|---|---|---|
| 到期 | 后台定时重建策略（`AUTHZ_REFRESH_SEC`，默认 60 秒） | ≤ 1 分钟 |
| 立即收回 | 「授权管理」页点「收回」→ `POST /grants/:id/revoke` | 立即（同步重建策略） |

定时重建走的是同一条 `syncAuthz`：按 `grant_record` 里
`revoked_at IS NULL AND expires_at > now()` 的记录全量重建，所以过期和吊销都会被清掉。

注意 `SyncGrants` 会先清空 p 段再重建，内置的 `dba` / `superadmin` 策略不在数据库里，
会被一起清掉。所以凡调用 `syncAuthz` 之后都必须补一次 `reAddBuiltinPolicies`，
否则刷新一次策略，DBA 自己就查不了任何数据了。

### 可申请配置示例

管理员在数据源管理页配置：

```json
{
  "apply_enabled": true,
  "max_apply_days": 14
}
```

**没有范围字段**：开放申请即代表审批通过后拥有该数据源下所有库表。
（旧版字段 `apply_scopes` 已废弃，仍可传入但会被忽略；存量数据只作为历史对照保留。）

查询请求体示例：

```json
{
  "datasource_id": 1,
  "database": "public",
  "tables": ["orders"],
  "sql": "SELECT id, phone FROM orders",
  "row_limit": 100
}
```

`tables` 字段是**选填**的，只写入审计明细便于事后追溯，不参与鉴权。鉴权粒度是
「数据源 + 库」（Casbin object 为 `ds:<id>:<库>:*`）：只要对该库有 read 权限，
库下所有表都可以查。表清单不能作为权限依据——SQL 实际访问哪些表由数据库决定，
不由请求体决定，按请求体里的表名鉴权等于允许用户少报一张表绕过。

## 技术栈

| 层 | 选型 |
|---|---|
| 后端 | Go 1.27+ / Gin |
| 元数据库 | PostgreSQL 16 |
| 数据源驱动 | pgx v5（PostgreSQL）、go-sql-driver（MySQL）、go-redis v9、mongo-driver v2 |
| 权限 | Casbin v2 |
| 密码 | bcrypt（`golang.org/x/crypto`）+ golang-jwt v5 |
| 前端 | React 18 + TypeScript + Ant Design 5 + CodeMirror 6 + Vite |

**没有 Trino。** 早期规划里写过「基于 Trino 的跨库跨实例联邦查询」，但代码中没有任何
Trino 依赖或实现，跨库查询目前只能靠用户自己在一条 SQL 里写 `库名.表名`
（同一数据源内）。请不要按 README 找 Trino 相关配置——它不存在。

## 快速开始

### 前置要求

- Go 1.27 或更高版本（`go.mod` 声明为 `go 1.27.4`；[下载](https://go.dev/dl/)，选 `.msi` 安装包）
- PostgreSQL 16
- Redis 7

### 配置

```bash
# 1. 复制环境变量模板
cp .env.example .env.local

# 2. 编辑 .env.local，填入你的数据库连接信息
#    DATABASE_URL            应用运行库
#    TEST_DATABASE_URL       测试专用库（测试会建表删表，不能与 DATABASE_URL 相同）
#    TEST_REDIS_ADDR         格式 host:port/db，请使用独立 db

# 3. 加载环境变量（仅运行测试时需要；启动服务会自动读取 .env.local）
source .env.local

# 4. 准备测试库
createdb -h HOST -U USER datawarden_test
```

### 运行后端

服务启动时会自动读取工作目录下的 `.env.local`（其次 `.env`），
因此在 LiteIDE / VS Code 里直接点运行也能拿到配置，无需手动加载。
进程环境中已存在的变量优先级更高，不会被文件覆盖。

```bash
# 启动服务（会自动执行数据库迁移）
go run ./cmd/server

# 运行全部测试
go test ./...

# 只跑不需要外部依赖的测试（跳过 PG / Redis 集成测试）
go test ./... -short
```

服务启动后默认监听 `:8080`。

### 运行前端

```bash
cd web
npm install
npm run dev
```

访问 http://localhost:5173 。Vite 已配置 `/api` 代理到 `localhost:8080`，无需处理跨域。

首次访问需要先有一个账号登录。系统不内置默认账号，推荐用现成的初始化脚本：

```bash
psql -h <host> -p 5432 -U <superuser> -f scripts/init.sql
```

该脚本会创建库、表结构与一个初始 `superadmin` 账号，整体幂等可重复执行
（重跑会重置初始账号密码）。也可手工插入：

```sql
-- 用 bcrypt 生成密码哈希后插入
INSERT INTO app_user (username, email, password_hash, role)
VALUES ('admin', 'admin@example.com', '<bcrypt-hash>', 'superadmin');
```

拿到 superadmin 后，后续账号都可以在「用户管理」页面创建，不需要再碰 SQL。

### 常用流程

1. **准备超级管理员**：按上节插入第一个 `superadmin` 账号（或用 `scripts/init.sql`）
2. **管理员建组与账号**：用户管理 → 新建用户，选择角色与所属组；初始密码只展示一次
3. **DBA 配置数据源**：数据源管理 → 新增，勾选「开放申请」，设置最长天数
4. **开发者申请**：申请授权 → 选择数据源 → 填申请天数与理由 → 提交（不用选库）
5. **DBA 审批**：授权审批 → 通过（可填意见）→ 授权立即生效，无需重启
6. **开发者查询**：数据查询 → 选数据源与库 → 执行（库下所有表都可查，无需选表）

> 审批天数决定授权有效期，到期后靠 `AUTHZ_REFRESH_SEC`（默认 60 秒）内的定时重建回收。

生产构建：

```bash
cd web && npm run build   # 产物在 web/dist
```

### 冒烟验证

```bash
# 1. 未带令牌应返回 401
curl -s -o /dev/null -w "%{http_code}\n" http://localhost:8080/api/v1/datasources

# 2. 登录换令牌
TOKEN=$(curl -s -X POST http://localhost:8080/api/v1/login \
  -H "Content-Type: application/json" \
  -d '{"username":"dba1","password":"<你的密码>"}' | jq -r .token)

# 3. 查看数据源
curl -s http://localhost:8080/api/v1/datasources -H "Authorization: Bearer $TOKEN"
```


## 文档

设计文档与实现计划属于内部资料，不随代码公开。

对外文档将随功能开发逐步补充到 `docs-public/` 目录。

## 安全须知

本项目会连接生产数据库，部署前请务必了解以下约束：

**当前已生效：**

- **数据源密码明文存储**，平台元数据库必须做访问控制与网络隔离，备份文件需加密（见上文「数据源凭据」）
- **Redis 查询绝不使用 `KEYS` 命令**，只允许 `SCAN` 游标；这条约束由自动化测试护栏保证
- **不要用生产库做测试库**，测试会执行建表和删表
- **账号管理只对 superadmin 开放**，dba 可通过改数据源与审批间接获得很多能力，不能再给它改账号的权限

**阶段二才涉及（当前代码中不存在，部署时不必准备）：**

- **临时账号方案**需要平台在生产库持有 `CREATE USER` / `DROP USER` 权限，这是风险集中点。账号名必须可反查工单，凭据阅后即焚，到期撤销失败必须重试并告警
- **行镜像存储生产数据副本**，平台元数据库会承载生产敏感数据。该库的安全等级、备份策略必须与业务库对齐，镜像内容需应用层加密后落库

### 账号删除是逻辑删除

删除用户**不会**物理删除数据库行，只把 `status` 置为 `deleted`，
并把用户名改写为 `<原名>#deleted#<id>`：

- 历史授权（`grant_record`）、审批单（`grant_request`）、审计日志（`audit_log`）
  全部保留，且只按 `user_id` / `actor_id` 关联。用户名复用不会让历史记录认错人
- 用户名被释放，可以立即分配给新账号
- 查看被删除的账号：`SELECT * FROM v_deleted_user;`（视图会还原出原名）
- **不要**用 `DELETE FROM app_user` 清理账号——那会因外键失败，
  或者在有级联的库上连带删掉审计口径
- 已删除账号的授权同时从 `ActiveGrants`（策略重建来源）与 `ListActive`
  （授权管理页）里排除，所以「人删了、权限也真的没了」

### 停用与删除不是即时踢出

停用或删除只修改 `status`，随后立即重建 Casbin 策略
（操作即时刷新 + `AUTHZ_REFRESH_SEC` 定时刷新两条路径），
所以**数据查询**会立刻 403。

但已签发的 JWT 在 12 小时有效期内仍能通过鉴权中间件
（中间件只验签，不查库），期间仍可访问不依赖 Casbin 的接口。
需要即时踢出，目前只能等令牌过期；引入令牌版本号或黑名单是后续独立需求。

### 账号管理只对超级管理员开放

`/api/v1/users` 与 `/api/v1/groups` 全部路由都挂 `requireSuperAdminMiddleware`，
`dba` 访问一律 403。原因：dba 已能改数据源与审批，若再给账号管理，
dba 可以把自己提成 superadmin，角色分级就形同虚设。

账号管理不走 Casbin 而直接判角色——Casbin 的策略主体来自 `app_user.role`，
用它来判断「谁有权改 `app_user`」会形成循环依赖：策略重建一旦出错，
连修账号的入口都会一起消失。

### 组不承载数据权限

组是纯归类的组织单元（类似部门），**不生成任何 Casbin 策略**。
用户能查什么，只取决于它自己的 `grant_record`。要改成「按组授权」，
必须同时改 `querysvc` 的鉴权与 `SyncGrants` 的策略重建，否则会出现
「界面上看起来有权、实际查不了」的分裂。

## 数据库迁移

迁移脚本在 `internal/db/migrations/`，**编号全局递增，只增不改**。
`db.Migrate` 没有版本表，靠脚本自身幂等来保证可重复执行，
所以新写的迁移必须写成 `IF NOT EXISTS` / `ADD COLUMN IF NOT EXISTS` 风格。

当前共 9 个迁移，新增前先 `ls internal/db/migrations` 确认编号没被占用
（`0003` 曾被复用过一次，是踩过的坑）：

| 编号 | 内容 |
|---|---|
| `0001` | 初始表结构 |
| `0002` | 可申请配置 |
| `0003` | 数据源凭据 |
| `0004` | 明文密码列 |
| `0005` | 数据源认证库 |
| `0006` | 数据源高级连接参数 |
| `0007` | 库级授权范围（`apply_scopes` 随后废弃） |
| `0008` | 授权范围提升为整数据源 |
| `0009` | 用户组与账号状态 |

## 贡献

提交前请确认（唯一权威来源是 `CONTRIBUTING.md`，冲突以它为准）：

1. 没有把 `.env.local` 或任何含真实凭据的文件加入暂存区
2. `go build ./...`、`go vet ./...`、`go test ./...` 通过；改动前端时
   `cd web && npm run lint && npm run build` 也通过
3. 新增的敏感配置项同步更新 `.env.example`（只写占位符）
4. 触及鉴权时：`syncAuthz` 后补了 `reAddBuiltinPolicies`，权限判定在
   `querysvc` / handler 而非 `adapter`
5. 新增逻辑带了测试；Redis 测试 key 带 `dwtest:` 前缀
6. **没有整体 `gofmt -w`**——仓库文件是 CRLF，整体格式化会产生全文件 diff
7. 每完成一项任务立即 `git commit`，但**不自动 push**

## 许可证

待定。
