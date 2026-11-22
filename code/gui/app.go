package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App struct
type App struct {
	ctx   context.Context
	store *Store
}

// NewApp creates a new App application struct
func NewApp() *App {
	return &App{}
}

func (a *App) setStore(store *Store) {
	a.store = store
}

// startup is called when the app starts. The context is saved
// so we can call the runtime methods
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

// Greet returns a greeting for the given name
func (a *App) Greet(name string) string {
	return fmt.Sprintf("Hello %s, It's show time!", name)
}

func (a *App) SelectBillFile() (string, error) {
	if a.ctx == nil {
		return "", errors.New("窗口尚未初始化")
	}
	return runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择账单文件",
		Filters: []runtime.FileFilter{
			{DisplayName: "账单文件 (*.xlsx;*.csv;*.tsv;*.txt)", Pattern: "*.xlsx;*.csv;*.tsv;*.txt"},
			{DisplayName: "CSV 账单文件 (*.csv)", Pattern: "*.csv"},
			{DisplayName: "Excel 账单文件 (*.xlsx)", Pattern: "*.xlsx"},
			{DisplayName: "所有文件 (*.*)", Pattern: "*.*"},
		},
	})
}

func (a *App) ImportBillFile(path, country string, confirmUpdate bool) (ImportResult, error) {
	if a.store == nil {
		return ImportResult{}, errors.New("数据服务尚未初始化")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return ImportResult{}, errors.New("请选择账单文件")
	}
	country = canonicalCountry(country)
	if country == "" {
		return ImportResult{}, errors.New("请选择国家")
	}
	info, err := os.Stat(path)
	if err != nil {
		return ImportResult{}, fmt.Errorf("读取账单文件失败：%w", err)
	}
	if info.IsDir() {
		return ImportResult{}, errors.New("请选择账单文件，不要选择文件夹")
	}
	filename := filepath.Base(path)
	diag := importFileDiagnostic(path, filename, info.Size())
	log.Printf("INFO 桌面导入请求 country=%s file=%s size=%d confirm=%v", country, path, info.Size(), confirmUpdate)

	a.store.importMu.Lock()
	defer a.store.importMu.Unlock()
	a.store.resetImportCancel()
	a.store.setImportProgress(ImportProgress{
		Active:   true,
		Phase:    "读取账单",
		Country:  country,
		Filename: filename,
		Message:  "正在读取账单文件，请稍候",
	})
	if !confirmUpdate && a.store.countryOrderCountLocked(country) == 0 {
		result, err := a.store.applyImportFileLocked(path, filename, country, ImportResult{})
		if err == nil {
			a.store.mu.Lock()
			err = a.store.SaveLocked()
			a.store.mu.Unlock()
		}
		if err != nil {
			log.Printf("ERROR 桌面导入失败 country=%s file=%s err=%v", country, path, err)
			return ImportResult{}, fmt.Errorf("保存账单失败：%w\n%s", err, diag)
		}
		if result.Message == "" {
			result.Message = importDoneMessage(result)
		}
		log.Printf("INFO 桌面导入完成 country=%s file=%s imported=%d updated=%d duplicates=%d", country, path, result.Imported, result.Updated, result.DuplicateCount)
		return result, nil
	}
	if confirmUpdate {
		result, err := a.store.replaceImportFileLocked(path, filename, country)
		if err == nil {
			a.store.mu.Lock()
			err = a.store.SaveLocked()
			a.store.mu.Unlock()
		}
		if err != nil {
			log.Printf("ERROR 桌面覆盖导入失败 country=%s file=%s err=%v", country, path, err)
			return ImportResult{}, fmt.Errorf("保存账单失败：%w\n%s", err, diag)
		}
		if result.Message == "" {
			result.Message = importDoneMessage(result)
		}
		log.Printf("INFO 桌面覆盖导入完成 country=%s file=%s imported=%d updated=%d duplicates=%d", country, path, result.Imported, result.Updated, result.DuplicateCount)
		return result, nil
	}
	result, err := a.store.previewImportFileLocked(path, filename, country)
	if err != nil {
		return ImportResult{}, fmt.Errorf("%w\n%s", err, diag)
	}
	if result.DuplicateCount > 0 {
		result.RequiresConfirm = true
		result.Message = importDuplicateMessage(result)
		log.Printf("INFO 桌面导入发现重复 country=%s file=%s duplicates=%d", country, path, result.DuplicateCount)
		return result, nil
	}
	result, err = a.store.applyImportFileLocked(path, filename, country, result)
	if err == nil {
		a.store.mu.Lock()
		err = a.store.SaveLocked()
		a.store.mu.Unlock()
	}
	if err != nil {
		log.Printf("ERROR 桌面导入失败 country=%s file=%s err=%v", country, path, err)
		return ImportResult{}, fmt.Errorf("保存账单失败：%w\n%s", err, diag)
	}
	if result.Message == "" {
		result.Message = importDoneMessage(result)
	}
	log.Printf("INFO 桌面导入完成 country=%s file=%s imported=%d updated=%d duplicates=%d", country, path, result.Imported, result.Updated, result.DuplicateCount)
	return result, nil
}

func (a *App) ImportProgress() ImportProgress {
	if a.store == nil {
		return ImportProgress{}
	}
	return a.store.currentImportProgress()
}

func (a *App) CancelImport() {
	if a.store == nil {
		return
	}
	a.store.requestImportCancel()
}
