# DataWarden

面向研发团队的数据审计工单平台。为开发人员提供生产数据库的受控查询与变更能力，支持 MySQL、PostgreSQL、Redis、MongoDB 四类数据源，所有变更可回滚。

## 核心特性

- **四类数据源**：MySQL、PostgreSQL、Redis、MongoDB
- **两种查询授权**：平台内限时申请数据源、申请可直连的临时账号
- **变更工单**：DBA 审批后执行，执行前生成行镜像，支持回滚
- **跨库跨实例查询**：基于 Trino 的联邦查询（Redis 走独立通道）
- **全链路审计**：所有操作留痕，审计表数据库层面不可篡改

> **项目状态**：设计阶段完成，阶段一（地基 + 只读查询 + Redis 通道）开发中。

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
# 启动服务
go run ./cmd/server

# 运行全部测试
go test ./...

# 只跑不需要外部依赖的测试
go test ./... -short
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
