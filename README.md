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
| 数据源管理 | ✅ | 增删改查，仅 DBA 可操作 |
| 可申请数据源配置 | ✅ | 按数据源设置是否开放、最长天数、可申请库表范围 |
| 授权申请与审批 | ✅ | 提交 → DBA 审批 → 授权即时生效；禁止自审 |
| PostgreSQL / MySQL 受控查询 | ✅ | 只读白名单、强制限行、超时中断 |
| MongoDB 受控查询 | ✅ | 集合+过滤条件或原生命令，写操作与 `$out`/`$merge` 在解析阶段拒绝 |
| Redis 查询通道 | ✅ | 精确 key + `SCAN` 模糊 key，绝不使用 `KEYS` |
| 查询授权与期限 | ✅ | 授权记录带过期时间，Casbin 按活跃授权重建 |
| 结果脱敏 | ✅ | 手机号、身份证、邮箱、银行卡按列名关键字匹配 |
| 全链路审计 | ✅ | 查询、数据源变更、审批动作全部留痕，表级触发器禁止篡改 |
| Web 界面 | ✅ | React 18 + Ant Design，四个页面，菜单按角色动态渲染 |

## 界面

前端为 React 18 + Ant Design，四个页面：

| 页面 | 可见角色 | 功能 |
|---|---|---|
| 数据查询 | 全部 | 选择数据源执行只读查询，结果自动脱敏 |
| 申请授权 | 全部 | 查看可申请的数据源、提交申请、查看自己的申请进度 |
| 数据源管理 | 全部可见，仅 DBA 可改 | 登记数据源，配置「是否开放申请 / 最长天数 / 可申请库表范围」 |
| 授权审批 | DBA / superadmin | 审批申请，通过后授权立即生效 |

侧边栏菜单按角色动态渲染：开发者看不到「授权审批」。

## API

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| POST | `/api/v1/login` | 公开 | 换取 JWT |
| GET | `/api/v1/datasources` | 已登录 | 数据源列表 |
| GET | `/api/v1/datasources/:id` | 已登录 | 数据源详情 |
| POST | `/api/v1/datasources` | dba / superadmin | 登记数据源（含可申请配置） |
| PUT | `/api/v1/datasources/:id` | dba / superadmin | 修改数据源与可申请配置 |
| DELETE | `/api/v1/datasources/:id` | dba / superadmin | 删除数据源 |
| POST | `/api/v1/datasources/test` | dba / superadmin | 测试连接（不写库） |
| POST | `/api/v1/query/sql` | 需表级授权 | 执行只读查询 |
| POST | `/api/v1/query/redis` | 需 key 前缀授权 | 执行 Redis 查询（精确 key 或 SCAN 模式） |
| POST | `/api/v1/query/mongo` | 需集合级授权 | 执行 MongoDB 查询（集合+过滤 或 原生命令） |
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
- 密码不通过任何接口返回，前端只能看到 `has_password`

**残余风险，部署前请确认能接受：**

- 任何能读 `datasource` 表的人（含 DBA、能拿到备份的人）都能直接看到生产库密码
- 审计日志能证明「谁在何时用了某数据源」，但无法证明「密码没有被读走」
- 如果平台库密码与业务库复用，一次平台库泄露等于业务库一起泄露

**如果将来要改为加密存储**：把 `password` 的编解码收拢到一层 `CredentialCodec`
接口（`Encode`/`Decode`），当前实现是恒等变换。切到 AES 只需替换实现，
并在列值上加版本前缀（如 `v2:<base64>`）以区分新旧格式，可逐行迁移。

### 密码在接口上永远不可见

`GET /api/v1/datasources` 只返回 `has_password: true/false`，不返回密码。
编辑数据源时密码框留空表示**不修改**现有密码，不会把它清空。

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

| 类型 | 账号 | 密码 | 库名 / db 号 |
|---|---|---|---|
| PostgreSQL | 必填 | 必填 | **必填** |
| MySQL | 必填 | 必填 | 可选 |
| MongoDB | 必填 | 必填 | **必填** |
| Redis | **可选** | 视实例而定 | 可选（默认 db 0） |

**为什么只有 PostgreSQL 必须填库名：** 它的连接串是
`postgres://user:pw@host:port/DBNAME`，库名是连接的一部分，留空会连到与用户同名的库
并直接失败。

MySQL 不一样，DSN 为 `user:pw@tcp(host:port)/DBNAME`，库名留空表示「连接但不选库」，
查询时写 `库名.表名` 完全可用，所以不该强制填写。

**Redis 默认只用密码认证**（`requirepass`），没有账号概念，界面上留空即可。
只有一种情况需要填账号：实例启用了 ACL 且使用的不是 `default` 用户。

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

**鉴权按集合判定**，object 形如 `ds:<数据源ID>:<库>:<集合>`，与 SQL 的表级授权同构。
所以申请权限时 scope 写 `库名:集合名`。换集合名就等于换授权对象，不存在「授权一个集合却能查另一个」的情况。

**类型采用白名单而不是黑名单。** MongoDB 的命令会随版本增加，黑名单漏掉一个就是一次写权限泄漏。

### 申请的三道校验

提交申请时后端会依次校验，任何一条不满足都拒绝：

1. 数据源已开启 `apply_enabled`
2. 申请天数在 `1 ~ max_apply_days` 之间
3. 申请的库表范围命中 `apply_scopes` 白名单

**审批人不能审批自己的申请**，前后端都有拦截。单人 DBA 团队需要另一个 DBA 或 superadmin 来批。

### 可申请配置示例

管理员在数据源管理页配置：

```json
{
  "apply_enabled": true,
  "max_apply_days": 14,
  "apply_scopes": [
    { "database": "public", "tables": ["dw_probe"] },
    { "database": "shop", "tables": [] }
  ]
}
```

`tables` 为空数组表示该库下**全部表**都可申请。`apply_scopes` 为空则任何人都申请不了。

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

`tables` 字段是**必填**的，平台据此逐表鉴权。任一表无权限则整条查询拒绝，且不提示是哪张表。

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

# 3. 加载环境变量（每个新终端都要执行）
source .env.local

# 4. 准备测试库
createdb -h HOST -U USER datawarden_test
```

### 运行后端

```bash
# 启动服务（会自动执行数据库迁移）
source .env.local
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
4. **开发者查询**：数据查询 → 选数据源、填库名与涉及的表 → 执行

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
