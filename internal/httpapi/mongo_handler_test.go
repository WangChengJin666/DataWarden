package httpapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/opensourceprj/datawarden/internal/auth"
)

// 命令行形式：鉴权 object 必须按集合判定
func TestMongoQuery_CommandFormAuthz(t *testing.T) {
	enforcer := &recordingEnforcer{allowed: true}

	gin.SetMode(gin.TestMode)
	issuer := auth.NewTokenIssuer("test-secret", time.Hour)
	r := NewRouter(Deps{
		Issuer:      issuer,
		AuthHandler: &AuthHandler{Users: &memUsers{passwords: map[string]string{"alice": "pw"}}, Issuer: issuer},
		Datasource:  &DatasourceHandler{Store: &memDatasources{}},
		Query: &QueryHandler{
			Service: newTestQueryServiceWithEnforcer(&recordingAuditor{}, enforcer),
			Factory: stubMongoFactory(),
		},
	})

	tok := loginAndGetToken(t, r, "alice", "pw")
	postJSON(t, r, tok, "/api/v1/query/mongo", map[string]any{
		"datasource_id": 7,
		"command":       `db.users.find({"status":"active"})`,
	})

	// 这里只关心鉴权对象是否按「集合」拼接，不关心后续执行是否成功
	want := "ds:7::users"
	if enforcer.lastObj != want {
		t.Errorf("auth object = %q, want %q", enforcer.lastObj, want)
	}
}

// 无权限必须 403
func TestMongoQuery_Forbidden(t *testing.T) {
	enforcer := &recordingEnforcer{allowed: false}

	gin.SetMode(gin.TestMode)
	issuer := auth.NewTokenIssuer("test-secret", time.Hour)
	r := NewRouter(Deps{
		Issuer:      issuer,
		AuthHandler: &AuthHandler{Users: &memUsers{passwords: map[string]string{"alice": "pw"}}, Issuer: issuer},
		Datasource:  &DatasourceHandler{Store: &memDatasources{}},
		Query: &QueryHandler{
			Service: newTestQueryServiceWithEnforcer(&recordingAuditor{}, enforcer),
			Factory: stubMongoFactory(),
		},
	})

	tok := loginAndGetToken(t, r, "alice", "pw")
	w := postJSON(t, r, tok, "/api/v1/query/mongo", map[string]any{
		"datasource_id": 7,
		"collection":    "users",
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", w.Code, w.Body.String())
	}
}

// 写命令必须在解析阶段就被拦下，返回 400 且不查库
func TestMongoQuery_RejectsWriteCommand(t *testing.T) {
	enforcer := &recordingEnforcer{allowed: true}

	gin.SetMode(gin.TestMode)
	issuer := auth.NewTokenIssuer("test-secret", time.Hour)
	r := NewRouter(Deps{
		Issuer:      issuer,
		AuthHandler: &AuthHandler{Users: &memUsers{passwords: map[string]string{"alice": "pw"}}, Issuer: issuer},
		Datasource:  &DatasourceHandler{Store: &memDatasources{}},
		Query: &QueryHandler{
			Service: newTestQueryServiceWithEnforcer(&recordingAuditor{}, enforcer),
			Factory: stubMongoFactory(),
		},
	})

	tok := loginAndGetToken(t, r, "alice", "pw")
	for _, cmd := range []string{
		`db.users.deleteMany({})`,
		`db.users.drop()`,
		`db.users.updateMany({},{$set:{x:1}})`,
		`db.users.aggregate([{"$out":"copy"}])`,
	} {
		w := postJSON(t, r, tok, "/api/v1/query/mongo", map[string]any{
			"datasource_id": 7,
			"command":       cmd,
		})
		if w.Code != http.StatusBadRequest {
			t.Errorf("command %q status = %d, want 400; body = %s", cmd, w.Code, w.Body.String())
		}
	}
}

// 集合形式：filter 不是对象时拒绝
func TestMongoQuery_FilterMustBeObject(t *testing.T) {
	gin.SetMode(gin.TestMode)
	issuer := auth.NewTokenIssuer("test-secret", time.Hour)
	r := NewRouter(Deps{
		Issuer:      issuer,
		AuthHandler: &AuthHandler{Users: &memUsers{passwords: map[string]string{"alice": "pw"}}, Issuer: issuer},
		Datasource:  &DatasourceHandler{Store: &memDatasources{}},
		Query: &QueryHandler{
			Service: newTestQueryService(&recordingAuditor{}),
			Factory: stubMongoFactory(),
		},
	})

	tok := loginAndGetToken(t, r, "alice", "pw")
	w := postJSON(t, r, tok, "/api/v1/query/mongo", map[string]any{
		"datasource_id": 7,
		"collection":    "users",
		"filter":        `db.users.drop()`,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
}

// 未登录必须 401
func TestMongoQuery_RequiresAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	issuer := auth.NewTokenIssuer("test-secret", time.Hour)
	r := NewRouter(Deps{
		Issuer:      issuer,
		AuthHandler: &AuthHandler{Users: &memUsers{}, Issuer: issuer},
		Datasource:  &DatasourceHandler{Store: &memDatasources{}},
		Query: &QueryHandler{
			Service: newTestQueryService(&recordingAuditor{}),
			Factory: stubMongoFactory(),
		},
	})

	w := postJSON(t, r, "", "/api/v1/query/mongo", map[string]any{
		"datasource_id": 7,
		"collection":    "users",
	})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

// 缺少 datasource_id 必须 400
func TestMongoQuery_RequiresDatasourceID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	issuer := auth.NewTokenIssuer("test-secret", time.Hour)
	r := NewRouter(Deps{
		Issuer:      issuer,
		AuthHandler: &AuthHandler{Users: &memUsers{passwords: map[string]string{"alice": "pw"}}, Issuer: issuer},
		Datasource:  &DatasourceHandler{Store: &memDatasources{}},
		Query: &QueryHandler{
			Service: newTestQueryService(&recordingAuditor{}),
			Factory: stubMongoFactory(),
		},
	})

	tok := loginAndGetToken(t, r, "alice", "pw")
	w := postJSON(t, r, tok, "/api/v1/query/mongo", map[string]any{
		"collection": "users",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// 空集合名必须 400
func TestMongoQuery_RequiresCollection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	issuer := auth.NewTokenIssuer("test-secret", time.Hour)
	r := NewRouter(Deps{
		Issuer:      issuer,
		AuthHandler: &AuthHandler{Users: &memUsers{passwords: map[string]string{"alice": "pw"}}, Issuer: issuer},
		Datasource:  &DatasourceHandler{Store: &memDatasources{}},
		Query: &QueryHandler{
			Service: newTestQueryService(&recordingAuditor{}),
			Factory: stubMongoFactory(),
		},
	})

	tok := loginAndGetToken(t, r, "alice", "pw")
	w := postJSON(t, r, tok, "/api/v1/query/mongo", map[string]any{
		"datasource_id": 7,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
}
