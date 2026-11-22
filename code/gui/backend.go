package main

import (
	"bytes"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

//go:embed static/* templates/*.html
var embeddedFiles embed.FS

var countries = []string{"新加坡", "泰国", "马来西亚", "菲律宾", "越南", "日本", "美国", "巴西", "墨西哥", "英国", "德国", "法国", "意大利", "西班牙"}
var settlementStatuses = []string{"客户未付款", "待确认", "已结算", "机构未付款", "不符合条件", "已冻结"}

const appVersion = "0.1.9"

type Store struct {
	mu             sync.Mutex
	idMu           sync.Mutex
	importMu       sync.Mutex
	statsMu        sync.Mutex
	progressMu     sync.Mutex
	cancelImport   bool
	path           string
	dbPath         string
	db             *sql.DB
	importProgress ImportProgress `json:"-"`
	importCache    *ImportCache   `json:"-"`
	Orders         []Order        `json:"orders,omitempty"`
	Companies      []Company      `json:"companies"`
	NextID         int64          `json:"nextId"`
	UpdatedAt      time.Time      `json:"updatedAt"`
}

type ImportCache struct {
	Path     string
	Filename string
	Country  string
	Size     int64
	ModTime  time.Time
	Orders   []Order
}

type Order struct {
	ID                          string            `json:"id"`
	Country                     string            `json:"country"`
	OrderID                     string            `json:"orderId"`
	SKUID                       string            `json:"skuId"`
	ProductID                   string            `json:"productId"`
	ProductName                 string            `json:"productName"`
	ProductPrice                float64           `json:"productPrice"`
	Quantity                    int               `json:"quantity"`
	SettlementStatus            string            `json:"settlementStatus"`
	TalentUsername              string            `json:"talentUsername"`
	StandardCommissionRate      string            `json:"standardCommissionRate"`
	ShopAdCommissionRate        string            `json:"shopAdCommissionRate"`
	CommissionGMV               float64           `json:"commissionGmv"`
	EstimatedBaseCommission     float64           `json:"estimatedBaseCommission"`
	EstimatedStandardCommission float64           `json:"estimatedStandardCommission"`
	CommissionBase              float64           `json:"commissionBase"`
	StandardCommission          float64           `json:"standardCommission"`
	BeforeSplitTalentIncome     float64           `json:"beforeSplitTalentIncome"`
	AgencySplitRate             string            `json:"agencySplitRate"`
	TalentTotalIncome           float64           `json:"talentTotalIncome"`
	AgencyPretaxTotalIncome     float64           `json:"agencyPretaxTotalIncome"`
	AgencyTotalIncome           float64           `json:"agencyTotalIncome"`
	InvitationID                string            `json:"invitationId"`
	CreatedAt                   string            `json:"createdAt"`
	CreatedDate                 string            `json:"createdDate"`
	DeliveredAt                 string            `json:"deliveredAt"`
	PaymentID                   string            `json:"paymentId"`
	PaymentStatus               string            `json:"paymentStatus"`
	Raw                         map[string]string `json:"raw"`
}

type Company struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Employees []Employee `json:"employees"`
}

type Employee struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Talents []string `json:"talents"`
}

type TalentDuplicate struct {
	Username string               `json:"username"`
	Refs     []TalentDuplicateRef `json:"refs"`
}

type TalentDuplicateRef struct {
	CompanyID    string `json:"companyId"`
	CompanyName  string `json:"companyName"`
	EmployeeID   string `json:"employeeId"`
	EmployeeName string `json:"employeeName"`
}

type TalentCreateResult struct {
	RequiresConfirm bool              `json:"requiresConfirm"`
	Added           int               `json:"added"`
	Skipped         int               `json:"skipped"`
	Duplicates      []TalentDuplicate `json:"duplicates"`
	Employee        Employee          `json:"employee"`
	Message         string            `json:"message"`
}

type StatsRequest struct {
	Country          string
	Start            string
	End              string
	SettlementStatus string
	Scope            string
	ID               string
	Usernames        []string
	USDRate          *float64
	RMBRate          *float64
	AgencyCutRate    *float64
	FeePermille      *float64
}

type StatsResponse struct {
	Total       float64         `json:"total"`
	OrderCount  int             `json:"orderCount"`
	TalentCount int             `json:"talentCount"`
	PerTalent   []TalentSummary `json:"perTalent"`
	StatusStats []StatusSummary `json:"statusStats"`
	FilterRange DateRange       `json:"filterRange"`
	Title       string          `json:"title"`
}

type OrdersResponse struct {
	Total       int             `json:"total"`
	Page        int             `json:"page"`
	PageSize    int             `json:"pageSize"`
	TotalPages  int             `json:"totalPages"`
	Income      float64         `json:"income"`
	StatusStats []StatusSummary `json:"statusStats"`
	FilterRange DateRange       `json:"filterRange"`
	Orders      []Order         `json:"orders"`
}

type TalentSummary struct {
	Username     string             `json:"username"`
	Total        float64            `json:"total"`
	Orders       int                `json:"orders"`
	StatusTotals map[string]float64 `json:"statusTotals"`
	StatusOrders map[string]int     `json:"statusOrders"`
}

type StatusSummary struct {
	Status string  `json:"status"`
	Total  float64 `json:"total"`
	Orders int     `json:"orders"`
}

type DateRange struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

type ImportResult struct {
	RequiresConfirm         bool                  `json:"requiresConfirm"`
	Imported                int                   `json:"imported"`
	Updated                 int                   `json:"updated"`
	SkippedUnchanged        int                   `json:"skippedUnchanged"`
	DuplicateCount          int                   `json:"duplicateCount"`
	ChangedDuplicateCount   int                   `json:"changedDuplicateCount"`
	UnchangedDuplicateCount int                   `json:"unchangedDuplicateCount"`
	ChangedFields           []ChangedFieldSummary `json:"changedFields"`
	Samples                 []DuplicateChange     `json:"samples"`
	Message                 string                `json:"message"`
}

type ImportProgress struct {
	Active             bool   `json:"active"`
	Phase              string `json:"phase"`
	Country            string `json:"country"`
	Filename           string `json:"filename"`
	Total              int    `json:"total"`
	Processed          int    `json:"processed"`
	Imported           int    `json:"imported"`
	Updated            int    `json:"updated"`
	SkippedUnchanged   int    `json:"skippedUnchanged"`
	DuplicateCount     int    `json:"duplicateCount"`
	ChangedDuplicate   int    `json:"changedDuplicate"`
	UnchangedDuplicate int    `json:"unchangedDuplicate"`
	Message            string `json:"message"`
	StartedAt          string `json:"startedAt"`
	UpdatedAt          string `json:"updatedAt"`
}

