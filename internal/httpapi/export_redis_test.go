package httpapi

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/opensourceprj/datawarden/internal/model"
	"github.com/opensourceprj/datawarden/internal/redisq"
)

// redisExportHandler 装配一个 Redis 导出链路：执行用替身，闸门与 key 级
// 鉴权各自可控。db 固定为 37，用来验证闸门拿到的是**数据源配置的 db 号**。
func redisExportHandler(grants ExportStore, enf ExportDenier, q redisq.Queryer) *ExportHandler {
	return &ExportHandler{
		Service:      newTestQueryServiceWithEnforcer(&recordingAuditor{}, fakeEnforcer{allow: true}),
		Factory:      fakeFactory,
		Grants:       grants,
		Enforcer:     enf,
		RedisFactory: stubRedisFactory(q, 37),
	}
}

// sheetText 取出 xlsx 里的工作表 XML，用来断言导出的真实内容。
//
// 只断言 200 + PK 魔数证明不了「导出的是 Redis 数据」——那同样
// 适用于一份只有表头的空文件。
func sheetText(t *testing.T, body []byte) string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("响应不是合法的 xlsx（zip）: %v", err)
	}
	for _, f := range zr.File {
		if f.Name == "xl/worksheets/sheet1.xml" {
			rc, err := f.Open()
			if err != nil {
				t.Fatalf("open sheet: %v", err)
			}
			defer rc.Close()
			var buf bytes.Buffer
			_, _ = buf.ReadFrom(rc)
			return buf.String()
		}
	}
	t.Fatal("xlsx 里没有 xl/worksheets/sheet1.xml")
	return ""
}

func doExportRedis(t *testing.T, h *ExportHandler, role string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	if body == nil {
		body = map[string]any{}
	}
	body["datasource_id"] = 2
	body["type"] = "redis"
	return doExport(t, h, role, body)
}

// 管理角色免授权：Redis 导出同样不拦，且不去查 grant_record。
func TestExportRedis_PrivilegedSkipsGate(t *testing.T) {
	grants := &fakeExportGrants{allowed: map[string]bool{}}
	q := &stubRedisQueryer{res: &redisq.QueryResponse{Exact: true, Items: []*redisq.KeyValue{
		{Key: "user:1", Type: "string", Value: "alice", TTL: 60},
	}}}
	h := redisExportHandler(grants, fakeEnforcer{allow: true}, q)

	for _, role := range []model.Role{model.RoleDBA, model.RoleSuperAdmin} {
		w := doExportRedis(t, h, string(role), map[string]any{"key": "user:1", "row_limit": 300})
		if w.Code != http.StatusOK {
			t.Fatalf("[%s] status = %d, want 200, body=%s", role, w.Code, w.Body.String())
		}
		sheet := sheetText(t, w.Body.Bytes())
		for _, want := range []string{"field_or_index", "user:1", "alice"} {
			if !strings.Contains(sheet, want) {
				t.Errorf("[%s] 工作表里缺少 %q", role, want)
			}
		}
	}
	if len(grants.called) != 0 {
		t.Errorf("管理角色不应查询 grant_record，实际调用 %v", grants.called)
	}
	if !q.called || q.got.Key != "user:1" || q.got.Pattern != "" {
		t.Errorf("执行参数不对：called=%v req=%+v", q.called, q.got)
	}
	// 行数上限必须透传：不传时 redisq 会退回它自己的默认值，
	// 用户在「限 N 行」里填的数就被丢掉了。
	if q.got.RowLimit != 300 {
		t.Errorf("RowLimit = %d, want 300", q.got.RowLimit)
	}
}

