# DataWarden

面向研发团队的数据审计工单平台。为开发人员提供生产数据库的受控查询与变更能力，支持 MySQL、PostgreSQL、Redis、MongoDB 四类数据源，所有变更可回滚。

## 核心特性

- **四类数据源**：MySQL、PostgreSQL、Redis、MongoDB
- **两种查询授权**：平台内限时申请数据源、申请可直连的临时账号
- **变更工单**：DBA 审批后执行，执行前生成行镜像，支持回滚
- **跨库跨实例查询**：基于 Trino 的联邦查询（Redis 走独立通道）
- **全链路审计**：所有操作留痕，审计表数据库层面不可篡改

> 上表是完整产品目标。当前仅**查询类能力**已落地，变更工单与临时账号属于后续阶段。

> **项目状态**：阶段一（地基 + 只读查询 + Redis 通道）已完成，13 个 Task 全部交付并通过真实数据库验证。
> 变更工单与回滚（阶段二）尚未开始。

## 已实现能力（阶段一）

| 能力 | 状态 | 说明 |
|---|---|---|
| 用户登录与 JWT 鉴权 | ✅ | bcrypt 密码哈希，令牌含角色 |
| 角色权限（developer / dba / superadmin） | ✅ | Casbin 策略，库级 `*` 通配 |
| 数据源管理 | ✅ | 增删改查，仅 DBA / superadmin 可访问（列表与详情同样限权） |
| 可申请数据源配置 | ✅ | 按数据源设置是否开放、最长天数、可申请库表范围 |
| 授权申请与审批 | ✅ | 提交 → DBA 审批 → 授权即时生效；禁止自审 |
| PostgreSQL / MySQL 受控查询 | ✅ | 只读白名单、强制限行、超时中断 |
| MongoDB 受控查询 | ✅ | 集合+过滤条件或原生命令，写操作与 `$out`/`$merge` 在解析阶段拒绝 |
| Redis 查询通道 | ✅ | 精确 key + `SCAN` 模糊 key，绝不使用 `KEYS` |
| 查询授权与期限 | ✅ | 授权记录带过期时间，Casbin 按活跃授权重建 |
| 结果脱敏 | ✅ | 手机号、身份证、邮箱、银行卡按列名关键字匹配 |
| 全链路审计 | ✅ | 查询、数据源变更、审批动作全部留痕，表级触发器禁止篡改 |
| Web 界面 | ✅ | React 18 + Ant Design + CodeMirror 6，五个页面，菜单按角色动态渲染 |

## 界面

前端为 React 18 + Ant Design + CodeMirror 6，五个页面：

| 页面 | 可见角色 | 功能 |
|---|---|---|
| 数据查询 | 全部 | 选择数据源执行只读查询，结果自动脱敏；SQL 编辑器带表名/字段名补全 |
| 申请授权 | 全部 | 查看可申请的数据源、提交申请、查看自己的申请进度 |
| 数据源管理 | DBA / superadmin | 登记数据源，配置「是否开放申请 / 最长天数 / 可申请库表范围 / 高级连接参数」 |
| 授权审批 | DBA / superadmin | 审批申请，通过后授权立即生效 |
| 授权管理 | DBA / superadmin | 查看生效中的授权，可立即收回 |

侧边栏菜单按角色动态渲染：开发者只看到「数据查询」和「申请授权」，看不到 DBA 的三个页面；
直接访问 `/datasources` 会被重定向回「申请授权」，接口层同样返回 403。

### SQL 编辑器的补全

编辑器是 CodeMirror 6 + `@codemirror/lang-sql`，补全候选来自元数据接口：

1. **库**：进入页面或切换数据源时按 `GET /datasources/:id/databases` 拉取（后端按授权过滤）
2. **表**：选定库后按 `GET /datasources/:id/tables?db=` 拉取，页面上是**只读的表名清单**
   （点击表名即复制）。它不参与鉴权（粒度是数据源 + 库），也不再需要用户勾选
3. **字段**：选定库后按 `GET /datasources/:id/schema?db=` 一次取回整库结构，
   输入 `别名.` / `表名.` 时直接查表给出该表字段

编辑器只在**带点号的限定前缀**之后给候选（`t.`、`app_user.`、`public.app_user.`），
`SELECT` 之后或 `FROM` 之后不再自动弹表名 / 字段：没有 FROM 时字段候选只能是整库并集，
上千条无从选择，表名则由右侧清单负责。Ctrl+Space 也只在限定前缀处有作用。

字段接口对 MongoDB 走「采样一条文档取键名」——Mongo 没有列定义，只能采样；
空集合取不到字段，界面上会显示成「未采样」而不是报错。

**库表未授权时补全退回手填：** 账号只有单库权限时 `ListDatabases` 会失败，
前端不报错，只把下拉变成可自由输入，不阻断查询。

