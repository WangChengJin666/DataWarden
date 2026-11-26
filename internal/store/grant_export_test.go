package store

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/opensourceprj/datawarden/internal/model"
)

// newGrantFixture 建一个开发者 + 一个数据源，返回两者的 ID。
func newGrantFixture(t *testing.T, username, dsName string) (pool *pgxpool.Pool, uid, dsID int64) {
	t.Helper()
	p := setupPool(t)
	us := NewUserStore(p)
	ds := NewDatasourceStore(p)
	uid = newUser(t, us, username, model.RoleDeveloper)
	id, err := ds.Create(context.Background(), &model.Datasource{
		Name: dsName, Type: model.TypePostgreSQL, Host: "127.0.0.1", Port: 5432,
		Database: "shop", Username: "ro", Password: "pw",
	})
	if err != nil {
		t.Fatalf("create datasource: %v", err)
	}
	return p, uid, id
}

// 导出权限必须落在「单条授权 + 单个数据源」上。
//
// 这是这条需求的核心断言：同一个人，A 数据源批了导出、B 数据源没批，
// CanExport 必须分别回答 true / false。若判定写成「这个人有没有任何
// 一条可导出授权」，A 源批过之后 B 源也会放开——而它们本来是两次审批。
func TestGrantStore_CanExportIsPerDatasource(t *testing.T) {
	pool, uid, dsA := newGrantFixture(t, "exporter", "pg-export-a")
	ctx := context.Background()
	gs := NewGrantStore(pool)

	dsStore := NewDatasourceStore(pool)
	dsB, err := dsStore.Create(ctx, &model.Datasource{
		Name: "pg-export-b", Type: model.TypePostgreSQL, Host: "127.0.0.1", Port: 5432,
		Database: "shop", Username: "ro", Password: "pw",
	})
	if err != nil {
		t.Fatalf("create datasource B: %v", err)
	}

	if _, err := gs.Create(ctx, &model.Grant{
		UserID: uid, DatasourceID: dsA, Scope: "*", Action: "read",
		CanExport: true, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("create grant A: %v", err)
	}
	if _, err := gs.Create(ctx, &model.Grant{
		UserID: uid, DatasourceID: dsB, Scope: "*", Action: "read",
		CanExport: false, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("create grant B: %v", err)
	}

	gotA, err := gs.CanExport(ctx, uid, dsA)
	if err != nil {
		t.Fatalf("CanExport A: %v", err)
	}
	if !gotA {
		t.Error("A 数据源已批导出，应当允许")
	}
	gotB, err := gs.CanExport(ctx, uid, dsB)
	if err != nil {
		t.Fatalf("CanExport B: %v", err)
	}
	if gotB {
		t.Error("B 数据源未批导出，不应因为 A 源批过而放开")
	}

	// 查询页下拉的 can_export 也要区分到数据源级别，否则前端会
	// 给 B 源也画出导出按钮。
	briefs, err := gs.MyDatasources(ctx, uid)
	if err != nil {
		t.Fatalf("MyDatasources: %v", err)
	}
	byID := map[int64]bool{}
	for _, b := range briefs {
		byID[b.ID] = b.CanExport
	}
	if !byID[dsA] || byID[dsB] {
		t.Errorf("下拉的 can_export 不对：A=%v B=%v，want true/false", byID[dsA], byID[dsB])
	}
}

// 已撤销 / 已过期的授权不能算导出权限——否则「回收了授权还能导数据」，
// 这是比查询侧泄漏更严重的事故（导出的是文件，出去了就收不回）。
func TestGrantStore_CanExportIgnoresInactiveGrants(t *testing.T) {
	pool, uid, dsID := newGrantFixture(t, "revoked", "pg-export-revoked")
	ctx := context.Background()
	gs := NewGrantStore(pool)

	// 已过期
	if _, err := gs.Create(ctx, &model.Grant{
		UserID: uid, DatasourceID: dsID, Scope: "*", Action: "read",
		CanExport: true, ExpiresAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("create expired grant: %v", err)
	}
	ok, err := gs.CanExport(ctx, uid, dsID)
	if err != nil {
		t.Fatalf("CanExport: %v", err)
	}
	if ok {
		t.Error("已过期授权不应提供导出权限")
	}

	// 已撤销
	gid, err := gs.Create(ctx, &model.Grant{
		UserID: uid, DatasourceID: dsID, Scope: "*", Action: "read",
		CanExport: true, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create grant: %v", err)
	}
	if err := gs.Revoke(ctx, gid); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	ok, err = gs.CanExport(ctx, uid, dsID)
	if err != nil {
		t.Fatalf("CanExport: %v", err)
	}
	if ok {
		t.Error("已撤销授权不应提供导出权限")
	}
}

// 审批通过时导出权限从申请单复制过去，不是重新给默认值。
//
// 复制错方向的两个后果都很糟：批了导出却拿不到（申请人白申请一次），
// 或者没申请导出却拿到了（审批人不知道自己批了什么）。
func TestGrantRequestStore_ApproveCopiesCanExport(t *testing.T) {
	for _, want := range []bool{true, false} {
		pool, uid, dsID := newGrantFixture(t, "approvee", "pg-approve-"+map[bool]string{true: "yes", false: "no"}[want])
		ctx := context.Background()
		rr := NewGrantRequestStore(pool)
		gs := NewGrantStore(pool)

		reqID, err := rr.Create(ctx, &model.GrantRequest{
			UserID: uid, DatasourceID: dsID, Scope: "*", Days: 3,
			Reason: "排查", CanExport: want,
		})
		if err != nil {
			t.Fatalf("create request: %v", err)
		}
		g, err := rr.Approve(ctx, reqID, 1, "")
		if err != nil {
			t.Fatalf("approve: %v", err)
		}
		if g.CanExport != want {
			t.Errorf("授权上的 can_export = %v, want %v", g.CanExport, want)
		}
		ok, err := gs.CanExport(ctx, uid, dsID)
		if err != nil {
			t.Fatalf("CanExport: %v", err)
		}
		if ok != want {
			t.Errorf("审批通过后 CanExport = %v, want %v", ok, want)
		}
		// 申请单自身的勾选不能被审批改写：它是"当时申请了什么"的留痕
		got, err := rr.Get(ctx, reqID)
		if err != nil {
			t.Fatalf("get request: %v", err)
		}
		if got.CanExport != want {
			t.Errorf("申请单 can_export 被改成了 %v, want %v", got.CanExport, want)
		}
	}
}

// SetCanExport 事后单独开关导出：DBA 审批时漏看勾选、或临时收紧都能用。
// 它只动授权记录，不动申请单（那是历史留痕）。
func TestGrantStore_SetCanExport(t *testing.T) {
	pool, uid, dsID := newGrantFixture(t, "toggler", "pg-toggle")
	ctx := context.Background()
	gs := NewGrantStore(pool)

	gid, err := gs.Create(ctx, &model.Grant{
		UserID: uid, DatasourceID: dsID, Scope: "*", Action: "read",
		CanExport: false, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create grant: %v", err)
	}
	hit, err := gs.SetCanExport(ctx, gid, true)
	if err != nil {
		t.Fatalf("SetCanExport: %v", err)
	}
	if !hit {
		t.Fatal("SetCanExport 应当命中刚建的授权")
	}
	ok, _ := gs.CanExport(ctx, uid, dsID)
	if !ok {
		t.Error("开启后应当允许导出")
	}

	hit, err = gs.SetCanExport(ctx, gid, false)
	if err != nil || !hit {
		t.Fatalf("SetCanExport(false) hit=%v err=%v", hit, err)
	}
	ok, _ = gs.CanExport(ctx, uid, dsID)
	if ok {
		t.Error("关闭后不应再允许导出")
	}

	// 不存在的 id 要能区分出来（handler 据此回 404 而不是 500）
	hit, err = gs.SetCanExport(ctx, 99999999, true)
	if err != nil {
		t.Fatalf("SetCanExport(missing) err = %v", err)
	}
	if hit {
		t.Error("不存在的授权不应报告命中")
	}
}

// 同一数据源上多条生效授权时，任一条批了导出就该能导出（取「或」）。
//
// 实际场景：改口径前批过一条库级授权（无导出），之后又申请到一条带导出的，
// 两条都还在有效期内。若按「最后一条」或「任意一条为 false 就 false」
// 来算，用户会看到「我明明批了导出，按钮却是灰的」。
func TestGrantStore_CanExportAnyGrantAllows(t *testing.T) {
	pool, uid, dsID := newGrantFixture(t, "multi", "pg-multi")
	ctx := context.Background()
	gs := NewGrantStore(pool)

	if _, err := gs.Create(ctx, &model.Grant{
		UserID: uid, DatasourceID: dsID, Scope: "shop:*", Action: "read",
		CanExport: false, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("create first grant: %v", err)
	}
	if _, err := gs.Create(ctx, &model.Grant{
		UserID: uid, DatasourceID: dsID, Scope: "*", Action: "read",
		CanExport: true, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("create second grant: %v", err)
	}

	ok, err := gs.CanExport(ctx, uid, dsID)
	if err != nil {
		t.Fatalf("CanExport: %v", err)
	}
	if !ok {
		t.Error("任一授权批了导出就应允许")
	}

	briefs, err := gs.MyDatasources(ctx, uid)
	if err != nil {
		t.Fatalf("MyDatasources: %v", err)
	}
	if len(briefs) != 1 || !briefs[0].CanExport {
		t.Errorf("下拉 can_export 应为 true，实际 %+v", briefs)
	}
}
