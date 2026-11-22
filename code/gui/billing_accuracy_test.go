package main

import (
	"database/sql"
	"math"
	"testing"
)

func TestParseMoneyCurrenciesAndFormats(t *testing.T) {
	cases := []struct {
		input string
		want  float64
	}{
		{"7.41", 7.41},
		{"Mex$ 7.41", 7.41},
		{"MXN 1,234.56", 1234.56},
		{"RM 12.30", 12.3},
		{"S$ 5.00", 5.0},
		{"₱100.50", 100.5},
		{"₫50,000", 50000},
		{"-Mex$ 7.41", -7.41},
		{"Mex$ -7.41", -7.41},
		{"(12.34)", -12.34},
		{"¥100", 100},
		{"£9.99", 9.99},
		{"-", 0},
		{"", 0},
	}
	for _, tc := range cases {
		got := parseMoney(tc.input)
		if math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("parseMoney(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestDuplicateCommissionsAndRewardsHeaders(t *testing.T) {
	headers := []string{
		"Order ID", "SKU ID", "Product ID", "Creator username",
		"Order settlement status", "Commissions & rewards",
		"Total agency earnings before tax", "ISR", "IVA",
		"Commissions & rewards", "Time Created", "Payment Status",
	}
	indexes := buildHeaderIndexes(headers)

	rowNormal := []string{
		"ord-1", "sku-1", "prod-1", "coloringeval",
		"Settled", "0", "7.41", "", "", "7.41", "31/10/2026", "Paid",
	}
	o1 := rowToOrderWithIndexes(rowNormal, headers, indexes, "墨西哥")
	if math.Abs(o1.AgencyTotalIncome-7.41) > 1e-9 {
		t.Fatalf("o1.AgencyTotalIncome = %v, want 7.41", o1.AgencyTotalIncome)
	}
	if o1.CreatedDate != "2026-10-31" {
		t.Fatalf("o1.CreatedDate = %q, want 2026-10-31", o1.CreatedDate)
	}

	// Reverse case: first duplicate column has value, second is 0
	rowReverse := []string{
		"ord-2", "sku-2", "prod-2", "coloringjudie",
		"Settled", "Mex$ 9.27", "9.27", "", "", "0", "31/10/2026", "Paid",
	}
	o2 := rowToOrderWithIndexes(rowReverse, headers, indexes, "墨西哥")
	if math.Abs(o2.AgencyTotalIncome-9.27) > 1e-9 {
		t.Fatalf("o2.AgencyTotalIncome = %v, want 9.27", o2.AgencyTotalIncome)
	}
}

func TestReplaceImportUpdatesSettlementStatusChange(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := &Store{db: db, NextID: 1}
	if err := store.createOrdersSchemaLocked(); err != nil {
		t.Fatal(err)
	}

	oldOrder := Order{
		Country:          "墨西哥",
		OrderID:          "ord-1",
		SKUID:            "sku-1",
		ProductID:        "prod-1",
		TalentUsername:   "coloringeval",
		Quantity:         1,
		SettlementStatus: "待确认",
	}
	key, ok := duplicateOrderKey(oldOrder)
	if !ok {
		t.Fatal("expected duplicate key")
	}
	oldOrder.ID = store.newOrderIDLocked(oldOrder)

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := insertOrderTx(tx, oldOrder); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	newOrder := oldOrder
	newOrder.SettlementStatus = "不符合条件"
	newOrder.Quantity = 2

	existing, err := store.findOrdersForImportCompareLocked([]string{key})
	if err != nil {
		t.Fatal(err)
	}
	_, written, updated, skipped, _, err := store.upsertReplaceOrdersBatchLocked([]Order{newOrder}, existing, nil)
	if err != nil {
		t.Fatal(err)
	}
	if updated != 1 || written != 1 || skipped != 0 {
		t.Fatalf("got written=%d updated=%d skipped=%d, want 1/1/0", written, updated, skipped)
	}

	var gotStatus string
	var gotQty int
	if err := db.QueryRow(`SELECT settlement_status, quantity FROM orders WHERE duplicate_key = ?`, key).Scan(&gotStatus, &gotQty); err != nil {
		t.Fatal(err)
	}
	if gotStatus != "不符合条件" || gotQty != 2 {
		t.Fatalf("got status=%q qty=%d, want 不符合条件/2", gotStatus, gotQty)
	}
}

func TestEffectiveSettlementStatusSQL(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := &Store{db: db, NextID: 1}
	if err := store.createOrdersSchemaLocked(); err != nil {
		t.Fatal(err)
	}

	orders := []Order{
		{ID: "1", Country: "墨西哥", OrderID: "1", SKUID: "1", ProductID: "1", TalentUsername: "t1", Quantity: 1, SettlementStatus: "已结算", PaymentStatus: "已付款", AgencyTotalIncome: 10},
		{ID: "2", Country: "墨西哥", OrderID: "2", SKUID: "1", ProductID: "1", TalentUsername: "t1", Quantity: 1, SettlementStatus: "已结算", PaymentStatus: "未付款", AgencyTotalIncome: 5},
		{ID: "3", Country: "墨西哥", OrderID: "3", SKUID: "1", ProductID: "1", TalentUsername: "t1", Quantity: 1, SettlementStatus: "已结算", PaymentStatus: "", AgencyTotalIncome: 3},
		{ID: "4", Country: "墨西哥", OrderID: "4", SKUID: "1", ProductID: "1", TalentUsername: "t1", Quantity: 1, SettlementStatus: "不符合条件", PaymentStatus: "", AgencyTotalIncome: 0},
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range orders {
		if err := insertOrderTx(tx, o); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	resp := store.dbStatsLocked(StatsRequest{Country: "墨西哥"})
	got := map[string]StatusSummary{}
	for _, s := range resp.StatusStats {
		got[s.Status] = s
	}
	if got["已结算"].Orders != 2 || math.Abs(got["已结算"].Total-13) > 1e-9 {
		t.Fatalf("已结算 = %+v, want orders=2 total=13", got["已结算"])
	}
	if got["机构未付款"].Orders != 1 || math.Abs(got["机构未付款"].Total-5) > 1e-9 {
		t.Fatalf("机构未付款 = %+v, want orders=1 total=5", got["机构未付款"])
	}
	if got["不符合条件"].Orders != 1 {
		t.Fatalf("不符合条件 = %+v, want orders=1", got["不符合条件"])
	}
}

func TestRoundCentsAndAgencySettlementConsistency(t *testing.T) {
	if got := moneyText(16.675); got != "16.68" {
		t.Fatalf("moneyText(16.675) = %q, want 16.68", got)
	}
	if got := moneyText(16.674); got != "16.67" {
		t.Fatalf("moneyText(16.674) = %q, want 16.67", got)
	}

	usd := 1.0
	rmb := 7.2
	cut := 10.0
	fee := 6.0
	req := StatsRequest{
		USDRate:       &usd,
		RMBRate:       &rmb,
		AgencyCutRate: &cut,
		FeePermille:   &fee,
	}
	row := agencySettlementExportRow(req, []StatusSummary{{Status: "已结算", Total: 100.05}})
	// 100.05 * 7.2 * 0.9 = 648.324 -> roundCents = 648.32
	// fee = 648.32 * 0.006 = 3.88992 -> roundCents = 3.89
	// actual = 648.32 - 3.89 = 644.43
	if row[5] != "3.89" || row[6] != "644.43" {
		t.Fatalf("agencySettlementExportRow fee=%v actual=%v, want 3.89 and 644.43", row[5], row[6])
	}
}

func TestNormalizeDateExcelSerial(t *testing.T) {
	if got := normalizeDate("46326"); got != "2026-10-31" {
		t.Fatalf("normalizeDate(46326) = %q, want 2026-10-31", got)
	}
}

func TestMalaysiaPretaxFallbackWhenCommissionsRewardsZero(t *testing.T) {
	headers := []string{
		"Order ID", "SKU ID", "Product ID", "Creator username",
		"Order settlement status", "Commissions & rewards",
		"Total agency earnings before tax", "Time Created", "Payment Status",
	}
	indexes := buildHeaderIndexes(headers)
	row := []string{
		"585841979143718219", "sku-1", "prod-1", "h7jwqlgrn0",
		"Settled", "0", "3.40", "01/09/2026 23:36:59", "Paid",
	}
	o := rowToOrderWithIndexes(row, headers, indexes, "马来西亚")
	if math.Abs(o.AgencyTotalIncome-3.40) > 1e-9 {
		t.Fatalf("o.AgencyTotalIncome = %v, want 3.40", o.AgencyTotalIncome)
	}
	if math.Abs(o.AgencyPretaxTotalIncome-3.40) > 1e-9 {
		t.Fatalf("o.AgencyPretaxTotalIncome = %v, want 3.40", o.AgencyPretaxTotalIncome)
	}

	cnHeaders := []string{
		"订单 ID", "SKU ID", "商品 ID", "达人用户名",
		"订单结算状态", "佣金与奖励", "代理机构税前总收益", "ISR", "IVA", "佣金与奖励",
		"创建时间", "付款状态",
	}
	cnIndexes := buildHeaderIndexes(cnHeaders)
	cnRow := []string{
		"587280806412846446", "1733245104480421304", "1733245102728512952", "h7jwqlgrn0",
		"已结算", "0", "1.02", "", "", "1.02",
		"20/11/2026 08:59:06", "未付款",
	}
	oCN := rowToOrderWithIndexes(cnRow, cnHeaders, cnIndexes, "马来西亚")
	if math.Abs(oCN.AgencyTotalIncome-1.02) > 1e-9 {
		t.Fatalf("oCN.AgencyTotalIncome = %v, want 1.02", oCN.AgencyTotalIncome)
	}
	if oCN.CreatedDate != "2026-11-20" {
		t.Fatalf("oCN.CreatedDate = %q, want 2026-11-20", oCN.CreatedDate)
	}
}

func TestMigrateOrderPretaxFallbackLocked(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := &Store{db: db, NextID: 1}
	if err := store.createOrdersSchemaLocked(); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := insertOrderTx(tx, Order{
		ID:                      "my-1",
		Country:                 "马来西亚",
		OrderID:                 "585841979143718219",
		SKUID:                   "sku-1",
		ProductID:               "prod-1",
		TalentUsername:          "h7jwqlgrn0",
		Quantity:                1,
		SettlementStatus:        "已结算",
		PaymentStatus:           "已付款",
		AgencyPretaxTotalIncome: 3.40,
		AgencyTotalIncome:       0,
	}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := store.migrateOrderPretaxFallbackLocked(); err != nil {
		t.Fatal(err)
	}
	var got float64
	if err := db.QueryRow(`SELECT agency_total_income FROM orders WHERE id = 'my-1'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if math.Abs(got-3.40) > 1e-9 {
		t.Fatalf("migrated agency_total_income = %v, want 3.40", got)
	}
}
