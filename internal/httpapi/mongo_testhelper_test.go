package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/opensourceprj/datawarden/internal/adapter"
	"github.com/opensourceprj/datawarden/internal/querysvc"
)

// strictEnforcer 永远拒绝，用于验证鉴权真的被调用。
type strictEnforcer struct{}

func (strictEnforcer) Enforce(subject, object, action string) bool { return false }

// allowAllEnforcer 永远放行，用于绕开鉴权测试下游逻辑。
type allowAllEnforcer struct{}

func (allowAllEnforcer) Enforce(subject, object, action string) bool { return true }

// recordingAuditor 记录写入的审计事件，便于断言。
type recordingAuditor struct {
	events []querysvc.AuditEvent
}

func (r *recordingAuditor) Record(ctx context.Context, e querysvc.AuditEvent) error {
	r.events = append(r.events, e)
	return nil
}

// newTestQueryService 构造一个鉴权全放行的查询服务。
func newTestQueryService(aud querysvc.Auditor) *querysvc.Service {
	return &querysvc.Service{
		Enforcer:        allowAllEnforcer{},
		Auditor:         aud,
		Rules:           querysvc.DefaultRules(),
		DefaultRowLimit: 100,
	}
}

// newTestQueryServiceWithEnforcer 允许注入自定义 Enforcer。
func newTestQueryServiceWithEnforcer(aud querysvc.Auditor, enf querysvc.Enforcer) *querysvc.Service {
	return &querysvc.Service{
		Enforcer:        enf,
		Auditor:         aud,
		Rules:           querysvc.DefaultRules(),
		DefaultRowLimit: 100,
	}
}

// postJSON 发送带 token 的 JSON 请求。
func postJSON(t *testing.T, r http.Handler, token, path string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// stubMongoFactory 返回一个始终报错的工厂。
// 写命令应在进入工厂前就被拒绝，所以这里报错反而能验证「没有查库」。
func stubMongoFactory() AdapterFactory {
	return func(datasourceID int64) (adapter.Adapter, error) {
		return nil, errStubFactoryReached
	}
}

// stubMongoAdapterFactory 返回一个可用的 Mongo 替身，用于验证响应结构。
type stubMongoAdapterFactory struct{}

func (stubMongoAdapterFactory) factory(datasourceID int64) (adapter.Adapter, error) {
	return nil, errStubFactoryReached
}

type stubFactoryErr string

func (e stubFactoryErr) Error() string { return string(e) }

const errStubFactoryReached = stubFactoryErr("stub factory reached: query should have been rejected earlier")
