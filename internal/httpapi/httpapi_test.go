package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestLogin_RejectsBadCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newTestRouter(t, map[string]string{"alice": "correct"})

	body := bytes.NewBufferString(`{"username":"alice","password":"wrong"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/login", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestLogin_RejectsUnknownUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newTestRouter(t, map[string]string{"alice": "correct"})

	body := bytes.NewBufferString(`{"username":"nobody","password":"whatever"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/login", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	// 错误信息不能泄露用户是否存在
	var resp map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["error"] != "invalid credentials" {
		t.Errorf("error message should be generic, got %q", resp["error"])
	}
}

func TestLogin_ReturnsToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newTestRouter(t, map[string]string{"alice": "correct"})

	body := bytes.NewBufferString(`{"username":"alice","password":"correct"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/login", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["token"] == "" {
		t.Error("token should not be empty")
	}
	if resp["role"] != "developer" {
		t.Errorf("role = %q, want developer", resp["role"])
	}
}

func TestLogin_RejectsMissingFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newTestRouter(t, map[string]string{"alice": "correct"})

	body := bytes.NewBufferString(`{"username":"alice"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/login", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestProtectedRoute_RequiresToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newTestRouter(t, map[string]string{"alice": "correct"})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/datasources", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestProtectedRoute_RejectsGarbageToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newTestRouter(t, map[string]string{"alice": "correct"})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/datasources", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-token")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestProtectedRoute_AcceptsValidToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newTestRouter(t, map[string]string{"alice": "correct"})

	tok := loginAndGetToken(t, r, "alice", "correct")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/datasources", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
}

func TestCreateDatasource_RequiresDBA(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newTestRouterWithRole(t, "developer")

	tok := loginAndGetToken(t, r, "alice", "correct")
	body := bytes.NewBufferString(`{
		"name":"shop","type":"postgresql","host":"h","port":5432,"secret_ref":"s"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/datasources", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for non-dba", w.Code)
	}
}

func TestCreateDatasource_DBAPasses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newTestRouterWithRole(t, "dba")

	tok := loginAndGetToken(t, r, "alice", "correct")
	body := bytes.NewBufferString(`{
		"name":"shop","type":"postgresql","host":"h","port":5432,"secret_ref":"s"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/datasources", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body=%s", w.Code, w.Body.String())
	}
}

func loginAndGetToken(t *testing.T, r http.Handler, user, pass string) string {
	t.Helper()
	body := bytes.NewBufferString(`{"username":"` + user + `","password":"` + pass + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/login", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	return resp["token"]
}
