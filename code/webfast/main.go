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
	"net"
	"net/http"
	"os"
	"os/exec"
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
var settlementStatuses = []string{"客户未付款", "待确认", "已结算", "不符合条件", "已冻结"}

type Store struct {
	mu        sync.Mutex
	idMu      sync.Mutex
	importMu  sync.Mutex
	path      string
	dbPath    string
	db        *sql.DB
	Orders    []Order   `json:"orders,omitempty"`
	Companies []Company `json:"companies"`
	NextID    int64     `json:"nextId"`
	UpdatedAt time.Time `json:"updatedAt"`
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

type StatsRequest struct {
	Country          string
	Start            string
	End              string
	SettlementStatus string
	Scope            string
	ID               string
	Usernames        []string
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
	DuplicateCount          int                   `json:"duplicateCount"`
	ChangedDuplicateCount   int                   `json:"changedDuplicateCount"`
	UnchangedDuplicateCount int                   `json:"unchangedDuplicateCount"`
	ChangedFields           []ChangedFieldSummary `json:"changedFields"`
	Samples                 []DuplicateChange     `json:"samples"`
	Message                 string                `json:"message"`
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

func main() {
	dataRoot := findDataRoot()
	dataPath := filepath.Join(dataRoot, "data", "web", "state.json")
	if err := os.MkdirAll(filepath.Dir(dataPath), 0755); err != nil {
		log.Fatal(err)
	}

	store := &Store{path: dataPath, dbPath: filepath.Join(dataRoot, "data", "web", "billing.db"), NextID: 1}
	if err := store.Load(); err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", serveIndex())
	mux.HandleFunc("/orders", serveOrders())
	staticFiles, err := fs.Sub(embeddedFiles, "static")
	if err != nil {
		log.Fatal(err)
	}
	mux.HandleFunc("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFiles))).ServeHTTP)
	mux.HandleFunc("/api/state", store.handleState)
	mux.HandleFunc("/api/import", store.handleImport)
	mux.HandleFunc("/api/stats", store.handleStats)
	mux.HandleFunc("/api/orders", store.handleOrders)
	mux.HandleFunc("/api/orders/delete", store.handleDeleteOrders)
	mux.HandleFunc("/api/export", store.handleExport)
	mux.HandleFunc("/api/company", store.handleCompany)
	mux.HandleFunc("/api/employee", store.handleEmployee)
	mux.HandleFunc("/api/talent", store.handleTalent)

	listener, url, err := listenLocal()
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("INFO 数据目录：%s", filepath.Join(dataRoot, "data", "web"))
	log.Printf("INFO 账单系统启动：%s", url)
	openBrowser(url)
	log.Fatal(http.Serve(listener, logRequest(mux)))
}

func findDataRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	if filepath.Base(wd) == "webfast" {
		return filepath.Clean(filepath.Join(wd, "..", ".."))
	}
	exe, err := os.Executable()
	if err != nil {
		return wd
	}
	return filepath.Dir(exe)
}

func serveIndex() http.HandlerFunc {
	tpl := template.Must(template.ParseFS(embeddedFiles, "templates/index.html"))
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		_ = tpl.Execute(w, map[string]any{"Title": "账单系统"})
	}
}

func serveOrders() http.HandlerFunc {
	tpl := template.Must(template.ParseFS(embeddedFiles, "templates/orders.html"))
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/orders" {
			http.NotFound(w, r)
			return
		}
		_ = tpl.Execute(w, map[string]any{"Title": "订单明细"})
	}
}

func listenLocal() (net.Listener, string, error) {
	port := strings.TrimSpace(os.Getenv("PORT"))
	if port != "" {
		addr := "127.0.0.1:" + port
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return nil, "", err
		}
		return ln, "http://" + addr, nil
	}
	for p := 8080; p <= 8099; p++ {
		addr := "127.0.0.1:" + strconv.Itoa(p)
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			return ln, "http://" + addr, nil
		}
	}
	return nil, "", errors.New("8080-8099 端口都被占用，无法启动")
}

func openBrowser(url string) {
	if strings.TrimSpace(os.Getenv("BILLING_NO_BROWSER")) == "1" {
		return
	}
	go func() {
		time.Sleep(400 * time.Millisecond)
		_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	}()
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
	if err := s.openOrdersDBLocked(); err != nil {
		return err
	}
	if len(s.Orders) > 0 {
		if err := s.migrateJSONOrdersLocked(); err != nil {
			return err
		}
		return s.SaveLocked()
	}
	return nil
}