## API

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| POST | `/api/v1/login` | 公开 | 换取 JWT |
| GET | `/api/v1/datasources` | dba / superadmin | 数据源列表（只返回 `has_password`） |
| GET | `/api/v1/datasources/:id` | dba / superadmin | 数据源详情（含明文 `password`，供编辑框直接回显） |
| POST | `/api/v1/datasources` | dba / superadmin | 登记数据源（含可申请配置） |
| PUT | `/api/v1/datasources/:id` | dba / superadmin | 修改数据源与可申请配置 |
| DELETE | `/api/v1/datasources/:id` | dba / superadmin | 删除数据源 |
| POST | `/api/v1/datasources/test` | dba / superadmin | 测试连接（不写库） |
| POST | `/api/v1/query/sql` | 需表级授权 | 执行只读查询 |
| POST | `/api/v1/query/redis` | 需 key 前缀授权 | 执行 Redis 查询（精确 key 或 SCAN 模式） |
| POST | `/api/v1/query/mongo` | 需集合级授权 | 执行 MongoDB 查询（集合+过滤 或 原生命令） |
| GET | `/api/v1/datasources/:id/databases` | 已登录 | 列出可见的库（按授权过滤） |
| GET | `/api/v1/datasources/:id/tables?db=` | 已登录 | 列出库下可见的表（按授权过滤） |
| GET | `/api/v1/datasources/:id/columns?db=&table=` | 需表级授权 | 列出表的字段（供编辑器补全） |
| GET | `/api/v1/grants` | dba / superadmin | 列出生效中的授权 |
| POST | `/api/v1/grants/:id/revoke` | dba / superadmin | 立即收回一条授权 |
| GET | `/api/v1/grant/applyable` | 已登录 | 列出开放申请的数据源 |
| POST | `/api/v1/grant/requests` | 已登录 | 提交授权申请 |
| GET | `/api/v1/grant/requests/mine` | 已登录 | 我的申请列表 |
| GET | `/api/v1/grant/requests` | dba / superadmin | 按状态列出申请（默认 pending） |
| POST | `/api/v1/grant/requests/:id/approve` | dba / superadmin | 审批通过 |
| POST | `/api/v1/grant/requests/:id/reject` | dba / superadmin | 驳回（必须填原因） |
| POST | `/api/v1/grant/requests/:id/cancel` | 申请人本人 | 撤销待审申请 |

### Redis 查询

Redis 没有表的概念，鉴权用 key 前缀。申请范围写成 `<db号>:<key前缀>`，例如 `37:user:1001`，
平台会把它转成鉴权对象 `ds:<数据源ID>:37:user:1001`。

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
| 后端 | Go 1.22+ / Gin |
| 元数据库 | PostgreSQL 16 |
| 联邦查询 | Trino |
| 缓存与队列 | Redis |
| 权限 | Casbin |
| 前端 | React 18 + TypeScript + Ant Design 5 + Vite |

## 快速开始

### 前置要求

- Go 1.22 或更高版本（[下载](https://go.dev/dl/)，选 `.msi` 安装包）
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

首次访问需要先有一个账号登录。系统不内置默认账号：

```sql
-- 用 bcrypt 生成密码哈希后插入
INSERT INTO app_user (username, email, password_hash, role)
VALUES ('admin', 'admin@example.com', '<bcrypt-hash>', 'superadmin');
```

### 常用流程

1. **DBA 配置数据源**：数据源管理 → 新增，勾选「开放申请」，设置最长天数与可申请库表范围
2. **开发者申请**：申请授权 → 选择数据源 → 选范围、填天数与理由 → 提交
3. **DBA 审批**：授权审批 → 通过（可填意见）→ 授权立即生效，无需重启
4. **开发者查询**：数据查询 → 选数据源与库 → 执行（库下所有表都可查，无需选表）

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

- **临时账号方案**需要平台在生产库持有 `CREATE USER` / `DROP USER` 权限，这是风险集中点。账号名必须可反查工单，凭据阅后即焚，到期撤销失败必须重试并告警
- **行镜像存储生产数据副本**，平台元数据库会承载生产敏感数据。该库的安全等级、备份策略必须与业务库对齐，镜像内容需应用层加密后落库
- **Redis 查询绝不使用 `KEYS` 命令**，只允许 `SCAN` 游标；这条约束由自动化测试护栏保证
- **不要用生产库做测试库**，测试会执行建表和删表

## 贡献

提交前请确认：

1. 没有把 `.env.local` 或任何含真实凭据的文件加入暂存区
2. `go build ./...` 和 `go test ./...` 通过
3. 新增的敏感配置项同步更新 `.env.example`（只写占位符）

## 许可证

待定。
