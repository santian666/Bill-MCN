package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateCompanyAndEmployeeBatch(t *testing.T) {
	store := &Store{path: filepath.Join(t.TempDir(), "state.json")}

	companyResult := postJSON(t, store.handleCreateCompany, map[string]string{"name": "甲公司\n乙公司\n甲公司"})
	if len(store.Companies) != 2 {
		t.Fatalf("公司数量=%d，期望=2，响应=%v", len(store.Companies), companyResult)
	}

	companyID := store.Companies[0].ID
	employeeResult := postJSON(t, store.handleCreateEmployee, map[string]string{
		"companyId": companyID,
		"name":      "员工甲\n员工乙\n员工甲",
	})
	if len(store.Companies[0].Employees) != 2 {
		t.Fatalf("员工数量=%d，期望=2，响应=%v", len(store.Companies[0].Employees), employeeResult)
	}

	repeatResult := postJSON(t, store.handleCreateEmployee, map[string]string{
		"companyId": companyID,
		"name":      "员工甲",
	})
	if len(repeatResult["skipped"].([]any)) != 1 {
		t.Fatalf("重名员工未明确返回，响应=%v", repeatResult)
	}

	var saved Store
	b, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatalf("读取状态文件失败：%v", err)
	}
	if err := json.Unmarshal(b, &saved); err != nil {
		t.Fatalf("状态文件不是有效 JSON：%v", err)
	}
}

func postJSON(t *testing.T, handler http.HandlerFunc, body any) map[string]any {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(b))
	recorder := httptest.NewRecorder()
	handler(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("接口状态码=%d，响应=%s", recorder.Code, recorder.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析接口响应失败：%v", err)
	}
	return result
}