// 开发者导出 Redis 的闸门按**数据源配置的 db 号**判定，而不是请求体里的
// 任何字段（Redis 请求里根本没有 database）。
//
// 用 37 而不是 "shop" 做键：如果实现错拿了别的值去查授权，
// 这条用例会直接 403，而不是碰巧通过。
func TestExportRedis_DeveloperGateUsesDatasourceDbNumber(t *testing.T) {
	grants := &fakeExportGrants{allowed: map[string]bool{grantKey(2, "37"): true}}
	q := &stubRedisQueryer{res: &redisq.QueryResponse{Items: []*redisq.KeyValue{
		{Key: "user:1", Type: "hash", Fields: map[string]string{"b": "2", "a": "1"}, TTL: 30},
	}}}
	h := redisExportHandler(grants, fakeEnforcer{allow: true}, q)

	w := doExportRedis(t, h, string(model.RoleDeveloper), map[string]any{"key": "user:1"})
	if w.Code != http.StatusOK {
		t.Fatalf("已批导出的 db 应放行，status = %d, body=%s", w.Code, w.Body.String())
	}
	if len(grants.called) != 1 || grants.called[0] != grantKey(2, "37") {
		t.Fatalf("闸门收到的判定依据 = %v, want [%s]", grants.called, grantKey(2, "37"))
	}

	sheet := sheetText(t, w.Body.Bytes())
	// hash 逐 field 展开成多行，field 名进 field_or_index
	for _, want := range []string{">a<", ">b<", ">1<", ">2<"} {
		if !strings.Contains(sheet, want) {
			t.Errorf("hash 未按 field 展开，工作表里缺少 %q", want)
		}
	}
}