type ChangedFieldSummary struct {
	Field string `json:"field"`
	Count int    `json:"count"`
}

type DuplicateChange struct {
	Key     string        `json:"key"`
	Changes []FieldChange `json:"changes"`
}

type FieldChange struct {
	Field string `json:"field"`
	Old   string `json:"old"`
	New   string `json:"new"`
}

type orderCompareField struct {
	label string
	value func(Order) string
}

func importDoneMessage(result ImportResult) string {
	return fmt.Sprintf("导入完成：新增 %d 条，覆盖更新 %d 条。", result.Imported, result.Updated)
}

func importDuplicateMessage(result ImportResult) string {
	return fmt.Sprintf("发现 %d 条重复订单。确认后将用新导入的数据覆盖旧数据。", result.DuplicateCount)
}

func newBillingHandler() (http.Handler, *Store, error) {
	dataRoot := findDataRoot()
	dataPath := filepath.Join(dataRoot, "data", "gui", "state.json")
	if err := os.MkdirAll(filepath.Dir(dataPath), 0755); err != nil {
		return nil, nil, err
	}

	store := &Store{path: dataPath, dbPath: filepath.Join(dataRoot, "data", "gui", "billing.db"), NextID: 1}
	if err := store.Load(); err != nil {
		return nil, nil, err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", serveIndex())
	mux.HandleFunc("/orders", serveOrders())
	staticFiles, err := fs.Sub(embeddedFiles, "static")
	if err != nil {
		return nil, nil, err
	}
	mux.HandleFunc("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFiles))).ServeHTTP)
	mux.HandleFunc("/api/state", store.handleState)
	mux.HandleFunc("/api/import", store.handleImport)
	mux.HandleFunc("/api/import/progress", store.handleImportProgress)
	mux.HandleFunc("/api/import/cancel", store.handleImportCancel)
	mux.HandleFunc("/api/stats", store.handleStats)
	mux.HandleFunc("/api/orders", store.handleOrders)
	mux.HandleFunc("/api/orders/delete", store.handleDeleteOrders)
	mux.HandleFunc("/api/export", store.handleExport)
	mux.HandleFunc("/api/company", store.handleCompany)
	mux.HandleFunc("/api/employee", store.handleEmployee)
	mux.HandleFunc("/api/talent", store.handleTalent)
	mux.HandleFunc("/api/update/check", store.handleUpdateCheck)
	mux.HandleFunc("/api/update/apply", store.handleUpdateApply)
	mux.HandleFunc("/api/update/config", store.handleUpdateConfig)

	log.Printf("INFO GUI 数据目录：%s", filepath.Join(dataRoot, "data", "gui"))
	return logRequest(mux), store, nil
}

func findDataRoot() string {
	wd, err := os.Getwd()
	wdOK := err == nil
	exe, err := os.Executable()
	if err == nil {
		exeDir := filepath.Dir(exe)
		if tempDir := os.TempDir(); tempDir != "" {
			if rel, relErr := filepath.Rel(tempDir, exeDir); relErr == nil && rel != "." && !strings.HasPrefix(rel, "..") {
				if wdOK && filepath.Base(wd) == "gui" {
					return filepath.Clean(filepath.Join(wd, "..", ".."))
				}
				if wdOK {
					return wd
				}
			}
		}
		return exeDir
	}
	if wdOK {
		return wd
	}
	return "."
}

func serveIndex() http.HandlerFunc {
	tpl := template.Must(template.ParseFS(embeddedFiles, "templates/index.html"))
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		_ = tpl.Execute(w, map[string]any{"Title": "账单系统", "Version": appVersion})
	}
}

func serveOrders() http.HandlerFunc {
	tpl := template.Must(template.ParseFS(embeddedFiles, "templates/orders.html"))
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/orders" {
			http.NotFound(w, r)
			return
		}
		_ = tpl.Execute(w, map[string]any{"Title": "订单明细", "Version": appVersion})
	}
}

func logRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("INFO %s %s", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

func (s *Store) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s.openOrdersDBLocked()
	}
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return s.openOrdersDBLocked()
	}
	if err := json.Unmarshal(b, s); err != nil {
		return err
	}
	changedTalents := s.normalizeTalentsLocked()
	if err := s.openOrdersDBLocked(); err != nil {
		return err
	}
	if len(s.Orders) > 0 {
		if err := s.migrateJSONOrdersLocked(); err != nil {
			return err
		}
		return s.SaveLocked()
	}
	if changedTalents {
		return s.SaveLocked()
	}
	return nil
}

func (s *Store) SaveLocked() error {
	s.normalizeTalentsLocked()
	s.UpdatedAt = time.Now()
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".state-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err = tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	for attempt := 0; attempt < 3; attempt++ {
		if err = os.Rename(tmpPath, s.path); err == nil {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return err
}

func (s *Store) newIDLocked(prefix string) string {
	s.idMu.Lock()
	defer s.idMu.Unlock()
	s.NextID++
	return fmt.Sprintf("%s%d", prefix, s.NextID)
}

func (s *Store) handleState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "方法不支持")
		return
	}
	globalRange, countryRanges, orderCount := s.dbStateMetaLocked()
	s.mu.Lock()
	companies := s.Companies
	talentDuplicates := s.talentDuplicatesLocked()
	updatedAt := s.UpdatedAt
	defer s.mu.Unlock()
	writeJSON(w, map[string]any{
		"countries":          countries,
		"settlementStatuses": settlementStatuses,
		"companies":          companies,
		"talentDuplicates":   talentDuplicates,
		"orderCount":         orderCount,
		"updatedAt":          updatedAt,
		"dateRange":          globalRange,
		"countryDateRanges":  countryRanges,
		"appVersion":         appVersion,
	})
}

func (s *Store) handleImportProgress(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "方法不支持")
		return
	}
	writeJSON(w, s.currentImportProgress())
}

func (s *Store) handleImportCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "方法不支持")
		return
	}
	s.requestImportCancel()
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Store) setImportProgress(next ImportProgress) {
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	now := time.Now().Format("2006-01-02 15:04:05")
	if next.StartedAt == "" {
		if s.importProgress.Active && s.importProgress.StartedAt != "" {
			next.StartedAt = s.importProgress.StartedAt
		} else {
			next.StartedAt = now
		}
	}
	next.UpdatedAt = now
	s.importProgress = next
}

func (s *Store) currentImportProgress() ImportProgress {
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	return s.importProgress
}

