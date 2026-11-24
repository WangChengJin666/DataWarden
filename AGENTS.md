# AGENTS.md

## 项目概述

**DataWarden** —— 面向研发团队的数据审计工单平台，为开发人员提供生产数据库的受控只读查询能力。
Go + Gin 单体后端（`cmd/server`）+ React SPA 前端（`web/`），元数据库为 PostgreSQL。

当前状态：阶段一（地基 + 只读查询 + Redis 通道）已交付；变更工单、回滚、临时账号属于阶段二，**尚未开始**。

不显然的依赖：

| 依赖 | 用途 |
|---|---|
| `casbin/v2` | 权限策略，object 形如 `ds:<数据源ID>:<库>:<表>` |
| `pgx/v5` | 平台元数据库连接池（非业务库） |
| `go-redis/v9` | Redis 查询通道 |
| `mongo-driver/v2` | MongoDB 查询通道（go.mod 中为 indirect，实际使用） |
| `golang-jwt/v5` + `x/crypto/bcrypt` | 登录与密码哈希 |
| Ant Design 5 + CodeMirror 6 | 前端 UI 与 SQL 编辑器补全 |

Trino 联邦查询在 README 中列为规划，**代码里没有实现**。

## 常用命令

```bash
# 后端
cp .env.example .env.local     # 首次；.env.local 含真实凭据，绝不提交
go run ./cmd/server            # 启动，自动执行 internal/db/migrations 下的迁移
go build ./...                 # 编译
go test ./...                  # 全量测试（PG/Redis 集成测试未配置 env 时自动 Skip）
go test ./... -short           # 只跑无外部依赖的单元测试
go vet ./...

# 前端（在 web/ 下）
npm install
npm run dev        # Vite :5173，/api 已代理到 :8080
npm run lint       # 实际是 tsc --noEmit，不是 ESLint
npm run build      # tsc -b && vite build，产物 web/dist
```

陷阱：

- **`.env.local` 需要 `source` 才影响测试**；服务启动时会自己读文件，但 `go test` 不会。
- `TEST_DATABASE_URL` 必须与 `DATABASE_URL` 不同，测试会建表删表。
- Redis 测试连 `TEST_REDIS_ADDR`（格式 `host:port/db`），所有测试 key 必须带 `dwtest:` 前缀。
- 仓库根目录与 `.run/` 下有已编译的 `server.exe` / `datawarden.exe`，属于本地产物，不要提交。

## 目录结构

```
cmd/server/main.go        # 唯一入口：配置 → 迁移 → 依赖装配 → 路由 → Casbin 定时刷新
internal/
  config/                 # 环境变量加载
  db/                     # pgxpool + migrations/*.sql（编号递增，只增不改）
  model/                  # 领域模型；Role.IsPrivileged() 是角色判定唯一入口
  store/                  # 元数据库读写：user / datasource / grant / grant_request
  auth/                   # JWT 签发
  authz/                  # Casbin 封装、内置策略、授权 scope 解析
  adapter/                # 业务库连接层：mysql / postgres / mongo / probe，不含权限判定
  querysvc/               # 查询编排：鉴权 → 只读校验 → 限行 → 执行 → 脱敏 → 审计
  redisq/                 # Redis 查询与 SCAN 安全护栏
  audit/                  # 审计落库
  httpapi/                # Gin handler + middleware + router，每类 handler 配同名 _test.go
  testutil/
web/src/
  api/client.ts           # axios 实例、token 存取、全部接口封装
  pages/                  # Login / Query / Apply / Review / Datasource / Grants 六个页面
  AppLayout.tsx           # 侧边栏，菜单按角色渲染
scripts/init.sql          # 手工初始化脚本（迁移之外的辅助）
docs/superpowers/         # plans / specs，内部设计资料
```

新增一个后端功能的落位顺序：
`model`（数据结构）→ `internal/db/migrations/NNNN_*.sql`（若需改表）→ `store`（持久化）→
业务服务（`querysvc` 等）→ `authz`（若涉及新权限）→ `httpapi/xxx_handler.go` + `router.go` 注册 →
`web/src/api/types.ts` → `web/src/api/client.ts` → `web/src/pages/*.tsx`。

## 代码规范

**唯一权威来源是 `CONTRIBUTING.md`**，冲突以它为准。`README.md` 补充了架构说明与安全须知。

