package main

import (
	"bufio"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"
	_ "modernc.org/sqlite"
)

func (s *Store) openOrdersDBLocked() error {
	if s.db != nil {
		return nil
	}
	if err := os.MkdirAll(filepathDir(s.dbPath), 0755); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", s.dbPath)
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	s.db = db
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL; PRAGMA temp_store=MEMORY; PRAGMA busy_timeout=5000;`); err != nil {
		return err
	}
	return s.createOrdersSchemaLocked()
}

func filepathDir(path string) string {
	i := strings.LastIndexAny(path, `\/`)
	if i < 0 {
		return "."
	}
	return path[:i]
}

func (s *Store) createOrdersSchemaLocked() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS orders (
  id TEXT PRIMARY KEY,
  duplicate_key TEXT NOT NULL DEFAULT '',
  country TEXT NOT NULL DEFAULT '',
  order_id TEXT NOT NULL DEFAULT '',
  sku_id TEXT NOT NULL DEFAULT '',
  product_id TEXT NOT NULL DEFAULT '',
  product_name TEXT NOT NULL DEFAULT '',
  product_price REAL NOT NULL DEFAULT 0,
  quantity INTEGER NOT NULL DEFAULT 0,
  settlement_status TEXT NOT NULL DEFAULT '',
  talent_username TEXT NOT NULL DEFAULT '',
  standard_commission_rate TEXT NOT NULL DEFAULT '',
  shop_ad_commission_rate TEXT NOT NULL DEFAULT '',
  commission_gmv REAL NOT NULL DEFAULT 0,
  estimated_base_commission REAL NOT NULL DEFAULT 0,
  estimated_standard_commission REAL NOT NULL DEFAULT 0,
  commission_base REAL NOT NULL DEFAULT 0,
  standard_commission REAL NOT NULL DEFAULT 0,
  before_split_talent_income REAL NOT NULL DEFAULT 0,
  agency_split_rate TEXT NOT NULL DEFAULT '',
  talent_total_income REAL NOT NULL DEFAULT 0,
  agency_pretax_total_income REAL NOT NULL DEFAULT 0,
  agency_total_income REAL NOT NULL DEFAULT 0,
  invitation_id TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT '',
  created_date TEXT NOT NULL DEFAULT '',
  delivered_at TEXT NOT NULL DEFAULT '',
  payment_id TEXT NOT NULL DEFAULT '',
  payment_status TEXT NOT NULL DEFAULT '',
  raw_json TEXT NOT NULL DEFAULT '{}'
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_orders_duplicate ON orders(duplicate_key) WHERE duplicate_key <> '';
CREATE INDEX IF NOT EXISTS idx_orders_country_date ON orders(country, created_date);
CREATE INDEX IF NOT EXISTS idx_orders_country_status_date ON orders(country, settlement_status, created_date);
CREATE INDEX IF NOT EXISTS idx_orders_status ON orders(settlement_status);
CREATE INDEX IF NOT EXISTS idx_orders_talent ON orders(talent_username);
CREATE INDEX IF NOT EXISTS idx_orders_talent_date ON orders(talent_username, created_date);
CREATE INDEX IF NOT EXISTS idx_orders_country_talent_date ON orders(country, talent_username, created_date);
CREATE INDEX IF NOT EXISTS idx_orders_country_status_talent_date ON orders(country, settlement_status, talent_username, created_date);
`)
	if err != nil {
		return err
	}
	return s.refreshDuplicateKeysLocked()
}

func (s *Store) refreshDuplicateKeysLocked() error {
	_, err := s.db.Exec(`
UPDATE orders
SET duplicate_key = lower(country || '|' || order_id || '|' || sku_id || '|' || product_id || '|' || talent_username)
WHERE country <> ''
  AND order_id <> ''
  AND sku_id <> ''
  AND product_id <> ''
  AND talent_username <> ''
  AND duplicate_key <> lower(country || '|' || order_id || '|' || sku_id || '|' || product_id || '|' || talent_username);
`)
	return err
}