func (s *Store) resetImportCancel() {
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	s.cancelImport = false
}

func (s *Store) requestImportCancel() {
	s.progressMu.Lock()
	s.cancelImport = true
	if s.importProgress.Active {
		s.importProgress.Phase = "正在中断"
		s.importProgress.Message = "已请求中断，将保留已导入的数据。"
		s.importProgress.UpdatedAt = time.Now().Format("2006-01-02 15:04:05")
	}
	s.progressMu.Unlock()
}

func (s *Store) importCanceled() bool {
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	return s.cancelImport
}

func (s *Store) talentDuplicatesLocked() []TalentDuplicate {
	type accountRef struct {
		username string
		ref      TalentDuplicateRef
	}
	grouped := map[string][]accountRef{}
	for _, company := range s.Companies {
		for _, employee := range company.Employees {
			for _, username := range employee.Talents {
				name := strings.TrimSpace(username)
				if name == "" {
					continue
				}
				key := strings.ToLower(name)
				grouped[key] = append(grouped[key], accountRef{
					username: name,
					ref: TalentDuplicateRef{
						CompanyID:    company.ID,
						CompanyName:  company.Name,
						EmployeeID:   employee.ID,
						EmployeeName: employee.Name,
					},
				})
			}
		}
	}
	var duplicates []TalentDuplicate
	for _, refs := range grouped {
		if len(refs) < 2 {
			continue
		}
		item := TalentDuplicate{Username: refs[0].username}
		for _, ref := range refs {
			item.Refs = append(item.Refs, ref.ref)
		}
		duplicates = append(duplicates, item)
	}
	sort.Slice(duplicates, func(i, j int) bool {
		return strings.ToLower(duplicates[i].Username) < strings.ToLower(duplicates[j].Username)
	})
	return duplicates
}

func (s *Store) findTalentDuplicatesLocked(names []string, targetCompanyID, targetEmployeeID string) []TalentDuplicate {
	wanted := map[string]string{}
	for _, name := range names {
		clean := strings.TrimSpace(name)
		if clean == "" {
			continue
		}
		wanted[strings.ToLower(clean)] = clean
	}
	if len(wanted) == 0 {
		return nil
	}
	refsByName := map[string][]TalentDuplicateRef{}
	for _, company := range s.Companies {
		for _, employee := range company.Employees {
			for _, username := range employee.Talents {
				key := strings.ToLower(strings.TrimSpace(username))
				if _, ok := wanted[key]; !ok {
					continue
				}
				refsByName[key] = append(refsByName[key], TalentDuplicateRef{
					CompanyID:    company.ID,
					CompanyName:  company.Name,
					EmployeeID:   employee.ID,
					EmployeeName: employee.Name,
				})
			}
		}
	}
	var duplicates []TalentDuplicate
	for key, refs := range refsByName {
		if len(refs) == 0 {
			continue
		}
		duplicates = append(duplicates, TalentDuplicate{
			Username: wanted[key],
			Refs:     refs,
		})
	}
	sort.Slice(duplicates, func(i, j int) bool {
		return strings.ToLower(duplicates[i].Username) < strings.ToLower(duplicates[j].Username)
	})
	_ = targetCompanyID
	_ = targetEmployeeID
	return duplicates
}

func (s *Store) handleImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "方法不支持")
		return
	}
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "上传内容过大或格式错误")
		return
	}
	country := canonicalCountry(r.FormValue("country"))
	if country == "" {
		writeError(w, http.StatusBadRequest, "閻犲洨鍏橀埀顒€顦扮€氥劑宕堕挊澶樺晙")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "请选择账单文件")
		return
	}
	defer file.Close()
	tmp, err := os.CreateTemp("", "billing-import-*"+filepath.Ext(header.Filename))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "创建临时文件失败")
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	written, err := io.Copy(tmp, file)
	if err != nil {
		_ = tmp.Close()
		writeError(w, http.StatusBadRequest, "保存上传文件失败")
		return
	}
	_ = tmp.Close()
	uploadDiag := importFileDiagnostic(tmpPath, header.Filename, written)

	confirmed := r.FormValue("confirmUpdate") == "1"
	s.importMu.Lock()
	defer s.importMu.Unlock()
	if !confirmed && s.countryOrderCountLocked(country) == 0 {
		result, err := s.applyImportFileLocked(tmpPath, header.Filename, country, ImportResult{})
		if err == nil {
			s.mu.Lock()
			err = s.SaveLocked()
			s.mu.Unlock()
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("保存账单失败：%v\n%s", err, uploadDiag))
			return
		}
		if result.Message == "" {
			result.Message = importDoneMessage(result)
		}
		writeJSON(w, result)
		return
	}
	if confirmed {
		result, err := s.replaceImportFileLocked(tmpPath, header.Filename, country)
		if err == nil {
			s.mu.Lock()
			err = s.SaveLocked()
			s.mu.Unlock()
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("保存账单失败：%v\n%s", err, uploadDiag))
			return
		}
		if result.Message == "" {
			result.Message = importDoneMessage(result)
		}
		writeJSON(w, result)
		return
	}
	result, err := s.previewImportFileLocked(tmpPath, header.Filename, country)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error()+"\n"+uploadDiag)
		return
	}
	if result.DuplicateCount > 0 {
		result.RequiresConfirm = true
		result.Message = importDuplicateMessage(result)
		writeJSON(w, result)
		return
	}
	result, err = s.applyImportFileLocked(tmpPath, header.Filename, country, result)
	if err == nil {
		s.mu.Lock()
		err = s.SaveLocked()
		s.mu.Unlock()
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("保存账单失败：%v\n%s", err, uploadDiag))
		return
	}
	if result.Message == "" {
		result.Message = importDoneMessage(result)
	}
	writeJSON(w, result)
}

func importFileDiagnostic(path, filename string, copied int64) string {
	sizeText := "閻犲洩顕цぐ鍥ㄥ緞鏉堫偉袝"
	if info, err := os.Stat(path); err == nil {
		sizeText = strconv.FormatInt(info.Size(), 10)
	}
	headText := "閻犲洩顕цぐ鍥ㄥ緞鏉堫偉袝"
	if f, err := os.Open(path); err == nil {
		buf := make([]byte, 16)
		n, readErr := f.Read(buf)
		_ = f.Close()
		if readErr == nil || errors.Is(readErr, io.EOF) {
			headText = fmt.Sprintf("% X", buf[:n])
		}
	}
	return fmt.Sprintf("诊断：文件=%s，大小=%d(%s)，头部=%s", filename, copied, sizeText, headText)
}

