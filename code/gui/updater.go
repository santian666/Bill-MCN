package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// defaultUpdateRepo 可配置为 "owner/repo"（GitHub 仓库）或直接指向 version.json 的 URL
const defaultUpdateRepo = "santian666/Bill-MCN"

type UpdateConfig struct {
	Repo string `json:"repo"`
	URL  string `json:"url"`
}

type UpdateCheckResult struct {
	CurrentVersion string `json:"currentVersion"`
	LatestVersion  string `json:"latestVersion"`
	HasUpdate      bool   `json:"hasUpdate"`
	DownloadURL    string `json:"downloadUrl"`
	ReleaseNotes   string `json:"releaseNotes"`
	PublishedAt    string `json:"publishedAt"`
	Repo           string `json:"repo"`
	URL            string `json:"url"`
	Configured     bool   `json:"configured"`
}

type githubRelease struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	Body        string `json:"body"`
	PublishedAt string `json:"published_at"`
	Assets      []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

type versionManifest struct {
	Version      string `json:"version"`
	DownloadURL  string `json:"downloadUrl"`
	ReleaseNotes string `json:"releaseNotes"`
	PublishedAt  string `json:"publishedAt"`
}

func (s *Store) updateConfigPath() string {
	return filepath.Join(filepath.Dir(s.path), "update.json")
}

func (s *Store) loadUpdateConfig() UpdateConfig {
	cfg := UpdateConfig{Repo: defaultUpdateRepo}
	b, err := os.ReadFile(s.updateConfigPath())
	if err != nil {
		return cfg
	}
	var saved UpdateConfig
	if json.Unmarshal(b, &saved) == nil {
		if strings.TrimSpace(saved.Repo) != "" {
			cfg.Repo = strings.TrimSpace(saved.Repo)
		}
		if strings.TrimSpace(saved.URL) != "" {
			cfg.URL = strings.TrimSpace(saved.URL)
		}
	}
	return cfg
}

func (s *Store) saveUpdateConfig(cfg UpdateConfig) error {
	cfg.Repo = strings.TrimSpace(cfg.Repo)
	cfg.URL = strings.TrimSpace(cfg.URL)
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.updateConfigPath(), b, 0644)
}

func (s *Store) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.loadUpdateConfig())
	case http.MethodPost:
		var body UpdateConfig
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "参数格式错误")
			return
		}
		if err := s.saveUpdateConfig(body); err != nil {
			writeError(w, http.StatusInternalServerError, "保存更新配置失败")
			return
		}
		writeJSON(w, s.loadUpdateConfig())
	default:
		writeError(w, http.StatusMethodNotAllowed, "方法不支持")
	}
}

func (s *Store) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "方法不支持")
		return
	}
	cfg := s.loadUpdateConfig()
	if qRepo := strings.TrimSpace(r.URL.Query().Get("repo")); qRepo != "" {
		cfg.Repo = qRepo
	}
	if qURL := strings.TrimSpace(r.URL.Query().Get("url")); qURL != "" {
		cfg.URL = qURL
	}
	if cfg.Repo == "" && cfg.URL == "" {
		writeJSON(w, UpdateCheckResult{
			CurrentVersion: appVersion,
			Configured:     false,
		})
		return
	}

	var res UpdateCheckResult
	var err error
	if cfg.URL != "" {
		res, err = fetchUpdateManifest(cfg.URL)
	} else {
		res, err = fetchGitHubLatestRelease(cfg.Repo)
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	res.CurrentVersion = appVersion
	res.Repo = cfg.Repo
	res.URL = cfg.URL
	res.Configured = true
	res.HasUpdate = compareSemver(res.LatestVersion, appVersion) > 0 && res.DownloadURL != ""
	writeJSON(w, res)
}