func (s *Store) migrateJSONOrdersLocked() error {
	if len(s.Orders) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(upsertOrderSQL())
	if err != nil {
		return err
	}
	defer stmt.Close()
	for i := range s.Orders {
		date := normalizeDate(s.Orders[i].CreatedAt)
		if s.Orders[i].CreatedDate != date {
			s.Orders[i].CreatedDate = date
		}
		if s.Orders[i].SettlementStatus == "" {
			if status := settlementStatusFromRaw(s.Orders[i].Raw); status != "" {
				s.Orders[i].SettlementStatus = status
			}
		}
		if s.Orders[i].ID == "" {
			s.Orders[i].ID = s.newIDLocked("o")
		}
		if err := execUpsertOrder(stmt, s.Orders[i]); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.Orders = nil
	return nil
}

func (s *Store) orderCountLocked() int {
	var count int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM orders`).Scan(&count)
	return count
}

func (s *Store) dbDateRangesLocked() (DateRange, map[string]DateRange) {
	global := DateRange{}
	countryRanges := map[string]DateRange{}
	_ = s.db.QueryRow(`SELECT COALESCE(MIN(created_date), ''), COALESCE(MAX(created_date), '') FROM orders WHERE created_date <> ''`).Scan(&global.Start, &global.End)
	rows, err := s.db.Query(`SELECT country, MIN(created_date), MAX(created_date) FROM orders WHERE created_date <> '' AND country <> '' GROUP BY country`)
	if err != nil {
		return global, countryRanges
	}
	defer rows.Close()
	for rows.Next() {
		var country, start, end string
		if rows.Scan(&country, &start, &end) == nil {
			countryRanges[country] = DateRange{Start: start, End: end}
		}
	}
	return global, countryRanges
}

func (s *Store) previewImportFileLocked(path, filename, country string) (ImportResult, error) {
	fieldCounts := map[string]int{}
	result := ImportResult{}
	batch := make([]Order, 0, 2000)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		keys := make([]string, 0, len(batch))
		for _, order := range batch {
			if key, ok := duplicateOrderKey(order); ok {
				keys = append(keys, key)
			}
		}
		existing, err := s.findOrdersByDuplicateKeysLocked(keys)
		if err != nil {
			return err
		}
		for _, order := range batch {
			key, ok := duplicateOrderKey(order)
			if !ok {
				continue
			}
			oldOrder, exists := existing[key]
			if !exists {
				continue
			}
			result.DuplicateCount++
			changes := compareOrders(oldOrder, order)
			if len(changes) == 0 {
				result.UnchangedDuplicateCount++
				continue
			}
			result.ChangedDuplicateCount++
			for _, change := range changes {
				fieldCounts[change.Field]++
			}
			if len(result.Samples) < 20 {
				result.Samples = append(result.Samples, DuplicateChange{Key: key, Changes: changes})
			}
		}
		batch = batch[:0]
		return nil
	}
	err := parseUploadedOrdersEach(path, filename, country, func(order Order) error {
		if _, ok := duplicateOrderKey(order); !ok {
			return nil
		}
		batch = append(batch, order)
		if len(batch) >= 2000 {
			return flush()
		}
		return nil
	})
	if err == nil {
		err = flush()
	}
	result.ChangedFields = changedFieldSummaries(fieldCounts)
	return result, err
}

func (s *Store) applyImportFileLocked(path, filename, country string, preview ImportResult) (ImportResult, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return preview, err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(upsertOrderSQL())
	if err != nil {
		return preview, err
	}
	defer stmt.Close()
	result := preview
	result.RequiresConfirm = false
	result.Imported = 0
	result.Updated = 0
	processed := 0
	err = parseUploadedOrdersEach(path, filename, country, func(order Order) error {
		order.ID = s.newIDLocked("o")
		processed++
		return execUpsertOrder(stmt, order)
	})
	if err != nil {
		return result, err
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	result.Updated = result.DuplicateCount
	result.Imported = processed - result.Updated
	if result.Imported < 0 {
		result.Imported = 0
	}
	return result, nil
}

func (s *Store) findOrderByDuplicateKeyLocked(key string) (Order, bool, error) {
	row := s.db.QueryRow(orderSelectSQL()+` WHERE duplicate_key = ? LIMIT 1`, key)
	order, err := scanOrder(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Order{}, false, nil
	}
	if err != nil {
		return Order{}, false, err
	}
	return order, true, nil
}

func (s *Store) findOrdersByDuplicateKeysLocked(keys []string) (map[string]Order, error) {
	result := map[string]Order{}
	if len(keys) == 0 {
		return result, nil
	}
	seen := map[string]bool{}
	unique := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, key)
	}
	const batchSize = 900
	for start := 0; start < len(unique); start += batchSize {
		end := start + batchSize
		if end > len(unique) {
			end = len(unique)
		}
		part := unique[start:end]
		marks := make([]string, len(part))
		args := make([]any, len(part))
		for i, key := range part {
			marks[i] = "?"
			args[i] = key
		}
		rows, err := s.db.Query(orderSelectSQL()+` WHERE duplicate_key IN (`+strings.Join(marks, ",")+`)`, args...)
		if err != nil {
			return result, err
		}
		for rows.Next() {
			order, err := scanOrderRows(rows)
			if err != nil {
				_ = rows.Close()
				return result, err
			}
			if key, ok := duplicateOrderKey(order); ok {
				result[key] = order
			}
		}
		if err := rows.Close(); err != nil {
			return result, err
		}
		if err := rows.Err(); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (s *Store) dbStatsLocked(req StatsRequest) StatsResponse {
	usernames, title := s.resolveScopeLocked(req)
	usernames = uniqueUsernames(usernames)
	where, args := buildOrderWhere(req, usernames)
	resp := StatsResponse{FilterRange: DateRange{Start: req.Start, End: req.End}, Title: title}
	_ = s.db.QueryRow(`SELECT COALESCE(SUM(agency_total_income), 0), COUNT(*), COUNT(DISTINCT CASE WHEN talent_username = '' THEN '未识别账号' ELSE talent_username END) FROM orders `+where, args...).Scan(&resp.Total, &resp.OrderCount, &resp.TalentCount)
	if len(usernames) > 0 {
		resp.TalentCount = len(usernames)
	}
	resp.StatusStats = s.dbStatusStatsLocked(where, args)
	resp.PerTalent = s.dbPerTalentLocked(where, args, usernames)
	return resp
}

func (s *Store) dbStatusStatsLocked(where string, args []any) []StatusSummary {
	stats := map[string]*StatusSummary{}
	for _, status := range settlementStatuses {
		stats[status] = &StatusSummary{Status: status}
	}
	rows, err := s.db.Query(`SELECT CASE WHEN settlement_status = '' THEN '未识别状态' ELSE settlement_status END, COUNT(*), COALESCE(SUM(agency_total_income), 0) FROM orders `+where+` GROUP BY CASE WHEN settlement_status = '' THEN '未识别状态' ELSE settlement_status END`, args...)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			item := StatusSummary{}
			if rows.Scan(&item.Status, &item.Orders, &item.Total) == nil {
				stats[item.Status] = &item
			}
		}
	}
	result := make([]StatusSummary, 0, len(stats))
	for _, status := range settlementStatuses {
		result = append(result, *stats[status])
		delete(stats, status)
	}
	var extra []string
	for status := range stats {
		extra = append(extra, status)
	}
	sort.Strings(extra)
	for _, status := range extra {
		result = append(result, *stats[status])
	}
	return result
}

func (s *Store) dbPerTalentLocked(where string, args []any, usernames []string) []TalentSummary {
	per := map[string]*TalentSummary{}
	var ordered []string
	for _, username := range usernames {
		key := strings.ToLower(strings.TrimSpace(username))
		if key == "" {
			continue
		}
		if _, ok := per[key]; ok {
			continue
		}
		per[key] = &TalentSummary{Username: username, StatusTotals: map[string]float64{}, StatusOrders: map[string]int{}}
		ordered = append(ordered, key)
	}
	rows, err := s.db.Query(`SELECT CASE WHEN talent_username = '' THEN '未识别账号' ELSE talent_username END, CASE WHEN settlement_status = '' THEN '未识别状态' ELSE settlement_status END, COUNT(*), COALESCE(SUM(agency_total_income), 0) FROM orders `+where+` GROUP BY CASE WHEN talent_username = '' THEN '未识别账号' ELSE talent_username END, CASE WHEN settlement_status = '' THEN '未识别状态' ELSE settlement_status END`, args...)
	if err != nil {
		return perTalentList(per, ordered)
	}
	defer rows.Close()
	for rows.Next() {
		var username, status string
		var orders int
		var total float64
		if rows.Scan(&username, &status, &orders, &total) != nil {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(username))
		if key == "" {
			key = username
		}
		if _, ok := per[key]; !ok {
			per[key] = &TalentSummary{Username: username, StatusTotals: map[string]float64{}, StatusOrders: map[string]int{}}
		}
		per[key].Total += total
		per[key].Orders += orders
		per[key].StatusTotals[status] += total
		per[key].StatusOrders[status] += orders
	}
	return perTalentList(per, ordered)
}

func perTalentList(per map[string]*TalentSummary, ordered []string) []TalentSummary {
	list := make([]TalentSummary, 0, len(per))
	if len(ordered) > 0 {
		used := map[string]bool{}
		for _, key := range ordered {
			if item, ok := per[key]; ok {
				list = append(list, *item)
				used[key] = true
			}
		}
		var extra []string
		for key := range per {
			if !used[key] {
				extra = append(extra, key)
			}
		}
		sort.Strings(extra)
		for _, key := range extra {
			list = append(list, *per[key])
		}
		return list
	}
	for _, item := range per {
		list = append(list, *item)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Total > list[j].Total })
	return list
}

func uniqueUsernames(usernames []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(usernames))
	for _, username := range usernames {
		name := strings.TrimSpace(username)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, name)
	}
	return result
}

func (s *Store) dbOrdersPageLocked(req StatsRequest, page, pageSize int) (OrdersResponse, error) {
	usernames, _ := s.resolveScopeLocked(req)
	where, args := buildOrderWhere(req, usernames)
	resp := OrdersResponse{PageSize: pageSize, FilterRange: DateRange{Start: req.Start, End: req.End}}
	if err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(agency_total_income), 0) FROM orders `+where, args...).Scan(&resp.Total, &resp.Income); err != nil {
		return resp, err
	}
	if resp.Total > 0 {
		resp.TotalPages = (resp.Total + pageSize - 1) / pageSize
		if page > resp.TotalPages {
			page = resp.TotalPages
		}
	}
	resp.Page = page
	resp.StatusStats = s.dbStatusStatsLocked(where, args)
	offset := 0
	if page > 1 {
		offset = (page - 1) * pageSize
	}
	qargs := append(append([]any{}, args...), pageSize, offset)
	rows, err := s.db.Query(orderSelectSQL()+` `+where+` ORDER BY created_date DESC, order_id DESC LIMIT ? OFFSET ?`, qargs...)
	if err != nil {
		return resp, err
	}
	defer rows.Close()
	for rows.Next() {
		order, err := scanOrderRows(rows)
		if err == nil {
			resp.Orders = append(resp.Orders, order)
		}
	}
	return resp, rows.Err()
}