func duplicateOrderKey(order Order) (string, bool) {
	parts := []string{
		strings.TrimSpace(order.Country),
		strings.TrimSpace(order.OrderID),
		strings.TrimSpace(order.SKUID),
		strings.TrimSpace(order.ProductID),
		strings.TrimSpace(order.TalentUsername),
	}
	for _, part := range parts {
		if part == "" {
			return "", false
		}
	}
	return strings.ToLower(strings.Join(parts, "|")), true
}

func compareOrders(oldOrder, newOrder Order) []FieldChange {
	var changes []FieldChange
	for _, field := range orderCompareFields() {
		oldValue := field.value(oldOrder)
		newValue := field.value(newOrder)
		if oldValue != newValue {
			changes = append(changes, FieldChange{Field: field.label, Old: oldValue, New: newValue})
		}
	}
	rawKeys := map[string]bool{}
	for key := range oldOrder.Raw {
		rawKeys[key] = true
	}
	for key := range newOrder.Raw {
		rawKeys[key] = true
	}
	var keys []string
	for key := range rawKeys {
		if normalizeHeader(key) == "" || isComparedRawField(key) {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		oldValue := strings.TrimSpace(oldOrder.Raw[key])
		newValue := strings.TrimSpace(newOrder.Raw[key])
		if oldValue != newValue {
			changes = append(changes, FieldChange{Field: key, Old: oldValue, New: newValue})
		}
	}
	return changes
}

func compareOrderCoreFields(oldOrder, newOrder Order) []FieldChange {
	var changes []FieldChange
	for _, field := range orderEffectiveFields() {
		oldValue := field.value(oldOrder)
		newValue := field.value(newOrder)
		if oldValue != newValue {
			changes = append(changes, FieldChange{Field: field.label, Old: oldValue, New: newValue})
		}
	}
	return changes
}

func orderEffectiveFields() []orderCompareField {
	return []orderCompareField{
		{label: "商品名称", value: func(o Order) string { return o.ProductName }},
		{label: "商品价格", value: func(o Order) string { return moneyText(o.ProductPrice) }},
		{label: "下单件数", value: func(o Order) string { return strconv.Itoa(o.Quantity) }},
		{label: "订单结算状态", value: func(o Order) string { return o.SettlementStatus }},
		{label: "代理机构税前总收益", value: func(o Order) string { return moneyText(o.AgencyPretaxTotalIncome) }},
		{label: "代理机构总收益", value: func(o Order) string { return moneyText(o.AgencyTotalIncome) }},
		{label: "Invitation ID", value: func(o Order) string { return o.InvitationID }},
		{label: "创建时间", value: func(o Order) string { return o.CreatedAt }},
		{label: "订单送达时间", value: func(o Order) string { return o.DeliveredAt }},
		{label: "付款 ID", value: func(o Order) string { return o.PaymentID }},
		{label: "付款状态", value: func(o Order) string { return o.PaymentStatus }},
	}
}

func isComparedRawField(field string) bool {
	switch normalizeHeader(field) {
	case "orderid", "skuid", "productid", "productname", "productprice", "quantity",
		"settlementstatus", "talentusername", "standardcommissionrate", "shopadcommissionrate",
		"commissiongmv", "estimatedbasecommission", "estimatedstandardcommission", "commissionbase",
		"standardcommission", "beforesplittalentincome", "agencysplitrate", "talenttotalincome",
		"agencypretaxtotalincome", "agencytotalincome", "invitationid", "createdtime",
		"deliveredtime", "paymentid", "paymentstatus":
		return true
	default:
		return false
	}
}

func orderCompareFields() []orderCompareField {
	return []orderCompareField{
		{label: "商品名称", value: func(o Order) string { return o.ProductName }},
		{label: "商品价格", value: func(o Order) string { return moneyText(o.ProductPrice) }},
		{label: "下单件数", value: func(o Order) string { return strconv.Itoa(o.Quantity) }},
		{label: "订单结算状态", value: func(o Order) string { return o.SettlementStatus }},
		{label: "标准佣金率", value: func(o Order) string { return o.StandardCommissionRate }},
		{label: "店铺广告佣金率", value: func(o Order) string { return o.ShopAdCommissionRate }},
		{label: "佣金 GMV", value: func(o Order) string { return moneyText(o.CommissionGMV) }},
		{label: "预计基础佣金", value: func(o Order) string { return moneyText(o.EstimatedBaseCommission) }},
		{label: "预计标准佣金", value: func(o Order) string { return moneyText(o.EstimatedStandardCommission) }},
		{label: "佣金基数", value: func(o Order) string { return moneyText(o.CommissionBase) }},
		{label: "标准佣金", value: func(o Order) string { return moneyText(o.StandardCommission) }},
		{label: "分成前的达人收益", value: func(o Order) string { return moneyText(o.BeforeSplitTalentIncome) }},
		{label: "代理机构佣金分成比例", value: func(o Order) string { return o.AgencySplitRate }},
		{label: "达人总收益", value: func(o Order) string { return moneyText(o.TalentTotalIncome) }},
		{label: "代理机构税前总收益", value: func(o Order) string { return moneyText(o.AgencyPretaxTotalIncome) }},
		{label: "代理机构总收益", value: func(o Order) string { return moneyText(o.AgencyTotalIncome) }},
		{label: "Invitation ID", value: func(o Order) string { return o.InvitationID }},
		{label: "创建时间", value: func(o Order) string { return o.CreatedAt }},
		{label: "订单送达时间", value: func(o Order) string { return o.DeliveredAt }},
		{label: "付款 ID", value: func(o Order) string { return o.PaymentID }},
		{label: "付款状态", value: func(o Order) string { return o.PaymentStatus }},
	}
}

func changedFieldSummaries(fieldCounts map[string]int) []ChangedFieldSummary {
	var list []ChangedFieldSummary
	for field, count := range fieldCounts {
		list = append(list, ChangedFieldSummary{Field: field, Count: count})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Count == list[j].Count {
			return list[i].Field < list[j].Field
		}
		return list[i].Count > list[j].Count
	})
	return list
}

func (s *Store) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "方法不支持")
		return
	}
	req := parseStatsRequest(r)
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	resp := s.dbStatsLocked(req)
	writeJSON(w, resp)
}

func (s *Store) handleOrders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "方法不支持")
		return
	}
	req := parseStatsRequest(r)
	page := parsePositiveInt(r.URL.Query().Get("page"), 1)
	pageSize := parsePageSize(r.URL.Query().Get("pageSize"))

	resp, err := s.dbOrdersPageLocked(req, page, pageSize)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取订单失败")
		return
	}
	writeJSON(w, resp)
}

