package secret

import (
	"fmt"
	"os"
	"strings"
)

// Resolver 把 secret_ref 解析成实际凭据。
//
// 这是阶段一的简化实现，只支持环境变量形式：
//
//	env:QUERY_DB_PASSWORD    → 读环境变量 QUERY_DB_PASSWORD
//
// 生产环境应替换为 Vault / KMS 实现，接口不变，只换实现。
type Resolver interface {
	Password(ref string) (string, error)
}

// EnvResolver 从环境变量读取。
type EnvResolver struct{}

func NewEnvResolver() *EnvResolver { return &EnvResolver{} }

// Password 解析 secret_ref。支持：
//   - "env:VAR_NAME"  读指定环境变量
//   - "env"           读默认变量 QUERY_DB_PASSWORD（阶段一所有数据源共用一组账号）
//   - ""              返回空，表示无密码（如无认证的 Redis）
func (r *EnvResolver) Password(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", nil
	}
	if ref == "env" {
		return os.Getenv("QUERY_DB_PASSWORD"), nil
	}
	if strings.HasPrefix(ref, "env:") {
		name := strings.TrimPrefix(ref, "env:")
		if name == "" {
			return "", fmt.Errorf("empty env var name in secret_ref %q", ref)
		}
		return os.Getenv(name), nil
	}
	return "", fmt.Errorf("unsupported secret_ref scheme %q (only env: is implemented)", ref)
}

// User 决定连接目标库使用的账号。
// 阶段一所有数据源共用 QUERY_DB_USER，阶段四改为按数据源配置。
func (r *EnvResolver) User() string {
	return os.Getenv("QUERY_DB_USER")
}