func (s *Store) writeExportXLSXLocked(w http.ResponseWriter, req StatsRequest, exportType string) error {
	if exportType == "" {
		exportType = "orders"
	}
	f := excelize.NewFile()
	defer f.Close()
	switch exportType {
	case "summary":
		f.SetSheetName("Sheet1", "筛选结果")
		if err := s.writeSummarySheetLocked(f, "筛选结果", req); err != nil {
			return err
		}
	case "both":
		f.SetSheetName("Sheet1", "筛选结果")
		if _, err := f.NewSheet("订单详情"); err != nil {
			return err
		}
		if err := s.writeSummarySheetLocked(f, "筛选结果", req); err != nil {
			return err
		}
		if err := s.writeOrdersSheetLocked(f, "订单详情", req); err != nil {
			return err
		}
		f.SetActiveSheet(0)
	case "orders":
		f.SetSheetName("Sheet1", "订单详情")
		if err := s.writeOrdersSheetLocked(f, "订单详情", req); err != nil {
			return err
		}
	default:
		return errors.New("导出类型不支持")
	}
	return f.Write(w)
}

func (s *Store) writeSummarySheetLocked(f *excelize.File, sheet string, req StatsRequest) error {
	resp := s.dbStatsLocked(req)
	sw, err := f.NewStreamWriter(sheet)
	if err != nil {
		return err
	}
	rows := [][]any{
		{"筛选结果"},
		{"总订单数", resp.OrderCount, "总结算金额", moneyText(resp.Total), "账号", resp.TalentCount},
		{"账号", "已结算", "客户未付款", "待确认", "不符合条件", "已冻结", "订单数"},
	}
	for i, row := range rows {
		cell, _ := excelize.CoordinatesToCellName(1, i+1)
		if err := sw.SetRow(cell, row); err != nil {
			return err
		}
	}
	for i, item := range resp.PerTalent {
		row := []any{
			item.Username,
			moneyText(talentSummaryStatusTotal(item, "已结算")),
			moneyText(talentSummaryStatusTotal(item, "客户未付款")),
			moneyText(talentSummaryStatusTotal(item, "待确认")),
			moneyText(talentSummaryStatusTotal(item, "不符合条件")),
			moneyText(talentSummaryStatusTotal(item, "已冻结")),
			item.Orders,
		}
		cell, _ := excelize.CoordinatesToCellName(1, i+4)
		if err := sw.SetRow(cell, row); err != nil {
			return err
		}
	}
	return sw.Flush()
}