func (s *Store) handleDeleteOrders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "方法不支持")
		return
	}
	mode := strings.TrimSpace(r.URL.Query().Get("mode"))
	var deleted int64
	var err error
	switch mode {
	case "all":
		req := parseStatsRequest(r)
		deleted, err = s.dbDeleteCountryOrdersLocked(req.Country)
	case "filter":
		req := parseStatsRequest(r)
		deleted, err = s.dbDeleteOrdersLocked(req)
	default:
		writeError(w, http.StatusBadRequest, "璇烽€夋嫨鍒犻櫎鏂瑰紡")
		return
	}
	if err != nil {
		log.Printf("ERROR 删除订单失败：%v", err)
		writeError(w, http.StatusInternalServerError, "鍒犻櫎璁㈠崟澶辫触")
		return
	}
	s.mu.Lock()
	err = s.SaveLocked()
	s.mu.Unlock()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "保存状态失败")
		return
	}
	writeJSON(w, map[string]any{"deleted": deleted})
}

func (s *Store) handleExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "方法不支持")
		return
	}
	req := parseStatsRequest(r)
	exportType := strings.TrimSpace(r.URL.Query().Get("type"))
	if exportType == "" {
		exportType = "orders"
	}
	filename := "orders.xlsx"
	switch exportType {
	case "summary":
		filename = "summary.xlsx"
	case "orders":
		filename = "orders.xlsx"
	case "both":
		filename = "summary_orders.xlsx"
	default:
		writeError(w, http.StatusBadRequest, "导出类型不支持")
		return
	}

	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	err := s.writeExportXLSXLocked(w, req, exportType)
	if err != nil {
		log.Printf("ERROR 导出失败：%v", err)
	}
}

func (s *Store) handleCompany(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.handleCreateCompany(w, r)
	case http.MethodPut:
		s.handleRenameCompany(w, r)
	case http.MethodDelete:
		s.handleDeleteCompany(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "方法不支持")
	}
}

func (s *Store) handleCreateCompany(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	names := splitNames(body.Name)
	if len(names) == 0 {
		writeError(w, http.StatusBadRequest, "请输入公司名称")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	oldCompanies := append([]Company(nil), s.Companies...)
	oldNextID := s.NextID
	oldUpdatedAt := s.UpdatedAt
	exists := make(map[string]bool, len(s.Companies))
	for _, company := range s.Companies {
		exists[strings.ToLower(strings.TrimSpace(company.Name))] = true
	}
	created := make([]Company, 0, len(names))
	skipped := make([]string, 0)
	for _, name := range names {
		key := strings.ToLower(name)
		if exists[key] {
			skipped = append(skipped, name)
			continue
		}
		company := Company{ID: s.newIDLocked("c"), Name: name}
		s.Companies = append(s.Companies, company)
		created = append(created, company)
		exists[key] = true
	}
	if len(created) > 0 {
		if err := s.SaveLocked(); err != nil {
			s.Companies = oldCompanies
			s.NextID = oldNextID
			s.UpdatedAt = oldUpdatedAt
			log.Printf("ERROR 保存新增公司失败 names=%q err=%v", names, err)
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("保存公司失败：%v", err))
			return
		}
	}
	writeJSON(w, map[string]any{"companies": created, "skipped": skipped})
}

func (s *Store) handleRenameCompany(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Name) == "" {
		writeError(w, http.StatusBadRequest, "请输入公司名称")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.Companies {
		if s.Companies[i].ID == body.ID {
			s.Companies[i].Name = strings.TrimSpace(body.Name)
			if err := s.SaveLocked(); err != nil {
				writeError(w, http.StatusInternalServerError, "保存失败")
				return
			}
			writeJSON(w, s.Companies[i])
			return
		}
	}
	writeError(w, http.StatusBadRequest, "公司不存在")
}

func (s *Store) handleDeleteCompany(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.ID) == "" {
		writeError(w, http.StatusBadRequest, "璇烽€夋嫨鍏徃")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.Companies {
		if s.Companies[i].ID == body.ID {
			s.Companies = append(s.Companies[:i], s.Companies[i+1:]...)
			if err := s.SaveLocked(); err != nil {
				writeError(w, http.StatusInternalServerError, "闁告帞濞€濞呭酣宕楅鈧鍐╁緞鏉堫偉袝")
				return
			}
			writeJSON(w, map[string]bool{"ok": true})
			return
		}
	}
	writeError(w, http.StatusBadRequest, "公司不存在")
}

func (s *Store) handleEmployee(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.handleCreateEmployee(w, r)
	case http.MethodPut:
		s.handleRenameEmployee(w, r)
	case http.MethodDelete:
		s.handleDeleteEmployee(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "方法不支持")
	}
}

func (s *Store) handleCreateEmployee(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CompanyID string `json:"companyId"`
		Name      string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "璇锋眰鏍煎紡閿欒")
		return
	}
	names := splitNames(body.Name)
	if len(names) == 0 {
		writeError(w, http.StatusBadRequest, "请输入员工名称")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.Companies {
		if s.Companies[i].ID == body.CompanyID {
			var created []Employee
			var skipped []string
			exists := map[string]bool{}
			for _, employee := range s.Companies[i].Employees {
				exists[strings.ToLower(strings.TrimSpace(employee.Name))] = true
			}
			for _, name := range names {
				if exists[strings.ToLower(name)] {
					skipped = append(skipped, name)
					continue
				}
				employee := Employee{ID: s.newIDLocked("e"), Name: name}
				s.Companies[i].Employees = append(s.Companies[i].Employees, employee)
				created = append(created, employee)
				exists[strings.ToLower(name)] = true
			}
			if len(created) > 0 {
				oldNextID := s.NextID - int64(len(created))
				oldUpdatedAt := s.UpdatedAt
				oldLen := len(s.Companies[i].Employees) - len(created)
				if err := s.SaveLocked(); err != nil {
					s.Companies[i].Employees = s.Companies[i].Employees[:oldLen]
					s.NextID = oldNextID
					s.UpdatedAt = oldUpdatedAt
					log.Printf("ERROR 保存新增员工失败 company_id=%s names=%q err=%v", body.CompanyID, names, err)
					writeError(w, http.StatusInternalServerError, fmt.Sprintf("保存员工失败：%v", err))
					return
				}
			}
			writeJSON(w, map[string]any{"employees": created, "skipped": skipped})
			return
		}
	}
	writeError(w, http.StatusBadRequest, "公司不存在")
}

