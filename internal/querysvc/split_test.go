package querysvc

import "testing"

// SplitDBCollection 必须原样上抛 adapter 的错误，不能吞掉后退回原值。
//
// 回归测试：早先这里在 err 时返回 (database, collection) 原值，
// 等于给鉴权层留了一条与执行层不一致的退路——拆分失败时
// 鉴权用一套值、执行用另一套，正是要消除的越权入口。
func TestSplitDBCollection_PropagatesError(t *testing.T) {
	// "shop." 末尾为空，集合名为空 → 必须报错
	db, coll, err := SplitDBCollection("shop.", "test")
	if err == nil {
		t.Fatalf("SplitDBCollection(%q) 应当报错，却返回 (%q,%q)", "shop.", db, coll)
	}
}

// 正常输入要与 adapter 的结果完全一致（唯一事实来源）。
func TestSplitDBCollection_MatchesAdapter(t *testing.T) {
	cases := []struct {
		coll, database   string
		wantDB, wantColl string
	}{
		{"users", "shop", "shop", "users"},
		{"shop.users", "test", "shop", "users"},
		{"system.js.slowlog", "", "system.js", "slowlog"},
		{"users", "", "", "users"},
	}
	for _, c := range cases {
		db, coll, err := SplitDBCollection(c.coll, c.database)
		if err != nil {
			t.Errorf("SplitDBCollection(%q,%q) 错误: %v", c.coll, c.database, err)
			continue
		}
		if db != c.wantDB || coll != c.wantColl {
			t.Errorf("SplitDBCollection(%q,%q) = (%q,%q), want (%q,%q)",
				c.coll, c.database, db, coll, c.wantDB, c.wantColl)
		}
	}
}
