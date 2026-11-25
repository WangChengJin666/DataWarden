package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/opensourceprj/datawarden/internal/auth"
	"github.com/opensourceprj/datawarden/internal/store"
)

// fakeSelfPasswordStore 是「改自己密码」的内存存储。
//
// 与 fakeUserStore 分开而不是给它补一个 FindByUsername：
// 后者实现的是一整套账号管理接口，用它测这条路径会让「接口只依赖两个方法」
// 这个设计约束在测试里看不出来——将来有人把 SelfPasswordLookup 换成
// 整个 UserStore，测试也照样通过。
type fakeSelfPasswordStore struct {
	hash         string // 库里当前的哈希
	changeCalled bool
	changeErr    error
	newHash      string
	lastVerify   func(string) bool
}

func (f *fakeSelfPasswordStore) FindByUsername(_ context.Context, username string) (int64, string, string, error) {
	if username != "alice" || f.hash == "" {
		return 0, "", "", errSelfUserNotFound
	}
	return 1, f.hash, "developer", nil
}

func (f *fakeSelfPasswordStore) ChangePassword(_ context.Context, _ int64, verify func(string) bool, newHash string) error {
	f.changeCalled = true
	f.lastVerify = verify
	if f.changeErr != nil {
		return f.changeErr
	}
	if !verify(f.hash) {
		return store.ErrOldPasswordMismatch
	}
	f.newHash = newHash
	return nil
}

var errSelfUserNotFound = errors.New("user not found")

// buildSelfPasswordRouter 装配只含「改自己密码」的路由。
//
// 刻意用一个**非超管**的登录身份：这条接口的核心要求就是「任何角色都能用」，
// 如果测试都用 superadmin，就测不出「它被误挂上超管中间件」这个回归。
func buildSelfPasswordRouter(t *testing.T, us SelfPasswordLookup) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	issuer := authIssuerForTest()
	return NewRouter(Deps{
		Issuer:       issuer,
		AuthHandler:  &AuthHandler{Users: &memUsers{passwords: map[string]string{"alice": "correct"}, role: "developer"}, Issuer: issuer},
		SelfPassword: &SelfPasswordHandler{Users: us},
	})
}

// changePasswordBody 生成请求体，避免每个用例手拼 JSON 造成转义错误。
func changePasswordBody(oldPW, newPW string) string {
	b, _ := json.Marshal(map[string]string{"old_password": oldPW, "new_password": newPW})
	return string(b)
}

func TestChangeOwnPassword_Success(t *testing.T) {
	for _, role := range []string{"developer", "dba", "superadmin"} {
		t.Run(role, func(t *testing.T) {
			hash, err := auth.HashPassword("correct")
			if err != nil {
				t.Fatal(err)
			}
			us := &fakeSelfPasswordStore{hash: hash}
			issuer := authIssuerForTest()
			r := NewRouter(Deps{
				Issuer:       issuer,
				AuthHandler:  &AuthHandler{Users: &memUsers{passwords: map[string]string{"alice": "correct"}, role: role}, Issuer: issuer},
				SelfPassword: &SelfPasswordHandler{Users: us},
			})
			tok := loginAndGetToken(t, r, "alice", "correct")

			req := httptest.NewRequest(http.MethodPost, "/api/v1/me/password",
				strings.NewReader(changePasswordBody("correct", "newpassword1")))
			req.Header.Set("Authorization", "Bearer "+tok)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s, want 200", w.Code, w.Body.String())
			}
			if !us.changeCalled {
				t.Fatal("ChangePassword 未被调用")
			}
			if !auth.VerifyPassword(us.newHash, "newpassword1") {
				t.Fatal("写入的哈希与新密码不匹配")
			}
			// 响应里绝不能出现密码：这是「明文只出现一次」约定的另一面。
			if body := w.Body.String(); strings.Contains(body, "newpassword1") || strings.Contains(body, "correct") {
				t.Fatalf("响应体泄露了密码: %s", body)
			}
		})
	}
}

func TestChangeOwnPassword_WrongOldPassword(t *testing.T) {
	hash, err := auth.HashPassword("correct")
	if err != nil {
		t.Fatal(err)
	}
	us := &fakeSelfPasswordStore{hash: hash}
	r := buildSelfPasswordRouter(t, us)
	tok := loginAndGetToken(t, r, "alice", "correct")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/me/password",
		strings.NewReader(changePasswordBody("wrongpassword", "newpassword1")))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// 必须是 403 而不是 401：前端拦截器见到 401 会清会话跳登录，
	// 而这里只是把当前密码打错了，把人踢出去只会更困惑。
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body=%s)", w.Code, w.Body.String())
	}
	if us.newHash != "" {
		t.Fatal("旧密码校验失败时不应写入新哈希")
	}
}

func TestChangeOwnPassword_Validation(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"太短", changePasswordBody("correct", "short"), "至少 8 个字符"},
		{"与旧密码相同", changePasswordBody("correctcorrect", "correctcorrect"), "不能与当前密码相同"},
		{"首尾有空白", changePasswordBody("correct", " newpassword1 "), "首尾不能有空白"},
		{"超长", changePasswordBody("correct", strings.Repeat("a", 73)), "不能超过 72 个字符"},
		{"缺字段", `{"old_password":"correct"}`, "new_password 必填"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hash, err := auth.HashPassword("correct")
			if err != nil {
				t.Fatal(err)
			}
			us := &fakeSelfPasswordStore{hash: hash}
			r := buildSelfPasswordRouter(t, us)
			tok := loginAndGetToken(t, r, "alice", "correct")

			req := httptest.NewRequest(http.MethodPost, "/api/v1/me/password", strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+tok)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body=%s)", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), tc.wantErr) {
				t.Fatalf("body = %s, want error containing %q", w.Body.String(), tc.wantErr)
			}
			if us.changeCalled {
				t.Fatal("参数不合法时不应触碰存储层")
			}
		})
	}
}

func TestChangeOwnPassword_RequiresAuth(t *testing.T) {
	r := buildSelfPasswordRouter(t, &fakeSelfPasswordStore{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/me/password",
		strings.NewReader(changePasswordBody("correct", "newpassword1")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}