func (s *Store) handleRenameEmployee(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CompanyID  string `json:"companyId"`
		EmployeeID string `json:"employeeId"`
		Name       string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Name) == "" {
		writeError(w, http.StatusBadRequest, "请输入员工名称")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for ci := range s.Companies {
		if s.Companies[ci].ID != body.CompanyID {
			continue
		}
		for ei := range s.Companies[ci].Employees {
			if s.Companies[ci].Employees[ei].ID == body.EmployeeID {
				s.Companies[ci].Employees[ei].Name = strings.TrimSpace(body.Name)
				if err := s.SaveLocked(); err != nil {
					writeError(w, http.StatusInternalServerError, "淇濆瓨鍛樺伐澶辫触")
					return
				}
				writeJSON(w, s.Companies[ci].Employees[ei])
				return
			}
		}
	}
	writeError(w, http.StatusBadRequest, "员工不存在")
}

func (s *Store) handleDeleteEmployee(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CompanyID  string `json:"companyId"`
		EmployeeID string `json:"employeeId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.EmployeeID) == "" {
		writeError(w, http.StatusBadRequest, "閻犲洨鍏橀埀顒€顦扮€氥劑宕ㄥΟ杞扮矗")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for ci := range s.Companies {
		if s.Companies[ci].ID != body.CompanyID {
			continue
		}
		for ei := range s.Companies[ci].Employees {
			if s.Companies[ci].Employees[ei].ID == body.EmployeeID {
				s.Companies[ci].Employees = append(s.Companies[ci].Employees[:ei], s.Companies[ci].Employees[ei+1:]...)
				if err := s.SaveLocked(); err != nil {
					writeError(w, http.StatusInternalServerError, "鍒犻櫎鍛樺伐澶辫触")
					return
				}
				writeJSON(w, map[string]bool{"ok": true})
				return
			}
		}
	}
	writeError(w, http.StatusBadRequest, "员工不存在")
}

func (s *Store) handleTalent(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPut {
		s.handleRenameTalent(w, r)
		return
	}
	if r.Method == http.MethodDelete {
		s.handleDeleteTalent(w, r)
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "方法不支持")
		return
	}
	var body struct {
		CompanyID        string `json:"companyId"`
		EmployeeID       string `json:"employeeId"`
		Usernames        string `json:"usernames"`
		ConfirmDuplicate bool   `json:"confirmDuplicate"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "璇锋眰鏍煎紡閿欒")
		return
	}
	names := splitUsernames(body.Usernames)
	if len(names) == 0 {
		writeError(w, http.StatusBadRequest, "请输入账号")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for ci := range s.Companies {
		if s.Companies[ci].ID != body.CompanyID {
			continue
		}
		for ei := range s.Companies[ci].Employees {
			if s.Companies[ci].Employees[ei].ID != body.EmployeeID {
				continue
			}
			duplicates := s.findTalentDuplicatesLocked(names, body.CompanyID, body.EmployeeID)
			duplicateKeys := map[string]bool{}
			for _, item := range duplicates {
				duplicateKeys[strings.ToLower(strings.TrimSpace(item.Username))] = true
			}
			added := 0
			skipped := 0
			for _, name := range names {
				if duplicateKeys[strings.ToLower(name)] && !body.ConfirmDuplicate {
					skipped++
					continue
				}
				s.Companies[ci].Employees[ei].Talents = append(s.Companies[ci].Employees[ei].Talents, name)
				added++
			}
			sort.Strings(s.Companies[ci].Employees[ei].Talents)
			if err := s.SaveLocked(); err != nil {
				writeError(w, http.StatusInternalServerError, "保存账号失败")
				return
			}
			result := TalentCreateResult{
				RequiresConfirm: len(duplicates) > 0 && !body.ConfirmDuplicate,
				Added:           added,
				Skipped:         skipped,
				Duplicates:      duplicates,
				Employee:        s.Companies[ci].Employees[ei],
				Message:         "账号已新增",
			}
			if result.RequiresConfirm {
				result.Message = "发现重复账号，未重复账号已新增"
			}
			writeJSON(w, result)
			return
		}
	}
	writeError(w, http.StatusBadRequest, "员工不存在")
}

func (s *Store) handleRenameTalent(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CompanyID        string `json:"companyId"`
		EmployeeID       string `json:"employeeId"`
		OldName          string `json:"oldName"`
		NewName          string `json:"newName"`
		ConfirmDuplicate bool   `json:"confirmDuplicate"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "璇锋眰鏍煎紡閿欒")
		return
	}
	oldName := normalizeTalentUsername(body.OldName)
	newName := normalizeTalentUsername(body.NewName)
	if oldName == "" || newName == "" {
		writeError(w, http.StatusBadRequest, "请输入账号")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for ci := range s.Companies {
		if s.Companies[ci].ID != body.CompanyID {
			continue
		}
		for ei := range s.Companies[ci].Employees {
			if s.Companies[ci].Employees[ei].ID != body.EmployeeID {
				continue
			}
			talents := s.Companies[ci].Employees[ei].Talents
			target := -1
			duplicateExists := false
			for i, name := range talents {
				if strings.EqualFold(strings.TrimSpace(name), oldName) {
					target = i
				} else if strings.EqualFold(strings.TrimSpace(name), newName) {
					duplicateExists = true
				}
			}
			if target < 0 {
				writeError(w, http.StatusBadRequest, "账号不存在")
				return
			}
			if duplicateExists && !body.ConfirmDuplicate {
				duplicates := s.findTalentDuplicatesLocked([]string{newName}, body.CompanyID, body.EmployeeID)
				writeJSON(w, map[string]any{
					"requiresConfirm": true,
					"duplicates":      duplicates,
					"message":         "账号已重复，是否继续修改？",
				})
				return
			}
			talents[target] = newName
			sort.Strings(talents)
			s.Companies[ci].Employees[ei].Talents = talents
			if err := s.SaveLocked(); err != nil {
				writeError(w, http.StatusInternalServerError, "保存账号失败")
				return
			}
			writeJSON(w, s.Companies[ci].Employees[ei])
			return
		}
	}
	writeError(w, http.StatusBadRequest, "员工不存在")
}

func (s *Store) handleDeleteTalent(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CompanyID  string `json:"companyId"`
		EmployeeID string `json:"employeeId"`
		Name       string `json:"name"`
		Index      *int   `json:"index"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Name) == "" {
		writeError(w, http.StatusBadRequest, "璇烽€夋嫨璐﹀彿")
		return
	}
	name := normalizeTalentUsername(body.Name)
	s.mu.Lock()
	defer s.mu.Unlock()
	for ci := range s.Companies {
		if s.Companies[ci].ID != body.CompanyID {
			continue
		}
		for ei := range s.Companies[ci].Employees {
			if s.Companies[ci].Employees[ei].ID != body.EmployeeID {
				continue
			}
			talents := s.Companies[ci].Employees[ei].Talents
			if body.Index != nil {
				index := *body.Index
				if index < 0 || index >= len(talents) || !strings.EqualFold(strings.TrimSpace(talents[index]), name) {
					writeError(w, http.StatusBadRequest, "璐﹀彿浣嶇疆宸插彉鍖栵紝璇峰埛鏂板悗閲嶈瘯")
					return
				}
				s.Companies[ci].Employees[ei].Talents = append(talents[:index], talents[index+1:]...)
				if err := s.SaveLocked(); err != nil {
					writeError(w, http.StatusInternalServerError, "闁告帞濞€濞呭海鎷归敃鈧ぐ鎸庡緞鏉堫偉袝")
					return
				}
				writeJSON(w, map[string]bool{"ok": true})
				return
			}
			for i, item := range talents {
				if strings.EqualFold(strings.TrimSpace(item), name) {
					s.Companies[ci].Employees[ei].Talents = append(talents[:i], talents[i+1:]...)
					if err := s.SaveLocked(); err != nil {
						writeError(w, http.StatusInternalServerError, "闁告帞濞€濞呭海鎷归敃鈧ぐ鎸庡緞鏉堫偉袝")
						return
					}
					writeJSON(w, map[string]bool{"ok": true})
					return
				}
			}
			writeError(w, http.StatusBadRequest, "账号不存在")
			return
		}
	}
	writeError(w, http.StatusBadRequest, "员工不存在")
}

