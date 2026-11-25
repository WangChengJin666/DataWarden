package httpapi

import (
	"time"

	"github.com/opensourceprj/datawarden/internal/auth"
)

// authIssuerForTest 返回测试用的令牌签发器。
//
// 独立出来是为了让新写的账号管理测试不必依赖 httpapi_test.go 里
// buildRouter 系列的具体签名——那些函数服务于数据源测试，
// 将来它们改了参数也不该牵连账号管理的测试。
func authIssuerForTest() *auth.TokenIssuer {
	return auth.NewTokenIssuer("test-secret", time.Hour)
}
