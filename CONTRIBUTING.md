# 贡献指南

## 提交前自查

**凭据安全是第一优先级。** 提交前务必确认：

```bash
# 查看将要提交的内容，逐行确认没有真实密码、IP、密钥
git diff --staged

# 确认敏感文件被忽略（有输出说明已忽略）
git check-ignore -v .env.local
```

**绝不能提交的内容**：

- `.env.local` 或任何含真实连接串的文件
- 生产数据库的 host、用户名、密码
- JWT 密钥、加密密钥、证书私钥
- 内网 IP 地址、内部域名

如果误提交了凭据，**仅删除文件是不够的**——git 历史里仍然存在。必须：

1. 立即轮换该凭据（改密码 / 重新签发密钥）
2. 用 `git filter-repo` 或 BFG 清理历史
3. 强制推送后通知所有协作者重新克隆

## 开发流程

### 分支

- `main` 保持可运行
- 功能开发用 `feat/<简述>`，修复用 `fix/<简述>`

### 提交信息

采用 Conventional Commits 格式：

```
<type>: <简述>

type 取值：
  feat      新功能
  fix       修复
  docs      文档
  refactor  重构
  test      测试
  chore     杂项（依赖升级、构建配置）
```

示例：

```
feat: add redis scan query with prefix guard
fix: prevent casbin ClearPolicy from wiping role bindings
docs: clarify row snapshot encryption strategy
```

### 代码规范

- 遵循 `gofmt`，提交前跑 `go fmt ./...`
- 通过 `go vet ./...`
- 新增逻辑必须有对应测试
- 注释用中文，公开函数必须有注释说明用途

### 测试要求

| 类型 | 要求 |
|---|---|
| 单元测试 | 必须写，不依赖外部服务 |
| 集成测试 | 用 `TEST_DATABASE_URL` / `TEST_REDIS_ADDR`，未配置时 `t.Skip` |
| 安全护栏 | 涉及敏感操作的改动必须补护栏测试 |

**Redis 测试硬约束**：所有测试 key 必须带 `dwtest:` 前缀，清理只能删这个前缀。这保证测试不会误删共享 Redis 上的真实数据。

## 安全红线

以下改动需要额外审查：

- 任何涉及 SQL 拼接的代码 → 必须用参数化查询
- 任何新增的 Redis 命令调用 → 禁止 `KEYS`、`FLUSHALL`、`FLUSHDB`
- 任何涉及权限判定的代码 → 权限判断必须在 `querysvc` 层，不能在 adapter 层
- 任何写入审计日志的代码 → 审计表有数据库触发器保护，不可 UPDATE / DELETE

## 报告问题

提交 Issue 时请勿包含真实数据库连接信息。用 `<host>` `<user>` 之类占位符替代。