func parseStatsRequest(r *http.Request) StatsRequest {
	q := r.URL.Query()
	return StatsRequest{
		Country:          canonicalCountry(q.Get("country")),
		Start:            q.Get("start"),
		End:              q.Get("end"),
		SettlementStatus: strings.TrimSpace(q.Get("settlementStatus")),
		Scope:            q.Get("scope"),
		ID:               q.Get("id"),
		Usernames:        splitUsernames(q.Get("usernames")),
		USDRate:          parseOptionalFloat(q.Get("usdRate")),
		RMBRate:          parseOptionalFloat(q.Get("rmbRate")),
		AgencyCutRate:    parseOptionalFloat(q.Get("agencyCutRate")),
		FeePermille:      parseOptionalFloat(q.Get("feePermille")),
	}
}

func parseOptionalFloat(s string) *float64 {
	text := strings.TrimSpace(s)
	if text == "" {
		return nil
	}
	v, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return nil
	}
	return &v
}

func parsePositiveInt(s string, fallback int) int {
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || v < 1 {
		return fallback
	}
	return v
}

func parsePageSize(s string) int {
	v := parsePositiveInt(s, 100)
	switch v {
	case 100, 500, 1000:
		return v
	default:
		return 100
	}
}

func normalizedSettlementStatus(status string) string {
	status = strings.TrimSpace(status)
	if status == "" {
		return "未识别状态"
	}
	switch strings.ToLower(status) {
	case "settled", "settlement completed", "決済済み":
		return "已结算"
	case "pending", "保留中":
		return "待确认"
	case "customer not paid", "customer unpaid", "顧客未払い":
		return "客户未付款"
	case "not eligible", "ineligible", "対象外":
		return "不符合条件"
	case "frozen", "凍結":
		return "已冻结"
	default:
		return status
	}
}

func normalizedPaymentStatus(status string) string {
	status = strings.TrimSpace(status)
	switch strings.ToLower(status) {
	case "paid", "支払い済み":
		return "已付款"
	case "unpaid", "未払い":
		return "未付款"
	case "not payable", "no payment required", "no payment needed", "支払い不要":
		return "无需付款"
	default:
		return status
	}
}

func (s *Store) resolveScopeLocked(req StatsRequest) ([]string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch req.Scope {
	case "manual":
		return req.Usernames, "手动账号筛选"
	case "talent":
		return req.Usernames, "账号筛选"
	case "employee":
		for _, c := range s.Companies {
			for _, e := range c.Employees {
				if e.ID == req.ID {
					return e.Talents, "员工：" + e.Name
				}
			}
		}
	case "company":
		for _, c := range s.Companies {
			if c.ID == req.ID {
				var names []string
				for _, e := range c.Employees {
					names = append(names, e.Talents...)
				}
				return names, "公司：" + c.Name
			}
		}
	}
	return nil, "全部订单"
}