func talentSummaryStatusTotal(item TalentSummary, status string) float64 {
	if item.StatusTotals == nil {
		return 0
	}
	return item.StatusTotals[status]
}

func (s *Store) writeOrdersSheetLocked(f *excelize.File, sheet string, req StatsRequest) error {
	usernames, _ := s.resolveScopeLocked(req)
	where, args := buildOrderWhere(req, usernames)
	rows, err := s.db.Query(orderSelectSQL()+` `+where+` ORDER BY created_date DESC, order_id DESC`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	sw, err := f.NewStreamWriter(sheet)
	if err != nil {
		return err
	}
	headers := []any{"国家", "订单 ID", "SKU ID", "商品 ID", "商品名称", "商品价格", "下单件数", "订单结算状态", "达人用户名", "标准佣金率", "店铺广告佣金率", "佣金 GMV", "预计基础佣金", "预计标准佣金", "佣金基数", "标准佣金", "分成前的达人收益", "代理机构佣金分成比例", "达人总收益", "代理机构税前总收益", "代理机构总收益", "Invitation ID", "创建时间", "订单送达时间", "付款 ID", "付款状态"}
	if err := sw.SetRow("A1", headers); err != nil {
		return err
	}
	rowNo := 2
	for rows.Next() {
		o, err := scanOrderRows(rows)
		if err != nil {
			return err
		}
		row := []any{
			o.Country, o.OrderID, o.SKUID, o.ProductID, o.ProductName, moneyText(o.ProductPrice), strconv.Itoa(o.Quantity), o.SettlementStatus, o.TalentUsername,
			o.StandardCommissionRate, o.ShopAdCommissionRate, moneyText(o.CommissionGMV), moneyText(o.EstimatedBaseCommission),
			moneyText(o.EstimatedStandardCommission), moneyText(o.CommissionBase), moneyText(o.StandardCommission),
			moneyText(o.BeforeSplitTalentIncome), o.AgencySplitRate, moneyText(o.TalentTotalIncome),
			moneyText(o.AgencyPretaxTotalIncome), moneyText(o.AgencyTotalIncome), o.InvitationID, o.CreatedAt, o.DeliveredAt,
			o.PaymentID, o.PaymentStatus,
		}
		cell, _ := excelize.CoordinatesToCellName(1, rowNo)
		if err := sw.SetRow(cell, row); err != nil {
			return err
		}
		rowNo++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return sw.Flush()
}

func (s *Store) dbDeleteCountryOrdersLocked(country string) (int64, error) {
	country = strings.TrimSpace(country)
	if country == "" {
		return 0, nil
	}
	result, err := s.db.Exec(`DELETE FROM orders WHERE country = ?`, country)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) dbDeleteOrdersLocked(req StatsRequest) (int64, error) {
	usernames, _ := s.resolveScopeLocked(req)
	where, args := buildOrderWhere(req, usernames)
	result, err := s.db.Exec(`DELETE FROM orders `+where, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func buildOrderWhere(req StatsRequest, usernames []string) (string, []any) {
	var parts []string
	var args []any
	if req.Country != "" {
		parts = append(parts, "country = ?")
		args = append(args, req.Country)
	} else {
		parts = append(parts, "1 = 0")
	}
	if req.Start != "" {
		parts = append(parts, "created_date >= ?")
		args = append(args, req.Start)
	}
	if req.End != "" {
		parts = append(parts, "created_date <= ?")
		args = append(args, req.End)
	}
	if req.SettlementStatus != "" {
		parts = append(parts, "settlement_status = ?")
		args = append(args, req.SettlementStatus)
	}
	if req.Scope != "" && req.Scope != "manual" && len(usernames) == 0 {
		parts = append(parts, "1 = 0")
	}
	if len(usernames) > 0 {
		marks := make([]string, 0, len(usernames))
		for _, name := range usernames {
			marks = append(marks, "?")
			args = append(args, strings.TrimSpace(name))
		}
		parts = append(parts, "talent_username IN ("+strings.Join(marks, ",")+")")
	}
	if len(parts) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(parts, " AND "), args
}

func parseUploadedOrdersEach(path, filename, country string, handle func(Order) error) error {
	name := strings.ToLower(filename)
	if strings.HasSuffix(name, ".xlsx") {
		return parseXLSXEach(path, country, handle)
	}
	if strings.HasSuffix(name, ".csv") || strings.HasSuffix(name, ".txt") || strings.HasSuffix(name, ".tsv") {
		return parseDelimitedEach(path, country, handle)
	}
	return errors.New("仅支持 xlsx、csv、tsv、txt 账单文件")
}

func parseXLSXEach(path, country string, handle func(Order) error) error {
	f, err := excelize.OpenFile(path)
	if err != nil {
		return errors.New("Excel 文件解析失败")
	}
	defer f.Close()
	for _, sheet := range f.GetSheetList() {
		rows, err := f.Rows(sheet)
		if err != nil {
			continue
		}
		count, err := parseRowsIterator(rows, country, handle)
		_ = rows.Close()
		if err != nil {
			return err
		}
		if count > 0 {
			return nil
		}
	}
	return errors.New("未识别到有效账单数据")
}

type rowIterator interface {
	Next() bool
	Columns(...excelize.Options) ([]string, error)
}

func parseRowsIterator(rows rowIterator, country string, handle func(Order) error) (int, error) {
	var headers []string
	index := map[string]int{}
	count := 0
	for rows.Next() {
		row, err := rows.Columns()
		if err != nil {
			return count, err
		}
		if headers == nil {
			score := 0
			for _, cell := range row {
				key := normalizeHeader(cell)
				if key == "orderid" || key == "talentusername" || key == "agencytotalincome" || key == "createdtime" {
					score++
				}
			}
			if score < 2 {
				continue
			}
			headers = row
			for i, h := range headers {
				key := normalizeHeader(h)
				if key != "" {
					index[key] = i
				}
			}
			continue
		}
		if isBlankRow(row) {
			continue
		}
		order := rowToOrder(row, headers, index, country)
		if order.TalentUsername == "" && order.OrderID == "" && order.AgencyTotalIncome == 0 {
			continue
		}
		if err := handle(order); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func parseDelimitedEach(path, country string, handle func(Order) error) error {
	file, err := os.Open(path)
	if err != nil {
		return errors.New("读取文件失败")
	}
	defer file.Close()
	peek, _ := io.ReadAll(io.LimitReader(file, 4096))
	_, _ = file.Seek(0, 0)
	delimiter := ','
	if strings.Count(string(peek), "\t") > strings.Count(string(peek), ",") {
		delimiter = '\t'
	}
	reader := csv.NewReader(bufio.NewReader(file))
	reader.Comma = delimiter
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true
	var headers []string
	index := map[string]int{}
	count := 0
	for {
		row, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return errors.New("表格文本解析失败")
		}
		if headers == nil {
			score := 0
			for _, cell := range row {
				key := normalizeHeader(cell)
				if key == "orderid" || key == "talentusername" || key == "agencytotalincome" || key == "createdtime" {
					score++
				}
			}
			if score < 2 {
				continue
			}
			headers = row
			for i, h := range headers {
				key := normalizeHeader(h)
				if key != "" {
					index[key] = i
				}
			}
			continue
		}
		if isBlankRow(row) {
			continue
		}
		order := rowToOrder(row, headers, index, country)
		if order.TalentUsername == "" && order.OrderID == "" && order.AgencyTotalIncome == 0 {
			continue
		}
		if err := handle(order); err != nil {
			return err
		}
		count++
	}
	if count == 0 {
		return errors.New("未识别到有效账单数据")
	}
	return nil
}

func rowToOrder(row, headers []string, index map[string]int, country string) Order {
	raw := map[string]string{}
	for i, h := range headers {
		if strings.TrimSpace(h) == "" {
			continue
		}
		raw[strings.TrimSpace(h)] = cell(row, i)
	}
	o := Order{
		Country:                     country,
		OrderID:                     value(row, index, "orderid"),
		SKUID:                       value(row, index, "skuid"),
		ProductID:                   value(row, index, "productid"),
		ProductName:                 value(row, index, "productname"),
		ProductPrice:                parseMoney(value(row, index, "productprice")),
		Quantity:                    parseInt(value(row, index, "quantity")),
		SettlementStatus:            value(row, index, "settlementstatus"),
		TalentUsername:              strings.TrimSpace(value(row, index, "talentusername")),
		StandardCommissionRate:      value(row, index, "standardcommissionrate"),
		ShopAdCommissionRate:        value(row, index, "shopadcommissionrate"),
		CommissionGMV:               parseMoney(value(row, index, "commissiongmv")),
		EstimatedBaseCommission:     parseMoney(value(row, index, "estimatedbasecommission")),
		EstimatedStandardCommission: parseMoney(value(row, index, "estimatedstandardcommission")),
		CommissionBase:              parseMoney(value(row, index, "commissionbase")),
		StandardCommission:          parseMoney(value(row, index, "standardcommission")),
		BeforeSplitTalentIncome:     parseMoney(value(row, index, "beforesplittalentincome")),
		AgencySplitRate:             value(row, index, "agencysplitrate"),
		TalentTotalIncome:           parseMoney(value(row, index, "talenttotalincome")),
		AgencyPretaxTotalIncome:     parseMoney(value(row, index, "agencypretaxtotalincome")),
		AgencyTotalIncome:           parseMoney(value(row, index, "agencytotalincome")),
		InvitationID:                value(row, index, "invitationid"),
		CreatedAt:                   value(row, index, "createdtime"),
		DeliveredAt:                 value(row, index, "deliveredtime"),
		PaymentID:                   value(row, index, "paymentid"),
		PaymentStatus:               value(row, index, "paymentstatus"),
		Raw:                         raw,
	}
	o.CreatedDate = normalizeDate(o.CreatedAt)
	return o
}

func orderSelectSQL() string {
	return `SELECT id, country, order_id, sku_id, product_id, product_name, product_price, quantity, settlement_status, talent_username, standard_commission_rate, shop_ad_commission_rate, commission_gmv, estimated_base_commission, estimated_standard_commission, commission_base, standard_commission, before_split_talent_income, agency_split_rate, talent_total_income, agency_pretax_total_income, agency_total_income, invitation_id, created_at, created_date, delivered_at, payment_id, payment_status, raw_json FROM orders`
}

type scanner interface {
	Scan(dest ...any) error
}

func scanOrder(row scanner) (Order, error) {
	var o Order
	raw := "{}"
	err := row.Scan(&o.ID, &o.Country, &o.OrderID, &o.SKUID, &o.ProductID, &o.ProductName, &o.ProductPrice, &o.Quantity, &o.SettlementStatus, &o.TalentUsername, &o.StandardCommissionRate, &o.ShopAdCommissionRate, &o.CommissionGMV, &o.EstimatedBaseCommission, &o.EstimatedStandardCommission, &o.CommissionBase, &o.StandardCommission, &o.BeforeSplitTalentIncome, &o.AgencySplitRate, &o.TalentTotalIncome, &o.AgencyPretaxTotalIncome, &o.AgencyTotalIncome, &o.InvitationID, &o.CreatedAt, &o.CreatedDate, &o.DeliveredAt, &o.PaymentID, &o.PaymentStatus, &raw)
	if err != nil {
		return o, err
	}
	_ = json.Unmarshal([]byte(raw), &o.Raw)
	if o.Raw == nil {
		o.Raw = map[string]string{}
	}
	return o, nil
}

func scanOrderRows(rows *sql.Rows) (Order, error) {
	return scanOrder(rows)
}

func insertOrderTx(tx *sql.Tx, order Order) error {
	raw, _ := json.Marshal(order.Raw)
	key, _ := duplicateOrderKey(order)
	_, err := tx.Exec(`INSERT INTO orders (id, duplicate_key, country, order_id, sku_id, product_id, product_name, product_price, quantity, settlement_status, talent_username, standard_commission_rate, shop_ad_commission_rate, commission_gmv, estimated_base_commission, estimated_standard_commission, commission_base, standard_commission, before_split_talent_income, agency_split_rate, talent_total_income, agency_pretax_total_income, agency_total_income, invitation_id, created_at, created_date, delivered_at, payment_id, payment_status, raw_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		order.ID, key, order.Country, order.OrderID, order.SKUID, order.ProductID, order.ProductName, order.ProductPrice, order.Quantity, order.SettlementStatus, order.TalentUsername, order.StandardCommissionRate, order.ShopAdCommissionRate, order.CommissionGMV, order.EstimatedBaseCommission, order.EstimatedStandardCommission, order.CommissionBase, order.StandardCommission, order.BeforeSplitTalentIncome, order.AgencySplitRate, order.TalentTotalIncome, order.AgencyPretaxTotalIncome, order.AgencyTotalIncome, order.InvitationID, order.CreatedAt, order.CreatedDate, order.DeliveredAt, order.PaymentID, order.PaymentStatus, string(raw))
	return err
}

func upsertOrderSQL() string {
	return `INSERT INTO orders (id, duplicate_key, country, order_id, sku_id, product_id, product_name, product_price, quantity, settlement_status, talent_username, standard_commission_rate, shop_ad_commission_rate, commission_gmv, estimated_base_commission, estimated_standard_commission, commission_base, standard_commission, before_split_talent_income, agency_split_rate, talent_total_income, agency_pretax_total_income, agency_total_income, invitation_id, created_at, created_date, delivered_at, payment_id, payment_status, raw_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(duplicate_key) WHERE duplicate_key <> '' DO UPDATE SET
  country = excluded.country,
  order_id = excluded.order_id,
  sku_id = excluded.sku_id,
  product_id = excluded.product_id,
  product_name = excluded.product_name,
  product_price = excluded.product_price,
  quantity = excluded.quantity,
  settlement_status = excluded.settlement_status,
  talent_username = excluded.talent_username,
  standard_commission_rate = excluded.standard_commission_rate,
  shop_ad_commission_rate = excluded.shop_ad_commission_rate,
  commission_gmv = excluded.commission_gmv,
  estimated_base_commission = excluded.estimated_base_commission,
  estimated_standard_commission = excluded.estimated_standard_commission,
  commission_base = excluded.commission_base,
  standard_commission = excluded.standard_commission,
  before_split_talent_income = excluded.before_split_talent_income,
  agency_split_rate = excluded.agency_split_rate,
  talent_total_income = excluded.talent_total_income,
  agency_pretax_total_income = excluded.agency_pretax_total_income,
  agency_total_income = excluded.agency_total_income,
  invitation_id = excluded.invitation_id,
  created_at = excluded.created_at,
  created_date = excluded.created_date,
  delivered_at = excluded.delivered_at,
  payment_id = excluded.payment_id,
  payment_status = excluded.payment_status,
  raw_json = excluded.raw_json`
}

func execUpsertOrder(stmt *sql.Stmt, order Order) error {
	raw, _ := json.Marshal(order.Raw)
	key, _ := duplicateOrderKey(order)
	if order.CreatedDate == "" {
		order.CreatedDate = normalizeDate(order.CreatedAt)
	}
	_, err := stmt.Exec(
		order.ID, key, order.Country, order.OrderID, order.SKUID, order.ProductID, order.ProductName,
		order.ProductPrice, order.Quantity, order.SettlementStatus, order.TalentUsername,
		order.StandardCommissionRate, order.ShopAdCommissionRate, order.CommissionGMV,
		order.EstimatedBaseCommission, order.EstimatedStandardCommission, order.CommissionBase,
		order.StandardCommission, order.BeforeSplitTalentIncome, order.AgencySplitRate,
		order.TalentTotalIncome, order.AgencyPretaxTotalIncome, order.AgencyTotalIncome,
		order.InvitationID, order.CreatedAt, order.CreatedDate, order.DeliveredAt,
		order.PaymentID, order.PaymentStatus, string(raw),
	)
	return err
}

func updateOrderTx(tx *sql.Tx, order Order) error {
	raw, _ := json.Marshal(order.Raw)
	key, _ := duplicateOrderKey(order)
	_, err := tx.Exec(`UPDATE orders SET duplicate_key=?, country=?, order_id=?, sku_id=?, product_id=?, product_name=?, product_price=?, quantity=?, settlement_status=?, talent_username=?, standard_commission_rate=?, shop_ad_commission_rate=?, commission_gmv=?, estimated_base_commission=?, estimated_standard_commission=?, commission_base=?, standard_commission=?, before_split_talent_income=?, agency_split_rate=?, talent_total_income=?, agency_pretax_total_income=?, agency_total_income=?, invitation_id=?, created_at=?, created_date=?, delivered_at=?, payment_id=?, payment_status=?, raw_json=? WHERE id=?`,
		key, order.Country, order.OrderID, order.SKUID, order.ProductID, order.ProductName, order.ProductPrice, order.Quantity, order.SettlementStatus, order.TalentUsername, order.StandardCommissionRate, order.ShopAdCommissionRate, order.CommissionGMV, order.EstimatedBaseCommission, order.EstimatedStandardCommission, order.CommissionBase, order.StandardCommission, order.BeforeSplitTalentIncome, order.AgencySplitRate, order.TalentTotalIncome, order.AgencyPretaxTotalIncome, order.AgencyTotalIncome, order.InvitationID, order.CreatedAt, order.CreatedDate, order.DeliveredAt, order.PaymentID, order.PaymentStatus, string(raw), order.ID)
	return err
}