func (s *Store) SaveLocked() error {
	s.UpdatedAt = time.Now()
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, b, 0644)
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
	globalRange, countryRanges := s.dbDateRangesLocked()
	orderCount := s.orderCountLocked()
	s.mu.Lock()
	companies := s.Companies
	updatedAt := s.UpdatedAt
	defer s.mu.Unlock()
	writeJSON(w, map[string]any{
		"countries":          countries,
		"settlementStatuses": settlementStatuses,
		"companies":          companies,
		"orderCount":         orderCount,
		"updatedAt":          updatedAt,
		"dateRange":          globalRange,
		"countryDateRanges":  countryRanges,
	})
}

func (s *Store) handleImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "方法不支持")
		return
	}
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "上传内容过大或格式错误")
		return
	}
	country := canonicalCountry(r.FormValue("country"))
	if country == "" {
		writeError(w, http.StatusBadRequest, "请选择国家")
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
	if _, err := io.Copy(tmp, file); err != nil {
		_ = tmp.Close()
		writeError(w, http.StatusBadRequest, "保存上传文件失败")
		return
	}
	_ = tmp.Close()

	confirmed := r.FormValue("confirmUpdate") == "1"
	s.importMu.Lock()
	defer s.importMu.Unlock()
	result, err := s.previewImportFileLocked(tmpPath, header.Filename, country)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if result.DuplicateCount > 0 && !confirmed {
		result.RequiresConfirm = true
		result.Message = fmt.Sprintf("发现 %d 条重复订单，其中 %d 条有字段变化。请确认是否用新数据覆盖旧数据。", result.DuplicateCount, result.ChangedDuplicateCount)
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
		writeError(w, http.StatusInternalServerError, "保存账单失败")
		return
	}
	result.Message = fmt.Sprintf("导入完成：新增 %d 条，更新 %d 条。", result.Imported, result.Updated)
	writeJSON(w, result)
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
		writeError(w, http.StatusBadRequest, "请选择删除方式")
		return
	}
	if err != nil {
		log.Printf("ERROR 删除订单失败：%v", err)
		writeError(w, http.StatusInternalServerError, "删除订单失败")
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
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Name) == "" {
		writeError(w, http.StatusBadRequest, "请输入公司名称")
		return
	}
	s.mu.Lock()
	company := Company{ID: s.newIDLocked("c"), Name: strings.TrimSpace(body.Name)}
	s.Companies = append(s.Companies, company)
	err := s.SaveLocked()
	s.mu.Unlock()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "保存公司失败")
		return
	}
	writeJSON(w, company)
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
				writeError(w, http.StatusInternalServerError, "保存公司失败")
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
		writeError(w, http.StatusBadRequest, "请选择公司")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.Companies {
		if s.Companies[i].ID == body.ID {
			s.Companies = append(s.Companies[:i], s.Companies[i+1:]...)
			if err := s.SaveLocked(); err != nil {
				writeError(w, http.StatusInternalServerError, "删除公司失败")
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
		writeError(w, http.StatusBadRequest, "请求格式错误")
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
			exists := map[string]bool{}
			for _, employee := range s.Companies[i].Employees {
				exists[strings.ToLower(strings.TrimSpace(employee.Name))] = true
			}
			for _, name := range names {
				if exists[strings.ToLower(name)] {
					continue
				}
				employee := Employee{ID: s.newIDLocked("e"), Name: name}
				s.Companies[i].Employees = append(s.Companies[i].Employees, employee)
				created = append(created, employee)
				exists[strings.ToLower(name)] = true
			}
			if err := s.SaveLocked(); err != nil {
				writeError(w, http.StatusInternalServerError, "保存员工失败")
				return
			}
			writeJSON(w, map[string]any{"employees": created})
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
					writeError(w, http.StatusInternalServerError, "保存员工失败")
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
		writeError(w, http.StatusBadRequest, "请选择员工")
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
					writeError(w, http.StatusInternalServerError, "删除员工失败")
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
		CompanyID  string `json:"companyId"`
		EmployeeID string `json:"employeeId"`
		Usernames  string `json:"usernames"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
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
			exists := map[string]bool{}
			for _, name := range s.Companies[ci].Employees[ei].Talents {
				exists[strings.ToLower(name)] = true
			}
			for _, name := range names {
				if !exists[strings.ToLower(name)] {
					s.Companies[ci].Employees[ei].Talents = append(s.Companies[ci].Employees[ei].Talents, name)
				}
			}
			sort.Strings(s.Companies[ci].Employees[ei].Talents)
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

func (s *Store) handleRenameTalent(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CompanyID  string `json:"companyId"`
		EmployeeID string `json:"employeeId"`
		OldName    string `json:"oldName"`
		NewName    string `json:"newName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	oldName := strings.TrimSpace(body.OldName)
	newName := strings.TrimSpace(body.NewName)
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
			for i, name := range talents {
				if strings.EqualFold(strings.TrimSpace(name), oldName) {
					target = i
				} else if strings.EqualFold(strings.TrimSpace(name), newName) {
					writeError(w, http.StatusBadRequest, "账号已存在")
					return
				}
			}
			if target < 0 {
				writeError(w, http.StatusBadRequest, "账号不存在")
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
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Name) == "" {
		writeError(w, http.StatusBadRequest, "请选择账号")
		return
	}
	name := strings.TrimSpace(body.Name)
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
			for i, item := range talents {
				if strings.EqualFold(strings.TrimSpace(item), name) {
					s.Companies[ci].Employees[ei].Talents = append(talents[:i], talents[i+1:]...)
					if err := s.SaveLocked(); err != nil {
						writeError(w, http.StatusInternalServerError, "删除账号失败")
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
	}
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
	return status
}

func (s *Store) resolveScopeLocked(req StatsRequest) ([]string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch req.Scope {
	case "manual":
		return req.Usernames, "手动账号筛选"
	case "talent":
		return req.Usernames, "账号收益"
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
	s = strings.TrimSpace(strings.ToLower(s))
	replacer := strings.NewReplacer(" ", "", "\t", "", "\n", "", "\r", "", "_", "", "-", "", "：", "", ":", "", "（", "", "）", "", "(", "", ")", "")
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
		"商品名称": "productname", "productname": "productname",
		"商品价格": "productprice", "productprice": "productprice",
		"下单件数": "quantity", "quantity": "quantity",
		"订单结算状态": "settlementstatus", "结算状态": "settlementstatus", "settlementstatus": "settlementstatus", "ordersettlementstatus": "settlementstatus",
		"达人用户名": "talentusername", "creatorusername": "talentusername", "talentusername": "talentusername",
		"标准佣金率": "standardcommissionrate", "standardcommissionrate": "standardcommissionrate",
		"店铺广告佣金率": "shopadcommissionrate", "shopadcommissionrate": "shopadcommissionrate",
		"佣金gmv": "commissiongmv", "commissiongmv": "commissiongmv",
		"预计基础佣金": "estimatedbasecommission", "estimatedbasecommission": "estimatedbasecommission",
		"预计标准佣金": "estimatedstandardcommission", "estimatedstandardcommission": "estimatedstandardcommission",
		"佣金基数": "commissionbase", "commissionbase": "commissionbase",
		"标准佣金": "standardcommission", "standardcommission": "standardcommission",
		"分成前的达人收益": "beforesplittalentincome", "beforesplittalentincome": "beforesplittalentincome",
		"代理机构佣金分成比例": "agencysplitrate", "agencysplitrate": "agencysplitrate",
		"达人总收益": "talenttotalincome", "talenttotalincome": "talenttotalincome",
		"代理机构税前总收益": "agencypretaxtotalincome", "agencypretaxtotalincome": "agencypretaxtotalincome",
		"代理机构总收益": "agencytotalincome", "agencytotalincome": "agencytotalincome",
		"invitationid": "invitationid",
		"创建时间":         "createdtime", "createdtime": "createdtime", "createtime": "createdtime",
		"订单送达时间": "deliveredtime", "deliveredtime": "deliveredtime", "ordersdeliveredtime": "deliveredtime",
		"付款id": "paymentid", "paymentid": "paymentid",
		"付款状态": "paymentstatus", "paymentstatus": "paymentstatus",
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
	s = strings.TrimSpace(s)
	if s == "" || s == "-" {
		return 0
	}
	s = strings.ReplaceAll(s, ",", "")
	s = strings.ReplaceAll(s, "￥", "")
	s = strings.ReplaceAll(s, "$", "")
	s = strings.ReplaceAll(s, "¥", "")
	s = strings.ReplaceAll(s, "SGD", "")
	s = strings.ReplaceAll(s, "THB", "")
	s = strings.ReplaceAll(s, "MYR", "")
	s = strings.TrimSpace(s)
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

func parseInt(s string) int {
	f := parseMoney(s)
	return int(f)
}

func normalizeDate(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil && f > 20000 {
		base := time.Date(1899, 12, 30, 0, 0, 0, 0, time.Local)
		return base.Add(time.Duration(f*24) * time.Hour).Format("2006-01-02")
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

func moneyText(v float64) string {
	return strconv.FormatFloat(v, 'f', 2, 64)
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
