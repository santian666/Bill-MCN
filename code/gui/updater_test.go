package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCompareSemver(t *testing.T) {
	if compareSemver("0.1.7", "0.1.6") <= 0 {
		t.Fatal("0.1.7 should be > 0.1.6")
	}
	if compareSemver("v1.0.0", "0.9.9") <= 0 {
		t.Fatal("v1.0.0 should be > 0.9.9")
	}
	if compareSemver("0.1.6", "0.1.6") != 0 {
		t.Fatal("0.1.6 should equal 0.1.6")
	}
	if compareSemver("0.1.5", "0.1.6") >= 0 {
		t.Fatal("0.1.5 should be < 0.1.6")
	}
}

func TestFetchUpdateManifest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(versionManifest{
			Version:      "v0.2.0",
			DownloadURL:  "https://example.com/app.exe",
			ReleaseNotes: "修复马来表头与提速",
		})
	}))
	defer srv.Close()

	res, err := fetchUpdateManifest(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if res.LatestVersion != "0.2.0" || res.DownloadURL != "https://example.com/app.exe" {
		t.Fatalf("unexpected manifest result: %+v", res)
	}
}

func TestPerformAtomicInPlaceUpdate(t *testing.T) {
	dir := t.TempDir()
	curExe := filepath.Join(dir, "app.exe")
	newExe := filepath.Join(dir, "app.exe.new")
	if err := os.WriteFile(curExe, []byte("old-binary"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newExe, []byte("new-binary"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := performAtomicInPlaceUpdate(curExe, newExe); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(curExe)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new-binary" {
		t.Fatalf("expected new-binary, got %q", string(got))
	}
	oldBytes, err := os.ReadFile(curExe + ".old")
	if err != nil || string(oldBytes) != "old-binary" {
		t.Fatalf("expected .old backup with old-binary, err=%v got=%q", err, string(oldBytes))
	}
}