// 导出闸门只到 db，查询权限却可以细到 key 前缀。key 级判定必须是闸门：
// 持 "37:user:1001" 前缀授权的人能查 user:1001:*，但 db 级的导出闸门
// 对它是放行的——少了这一级，他就能导出 db 37 里的任意 key。
func TestExportRedis_KeyOutsideGrantPrefixDenied(t *testing.T) {
	grants := &fakeExportGrants{allowed: map[string]bool{grantKey(2, "37"): true}}
	q := &stubRedisQueryer{}
	h := redisExportHandler(grants, fakeEnforcer{allow: false}, q)

	w := doExportRedis(t, h, string(model.RoleDeveloper), map[string]any{"key": "order:9"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("key 不在授权前缀内应拒绝，status = %d, body=%s", w.Code, w.Body.String())
	}
	if body := w.Body.String(); !strings.Contains(body, "no permission for this redis key or pattern") {
		t.Errorf("提示应指向 key 级查询权限，实际 %s", body)
	}
	if q.called {
		t.Error("被拒绝的请求不应执行 Redis 查询")
	}
}

// 装配了闸门却没装配 Enforcer 时，无法证明 key 在授权范围内，必须拒绝。
func TestExportRedis_FailsClosedWithoutEnforcer(t *testing.T) {
	q := &stubRedisQueryer{}
	h := redisExportHandler(&fakeExportGrants{allowed: map[string]bool{grantKey(2, "37"): true}}, nil, q)
	w := doExportRedis(t, h, string(model.RoleDeveloper), map[string]any{"key": "user:1"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body=%s", w.Code, w.Body.String())
	}
	if q.called {
		t.Error("权限不明时不应执行 Redis 查询")
	}
}

// 没批导出时要区分两种拒绝，与 SQL / Mongo 同一口径。
func TestExportRedis_DistinguishesNoGrantFromNoExport(t *testing.T) {
	grants := &fakeExportGrants{allowed: map[string]bool{}}

	noExport := redisExportHandler(grants, fakeEnforcer{allow: true}, &stubRedisQueryer{})
	w := doExportRedis(t, noExport, string(model.RoleDeveloper), map[string]any{"pattern": "user:*"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body=%s", w.Code, w.Body.String())
	}
	if body := w.Body.String(); !strings.Contains(body, "导出权限") {
		t.Errorf("有查询权限但没批导出，提示应指向导出权限，实际 %s", body)
	}

	noGrant := redisExportHandler(grants, fakeEnforcer{allow: false}, &stubRedisQueryer{})
	w = doExportRedis(t, noGrant, string(model.RoleDeveloper), map[string]any{"pattern": "user:*"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, "no permission for this redis key or pattern") {
		t.Errorf("连 key 都没权限时提示应指向查询权限，实际 %s", body)
	}
}

// 参数问题是 400，必须先于权限判定——否则没批导出的用户填错参数，
// 会被告知「去申请导出」，申请下来还是错。
func TestExportRedis_ParametersBeforeGate(t *testing.T) {
	grants := &fakeExportGrants{allowed: map[string]bool{}}
	h := redisExportHandler(grants, fakeEnforcer{allow: true}, &stubRedisQueryer{})

	cases := map[string]map[string]any{
		"都不填":   {},
		"都填":    {"key": "user:1", "pattern": "user:*"},
		"只有空白": {"key": "   ", "pattern": "  "},
	}
	for name, body := range cases {
		w := doExportRedis(t, h, string(model.RoleDeveloper), body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400, body=%s", name, w.Code, w.Body.String())
		}
	}
	if len(grants.called) != 0 {
		t.Errorf("参数非法时不应进入权限判定，实际调用了 %v", grants.called)
	}
}

// 模糊查询的宽泛模式由 redisq 拒绝，导出沿用同一个 400。
func TestExportRedis_PatternTooBroad(t *testing.T) {
	q := &stubRedisQueryer{err: redisq.ErrPatternTooBroad}
	h := redisExportHandler(&fakeExportGrants{allowed: map[string]bool{}}, fakeEnforcer{allow: true}, q)
	w := doExportRedis(t, h, string(model.RoleSuperAdmin), map[string]any{"pattern": "u*"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", w.Code, w.Body.String())
	}
}

// 闸门查不出来时 fail-closed。
func TestExportRedis_FailsClosedWhenGateErrors(t *testing.T) {
	q := &stubRedisQueryer{}
	h := redisExportHandler(&fakeExportGrants{err: contextError{}}, fakeEnforcer{allow: true}, q)
	w := doExportRedis(t, h, string(model.RoleDeveloper), map[string]any{"key": "user:1"})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if q.called {
		t.Error("权限不明时不得执行查询")
	}
}

// 被拒绝的请求不能落到底层执行。
func TestExportRedis_DeniedDoesNotQuery(t *testing.T) {
	q := &stubRedisQueryer{}
	h := redisExportHandler(&fakeExportGrants{allowed: map[string]bool{}}, fakeEnforcer{allow: true}, q)
	doExportRedis(t, h, string(model.RoleDeveloper), map[string]any{"key": "user:1"})
	if q.called {
		t.Error("未批导出时不应执行 Redis 查询")
	}
}

// 没装配 Redis 连接时明确 503，而不是空指针。
func TestExportRedis_NotConfigured(t *testing.T) {
	h := newExportHandler(&fakeExportGrants{allowed: map[string]bool{grantKey(1, "shop"): true}})
	w := doExportRedis(t, h, string(model.RoleSuperAdmin), map[string]any{"key": "user:1"})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503, body=%s", w.Code, w.Body.String())
	}
}

// 摊平规则是 Redis 导出的核心语义：一个元素一行，key 信息逐行重复。
//
// 每个子用例对应一种 Redis 类型，断言「行数 + 各行的 field / value」，
// 只数行数会让「hash 没展开、整串塞进一格」也通过。
func TestRedisExportRows(t *testing.T) {
	cases := []struct {
		name     string
		item     *redisq.KeyValue
		wantRows [][]any // 只列 field / value 两列
		wantTTL  any
		wantEnc  string
	}{
		{
			name:     "string 文本",
			item:     &redisq.KeyValue{Key: "k", Type: "string", Value: "hello", TTL: 10, Encoding: redisq.EncodingText},
			wantRows: [][]any{{"", "hello"}},
			wantTTL:  int64(10),
			wantEnc:  "text",
		},
		{
			name:     "string 永久",
			item:     &redisq.KeyValue{Key: "k", Type: "string", Value: "x", TTL: -1},
			wantRows: [][]any{{"", "x"}},
			wantTTL:  "",
			wantEnc:  "",
		},
		{
			// 二进制必须导出十六进制并标明形态，否则用户会把 hex 当成真实值
			name:     "string 二进制",
			item:     &redisq.KeyValue{Key: "k", Type: "string", Hex: "00ff", TTL: 1, Encoding: redisq.EncodingBinary},
			wantRows: [][]any{{"", "00ff"}},
			wantTTL:  int64(1),
			wantEnc:  "binary",
		},
		{
			name:     "hash 按字段名排序",
			item:     &redisq.KeyValue{Key: "k", Type: "hash", Fields: map[string]string{"b": "2", "a": "1"}, TTL: 1},
			wantRows: [][]any{{"a", "1"}, {"b", "2"}},
			wantTTL:  int64(1),
		},
		{
			name:     "list 带下标",
			item:     &redisq.KeyValue{Key: "k", Type: "list", Items: []string{"x", "y"}, TTL: 1},
			wantRows: [][]any{{0, "x"}, {1, "y"}},
			wantTTL:  int64(1),
		},
		{
			name:     "set 排序去随机序",
			item:     &redisq.KeyValue{Key: "k", Type: "set", Items: []string{"y", "x"}, TTL: 1},
			wantRows: [][]any{{"", "x"}, {"", "y"}},
			wantTTL:  int64(1),
		},
		{
			// member 本身含冒号，必须按最后一个冒号切
			name:     "zset member 含冒号",
			item:     &redisq.KeyValue{Key: "k", Type: "zset", Items: []string{"a:b:12.5", "c:-1"}, TTL: 1},
			wantRows: [][]any{{"a:b", "12.5"}, {"c", "-1"}},
			wantTTL:  int64(1),
		},
		{
			name:     "key 不存在",
			item:     &redisq.KeyValue{Key: "k", Type: "none"},
			wantRows: [][]any{{"", ""}},
			wantTTL:  "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cols, rows := redisExportRows(&redisq.QueryResponse{Items: []*redisq.KeyValue{c.item}})
			wantCols := []string{"key", "type", "field_or_index", "value", "ttl_seconds", "encoding"}
			if strings.Join(cols, ",") != strings.Join(wantCols, ",") {
				t.Fatalf("columns = %v, want %v", cols, wantCols)
			}
			if len(rows) != len(c.wantRows) {
				t.Fatalf("行数 = %d, want %d： %+v", len(rows), len(c.wantRows), rows)
			}
			for i, want := range c.wantRows {
				got := rows[i]
				if len(got) != 6 {
					t.Fatalf("row %d 列数 = %d, want 6", i, len(got))
				}
				if got[0] != "k" || got[1] != c.item.Type {
					t.Errorf("row %d 的 key/type = %v/%v", i, got[0], got[1])
				}
				if got[2] != want[0] || got[3] != want[1] {
					t.Errorf("row %d field/value = %v/%v, want %v/%v", i, got[2], got[3], want[0], want[1])
				}
				if got[4] != c.wantTTL {
					t.Errorf("row %d ttl = %v, want %v", i, got[4], c.wantTTL)
				}
				if got[5] != c.wantEnc {
					t.Errorf("row %d encoding = %v, want %q", i, got[5], c.wantEnc)
				}
			}
		})
	}
}

// 空结果与 nil 元素不能让导出崩掉。
func TestRedisExportRows_Empty(t *testing.T) {
	if _, rows := redisExportRows(nil); len(rows) != 0 {
		t.Errorf("nil 响应应得到 0 行，实际 %d", len(rows))
	}
	_, rows := redisExportRows(&redisq.QueryResponse{Items: []*redisq.KeyValue{nil}})
	if len(rows) != 0 {
		t.Errorf("nil 元素应被跳过，实际 %d 行", len(rows))
	}
}
