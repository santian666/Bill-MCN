package main

import (
	"archive/zip"
	"bufio"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"
	_ "modernc.org/sqlite"
)

var errXLSXPartialStream = errors.New("xlsx 流式解析已处理部分数据后失败")
var errImportCanceled = errors.New("导入已中断")

const ordersStartupMaintenanceVersion = "20260611_no_startup_fullscan"
const ordersStatusLocaleMigrationVersion = "20260902_status_locale"
const ordersPretaxFallbackMigrationVersion = "20261122_pretax_fallback"
const importBatchSize = 10000
const importPreviewBatchSize = 10000
const importPreviewCacheLimit = 100000
const walCleanupThreshold = 64 * 1024 * 1024

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
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s.db = db
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL; PRAGMA temp_store=MEMORY; PRAGMA busy_timeout=10000; PRAGMA cache_size=-131072; PRAGMA mmap_size=268435456;`); err != nil {
		return err
	}
	if err := s.createOrdersSchemaLocked(); err != nil {
		return err
	}
	if err := s.migrateOrderStatusLocaleLocked(); err != nil {
		return err
	}
	if err := s.migrateOrderPretaxFallbackLocked(); err != nil {
		return err
	}
	s.cleanupLargeWALLocked("startup", false)
	return nil
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
CREATE INDEX IF NOT EXISTS idx_orders_country_created_order ON orders(country, created_date DESC, order_id DESC);
CREATE INDEX IF NOT EXISTS idx_orders_stats_cover ON orders(country, created_date, talent_username, settlement_status, payment_status, quantity, agency_total_income);
CREATE TABLE IF NOT EXISTS app_meta (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
`)
	if err != nil {
		return err
	}
	return s.markOrdersStartupMaintenanceLocked()
}

func (s *Store) markOrdersStartupMaintenanceLocked() error {
	_, err := s.db.Exec(`
INSERT INTO app_meta (key, value, updated_at)
VALUES ('orders_startup_maintenance_version', ?, CURRENT_TIMESTAMP)
ON CONFLICT(key) DO UPDATE SET
  value = excluded.value,
  updated_at = CURRENT_TIMESTAMP
WHERE value <> excluded.value;
`, ordersStartupMaintenanceVersion)
	return err
}

func (s *Store) migrateOrderStatusLocaleLocked() error {
	var version string
	err := s.db.QueryRow(`SELECT value FROM app_meta WHERE key = 'orders_status_locale_migration'`).Scan(&version)
	if err == nil && version == ordersStatusLocaleMigrationVersion {
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`
UPDATE orders SET settlement_status = CASE LOWER(TRIM(settlement_status))
  WHEN 'settled' THEN '已结算'
  WHEN 'pending' THEN '待确认'
  WHEN 'ineligible' THEN '不符合条件'
  WHEN 'frozen' THEN '已冻结'
  WHEN '決済済み' THEN '已结算'
  WHEN '保留中' THEN '待确认'
  WHEN '対象外' THEN '不符合条件'
  WHEN '凍結' THEN '已冻结'
  ELSE settlement_status END
WHERE LOWER(TRIM(settlement_status)) IN ('settled', 'pending', 'ineligible', 'frozen', '決済済み', '保留中', '対象外', '凍結');
UPDATE orders SET payment_status = CASE LOWER(TRIM(payment_status))
  WHEN 'paid' THEN '已付款'
  WHEN 'unpaid' THEN '未付款'
  WHEN 'no payment needed' THEN '无需付款'
  WHEN 'no payment required' THEN '无需付款'
  WHEN '支払い済み' THEN '已付款'
  WHEN '未払い' THEN '未付款'
  WHEN '支払い不要' THEN '无需付款'
  ELSE payment_status END
WHERE LOWER(TRIM(payment_status)) IN ('paid', 'unpaid', 'no payment needed', 'no payment required', '支払い済み', '未払い', '支払い不要');
INSERT INTO app_meta (key, value, updated_at) VALUES ('orders_status_locale_migration', ?, CURRENT_TIMESTAMP)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP;
`, ordersStatusLocaleMigrationVersion); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	log.Printf("INFO 订单语言状态迁移完成 version=%s", ordersStatusLocaleMigrationVersion)
	return nil
}

