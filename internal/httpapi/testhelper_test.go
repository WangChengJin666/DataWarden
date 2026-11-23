package httpapi

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/opensourceprj/datawarden/internal/auth"
	"github.com/opensourceprj/datawarden/internal/model"
)

// memUsers 是内存用户存储，供 HTTP 层测试使用。
type memUsers struct {
	passwords map[string]string
	role      string
}

func (m *memUsers) FindByUsername(ctx context.Context, username string) (int64, string, string, error) {
	pw, ok := m.passwords[username]
	if !ok {
		return 0, "", "", errors.New("not found")
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return 0, "", "", err
	}
	role := m.role
	if role == "" {
		role = "developer"
	}
	return 1, hash, role, nil
}

// memDatasources 是内存数据源存储。
type memDatasources struct {
	items []model.Datasource
}

func (m *memDatasources) List(ctx context.Context) ([]model.Datasource, error) {
	if m.items == nil {
		return []model.Datasource{}, nil
	}
	return m.items, nil
}

func (m *memDatasources) Get(ctx context.Context, id int64) (*model.Datasource, error) {
	for i := range m.items {
		if m.items[i].ID == id {
			d := m.items[i]
			return &d, nil
		}
	}
	return nil, errors.New("datasource not found")
}

func (m *memDatasources) Create(ctx context.Context, ds *model.Datasource) (int64, error) {
	return 99, nil
}

// Update 与真实 store 保持一致：密码为空表示「不修改」，保留原值。
// 内存实现如果直接整体覆盖，就没法验证「编辑时不重填密码不会清空密码」这条规则。
func (m *memDatasources) Update(ctx context.Context, ds *model.Datasource) error {
	for i := range m.items {
		if m.items[i].ID == ds.ID {
			if ds.Password == "" {
				ds.Password = m.items[i].Password
			}
			m.items[i] = *ds
			return nil
		}
	}
	return errors.New("not found")
}

func (m *memDatasources) Delete(ctx context.Context, id int64) error {
	return nil
}

func (m *memDatasources) ListApplyable(ctx context.Context) ([]model.Datasource, error) {
	out := []model.Datasource{}
	for _, d := range m.items {
		if d.ApplyEnabled {
			out = append(out, d)
		}
	}
	return out, nil
}

func newTestRouter(t *testing.T, passwords map[string]string) *gin.Engine {
	t.Helper()
	return buildRouter(t, passwords, "")
}

func newTestRouterWithRole(t *testing.T, role string) *gin.Engine {
	t.Helper()
	return buildRouter(t, map[string]string{"alice": "correct"}, role)
}

func buildRouter(t *testing.T, passwords map[string]string, role string) *gin.Engine {
	t.Helper()
	issuer := auth.NewTokenIssuer("test-secret", time.Hour)
	return NewRouter(Deps{
		Issuer:      issuer,
		AuthHandler: &AuthHandler{Users: &memUsers{passwords: passwords, role: role}, Issuer: issuer},
		Datasource:  &DatasourceHandler{Store: &memDatasources{}},
		Query:       &QueryHandler{},
	})
}

// buildRouterWithStore 与 buildRouter 相同，但允许注入带数据的内存存储。
func buildRouterWithStore(t *testing.T, ds *memDatasources, role string) *gin.Engine {
	t.Helper()
	issuer := auth.NewTokenIssuer("test-secret", time.Hour)
	return NewRouter(Deps{
		Issuer:      issuer,
		AuthHandler: &AuthHandler{Users: &memUsers{passwords: map[string]string{"alice": "correct"}, role: role}, Issuer: issuer},
		Datasource:  &DatasourceHandler{Store: ds},
		Query:       &QueryHandler{},
	})
}

// recordingEnforcer 记录最后一次鉴权 object，便于断言。
type recordingEnforcer struct {
	allowed bool
	lastObj string
}

func (r *recordingEnforcer) Enforce(subject, object, action string) bool {
	r.lastObj = object
	return r.allowed
}