| 文档 | 核心要求 |
|---|---|
| `CONTRIBUTING.md` 提交时机 | 每完成一项任务**立即 commit**，不询问；一个任务一个 commit；禁止自动 push |
| `CONTRIBUTING.md` 提交前自查 | 逐行看 `git diff --staged`，禁止提交 `.env.local`、真实连接串、JWT 密钥、内网地址 |
| `CONTRIBUTING.md` 分支与提交信息 | `feat/<简述>`、`fix/<简述>`；Conventional Commits（feat/fix/docs/refactor/test/chore） |
| `CONTRIBUTING.md` 代码规范 | `go vet` 通过；新增逻辑必须有测试；注释用中文，公开函数必须写用途说明 |
| `CONTRIBUTING.md` 安全红线 | SQL 必须参数化；禁止 Redis `KEYS`/`FLUSHALL`/`FLUSHDB`；权限判定只在 `querysvc`，不在 adapter |
| `SECURITY.md` | 漏洞报告流程 |
| `.editorconfig` | UTF-8 / LF，Go 用 tab，前端 2 空格，Markdown 保留行尾空格 |

## 项目特定注意

- **换行符是 CRLF**：所有 `.go` 与前端源文件实际存为 CRLF，`.editorconfig` 写的是 LF。
  因此 `gofmt -l` 会把几乎所有文件都报出来——**绝不要整体 `gofmt`/统一换行**，只做局部 Edit。
- **`SyncGrants` 会清空整个 p 段**，内置 `dba`/`superadmin` 策略不在数据库里，会被一起清掉。
  凡调用 `syncAuthz` 之后**必须补一次 `reAddBuiltinPolicies`**，否则刷新一次策略管理角色就全线 403。
- **内置策略对象必须是四段 `ds:*:*:*`**（`authz.BuiltinPolicyObject`）。写成 `ds:*` 在 object 段数变化时会静默失效。
- **`internal/authz` 里 `model` 被 casbin 占用**，引用项目自己的 model 包必须用别名 `appmodel`。
- **Casbin 策略在内存里**，授权过期只体现在数据库，靠 `AUTHZ_REFRESH_SEC`（默认 60s）定时重建回收；
  吊销与审批走即时刷新路径。改动鉴权时别把定时刷新删掉。
- **角色判定统一走 `model.Role.IsPrivileged()`**，不要在别处重复写 `role == "dba" || role == "superadmin"`。
- **权限判定必须在 `querysvc` / handler 层，不能在 `adapter` 层**——adapter 只管连库。
- **路由注册顺序**：`/datasources/test` 必须写在 `/datasources/:id` 之前，否则 `test` 会被当成 id。
- **开发者与 DBA 的数据源接口是两套**：`GET /datasources` 仅 DBA（返回 host/port/username），
  开发者用 `GET /grants/mine` 拿自己可查的数据源。
- **数据源密码以明文存 `datasource` 表**，没有应用层加密（`DATAWARDEN_MASTER_KEY` 已废弃）。
  平台元数据库权限与备份加密必须按此约束设计。
- **SQL 查询请求的 `tables` 是必填的**，平台据此逐表鉴权；缺表时 `querysvc` 返回 `ErrNoTables`，这是唯一校验点。
- **Redis 前缀授权向下覆盖**：对目标 key 逐级向上生成候选，任一命中即放行；`redisq/safety_test.go`
  通过解析源码强制"不使用 `KEYS`"，有人写了会直接测试失败。
- **MongoDB 用白名单而非黑名单**校验命令，`$out`/`$merge` 在解析阶段拒绝。
- 本地诊断可用临时 Go 程序走 pgxpool，用完删除临时目录，不要留在仓库里。

## 自检清单

提交前逐条确认：

1. `go build ./...`、`go test ./...` 通过；改动前端时 `cd web && npm run lint && npm run build` 也通过。
2. `git status` 逐个看过未跟踪文件，没有 `.env.local`、账号记录、临时诊断程序、`*.exe`。
3. 没有整体格式化（`gofmt -w`）带来的无关行变更。
4. 触及鉴权时：`syncAuthz` 后补了 `reAddBuiltinPolicies`，权限判定在 `querysvc`/handler 而非 adapter。
5. 新增 SQL 全部参数化，没有字符串拼接。
6. 没有引入 Redis `KEYS` / `FLUSHALL` / `FLUSHDB`。
7. 新增逻辑带了测试；Redis 测试 key 带 `dwtest:` 前缀。
8. 公开函数有中文注释，说明了"为什么"而不只是"做什么"。
9. 已 `git commit`（一个任务一个），**未 push**。
