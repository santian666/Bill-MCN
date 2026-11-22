package main

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"
)

func TestDuplicateOrderKeyIncludesCountry(t *testing.T) {
	base := Order{
		OrderID:        "order-1",
		SKUID:          "sku-1",
		ProductID:      "product-1",
		TalentUsername: "talent-1",
	}

	sg := base
	sg.Country = "新加坡"
	th := base
	th.Country = "泰国"

	sgKey, ok := duplicateOrderKey(sg)
	if !ok {
		t.Fatal("新加坡订单应该生成重复判断 key")
	}
	thKey, ok := duplicateOrderKey(th)
	if !ok {
		t.Fatal("泰国订单应该生成重复判断 key")
	}
	if sgKey == thKey {
		t.Fatalf("不同国家的相同订单信息不能生成相同 key: %q", sgKey)
	}
	if !strings.HasPrefix(sgKey, "新加坡|") || !strings.HasPrefix(thKey, "泰国|") {
		t.Fatalf("重复判断 key 必须包含国家，得到 %q 和 %q", sgKey, thKey)
	}

	missingCountry := base
	if key, ok := duplicateOrderKey(missingCountry); ok || key != "" {
		t.Fatalf("缺少国家时不能生成重复判断 key，得到 key=%q ok=%v", key, ok)
	}
}

func TestBuildOrderWhereRequiresCountry(t *testing.T) {
	where, args := buildOrderWhere(StatsRequest{}, nil)
	if where != "WHERE 1 = 0" {
		t.Fatalf("未选择国家时必须返回空结果条件，得到 %q", where)
	}
	if len(args) != 0 {
		t.Fatalf("未选择国家时不应产生参数，得到 %#v", args)
	}
}

func TestBuildOrderWhereKeepsCountryWithFilters(t *testing.T) {
	req := StatsRequest{
		Country:          "新加坡",
		Start:            "2026-01-01",
		End:              "2026-01-31",
		SettlementStatus: "已结算",
	}
	where, args := buildOrderWhere(req, []string{"账号A", "账号B"})

	wantWhere := "WHERE country = ? AND created_date >= ? AND created_date <= ? AND settlement_status = ? AND talent_username IN (?,?)"
	if where != wantWhere {
		t.Fatalf("筛选条件不符合预期\nwant: %s\n got: %s", wantWhere, where)
	}
	wantArgs := []any{"新加坡", "2026-01-01", "2026-01-31", "已结算", "账号A", "账号B"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("筛选参数不符合预期\nwant: %#v\n got: %#v", wantArgs, args)
	}
}

func TestDeleteCountryOrdersDoesNotDeleteOtherCountries(t *testing.T) {
	store := newTestStore(t)
	insertTestOrder(t, store, Order{
		ID:             "sg-1",
		Country:        "\u65b0\u52a0\u5761",
		OrderID:        "order-1",
		SKUID:          "sku-1",
		ProductID:      "product-1",
		TalentUsername: "talent-1",
	})
	insertTestOrder(t, store, Order{
		ID:             "th-1",
		Country:        "\u6cf0\u56fd",
		OrderID:        "order-1",
		SKUID:          "sku-1",
		ProductID:      "product-1",
		TalentUsername: "talent-1",
	})

	deleted, err := store.dbDeleteCountryOrdersLocked("\u65b0\u52a0\u5761")
	if err != nil {
		t.Fatalf("删除当前国家订单失败: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("应该只删除当前国家 1 条订单，实际删除 %d 条", deleted)
	}

	assertCountryCount(t, store, "\u65b0\u52a0\u5761", 0)
	assertCountryCount(t, store, "\u6cf0\u56fd", 1)
}

func TestDeleteCountryOrdersWithEmptyCountryDeletesNothing(t *testing.T) {
	store := newTestStore(t)
	insertTestOrder(t, store, Order{
		ID:             "sg-1",
		Country:        "\u65b0\u52a0\u5761",
		OrderID:        "order-1",
		SKUID:          "sku-1",
		ProductID:      "product-1",
		TalentUsername: "talent-1",
	})

	deleted, err := store.dbDeleteCountryOrdersLocked("")
	if err != nil {
		t.Fatalf("空国家删除不应报错: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("空国家不能删除任何订单，实际删除 %d 条", deleted)
	}
	assertCountryCount(t, store, "\u65b0\u52a0\u5761", 1)
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := &Store{db: db}
	if err := store.createOrdersSchemaLocked(); err != nil {
		t.Fatalf("创建测试表失败: %v", err)
	}
	return store
}

func insertTestOrder(t *testing.T, store *Store, order Order) {
	t.Helper()
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatalf("开始事务失败: %v", err)
	}
	if err := insertOrderTx(tx, order); err != nil {
		_ = tx.Rollback()
		t.Fatalf("插入测试订单失败: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("提交测试订单失败: %v", err)
	}
}

func assertCountryCount(t *testing.T, store *Store, country string, want int) {
	t.Helper()
	var got int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM orders WHERE country = ?`, country).Scan(&got); err != nil {
		t.Fatalf("读取国家订单数失败: %v", err)
	}
	if got != want {
		t.Fatalf("国家 %q 订单数不符合预期，want=%d got=%d", country, want, got)
	}
}