func normalizeHeader(s string) string {
	s = strings.TrimPrefix(strings.TrimSpace(strings.ToLower(s)), "\ufeff")
	replacer := strings.NewReplacer(" ", "", "\t", "", "\n", "", "\r", "", "_", "", "-", "", "：", "", ":", "", "，", "", ",", "", "(", "", ")", "")
	s = replacer.Replace(s)
	s = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r > 127 {
			return r
		}
		return -1
	}, s)
	aliases := map[string]string{
		"订单id": "orderid", "orderid": "orderid",
		"skuid": "skuid",
		"商品id":  "productid", "productid": "productid",
		"商品名称": "productname", "productname": "productname", "商品名": "productname",
		"商品价格": "productprice", "productprice": "productprice", "price": "productprice", "価格": "productprice",
		"下单件数": "quantity", "quantity": "quantity", "数量": "quantity",
		"订单结算状态": "settlementstatus", "结算状态": "settlementstatus", "settlementstatus": "settlementstatus", "ordersettlementstatus": "settlementstatus",
		"达人用户名": "talentusername", "账号": "talentusername", "creatorusername": "talentusername", "talentusername": "talentusername", "クリエイターのユーザー名": "talentusername",
		"标准佣金率": "standardcommissionrate", "standardcommissionrate": "standardcommissionrate", "標準成果報酬率": "standardcommissionrate",
		"店铺广告佣金率": "shopadcommissionrate", "shopadcommissionrate": "shopadcommissionrate", "ショップ広告成果報酬率": "shopadcommissionrate",
		"commissiongmv": "commissiongmv", "佣金gmv": "commissiongmv", "成果報酬gmv": "commissiongmv",
		"预计基础佣金": "estimatedbasecommission", "estimatedbasecommission": "estimatedbasecommission", "estbasecommission": "estimatedbasecommission", "推定ベース成果報酬額": "estimatedbasecommission",
		"预计标准佣金": "estimatedstandardcommission", "estimatedstandardcommission": "estimatedstandardcommission", "eststandardcommission": "estimatedstandardcommission", "推定標準成果報酬": "estimatedstandardcommission",
		"佣金基数": "commissionbase", "commissionbase": "commissionbase", "成果報酬ベース": "commissionbase",
		"标准佣金": "standardcommission", "standardcommission": "standardcommission", "標準成果報酬": "standardcommission",
		"分成前的达人收益": "beforesplittalentincome", "beforesplittalentincome": "beforesplittalentincome", "creatorearningsbeforerevenuesharing": "beforesplittalentincome", "収益分配前のクリエイター収益": "beforesplittalentincome",
		"代理机构佣金分成比例": "agencysplitrate", "agencysplitrate": "agencysplitrate", "portionofagencycommissionsharing": "agencysplitrate", "エージェンシー成果報酬分配の一部": "agencysplitrate",
		"达人总收益": "talenttotalincome", "talenttotalincome": "talenttotalincome", "totalcreatorearnings": "talenttotalincome", "クリエイターの収益総額": "talenttotalincome",
		"代理机构税前总收益": "agencypretaxtotalincome", "agencypretaxtotalincome": "agencypretaxtotalincome", "totalagencyearningsbeforetax": "agencypretaxtotalincome", "課税前のエージェンシーの収益総額": "agencypretaxtotalincome",
		"代理机构总收益": "agencytotalincome", "agencytotalincome": "agencytotalincome", "totalagencyearnings": "agencytotalincome", "エージェンシーの収益総額": "agencytotalincome",
		"commissionsrewards": "commissionsrewards", "佣金与奖励": "commissionsrewards",
		"invitationid": "invitationid",
		"创建时间":         "createdtime", "createdtime": "createdtime", "createtime": "createdtime", "timecreated": "createdtime", "作成日時": "createdtime",
		"订单送达时间": "deliveredtime", "deliveredtime": "deliveredtime", "ordersdeliveredtime": "deliveredtime", "timeorderdelivered": "deliveredtime", "注文配達日時": "deliveredtime",
		"付款id": "paymentid", "paymentid": "paymentid", "支払いid": "paymentid",
		"付款状态": "paymentstatus", "paymentstatus": "paymentstatus", "支払い状況": "paymentstatus",
		"注文id": "orderid", "注文の決済状況": "settlementstatus",
	}
	if v, ok := aliases[s]; ok {
		return v
	}
	return s
}

func settlementStatusFromRaw(raw map[string]string) string {
	for key, value := range raw {
		if normalizeHeader(key) == "settlementstatus" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func value(row []string, index map[string]int, key string) string {
	i, ok := index[key]
	if !ok {
		return ""
	}
	return strings.TrimSpace(cell(row, i))
}

func cell(row []string, i int) string {
	if i < 0 || i >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[i])
}

func isBlankRow(row []string) bool {
	for _, c := range row {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

func parseMoney(s string) float64 {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\u00a0", " "))
	if s == "" || s == "-" || s == "--" {
		return 0
	}
	negative := false
	if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		negative = true
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	var b strings.Builder
	seenDigit := false
	seenDot := false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			seenDigit = true
		case r == '.' && !seenDot:
			b.WriteRune(r)
			seenDot = true
		case r == '-' && !seenDigit:
			negative = true
		case r == '+' && !seenDigit:
			// ignore explicit plus sign
		case r == ',' || unicode.IsSpace(r):
			// ignore thousands separators and whitespace
		}
	}
	numStr := b.String()
	if numStr == "" || numStr == "." {
		return 0
	}
	v, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return 0
	}
	if negative {
		v = -v
	}
	return v
}

func parseInt(s string) int {
	return int(math.Round(parseMoney(s)))
}

func normalizeDate(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil && f > 20000 {
		base := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
		return base.AddDate(0, 0, int(math.Floor(f+1e-9))).Format("2006-01-02")
	}
	layouts := []string{
		"2006-01-02 15:04:05", "2006/01/02 15:04:05", "2006-1-2 15:04:05", "2006/1/2 15:04:05",
		"2006-01-02", "2006/01/02", "2006-1-2", "2006/1/2", "01/02/2006 15:04:05", "01/02/2006",
	}
	if strings.Contains(s, "/") {
		layouts = append([]string{"02/01/2006 15:04:05", "2/1/2006 15:04:05", "02/01/2006", "2/1/2006"}, layouts...)
	}
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t.Format("2006-01-02")
		}
	}
	if len(s) >= 10 {
		candidate := strings.ReplaceAll(s[:10], "/", "-")
		if t, err := time.ParseInLocation("2006-01-02", candidate, time.Local); err == nil {
			return t.Format("2006-01-02")
		}
	}
	return ""
}

func inDateRange(date, start, end string) bool {
	if date == "" {
		return start == "" && end == ""
	}
	if start != "" && date < start {
		return false
	}
	if end != "" && date > end {
		return false
	}
	return true
}

func canonicalCountry(s string) string {
	s = strings.TrimSpace(s)
	if s == "马来" {
		return "马来西亚"
	}
	for _, c := range countries {
		if s == c {
			return c
		}
	}
	return ""
}

func splitUsernames(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == '，' || r == '\n' || r == '\r' || r == '\t' || r == ';' || r == '；'
	})
	seen := map[string]bool{}
	var names []string
	for _, f := range fields {
		name := normalizeTalentUsername(f)
		if name == "" {
			continue
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}

func normalizeTalentUsername(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func (s *Store) normalizeTalentsLocked() bool {
	changed := false
	for ci := range s.Companies {
		for ei := range s.Companies[ci].Employees {
			talents := s.Companies[ci].Employees[ei].Talents
			for i, name := range talents {
				clean := normalizeTalentUsername(name)
				if clean != name {
					talents[i] = clean
					changed = true
				}
			}
			sort.Strings(talents)
			s.Companies[ci].Employees[ei].Talents = talents
		}
	}
	return changed
}

func splitNames(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == '\n' || r == '\r' || r == '\t' || r == ',' || r == '，' || r == ';' || r == '；'
	})
	seen := map[string]bool{}
	var names []string
	for _, f := range fields {
		name := strings.TrimSpace(f)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if !seen[key] {
			seen[key] = true
			names = append(names, name)
		}
	}
	return names
}

func roundCents(v float64) float64 {
	if v == 0 {
		return 0
	}
	rounded := math.Round((v+math.Copysign(1e-9, v))*100) / 100
	if rounded == 0 {
		return 0
	}
	return rounded
}

func moneyText(v float64) string {
	return strconv.FormatFloat(roundCents(v), 'f', 2, 64)
}

func writeJSON(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