func (s *Store) migrateOrderPretaxFallbackLocked() error {
	var version string
	err := s.db.QueryRow(`SELECT value FROM app_meta WHERE key = 'orders_pretax_fallback_version'`).Scan(&version)
	if err == nil && version == ordersPretaxFallbackMigrationVersion {
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	res, err := tx.Exec(`UPDATE orders SET agency_total_income = agency_pretax_total_income WHERE agency_total_income = 0 AND agency_pretax_total_income <> 0`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	affected, _ := res.RowsAffected()
	if _, err := tx.Exec(`
INSERT INTO app_meta (key, value, updated_at)
VALUES ('orders_pretax_fallback_version', ?, CURRENT_TIMESTAMP)
ON CONFLICT(key) DO UPDATE SET
  value = excluded.value,
  updated_at = CURRENT_TIMESTAMP;
`, ordersPretaxFallbackMigrationVersion); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	log.Printf("INFO 订单税前收益兜底迁移完成 version=%s updated=%d", ordersPretaxFallbackMigrationVersion, affected)
	return nil
}

func (s *Store) beginBulkImportLocked() func() {
	if s.db == nil {
		return func() {}
	}
	_, _ = s.db.Exec(`PRAGMA wal_autocheckpoint=4000; PRAGMA temp_store=MEMORY;`)
	return func() {
		s.cleanupLargeWALLocked("import_finish", true)
		_, _ = s.db.Exec(`PRAGMA optimize; PRAGMA wal_autocheckpoint=1000; PRAGMA temp_store=MEMORY;`)
	}
}

func (s *Store) cleanupLargeWALLocked(reason string, force bool) {
	if s.db == nil || s.dbPath == "" {
		return
	}
	walPath := s.dbPath + "-wal"
	info, err := os.Stat(walPath)
	if err != nil {
		return
	}
	if !force && info.Size() < walCleanupThreshold {
		return
	}
	start := time.Now()
	mode := "PASSIVE"
	if force || info.Size() >= walCleanupThreshold {
		mode = "TRUNCATE"
	}
	_, err = s.db.Exec(`PRAGMA wal_checkpoint(` + mode + `);`)
	if err != nil {
		log.Printf("WARN WAL整理失败 reason=%s mode=%s size=%d err=%v", reason, mode, info.Size(), err)
		return
	}
	afterSize := int64(0)
	if after, statErr := os.Stat(walPath); statErr == nil {
		afterSize = after.Size()
	}
	log.Printf("INFO WAL整理完成 reason=%s mode=%s before=%d after=%d elapsed=%s", reason, mode, info.Size(), afterSize, time.Since(start))
}

func (s *Store) normalizeOrderTalentUsernamesLocked() error {
	_, err := s.db.Exec(`UPDATE orders SET talent_username = lower(trim(talent_username)) WHERE talent_username <> lower(trim(talent_username));`)
	return err
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
	stmt, err := tx.Prepare(insertOrderSQL())
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
		s.Orders[i].ID = s.newOrderIDLocked(s.Orders[i])
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

func (s *Store) countryOrderCountLocked(country string) int {
	var count int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM orders WHERE country = ?`, country).Scan(&count)
	return count
}

func (s *Store) dbStateMetaLocked() (DateRange, map[string]DateRange, int) {
	global := DateRange{}
	countryRanges := map[string]DateRange{}
	totalCount := 0
	rows, err := s.db.Query(`SELECT country, COALESCE(MIN(NULLIF(created_date, '')), ''), COALESCE(MAX(NULLIF(created_date, '')), ''), COUNT(*) FROM orders GROUP BY country`)
	if err != nil {
		return global, countryRanges, totalCount
	}
	defer rows.Close()
	for rows.Next() {
		var country, start, end string
		var cnt int
		if rows.Scan(&country, &start, &end, &cnt) == nil {
			totalCount += cnt
			if country != "" && (start != "" || end != "") {
				countryRanges[country] = DateRange{Start: start, End: end}
				if global.Start == "" || (start != "" && start < global.Start) {
					global.Start = start
				}
				if global.End == "" || (end != "" && end > global.End) {
					global.End = end
				}
			}
		}
	}
	return global, countryRanges, totalCount
}

func (s *Store) dbDateRangesLocked() (DateRange, map[string]DateRange) {
	global, countryRanges, _ := s.dbStateMetaLocked()
	return global, countryRanges
}

func (s *Store) previewImportFileLocked(path, filename, country string) (ImportResult, error) {
	info, _ := os.Stat(path)
	s.setImportProgress(ImportProgress{
		Active:   true,
		Phase:    "分析重复订单",
		Country:  country,
		Filename: filename,
		Message:  "正在快速检查重复订单",
	})
	result := ImportResult{}
	batch := make([]Order, 0, importBatchSize)
	cachedOrders := make([]Order, 0, importBatchSize)
	cacheEnabled := true
	processed := 0
	flush := func() error {
		if s.importCanceled() {
			return errImportCanceled
		}
		if len(batch) == 0 {
			return nil
		}
		keys := make([]string, 0, len(batch))
		for _, order := range batch {
			if key, ok := duplicateOrderKey(order); ok {
				keys = append(keys, key)
			}
		}
		existing, err := s.findOrdersForImportCompareLocked(keys)
		if err != nil {
			return err
		}
		if s.importCanceled() {
			return errImportCanceled
		}
		for _, order := range batch {
			key, ok := duplicateOrderKey(order)
			if !ok {
				continue
			}
			if oldOrder, exists := existing[key]; exists {
				result.DuplicateCount++
				if len(compareOrderCoreFields(oldOrder, order)) == 0 {
					result.UnchangedDuplicateCount++
				} else {
					result.ChangedDuplicateCount++
				}
			}
		}
		batch = batch[:0]
		log.Printf("INFO 重复预览批次完成 country=%s file=%s processed=%d duplicates=%d changed=%d unchanged=%d", country, filename, processed, result.DuplicateCount, result.ChangedDuplicateCount, result.UnchangedDuplicateCount)
		s.setImportProgress(ImportProgress{
			Active:             true,
			Phase:              "分析重复订单",
			Country:            country,
			Filename:           filename,
			Processed:          processed,
			DuplicateCount:     result.DuplicateCount,
			ChangedDuplicate:   result.ChangedDuplicateCount,
			UnchangedDuplicate: result.UnchangedDuplicateCount,
			Message:            "正在快速检查重复订单",
		})
		return nil
	}
	err := parseUploadedOrdersEach(path, filename, country, s.importCanceled, func(order Order) error {
		if s.importCanceled() {
			return errImportCanceled
		}
		if _, ok := duplicateOrderKey(order); !ok {
			return nil
		}
		if cacheEnabled {
			if len(cachedOrders) < importPreviewCacheLimit {
				cachedOrders = append(cachedOrders, order)
			} else {
				cacheEnabled = false
				cachedOrders = nil
			}
		}
		batch = append(batch, order)
		processed++
		if len(batch) >= importPreviewBatchSize {
			return flush()
		}
		return nil
	})
	if err == nil {
		err = flush()
	}
	if err == nil && cacheEnabled && info != nil {
		s.importCache = &ImportCache{
			Path:     path,
			Filename: filename,
			Country:  country,
			Size:     info.Size(),
			ModTime:  info.ModTime(),
			Orders:   cachedOrders,
		}
	} else {
		s.importCache = nil
	}
	if errors.Is(err, errImportCanceled) {
		s.setImportProgress(ImportProgress{
			Active:             false,
			Phase:              "已中断",
			Country:            country,
			Filename:           filename,
			Processed:          processed,
			DuplicateCount:     result.DuplicateCount,
			ChangedDuplicate:   result.ChangedDuplicateCount,
			UnchangedDuplicate: result.UnchangedDuplicateCount,
			Message:            "导入已中断，未提交任何新数据。",
		})
		return result, err
	}
	s.setImportProgress(ImportProgress{
		Active:             false,
		Phase:              "分析完成",
		Country:            country,
		Filename:           filename,
		Processed:          processed,
		DuplicateCount:     result.DuplicateCount,
		ChangedDuplicate:   result.ChangedDuplicateCount,
		UnchangedDuplicate: result.UnchangedDuplicateCount,
		Message:            "重复订单检查完成",
	})
	return result, err
}

func (s *Store) applyImportFileLocked(path, filename, country string, preview ImportResult) (ImportResult, error) {
	return s.applyImportFileCountingLocked(path, filename, country, preview, true)
}

func (s *Store) replaceImportFileLocked(path, filename, country string) (ImportResult, error) {
	if orders, ok := s.cachedImportOrdersLocked(path, filename, country); ok {
		return s.replaceOrdersLocked(orders, filename, country, true)
	}
	return s.replaceImportFileStreamLocked(path, filename, country)
}

func (s *Store) replaceImportFileStreamLocked(path, filename, country string) (ImportResult, error) {
	finishBulkImport := s.beginBulkImportLocked()
	bulkImportFinished := false
	defer func() {
		if !bulkImportFinished {
			finishBulkImport()
		}
	}()
	s.setImportProgress(ImportProgress{
		Active:   true,
		Phase:    "读取账单",
		Country:  country,
		Filename: filename,
		Message:  "正在读取账单并准备覆盖旧订单",
	})
	result := ImportResult{}
	batch := make([]Order, 0, importBatchSize)
	processed := 0
	deleted := 0
	inserted := 0
	skipped := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		batchStart := time.Now()
		batchSize := len(batch)
		keys := make([]string, 0, len(batch))
		for _, order := range batch {
			if key, ok := duplicateOrderKey(order); ok {
				keys = append(keys, key)
			}
		}
		existing, err := s.findOrdersForImportCompareLocked(keys)
		if err != nil {
			return err
		}
		s.setImportProgress(ImportProgress{
			Active:           true,
			Phase:            "写入数据",
			Country:          country,
			Filename:         filename,
			Processed:        processed + batchSize,
			Imported:         inserted,
			Updated:          deleted,
			DuplicateCount:   deleted,
			ChangedDuplicate: deleted,
			Message:          fmt.Sprintf("正在写入本批 %d 条数据", batchSize),
		})
		batchProcessed, batchWritten, batchUpdated, batchSkipped, stopped, err := s.upsertReplaceOrdersBatchLocked(batch, existing, s.importCanceled)
		if err != nil {
			return err
		}
		deleted += batchUpdated
		inserted += batchWritten
		skipped += batchSkipped
		processed += batchProcessed
		log.Printf("INFO 覆盖导入批次完成 country=%s file=%s batch=%d processed=%d upsert_batch=%d updated_batch=%d skipped_unchanged=%d interrupted=%v elapsed=%s", country, filename, batchSize, processed, batchWritten, batchUpdated, batchSkipped, stopped, time.Since(batchStart))
		if stopped {
			log.Printf("INFO 导入中断位置 country=%s file=%s processed=%d imported=%d updated=%d skipped=%d", country, filename, processed, inserted, deleted, skipped)
		}
		batch = batch[:0]
		s.setImportProgress(ImportProgress{
			Active:           true,
			Phase:            "覆盖导入",
			Country:          country,
			Filename:         filename,
			Processed:        processed,
			Imported:         inserted,
			Updated:          deleted,
			SkippedUnchanged: skipped,
			DuplicateCount:   deleted,
			ChangedDuplicate: deleted,
			Message:          "正在删除旧订单并导入新订单",
		})
		if stopped {
			return errImportCanceled
		}
		return nil
	}
	err := parseUploadedOrdersEach(path, filename, country, s.importCanceled, func(order Order) error {
		if s.importCanceled() {
			return errImportCanceled
		}
		batch = append(batch, order)
		if len(batch) >= importBatchSize {
			return flush()
		}
		if len(batch)%200 == 0 {
			s.setImportProgress(ImportProgress{
				Active:    true,
				Phase:     "读取账单",
				Country:   country,
				Filename:  filename,
				Processed: processed + len(batch),
				Imported:  inserted,
				Updated:   deleted,
				Message:   "正在读取账单并准备覆盖旧订单",
			})
		}
		return nil
	})
	if err == nil {
		err = flush()
	}
	interrupted := errors.Is(err, errImportCanceled)
	if interrupted {
		batch = batch[:0]
		err = nil
	}
	if err != nil {
		return result, err
	}
	s.setImportProgress(ImportProgress{
		Active:           true,
		Phase:            "整理数据库",
		Country:          country,
		Filename:         filename,
		Processed:        processed,
		Imported:         inserted - deleted,
		Updated:          deleted,
		SkippedUnchanged: skipped,
		DuplicateCount:   deleted,
		ChangedDuplicate: deleted,
		Message:          "正在整理数据库文件，请稍候",
	})
	finishBulkImport()
	bulkImportFinished = true
	result.Imported = inserted - deleted
	if result.Imported < 0 {
		result.Imported = 0
	}
	result.Updated = deleted
	result.SkippedUnchanged = skipped
	result.DuplicateCount = deleted
	result.ChangedDuplicateCount = deleted
	if interrupted {
		result.Message = importFinishMessage(true, "")
	}
	s.setImportProgress(ImportProgress{
		Active:           false,
		Phase:            importFinishPhase(interrupted),
		Country:          country,
		Filename:         filename,
		Processed:        processed,
		Imported:         inserted - deleted,
		Updated:          deleted,
		SkippedUnchanged: skipped,
		DuplicateCount:   deleted,
		ChangedDuplicate: deleted,
		Message:          importFinishMessage(interrupted, "覆盖导入完成"),
	})
	return result, nil
}

func (s *Store) cachedImportOrdersLocked(path, filename, country string) ([]Order, bool) {
	cache := s.importCache
	if cache == nil || cache.Path != path || cache.Filename != filename || cache.Country != country || len(cache.Orders) == 0 {
		return nil, false
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() != cache.Size || !info.ModTime().Equal(cache.ModTime) {
		s.importCache = nil
		return nil, false
	}
	return cache.Orders, true
}

func (s *Store) replaceOrdersLocked(orders []Order, filename, country string, fromCache bool) (ImportResult, error) {
	finishBulkImport := s.beginBulkImportLocked()
	bulkImportFinished := false
	defer func() {
		if !bulkImportFinished {
			finishBulkImport()
		}
	}()
	result := ImportResult{}
	processed := 0
	updated := 0
	written := 0
	skipped := 0
	for start := 0; start < len(orders); start += importBatchSize {
		if s.importCanceled() {
			result.Message = "导入已中断，已保留已导入的数据。"
			break
		}
		end := start + importBatchSize
		if end > len(orders) {
			end = len(orders)
		}
		batchStart := time.Now()
		batch := orders[start:end]
		keys := make([]string, 0, len(batch))
		for _, order := range batch {
			if key, ok := duplicateOrderKey(order); ok {
				keys = append(keys, key)
			}
		}
		existing, err := s.findOrdersForImportCompareLocked(keys)
		if err != nil {
			return result, err
		}
		batchProcessed, batchWritten, batchUpdated, batchSkipped, stopped, err := s.upsertReplaceOrdersBatchLocked(batch, existing, s.importCanceled)
		if err != nil {
			return result, err
		}
		processed += batchProcessed
		written += batchWritten
		updated += batchUpdated
		skipped += batchSkipped
		log.Printf("INFO 覆盖导入批次完成 country=%s file=%s source_cache=%v batch=%d processed=%d upsert_batch=%d updated_batch=%d skipped_unchanged=%d interrupted=%v elapsed=%s", country, filename, fromCache, end-start, processed, batchWritten, batchUpdated, batchSkipped, stopped, time.Since(batchStart))
		if stopped {
			log.Printf("INFO 导入中断位置 country=%s file=%s processed=%d imported=%d updated=%d skipped=%d", country, filename, processed, written-updated, updated, skipped)
		}
		s.setImportProgress(ImportProgress{
			Active:           true,
			Phase:            "覆盖导入",
			Country:          country,
			Filename:         filename,
			Processed:        processed,
			Imported:         written - updated,
			Updated:          updated,
			SkippedUnchanged: skipped,
			DuplicateCount:   updated,
			ChangedDuplicate: updated,
			Message:          "正在快速覆盖写入数据库",
		})
		if stopped {
			result.Message = "导入已中断，已保留已导入的数据。"
			break
		}
	}
	s.setImportProgress(ImportProgress{
		Active:           true,
		Phase:            "整理数据库",
		Country:          country,
		Filename:         filename,
		Processed:        processed,
		Imported:         written - updated,
		Updated:          updated,
		SkippedUnchanged: skipped,
		DuplicateCount:   updated,
		ChangedDuplicate: updated,
		Message:          "正在整理数据库文件，请稍候",
	})
	finishBulkImport()
	bulkImportFinished = true
	result.Imported = written - updated
	if result.Imported < 0 {
		result.Imported = 0
	}
	result.Updated = updated
	result.SkippedUnchanged = skipped
	result.DuplicateCount = updated
	result.ChangedDuplicateCount = updated
	s.importCache = nil
	s.setImportProgress(ImportProgress{
		Active:           false,
		Phase:            importFinishPhase(result.Message != ""),
		Country:          country,
		Filename:         filename,
		Processed:        processed,
		Imported:         result.Imported,
		Updated:          updated,
		SkippedUnchanged: skipped,
		DuplicateCount:   updated,
		ChangedDuplicate: updated,
		Message:          importFinishMessage(result.Message != "", "覆盖导入完成"),
	})
	return result, nil
}

func (s *Store) applyImportFileCountingLocked(path, filename, country string, preview ImportResult, trustPreview bool) (ImportResult, error) {
	finishBulkImport := s.beginBulkImportLocked()
	bulkImportFinished := false
	defer func() {
		if !bulkImportFinished {
			finishBulkImport()
		}
	}()
	s.setImportProgress(ImportProgress{
		Active:   true,
		Phase:    "写入数据",
		Country:  country,
		Filename: filename,
		Message:  "正在读取账单并写入数据库",
	})
	result := preview
	result.RequiresConfirm = false
	result.Imported = 0
	result.Updated = 0
	result.SkippedUnchanged = 0
	batch := make([]Order, 0, importBatchSize)
	processed := 0
	imported := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		batchStart := time.Now()
		batchSize := len(batch)
		s.setImportProgress(ImportProgress{
			Active:             true,
			Phase:              "写入数据",
			Country:            country,
			Filename:           filename,
			Processed:          processed,
			Imported:           imported,
			Updated:            0,
			SkippedUnchanged:   0,
			DuplicateCount:     result.DuplicateCount,
			ChangedDuplicate:   result.ChangedDuplicateCount,
			UnchangedDuplicate: result.UnchangedDuplicateCount,
			Message:            fmt.Sprintf("正在写入本批 %d 条数据", batchSize),
		})
		batchImported, stopped, err := s.upsertOrdersBatchLocked(batch, s.importCanceled)
		if err != nil {
			return err
		}
		imported += batchImported
		processed += batchImported
		log.Printf("INFO 导入批次完成 country=%s file=%s batch=%d processed=%d inserted_or_updated_batch=%d interrupted=%v elapsed=%s", country, filename, batchSize, processed, batchImported, stopped, time.Since(batchStart))
		if stopped {
			log.Printf("INFO 导入中断位置 country=%s file=%s processed=%d imported=%d updated=%d skipped=%d", country, filename, processed, imported, 0, 0)
		}
		batch = batch[:0]
		s.setImportProgress(ImportProgress{
			Active:             true,
			Phase:              "写入数据",
			Country:            country,
			Filename:           filename,
			Processed:          processed,
			Imported:           imported,
			Updated:            0,
			SkippedUnchanged:   0,
			DuplicateCount:     result.DuplicateCount,
			ChangedDuplicate:   result.ChangedDuplicateCount,
			UnchangedDuplicate: result.UnchangedDuplicateCount,
			Message:            "正在批量写入数据库，已按批提交",
		})
		if stopped {
			return errImportCanceled
		}
		return nil
	}
	err := parseUploadedOrdersEach(path, filename, country, s.importCanceled, func(order Order) error {
		if s.importCanceled() {
			return errImportCanceled
		}
		batch = append(batch, order)
		if len(batch)%200 == 0 {
			s.setImportProgress(ImportProgress{
				Active:             true,
				Phase:              "读取账单",
				Country:            country,
				Filename:           filename,
				Processed:          processed + len(batch),
				Imported:           imported,
				Updated:            0,
				SkippedUnchanged:   0,
				DuplicateCount:     result.DuplicateCount,
				ChangedDuplicate:   result.ChangedDuplicateCount,
				UnchangedDuplicate: result.UnchangedDuplicateCount,
				Message:            "正在读取账单并准备批量写入",
			})
		}
		if len(batch) >= importBatchSize {
			return flush()
		}
		return nil
	})
	if err == nil {
		err = flush()
	}
	interrupted := errors.Is(err, errImportCanceled)
	if interrupted {
		batch = batch[:0]
		err = nil
	}
	if err != nil {
		return result, err
	}
	s.setImportProgress(ImportProgress{
		Active:   true,
		Phase:    "整理数据库",
		Country:  country,
		Filename: filename,
		Message:  "正在整理数据库文件，请稍候",
	})
	finishBulkImport()
	bulkImportFinished = true
	result.Updated = 0
	result.Imported = imported
	result.SkippedUnchanged = 0
	if interrupted {
		result.Message = importFinishMessage(true, "")
	}
	s.setImportProgress(ImportProgress{
		Active:             false,
		Phase:              importFinishPhase(interrupted),
		Country:            country,
		Filename:           filename,
		Processed:          processed,
		Imported:           result.Imported,
		Updated:            result.Updated,
		SkippedUnchanged:   result.SkippedUnchanged,
		DuplicateCount:     result.DuplicateCount,
		ChangedDuplicate:   result.ChangedDuplicateCount,
		UnchangedDuplicate: result.UnchangedDuplicateCount,
		Message:            importFinishMessage(interrupted, "导入处理完成"),
	})
	return result, nil
}

func importFinishPhase(interrupted bool) string {
	if interrupted {
		return "已中断"
	}
	return "完成"
}

func importFinishMessage(interrupted bool, done string) string {
	if interrupted {
		return "导入已中断，已保留已导入的数据。"
	}
	return done
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

func (s *Store) findExistingDuplicateKeysLocked(keys []string) (map[string]bool, error) {
	result := map[string]bool{}
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
		rows, err := s.db.Query(`SELECT duplicate_key FROM orders WHERE duplicate_key IN (`+strings.Join(marks, ",")+`)`, args...)
		if err != nil {
			return result, err
		}
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				_ = rows.Close()
				return result, err
			}
			if key != "" {
				result[key] = true
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

func (s *Store) findOrdersForImportCompareLocked(keys []string) (map[string]Order, error) {
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
	if len(unique) <= 200 {
		return findOrdersForImportCompareWith(s.db, unique)
	}
	sort.Strings(unique)
	tx, err := s.db.Begin()
	if err != nil {
		return findOrdersForImportCompareWith(s.db, unique)
	}
	if _, err := tx.Exec(`CREATE TEMP TABLE IF NOT EXISTS temp_compare_keys (duplicate_key TEXT PRIMARY KEY)`); err != nil {
		_ = tx.Rollback()
		return findOrdersForImportCompareWith(s.db, unique)
	}
	if _, err := tx.Exec(`DELETE FROM temp_compare_keys`); err != nil {
		_ = tx.Rollback()
		return findOrdersForImportCompareWith(s.db, unique)
	}
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO temp_compare_keys(duplicate_key) VALUES (?)`)
	if err != nil {
		_ = tx.Rollback()
		return findOrdersForImportCompareWith(s.db, unique)
	}
	for _, key := range unique {
		if _, err := stmt.Exec(key); err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			return findOrdersForImportCompareWith(s.db, unique)
		}
	}
	if err := stmt.Close(); err != nil {
		_ = tx.Rollback()
		return findOrdersForImportCompareWith(s.db, unique)
	}
	rows, err := tx.Query(orderCompareJoinSQL())
	if err != nil {
		_ = tx.Rollback()
		return findOrdersForImportCompareWith(s.db, unique)
	}
	for rows.Next() {
		order, duplicateKey, err := scanOrderCompareRows(rows)
		if err != nil {
			_ = rows.Close()
			_ = tx.Rollback()
			return result, err
		}
		if duplicateKey != "" {
			result[duplicateKey] = order
		}
	}
	if err := rows.Close(); err != nil {
		_ = tx.Rollback()
		return result, err
	}
	if err := rows.Err(); err != nil {
		_ = tx.Rollback()
		return result, err
	}
	_ = tx.Commit()
	return result, nil
}

func findOrdersForImportCompareTx(tx *sql.Tx, keys []string) (map[string]Order, error) {
	return findOrdersForImportCompareWith(tx, keys)
}

type orderQueryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

func findOrdersForImportCompareWith(q orderQueryer, keys []string) (map[string]Order, error) {
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
		rows, err := q.Query(orderCompareSelectSQL()+` WHERE duplicate_key IN (`+strings.Join(marks, ",")+`)`, args...)
		if err != nil {
			return result, err
		}
		for rows.Next() {
			order, duplicateKey, err := scanOrderCompareRows(rows)
			if err != nil {
				_ = rows.Close()
				return result, err
			}
			if duplicateKey != "" {
				result[duplicateKey] = order
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

func deleteOrdersByDuplicateKeysTx(tx *sql.Tx, keys []string) (int64, error) {
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
	if len(unique) == 0 {
		return 0, nil
	}
	if _, err := tx.Exec(`DROP TABLE IF EXISTS temp_import_delete_keys`); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`CREATE TEMP TABLE temp_import_delete_keys (duplicate_key TEXT PRIMARY KEY)`); err != nil {
		return 0, err
	}
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO temp_import_delete_keys(duplicate_key) VALUES (?)`)
	if err != nil {
		return 0, err
	}
	for _, key := range unique {
		if _, err := stmt.Exec(key); err != nil {
			_ = stmt.Close()
			return 0, err
		}
	}
	if err := stmt.Close(); err != nil {
		return 0, err
	}
	result, err := tx.Exec(`DELETE FROM orders WHERE duplicate_key IN (SELECT duplicate_key FROM temp_import_delete_keys)`)
	if err != nil {
		return 0, err
	}
	deleted, _ := result.RowsAffected()
	_, _ = tx.Exec(`DROP TABLE IF EXISTS temp_import_delete_keys`)
	return deleted, nil
}

func (s *Store) upsertOrdersBatchLocked(orders []Order, shouldStop func() bool) (int, bool, error) {
	if len(orders) == 0 {
		return 0, false, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, false, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	stmt, err := tx.Prepare(upsertOrderSQL())
	if err != nil {
		return 0, false, err
	}
	defer stmt.Close()
	written := 0
	for _, order := range orders {
		if shouldStop != nil && shouldStop() {
			break
		}
		order.ID = s.newOrderIDLocked(order)
		if err := execUpsertOrder(stmt, order); err != nil {
			return written, false, err
		}
		written++
	}
	if err := tx.Commit(); err != nil {
		return written, false, err
	}
	committed = true
	return written, written < len(orders), nil
}

func (s *Store) upsertReplaceOrdersBatchLocked(orders []Order, existing map[string]Order, shouldStop func() bool) (int, int, int, int, bool, error) {
	if len(orders) == 0 {
		return 0, 0, 0, 0, false, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, 0, 0, 0, false, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	stmt, err := tx.Prepare(upsertOrderSQL())
	if err != nil {
		return 0, 0, 0, 0, false, err
	}
	defer stmt.Close()
	seen := map[string]bool{}
	processed := 0
	written := 0
	updated := 0
	skipped := 0
	stopped := false
	for _, order := range orders {
		if shouldStop != nil && shouldStop() {
			stopped = true
			break
		}
		key, ok := duplicateOrderKey(order)
		if ok {
			if seen[key] {
				processed++
				skipped++
				continue
			}
			seen[key] = true
			if oldOrder, exists := existing[key]; exists {
				if len(compareOrderCoreFields(oldOrder, order)) == 0 {
					processed++
					skipped++
					continue
				}
				updated++
			}
		}
		order.ID = s.newOrderIDLocked(order)
		if err := execUpsertOrder(stmt, order); err != nil {
			return processed, written, updated, skipped, stopped, err
		}
		processed++
		written++
	}
	if err := tx.Commit(); err != nil {
		return processed, written, updated, skipped, stopped, err
	}
	committed = true
	return processed, written, updated, skipped, stopped, nil
}

func dedupeOrdersForReplace(orders []Order) []Order {
	positions := map[string]int{}
	result := make([]Order, 0, len(orders))
	for _, order := range orders {
		key, ok := duplicateOrderKey(order)
		if !ok {
			result = append(result, order)
			continue
		}
		if pos, exists := positions[key]; exists {
			result[pos] = order
			continue
		}
		positions[key] = len(result)
		result = append(result, order)
	}
	return result
}

func stableOrderID(order Order) string {
	key, ok := duplicateOrderKey(order)
	if !ok {
		return ""
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return fmt.Sprintf("o_%016x", h.Sum64())
}

func (s *Store) newOrderIDLocked(order Order) string {
	if id := stableOrderID(order); id != "" {
		return id
	}
	return s.newIDLocked("o")
}

func (s *Store) dbStatsLocked(req StatsRequest) StatsResponse {
	usernames, title := s.resolveScopeLocked(req)
	usernames = uniqueUsernames(usernames)
	if len(usernames) > 200 {
		return s.dbStatsWithTempTalentsLocked(req, usernames, title)
	}
	where, args := buildOrderWhere(req, usernames)
	return s.dbStatsAggregateLocked(where, args, usernames, req, title)
}

func (s *Store) dbStatsWithTempTalentsLocked(req StatsRequest, usernames []string, title string) StatsResponse {
	var resp StatsResponse
	if err := s.withTempTalentsLocked(usernames, func() error {
		filterReq := req
		filterReq.Scope = ""
		where, args := buildOrderWhere(filterReq, nil)
		if where == "" {
			where = "WHERE orders.talent_username IN (SELECT username FROM temp_filter_talents)"
		} else {
			where += " AND orders.talent_username IN (SELECT username FROM temp_filter_talents)"
		}
		resp = s.dbStatsAggregateLocked(where, args, usernames, req, title)
		return nil
	}); err != nil {
		resp = StatsResponse{
			FilterRange: DateRange{Start: req.Start, End: req.End},
			Title:       title,
			TalentCount: len(usernames),
			StatusStats: buildStatusSummaryList(map[string]*StatusSummary{}),
			PerTalent:   perTalentList(map[string]*TalentSummary{}, usernames),
		}
	}
	return resp
}

func (s *Store) dbStatsAggregateLocked(where string, args []any, usernames []string, req StatsRequest, title string) StatsResponse {
	resp := StatsResponse{
		FilterRange: DateRange{Start: req.Start, End: req.End},
		Title:       title,
	}
	statusMap := map[string]*StatusSummary{}
	for _, status := range settlementStatuses {
		statusMap[status] = &StatusSummary{Status: status}
	}
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

	effectiveStatus := effectiveSettlementStatusSQL()
	query := `SELECT CASE WHEN talent_username = '' THEN '未识别账号' ELSE talent_username END, ` + effectiveStatus + `, COALESCE(SUM(quantity), 0), COALESCE(SUM(agency_total_income), 0) FROM orders ` + where + ` GROUP BY CASE WHEN talent_username = '' THEN '未识别账号' ELSE talent_username END, ` + effectiveStatus
	rows, err := s.db.Query(query, args...)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var username, status string
			var orders int
			var total float64
			if rows.Scan(&username, &status, &orders, &total) != nil {
				continue
			}
			resp.Total += total
			resp.OrderCount += orders

			sm, ok := statusMap[status]
			if !ok {
				sm = &StatusSummary{Status: status}
				statusMap[status] = sm
			}
			sm.Orders += orders
			sm.Total += total

			key := strings.ToLower(strings.TrimSpace(username))
			if key == "" {
				key = username
			}
			tItem, ok := per[key]
			if !ok {
				tItem = &TalentSummary{Username: username, StatusTotals: map[string]float64{}, StatusOrders: map[string]int{}}
				per[key] = tItem
			}
			tItem.Total += total
			tItem.Orders += orders
			tItem.StatusTotals[status] += total
			tItem.StatusOrders[status] += orders
		}
	}

	resp.Total = roundCents(resp.Total)
	if len(usernames) > 0 {
		resp.TalentCount = len(usernames)
	} else {
		resp.TalentCount = len(per)
	}
	for _, sm := range statusMap {
		sm.Total = roundCents(sm.Total)
	}
	for _, item := range per {
		item.Total = roundCents(item.Total)
		for st, val := range item.StatusTotals {
			item.StatusTotals[st] = roundCents(val)
		}
	}
	resp.StatusStats = buildStatusSummaryList(statusMap)
	resp.PerTalent = perTalentList(per, ordered)
	return resp
}

func buildStatusSummaryList(stats map[string]*StatusSummary) []StatusSummary {
	for _, status := range settlementStatuses {
		if _, ok := stats[status]; !ok {
			stats[status] = &StatusSummary{Status: status}
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

func (s *Store) withTempTalentsLocked(usernames []string, run func() error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE IF EXISTS temp_filter_talents`); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`CREATE TEMP TABLE temp_filter_talents (username TEXT PRIMARY KEY)`); err != nil {
		_ = tx.Rollback()
		return err
	}
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO temp_filter_talents(username) VALUES (?)`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	for _, username := range usernames {
		if name := normalizeTalentUsername(username); name != "" {
			if _, err := stmt.Exec(name); err != nil {
				_ = stmt.Close()
				_ = tx.Rollback()
				return err
			}
		}
	}
	if err := stmt.Close(); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	defer s.db.Exec(`DROP TABLE IF EXISTS temp_filter_talents`)
	return run()
}

func (s *Store) dbStatusStatsLocked(where string, args []any) []StatusSummary {
	stats := map[string]*StatusSummary{}
	for _, status := range settlementStatuses {
		stats[status] = &StatusSummary{Status: status}
	}
	effectiveStatus := effectiveSettlementStatusSQL()
	rows, err := s.db.Query(`SELECT `+effectiveStatus+`, COALESCE(SUM(quantity), 0), COALESCE(SUM(agency_total_income), 0) FROM orders `+where+` GROUP BY `+effectiveStatus, args...)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			item := StatusSummary{}
			if rows.Scan(&item.Status, &item.Orders, &item.Total) == nil {
				item.Total = roundCents(item.Total)
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
	effectiveStatus := effectiveSettlementStatusSQL()
	rows, err := s.db.Query(`SELECT CASE WHEN talent_username = '' THEN '未识别账号' ELSE talent_username END, `+effectiveStatus+`, COALESCE(SUM(quantity), 0), COALESCE(SUM(agency_total_income), 0) FROM orders `+where+` GROUP BY CASE WHEN talent_username = '' THEN '未识别账号' ELSE talent_username END, `+effectiveStatus, args...)
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
		per[key].Total = roundCents(per[key].Total + total)
		per[key].Orders += orders
		per[key].StatusTotals[status] = roundCents(per[key].StatusTotals[status] + total)
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
		name := normalizeTalentUsername(username)
		if name == "" {
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		result = append(result, name)
	}
	return result
}

func (s *Store) dbOrdersPageLocked(req StatsRequest, page, pageSize int) (OrdersResponse, error) {
	usernames, _ := s.resolveScopeLocked(req)
	where, args := buildOrderWhere(req, usernames)
	resp := OrdersResponse{PageSize: pageSize, FilterRange: DateRange{Start: req.Start, End: req.End}}
	statusMap := map[string]*StatusSummary{}
	effectiveStatus := effectiveSettlementStatusSQL()
	aggRows, err := s.db.Query(`SELECT `+effectiveStatus+`, COUNT(*), COALESCE(SUM(quantity), 0), COALESCE(SUM(agency_total_income), 0) FROM orders `+where+` GROUP BY `+effectiveStatus, args...)
	if err != nil {
		return resp, err
	}
	for aggRows.Next() {
		var status string
		var rowCount, qty int
		var income float64
		if scanErr := aggRows.Scan(&status, &rowCount, &qty, &income); scanErr == nil {
			resp.Total += rowCount
			resp.Income += income
			statusMap[status] = &StatusSummary{Status: status, Orders: qty, Total: roundCents(income)}
		}
	}
	_ = aggRows.Close()
	resp.Income = roundCents(resp.Income)
	resp.StatusStats = buildStatusSummaryList(statusMap)
	if resp.Total > 0 {
		resp.TotalPages = (resp.Total + pageSize - 1) / pageSize
		if page > resp.TotalPages {
			page = resp.TotalPages
		}
	}
	resp.Page = page
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
	bw := bufio.NewWriterSize(w, 256*1024)
	if err := f.Write(bw); err != nil {
		return err
	}
	return bw.Flush()
}

func (s *Store) writeSummarySheetLocked(f *excelize.File, sheet string, req StatsRequest) error {
	resp := s.dbStatsLocked(req)
	sw, err := f.NewStreamWriter(sheet)
	if err != nil {
		return err
	}
	rowIndex := 1
	rows := [][]any{
		{"筛选结果"},
		{"总订单数", resp.OrderCount, "总结算金额", moneyText(resp.Total), "账号", resp.TalentCount},
		{"状态统计"},
		{"状态", "数量", "金额", "状态", "数量", "金额", "状态", "数量", "金额"},
		statusSummaryExportRow(resp.StatusStats),
		{"机构结算"},
		agencySettlementExportHeader(req),
		agencySettlementExportRow(req, resp.StatusStats),
		{"账号明细"},
		{"账号", "已结算", "已结算数量", "机构未付款", "机构未付款数量", "客户未付款", "客户未付款数量", "待确认", "待确认数量", "不符合条件", "不符合条件数量", "已冻结", "已冻结数量", "总订单数"},
	}
	for _, row := range rows {
		cell, _ := excelize.CoordinatesToCellName(1, rowIndex)
		if err := sw.SetRow(cell, row); err != nil {
			return err
		}
		rowIndex++
	}
	for i, item := range resp.PerTalent {
		row := []any{
			item.Username,
			moneyText(talentSummaryStatusTotal(item, "已结算")),
			talentSummaryStatusOrders(item, "已结算"),
			moneyText(talentSummaryStatusTotal(item, "机构未付款")),
			talentSummaryStatusOrders(item, "机构未付款"),
			moneyText(talentSummaryStatusTotal(item, "客户未付款")),
			talentSummaryStatusOrders(item, "客户未付款"),
			moneyText(talentSummaryStatusTotal(item, "待确认")),
			talentSummaryStatusOrders(item, "待确认"),
			moneyText(talentSummaryStatusTotal(item, "不符合条件")),
			talentSummaryStatusOrders(item, "不符合条件"),
			moneyText(talentSummaryStatusTotal(item, "已冻结")),
			talentSummaryStatusOrders(item, "已冻结"),
			item.Orders,
		}
		cell, _ := excelize.CoordinatesToCellName(1, rowIndex+i)
		if err := sw.SetRow(cell, row); err != nil {
			return err
		}
	}
	return sw.Flush()
}

func statusSummaryExportRow(items []StatusSummary) []any {
	stats := statusSummaryMap(items)
	row := make([]any, 0, len(settlementStatuses)*3)
	for _, status := range settlementStatuses {
		item := stats[status]
		row = append(row, status, item.Orders, moneyText(item.Total))
	}
	return row
}

func statusSummaryMap(items []StatusSummary) map[string]StatusSummary {
	result := map[string]StatusSummary{}
	for _, item := range items {
		result[item.Status] = item
	}
	return result
}

func agencySettlementExportHeader(req StatsRequest) []any {
	return []any{"已结算", "美金", "人民币", "平台抽成", "手续费", "手续费金额", "实际结算"}
}

func agencySettlementExportRow(req StatsRequest, stats []StatusSummary) []any {
	settled := roundCents(statusSummaryTotal(stats, "已结算"))
	usdRate := optionalFloatValue(req.USDRate, 1)
	rmbRate := optionalFloatValue(req.RMBRate, 1)
	cutInput := optionalPositiveFloatValue(req.AgencyCutRate, 0)
	feePermille := optionalPositiveFloatValue(req.FeePermille, 0)
	cutRate := math.Max(0, (100-cutInput)/100)
	base := settled * usdRate * rmbRate
	beforeFee := roundCents(base * cutRate)
	fee := roundCents(beforeFee * feePermille / 1000)
	actual := roundCents(beforeFee - fee)
	return []any{
		moneyText(settled),
		optionalFloatText(req.USDRate),
		optionalFloatText(req.RMBRate),
		optionalPercentText(req.AgencyCutRate),
		optionalPermilleText(req.FeePermille),
		moneyText(fee),
		moneyText(actual),
	}
}

func statusSummaryTotal(items []StatusSummary, status string) float64 {
	for _, item := range items {
		if item.Status == status {
			return item.Total
		}
	}
	return 0
}

func optionalFloatValue(value *float64, emptyValue float64) float64 {
	if value == nil {
		return emptyValue
	}
	return *value
}

func optionalPositiveFloatValue(value *float64, emptyValue float64) float64 {
	if value == nil || *value <= 0 {
		return emptyValue
	}
	return *value
}

func optionalFloatText(value *float64) string {
	if value == nil {
		return ""
	}
	return strconv.FormatFloat(*value, 'f', -1, 64)
}

func percentText(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64) + "%"
}

func permilleText(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64) + "‰"
}

func optionalPercentText(value *float64) string {
	if value == nil || *value <= 0 {
		return ""
	}
	return percentText(*value)
}

func optionalPermilleText(value *float64) string {
	if value == nil || *value <= 0 {
		return ""
	}
	return permilleText(*value)
}

func talentSummaryStatusTotal(item TalentSummary, status string) float64 {
	if item.StatusTotals == nil {
		return 0
	}
	return item.StatusTotals[status]
}

func talentSummaryStatusOrders(item TalentSummary, status string) int {
	if item.StatusOrders == nil {
		return 0
	}
	return item.StatusOrders[status]
}

func (s *Store) writeOrdersSheetLocked(f *excelize.File, sheet string, req StatsRequest) error {
	usernames, _ := s.resolveScopeLocked(req)
	usernames = uniqueUsernames(usernames)
	if len(usernames) > 200 {
		return s.withTempTalentsLocked(usernames, func() error {
			filterReq := req
			filterReq.Scope = ""
			where, args := buildOrderWhere(filterReq, nil)
			if where == "" {
				where = "WHERE orders.talent_username IN (SELECT username FROM temp_filter_talents)"
			} else {
				where += " AND orders.talent_username IN (SELECT username FROM temp_filter_talents)"
			}
			return s.writeOrdersSheetQueryLocked(f, sheet, where, args)
		})
	}
	where, args := buildOrderWhere(req, usernames)
	return s.writeOrdersSheetQueryLocked(f, sheet, where, args)
}

func (s *Store) writeOrdersSheetQueryLocked(f *excelize.File, sheet, where string, args []any) error {
	rows, err := s.db.Query(`SELECT country, order_id, sku_id, product_id, product_name, product_price, quantity, settlement_status, talent_username, standard_commission_rate, shop_ad_commission_rate, commission_gmv, estimated_base_commission, estimated_standard_commission, commission_base, standard_commission, before_split_talent_income, agency_split_rate, talent_total_income, agency_pretax_total_income, agency_total_income, invitation_id, created_at, delivered_at, payment_id, payment_status FROM orders `+where+` ORDER BY created_date DESC, order_id DESC`, args...)
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
	row := make([]any, 26)
	var (
		country, orderID, skuID, productID, productName                      string
		productPrice                                                         float64
		quantity                                                             int
		settlementStatus, talentUsername, stdRate, adRate                    string
		commGMV, estBase, estStd, commBase, stdComm, beforeSplit             float64
		splitRate                                                            string
		talentTotal, agencyPretax, agencyTotal                               float64
		invitationID, createdAt, deliveredAt, paymentID, paymentStatus       string
	)
	for rows.Next() {
		if err := rows.Scan(
			&country, &orderID, &skuID, &productID, &productName,
			&productPrice, &quantity, &settlementStatus, &talentUsername,
			&stdRate, &adRate, &commGMV, &estBase, &estStd, &commBase,
			&stdComm, &beforeSplit, &splitRate, &talentTotal,
			&agencyPretax, &agencyTotal, &invitationID, &createdAt,
			&deliveredAt, &paymentID, &paymentStatus,
		); err != nil {
			return err
		}
		row[0] = country
		row[1] = orderID
		row[2] = skuID
		row[3] = productID
		row[4] = productName
		row[5] = moneyText(productPrice)
		row[6] = quantity
		row[7] = settlementStatus
		row[8] = talentUsername
		row[9] = stdRate
		row[10] = adRate
		row[11] = moneyText(commGMV)
		row[12] = moneyText(estBase)
		row[13] = moneyText(estStd)
		row[14] = moneyText(commBase)
		row[15] = moneyText(stdComm)
		row[16] = moneyText(beforeSplit)
		row[17] = splitRate
		row[18] = moneyText(talentTotal)
		row[19] = moneyText(agencyPretax)
		row[20] = moneyText(agencyTotal)
		row[21] = invitationID
		row[22] = createdAt
		row[23] = deliveredAt
		row[24] = paymentID
		row[25] = paymentStatus
		if err := sw.SetRow("A"+strconv.Itoa(rowNo), row); err != nil {
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
		parts = append(parts, effectiveSettlementStatusSQL()+" = ?")
		args = append(args, req.SettlementStatus)
	}
	if req.Scope != "" && req.Scope != "manual" && len(usernames) == 0 {
		parts = append(parts, "1 = 0")
	}
	if len(usernames) > 0 {
		marks := make([]string, 0, len(usernames))
		for _, name := range usernames {
			marks = append(marks, "?")
			args = append(args, normalizeTalentUsername(name))
		}
		parts = append(parts, "talent_username IN ("+strings.Join(marks, ",")+")")
	}
	if len(parts) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(parts, " AND "), args
}

func effectiveSettlementStatusSQL() string {
	return `CASE
WHEN TRIM(COALESCE(settlement_status, '')) = '已结算' AND TRIM(COALESCE(payment_status, '')) = '未付款' THEN '机构未付款'
WHEN TRIM(COALESCE(settlement_status, '')) = '已结算' THEN '已结算'
WHEN TRIM(COALESCE(settlement_status, '')) = '客户未付款' THEN '客户未付款'
WHEN TRIM(COALESCE(settlement_status, '')) = '待确认' THEN '待确认'
WHEN TRIM(COALESCE(settlement_status, '')) = '不符合条件' THEN '不符合条件'
WHEN TRIM(COALESCE(settlement_status, '')) = '已冻结' THEN '已冻结'
WHEN TRIM(COALESCE(settlement_status, '')) = '' THEN '未识别状态'
ELSE TRIM(COALESCE(settlement_status, ''))
END`
}

func parseUploadedOrdersEach(path, filename, country string, shouldCancel func() bool, handle func(Order) error) error {
	name := strings.ToLower(filename)
	if strings.HasSuffix(name, ".xlsx") {
		return parseXLSXEach(path, country, shouldCancel, handle)
	}
	if strings.HasSuffix(name, ".csv") || strings.HasSuffix(name, ".txt") || strings.HasSuffix(name, ".tsv") {
		return parseDelimitedEach(path, country, shouldCancel, handle)
	}
	return errors.New("仅支持 xlsx、csv、tsv、txt 账单文件")
}

func parseXLSXEach(path, country string, shouldCancel func() bool, handle func(Order) error) error {
	if zipErr := parseXLSXZipEach(path, country, shouldCancel, handle); zipErr == nil {
		return nil
	} else if errors.Is(zipErr, errXLSXPartialStream) {
		return zipErr
	}
	f, err := excelize.OpenFile(path)
	if err != nil {
		return fmt.Errorf("Excel 文件解析失败：%v", err)
	}
	defer f.Close()
	for _, sheet := range f.GetSheetList() {
		rows, err := f.Rows(sheet)
		if err != nil {
			continue
		}
		count, err := parseRowsIterator(rows, country, shouldCancel, handle)
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

func parseXLSXZipEach(path, country string, shouldCancel func() bool, handle func(Order) error) error {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("zip打开失败：%w", err)
	}
	defer reader.Close()
	files := map[string]*zip.File{}
	for _, file := range reader.File {
		files[file.Name] = file
	}
	log.Printf("INFO XLSX解析开始 file=%s", filepath.Base(path))
	shared, err := readXLSXSharedStrings(files["xl/sharedStrings.xml"], shouldCancel)
	if err != nil {
		return fmt.Errorf("共享文本解析失败：%w", err)
	}
	log.Printf("INFO XLSX共享文本完成 file=%s count=%d", filepath.Base(path), len(shared))
	sheets := xlsxSheetPaths(files)
	if len(sheets) == 0 {
		return errors.New("未找到工作表 XML")
	}
	for _, sheetPath := range sheets {
		file := files[sheetPath]
		if file == nil {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return fmt.Errorf("打开工作表失败：%s：%w", sheetPath, err)
		}
		log.Printf("INFO XLSX工作表解析开始 file=%s sheet=%s", filepath.Base(path), sheetPath)
		count, parseErr := parseXLSXSheetXML(rc, shared, country, shouldCancel, handle)
		_ = rc.Close()
		if parseErr != nil {
			if count > 0 {
				return fmt.Errorf("%w：%s：%v", errXLSXPartialStream, sheetPath, parseErr)
			}
			return fmt.Errorf("解析工作表失败：%s：%w", sheetPath, parseErr)
		}
		if count > 0 {
			log.Printf("INFO XLSX工作表解析完成 file=%s sheet=%s rows=%d", filepath.Base(path), sheetPath, count)
			return nil
		}
	}
	return errors.New("未识别到有效账单数据")
}

func xlsxSheetPaths(files map[string]*zip.File) []string {
	var paths []string
	for name := range files {
		if strings.HasPrefix(name, "xl/worksheets/sheet") && strings.HasSuffix(name, ".xml") {
			paths = append(paths, name)
		}
	}
	sort.Slice(paths, func(i, j int) bool {
		return sheetPathNumber(paths[i]) < sheetPathNumber(paths[j])
	})
	return paths
}

func sheetPathNumber(path string) int {
	base := path
	if idx := strings.LastIndexAny(base, `/\`); idx >= 0 {
		base = base[idx+1:]
	}
	base = strings.TrimSuffix(strings.TrimPrefix(base, "sheet"), ".xml")
	n, err := strconv.Atoi(base)
	if err != nil {
		return 1 << 30
	}
	return n
}

func readXLSXSharedStrings(file *zip.File, shouldCancel func() bool) ([]string, error) {
	if file == nil {
		return nil, nil
	}
	rc, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	dec := xml.NewDecoder(rc)
	var values []string
	for {
		if shouldCancel != nil && shouldCancel() {
			return nil, errImportCanceled
		}
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "si" {
			continue
		}
		text, err := readXLSXSharedItem(dec)
		if err != nil {
			return nil, err
		}
		values = append(values, text)
	}
	return values, nil
}

func readXLSXSharedItem(dec *xml.Decoder) (string, error) {
	var b strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "t" {
				text, err := readXMLElementText(dec, "t")
				if err != nil {
					return "", err
				}
				b.WriteString(text)
			}
		case xml.EndElement:
			if t.Name.Local == "si" {
				return b.String(), nil
			}
		}
	}
}

func parseXLSXSheetXML(reader io.Reader, shared []string, country string, shouldCancel func() bool, handle func(Order) error) (int, error) {
	dec := xml.NewDecoder(bufio.NewReaderSize(reader, 256*1024))
	var headers []string
	indexes := map[string][]int{}
	count := 0
	for {
		if shouldCancel != nil && shouldCancel() {
			return count, errImportCanceled
		}
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return count, err
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "row" {
			continue
		}
		row, err := readXLSXRow(dec, shared)
		if err != nil {
			return count, err
		}
		if headers == nil {
			if !isHeaderRow(row) {
				continue
			}
			headers = row
			indexes = buildHeaderIndexes(headers)
			continue
		}
		if isBlankRow(row) {
			continue
		}
		order := rowToOrderWithIndexes(row, headers, indexes, country)
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

func readXLSXRow(dec *xml.Decoder, shared []string) ([]string, error) {
	var row []string
	for {
		tok, err := dec.Token()
		if err != nil {
			return row, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local != "c" {
				continue
			}
			ref := attrValue(t.Attr, "r")
			col := xlsxColumnIndex(ref)
			if col < 0 {
				col = len(row)
			}
			for len(row) <= col {
				row = append(row, "")
			}
			value, err := readXLSXCell(dec, t, shared)
			if err != nil {
				return row, err
			}
			row[col] = value
		case xml.EndElement:
			if t.Name.Local == "row" {
				return row, nil
			}
		}
	}
}

func readXLSXCell(dec *xml.Decoder, start xml.StartElement, shared []string) (string, error) {
	cellType := attrValue(start.Attr, "t")
	var rawValue string
	var inline strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "v":
				text, err := readXMLElementText(dec, "v")
				if err != nil {
					return "", err
				}
				rawValue = text
			case "t":
				text, err := readXMLElementText(dec, "t")
				if err != nil {
					return "", err
				}
				inline.WriteString(text)
			}
		case xml.EndElement:
			if t.Name.Local != "c" {
				continue
			}
			if cellType == "s" {
				idx, err := strconv.Atoi(strings.TrimSpace(rawValue))
				if err == nil && idx >= 0 && idx < len(shared) {
					return strings.TrimSpace(shared[idx]), nil
				}
				return "", nil
			}
			if inline.Len() > 0 {
				return strings.TrimSpace(inline.String()), nil
			}
			return strings.TrimSpace(rawValue), nil
		}
	}
}

func readXMLElementText(dec *xml.Decoder, end string) (string, error) {
	var b strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.CharData:
			b.Write([]byte(t))
		case xml.EndElement:
			if t.Name.Local == end {
				return b.String(), nil
			}
		}
	}
}

func attrValue(attrs []xml.Attr, name string) string {
	for _, attr := range attrs {
		if attr.Name.Local == name {
			return attr.Value
		}
	}
	return ""
}

func xlsxColumnIndex(ref string) int {
	col := 0
	found := false
	for _, r := range ref {
		if r >= 'A' && r <= 'Z' {
			col = col*26 + int(r-'A'+1)
			found = true
		} else if r >= 'a' && r <= 'z' {
			col = col*26 + int(r-'a'+1)
			found = true
		} else if found {
			break
		}
	}
	if !found {
		return -1
	}
	return col - 1
}

type rowIterator interface {
	Next() bool
	Columns(...excelize.Options) ([]string, error)
}

func parseRowsIterator(rows rowIterator, country string, shouldCancel func() bool, handle func(Order) error) (int, error) {
	var headers []string
	indexes := map[string][]int{}
	count := 0
	for rows.Next() {
		if shouldCancel != nil && shouldCancel() {
			return count, errImportCanceled
		}
		row, err := rows.Columns()
		if err != nil {
			return count, err
		}
		if headers == nil {
			if !isHeaderRow(row) {
				continue
			}
			headers = row
			indexes = buildHeaderIndexes(headers)
			continue
		}
		if isBlankRow(row) {
			continue
		}
		order := rowToOrderWithIndexes(row, headers, indexes, country)
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

func parseDelimitedEach(path, country string, shouldCancel func() bool, handle func(Order) error) error {
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
	reader.LazyQuotes = true
	reader.TrimLeadingSpace = true
	var headers []string
	indexes := map[string][]int{}
	count := 0
	for {
		if shouldCancel != nil && shouldCancel() {
			return errImportCanceled
		}
		row, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return errors.New("表格文本解析失败")
		}
		if headers == nil {
			if !isHeaderRow(row) {
				continue
			}
			headers = row
			indexes = buildHeaderIndexes(headers)
			continue
		}
		if isBlankRow(row) {
			continue
		}
		order := rowToOrderWithIndexes(row, headers, indexes, country)
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

func isHeaderRow(row []string) bool {
	score := 0
	for _, cell := range row {
		key := normalizeHeader(cell)
		if key == "orderid" || key == "talentusername" || key == "agencytotalincome" || key == "agencypretaxtotalincome" || key == "commissionsrewards" || key == "createdtime" {
			score++
		}
	}
	return score >= 2
}

func buildHeaderIndexes(headers []string) map[string][]int {
	indexes := map[string][]int{}
	for i, h := range headers {
		key := normalizeHeader(h)
		if key != "" {
			indexes[key] = append(indexes[key], i)
		}
	}
	return indexes
}

func valueFromIndexes(row []string, indexes map[string][]int, key string) string {
	cols := indexes[key]
	for i := len(cols) - 1; i >= 0; i-- {
		if v := strings.TrimSpace(cell(row, cols[i])); v != "" {
			return v
		}
	}
	return ""
}

func moneyFromIndexes(row []string, indexes map[string][]int, key string) float64 {
	cols := indexes[key]
	if len(cols) == 0 {
		return 0
	}
	var fallback float64
	hasFallback := false
	for i := len(cols) - 1; i >= 0; i-- {
		raw := strings.TrimSpace(cell(row, cols[i]))
		if raw == "" {
			continue
		}
		v := parseMoney(raw)
		if v != 0 {
			return v
		}
		if !hasFallback {
			fallback = v
			hasFallback = true
		}
	}
	return fallback
}

func rowToOrder(row, headers []string, index map[string]int, country string) Order {
	indexes := make(map[string][]int, len(index))
	for k, i := range index {
		indexes[k] = []int{i}
	}
	return rowToOrderWithIndexes(row, headers, indexes, country)
}

func rowToOrderWithIndexes(row, headers []string, indexes map[string][]int, country string) Order {
	agencyTotal := moneyFromIndexes(row, indexes, "agencytotalincome")
	if agencyTotal == 0 {
		if commRewards := moneyFromIndexes(row, indexes, "commissionsrewards"); commRewards != 0 {
			agencyTotal = commRewards
		}
	}
	agencyPretax := moneyFromIndexes(row, indexes, "agencypretaxtotalincome")
	if agencyTotal == 0 && agencyPretax != 0 {
		agencyTotal = agencyPretax
	}
	o := Order{
		Country:                     country,
		OrderID:                     valueFromIndexes(row, indexes, "orderid"),
		SKUID:                       valueFromIndexes(row, indexes, "skuid"),
		ProductID:                   valueFromIndexes(row, indexes, "productid"),
		ProductName:                 valueFromIndexes(row, indexes, "productname"),
		ProductPrice:                moneyFromIndexes(row, indexes, "productprice"),
		Quantity:                    parseInt(valueFromIndexes(row, indexes, "quantity")),
		SettlementStatus:            normalizedSettlementStatus(valueFromIndexes(row, indexes, "settlementstatus")),
		TalentUsername:              normalizeTalentUsername(valueFromIndexes(row, indexes, "talentusername")),
		StandardCommissionRate:      valueFromIndexes(row, indexes, "standardcommissionrate"),
		ShopAdCommissionRate:        valueFromIndexes(row, indexes, "shopadcommissionrate"),
		CommissionGMV:               moneyFromIndexes(row, indexes, "commissiongmv"),
		EstimatedBaseCommission:     moneyFromIndexes(row, indexes, "estimatedbasecommission"),
		EstimatedStandardCommission: moneyFromIndexes(row, indexes, "estimatedstandardcommission"),
		CommissionBase:              moneyFromIndexes(row, indexes, "commissionbase"),
		StandardCommission:          moneyFromIndexes(row, indexes, "standardcommission"),
		BeforeSplitTalentIncome:     moneyFromIndexes(row, indexes, "beforesplittalentincome"),
		AgencySplitRate:             valueFromIndexes(row, indexes, "agencysplitrate"),
		TalentTotalIncome:           moneyFromIndexes(row, indexes, "talenttotalincome"),
		AgencyPretaxTotalIncome:     agencyPretax,
		AgencyTotalIncome:           agencyTotal,
		InvitationID:                valueFromIndexes(row, indexes, "invitationid"),
		CreatedAt:                   valueFromIndexes(row, indexes, "createdtime"),
		DeliveredAt:                 valueFromIndexes(row, indexes, "deliveredtime"),
		PaymentID:                   valueFromIndexes(row, indexes, "paymentid"),
		PaymentStatus:               normalizedPaymentStatus(valueFromIndexes(row, indexes, "paymentstatus")),
	}
	o.CreatedDate = normalizeDate(o.CreatedAt)
	return o
}

func orderSelectSQL() string {
	return `SELECT id, country, order_id, sku_id, product_id, product_name, product_price, quantity, settlement_status, talent_username, standard_commission_rate, shop_ad_commission_rate, commission_gmv, estimated_base_commission, estimated_standard_commission, commission_base, standard_commission, before_split_talent_income, agency_split_rate, talent_total_income, agency_pretax_total_income, agency_total_income, invitation_id, created_at, created_date, delivered_at, payment_id, payment_status, raw_json FROM orders`
}

func orderCompareSelectSQL() string {
	return `SELECT duplicate_key, product_name, product_price, quantity, settlement_status, agency_pretax_total_income, agency_total_income, invitation_id, created_at, delivered_at, payment_id, payment_status FROM orders`
}

func orderCompareJoinSQL() string {
	return `SELECT o.duplicate_key, o.product_name, o.product_price, o.quantity, o.settlement_status, o.agency_pretax_total_income, o.agency_total_income, o.invitation_id, o.created_at, o.delivered_at, o.payment_id, o.payment_status FROM temp_compare_keys k JOIN orders o ON o.duplicate_key = k.duplicate_key`
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
	if raw != "" && raw != "{}" && raw != "null" {
		_ = json.Unmarshal([]byte(raw), &o.Raw)
	}
	if o.Raw == nil {
		o.Raw = map[string]string{}
	}
	return o, nil
}

func scanOrderRows(rows *sql.Rows) (Order, error) {
	return scanOrder(rows)
}

func scanOrderCompareRows(rows *sql.Rows) (Order, string, error) {
	var o Order
	var duplicateKey string
	err := rows.Scan(&duplicateKey, &o.ProductName, &o.ProductPrice, &o.Quantity, &o.SettlementStatus, &o.AgencyPretaxTotalIncome, &o.AgencyTotalIncome, &o.InvitationID, &o.CreatedAt, &o.DeliveredAt, &o.PaymentID, &o.PaymentStatus)
	return o, duplicateKey, err
}

func marshalOrderRawJSON(raw map[string]string) string {
	if len(raw) == 0 {
		return "{}"
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func insertOrderTx(tx *sql.Tx, order Order) error {
	raw := marshalOrderRawJSON(order.Raw)
	key, _ := duplicateOrderKey(order)
	_, err := tx.Exec(`INSERT INTO orders (id, duplicate_key, country, order_id, sku_id, product_id, product_name, product_price, quantity, settlement_status, talent_username, standard_commission_rate, shop_ad_commission_rate, commission_gmv, estimated_base_commission, estimated_standard_commission, commission_base, standard_commission, before_split_talent_income, agency_split_rate, talent_total_income, agency_pretax_total_income, agency_total_income, invitation_id, created_at, created_date, delivered_at, payment_id, payment_status, raw_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		order.ID, key, order.Country, order.OrderID, order.SKUID, order.ProductID, order.ProductName, order.ProductPrice, order.Quantity, order.SettlementStatus, order.TalentUsername, order.StandardCommissionRate, order.ShopAdCommissionRate, order.CommissionGMV, order.EstimatedBaseCommission, order.EstimatedStandardCommission, order.CommissionBase, order.StandardCommission, order.BeforeSplitTalentIncome, order.AgencySplitRate, order.TalentTotalIncome, order.AgencyPretaxTotalIncome, order.AgencyTotalIncome, order.InvitationID, order.CreatedAt, order.CreatedDate, order.DeliveredAt, order.PaymentID, order.PaymentStatus, raw)
	return err
}

func insertOrderSQL() string {
	return `INSERT INTO orders (id, duplicate_key, country, order_id, sku_id, product_id, product_name, product_price, quantity, settlement_status, talent_username, standard_commission_rate, shop_ad_commission_rate, commission_gmv, estimated_base_commission, estimated_standard_commission, commission_base, standard_commission, before_split_talent_income, agency_split_rate, talent_total_income, agency_pretax_total_income, agency_total_income, invitation_id, created_at, created_date, delivered_at, payment_id, payment_status, raw_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
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
	raw := marshalOrderRawJSON(order.Raw)
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
		order.PaymentID, order.PaymentStatus, raw,
	)
	return err
}

func execInsertOrder(stmt *sql.Stmt, order Order) error {
	raw := marshalOrderRawJSON(order.Raw)
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
		order.PaymentID, order.PaymentStatus, raw,
	)
	return err
}

func updateOrderTx(tx *sql.Tx, order Order) error {
	raw := marshalOrderRawJSON(order.Raw)
	key, _ := duplicateOrderKey(order)
	_, err := tx.Exec(`UPDATE orders SET duplicate_key=?, country=?, order_id=?, sku_id=?, product_id=?, product_name=?, product_price=?, quantity=?, settlement_status=?, talent_username=?, standard_commission_rate=?, shop_ad_commission_rate=?, commission_gmv=?, estimated_base_commission=?, estimated_standard_commission=?, commission_base=?, standard_commission=?, before_split_talent_income=?, agency_split_rate=?, talent_total_income=?, agency_pretax_total_income=?, agency_total_income=?, invitation_id=?, created_at=?, created_date=?, delivered_at=?, payment_id=?, payment_status=?, raw_json=? WHERE id=?`,
		key, order.Country, order.OrderID, order.SKUID, order.ProductID, order.ProductName, order.ProductPrice, order.Quantity, order.SettlementStatus, order.TalentUsername, order.StandardCommissionRate, order.ShopAdCommissionRate, order.CommissionGMV, order.EstimatedBaseCommission, order.EstimatedStandardCommission, order.CommissionBase, order.StandardCommission, order.BeforeSplitTalentIncome, order.AgencySplitRate, order.TalentTotalIncome, order.AgencyPretaxTotalIncome, order.AgencyTotalIncome, order.InvitationID, order.CreatedAt, order.CreatedDate, order.DeliveredAt, order.PaymentID, order.PaymentStatus, raw, order.ID)
	return err
}
