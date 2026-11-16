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
| 数据源登记 | ✅ | 支持 MySQL / PostgreSQL / Redis / MongoDB 类型，仅 DBA 可操作 |
| PostgreSQL / MySQL 受控查询 | ✅ | 只读白名单、强制限行、超时中断 |
| Redis 查询通道 | ✅ | 精确 key + `SCAN` 模糊 key，绝不使用 `KEYS` |
| 查询授权与期限 | ✅ | 授权记录带过期时间，Casbin 按活跃授权重建 |
| 结果脱敏 | ✅ | 手机号、身份证、邮箱、银行卡按列名关键字匹配 |
| 全链路审计 | ✅ | 成功/失败/拒绝全部留痕，数据库触发器禁止篡改 |

## API

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| POST | `/api/v1/login` | 公开 | 换取 JWT |
| GET | `/api/v1/datasources` | 已登录 | 数据源列表 |
| POST | `/api/v1/datasources` | dba / superadmin | 登记数据源 |
| POST | `/api/v1/query/sql` | 需表级授权 | 执行只读查询 |

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
| 前端 | React 18 + TypeScript + Ant Design Pro |

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
#    DATABASE_URL          应用运行库
#    TEST_DATABASE_URL     测试专用库（测试会建表删表，不能与 DATABASE_URL 相同）
#    TEST_REDIS_ADDR       格式 host:port/db，请使用独立 db

# 3. 加载环境变量（每个新终端都要执行）
source .env.local

# 4. 准备测试库
createdb -h HOST -U USER datawarden_test
```

### 运行

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

### 创建初始用户

系统不内置默认账号，首次使用需手工插入一个 superadmin：

```sql
-- 先用 bcrypt 生成密码哈希，再插入
INSERT INTO app_user (username, email, password_hash, role)
VALUES ('admin', 'admin@example.com', '<bcrypt-hash>', 'superadmin');
```

密码哈希可用一小段 Go 代码生成：`auth.HashPassword("你的密码")`。

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