func (s *Store) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "方法不支持")
		return
	}
	var body struct {
		DownloadURL string `json:"downloadUrl"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "参数格式错误")
		return
	}
	downloadURL := strings.TrimSpace(body.DownloadURL)
	if downloadURL == "" {
		cfg := s.loadUpdateConfig()
		if cfg.URL != "" {
			if res, err := fetchUpdateManifest(cfg.URL); err == nil {
				downloadURL = res.DownloadURL
			}
		} else if cfg.Repo != "" {
			if res, err := fetchGitHubLatestRelease(cfg.Repo); err == nil {
				downloadURL = res.DownloadURL
			}
		}
	}
	if downloadURL == "" {
		writeError(w, http.StatusBadRequest, "未提供有效的更新包下载地址")
		return
	}

	exePath, err := os.Executable()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法获取当前程序路径")
		return
	}
	newExePath := exePath + ".new"
	if err := downloadUpdateExecutable(downloadURL, newExePath); err != nil {
		_ = os.Remove(newExePath)
		writeError(w, http.StatusBadGateway, "下载更新失败："+err.Error())
		return
	}

	if err := performAtomicInPlaceUpdate(exePath, newExePath); err != nil {
		_ = os.Remove(newExePath)
		writeError(w, http.StatusInternalServerError, "替换程序失败："+err.Error())
		return
	}

	writeJSON(w, map[string]any{"ok": true, "message": "更新已下载，正在重启新版本..."})
	go func() {
		time.Sleep(250 * time.Millisecond)
		if s.db != nil {
			_ = s.db.Close()
		}
		if err := launchNewAppProcess(exePath); err != nil {
			log.Printf("ERROR 启动新版本失败：%v", err)
		}
		os.Exit(0)
	}()
}

func fetchGitHubLatestRelease(repo string) (UpdateCheckResult, error) {
	repo = strings.TrimSpace(repo)
	repo = strings.TrimPrefix(repo, "https://github.com/")
	repo = strings.TrimPrefix(repo, "http://github.com/")
	repo = strings.TrimSuffix(repo, ".git")
	repo = strings.Trim(repo, "/")
	if !strings.Contains(repo, "/") {
		return UpdateCheckResult{}, errors.New("GitHub 仓库格式应为 owner/repo")
	}

	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return UpdateCheckResult{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "billing-system-updater/"+appVersion)
	resp, err := client.Do(req)
	if err != nil {
		return UpdateCheckResult{}, fmt.Errorf("连接 GitHub 失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return UpdateCheckResult{
			LatestVersion: appVersion,
		}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return UpdateCheckResult{}, fmt.Errorf("检查更新失败（HTTP %d）", resp.StatusCode)
	}
	var rel githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return UpdateCheckResult{}, fmt.Errorf("解析更新信息失败：%w", err)
	}
	downloadURL := ""
	for _, asset := range rel.Assets {
		lower := strings.ToLower(asset.Name)
		if strings.HasSuffix(lower, ".zip") && (strings.Contains(lower, "windows") || strings.Contains(lower, "amd64") || strings.Contains(lower, "bill")) {
			downloadURL = asset.BrowserDownloadURL
			break
		}
	}
	if downloadURL == "" {
		for _, asset := range rel.Assets {
			lower := strings.ToLower(asset.Name)
			if strings.HasSuffix(lower, ".zip") || strings.HasSuffix(lower, ".exe") {
				downloadURL = asset.BrowserDownloadURL
				break
			}
		}
	}
	return UpdateCheckResult{
		LatestVersion: strings.TrimPrefix(strings.TrimSpace(rel.TagName), "v"),
		DownloadURL:   downloadURL,
		ReleaseNotes:  rel.Body,
		PublishedAt:   rel.PublishedAt,
	}, nil
}

func fetchUpdateManifest(manifestURL string) (UpdateCheckResult, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(manifestURL)
	if err != nil {
		return UpdateCheckResult{}, fmt.Errorf("获取更新清单失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return UpdateCheckResult{}, fmt.Errorf("获取更新清单失败（HTTP %d）", resp.StatusCode)
	}
	var m versionManifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return UpdateCheckResult{}, fmt.Errorf("解析更新清单失败：%w", err)
	}
	return UpdateCheckResult{
		LatestVersion: strings.TrimPrefix(strings.TrimSpace(m.Version), "v"),
		DownloadURL:   strings.TrimSpace(m.DownloadURL),
		ReleaseNotes:  m.ReleaseNotes,
		PublishedAt:   m.PublishedAt,
	}, nil
}

func downloadUpdateExecutable(downloadURL, destPath string) error {
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(downloadURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	tmpPath := destPath + ".dl"
	f, err := os.Create(tmpPath)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(f, resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		return closeErr
	}

	if strings.HasSuffix(strings.ToLower(downloadURL), ".zip") {
		if err := extractExeFromZip(tmpPath, destPath); err != nil {
			_ = os.Remove(tmpPath)
			return err
		}
		_ = os.Remove(tmpPath)
		return validateWindowsExe(destPath)
	}

	if err := validateWindowsExe(tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, destPath)
}

func extractExeFromZip(zipPath, destExePath string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, file := range zr.File {
		if file.FileInfo().IsDir() {
			continue
		}
		if strings.HasSuffix(strings.ToLower(file.Name), ".exe") {
			rc, err := file.Open()
			if err != nil {
				return err
			}
			out, err := os.Create(destExePath)
			if err != nil {
				_ = rc.Close()
				return err
			}
			_, copyErr := io.Copy(out, rc)
			_ = rc.Close()
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			return closeErr
		}
	}
	return errors.New("压缩包中未找到 .exe 可执行文件")
}

func validateWindowsExe(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() < 1024*1024 {
		return errors.New("下载的更新文件异常偏小，已中止覆盖")
	}
	var magic [2]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return err
	}
	if !bytes.Equal(magic[:], []byte{'M', 'Z'}) {
		return errors.New("下载的文件不是有效的 Windows 可执行程序")
	}
	return nil
}

// performAtomicInPlaceUpdate 参照 SanTian-Singbox：利用 NTFS 允许重命名运行中 exe 的特性原地替换主程序。
func performAtomicInPlaceUpdate(exePath, newExePath string) error {
	oldExe := exePath + ".old"
	_ = os.Remove(oldExe)

	var renameErr error
	for i := 0; i < 5; i++ {
		renameErr = os.Rename(exePath, oldExe)
		if renameErr == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if renameErr != nil {
		return fmt.Errorf("重命名旧主程序失败（可能被安全软件占用）：%w", renameErr)
	}

	if err := copyFile(newExePath, exePath); err != nil {
		_ = os.Rename(oldExe, exePath)
		return fmt.Errorf("写入新主程序失败：%w", err)
	}
	_ = os.Remove(newExePath)
	return nil
}

func copyFile(srcPath, dstPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0755)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(dst, src)
	closeErr := dst.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func launchNewAppProcess(exePath string) error {
	cmd := exec.Command(exePath)
	cmd.Dir = filepath.Dir(exePath)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
	return cmd.Start()
}

func cleanupOldUpdateArtifacts() {
	exePath, err := os.Executable()
	if err != nil || exePath == "" {
		return
	}
	_ = os.Remove(exePath + ".old")
	_ = os.Remove(exePath + ".new")
	_ = os.Remove(exePath + ".new.dl")
}

func compareSemver(a, b string) int {
	pa := parseSemverParts(a)
	pb := parseSemverParts(b)
	for i := 0; i < 3; i++ {
		if pa[i] > pb[i] {
			return 1
		}
		if pa[i] < pb[i] {
			return -1
		}
	}
	return 0
}

func parseSemverParts(v string) [3]int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if idx := strings.IndexAny(v, "-+"); idx >= 0 {
		v = v[:idx]
	}
	parts := strings.Split(v, ".")
	var out [3]int
	for i := 0; i < 3 && i < len(parts); i++ {
		n, err := strconv.Atoi(strings.TrimSpace(parts[i]))
		if err == nil {
			out[i] = n
		}
	}
	return out
}
