package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestWindowsUpdate 验证正式版本选择与当前目录内的下载替换
func TestWindowsUpdate(t *testing.T) {
	for _, sample := range []struct {
		candidate string
		current   string
		newer     bool
	}{
		{"v0.1.0", "0.1.0", false},
		{"v0.10.0", "0.2.0", true},
		{"v0.2.0", "0.10.0", false},
		{"v1.0.0", "0.9.9", true},
	} {
		newer, err := newerVersion(sample.candidate, sample.current)
		if err != nil || newer != sample.newer {
			t.Fatalf("版本比较 %s / %s: %v, %v", sample.candidate, sample.current, newer, err)
		}
	}
	var release githubRelease
	release.Tag = "v0.2.0"
	release.Assets = append(release.Assets, struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	}{"CCBar.exe", "https://github.com/Mag1cFall/cc-bar/releases/download/v0.2.0/CCBar.exe", 1024})
	selected, _, err := selectUpdate(release, "0.1.0")
	if err != nil || selected.Status != "available" || selected.LatestVersion != "0.2.0" {
		t.Fatalf("正式 Windows 版本选择: %+v, %v", selected, err)
	}
	release.Assets[0].URL = "https://github.com/nanvon/cc-bar/releases/download/v0.2.0/CCBar.exe"
	if _, _, err = selectUpdate(release, "0.1.0"); err == nil {
		t.Fatal("错误仓库的更新文件被选中")
	}
	release.Assets[0].Name = "CCBar.dmg"
	selected, _, err = selectUpdate(release, "0.1.0")
	if err != nil || selected.Status != "unpublished" {
		t.Fatalf("尚未发布 Windows 产物: %+v, %v", selected, err)
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	image, err := os.Stat(executable)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.ServeFile(writer, request, executable)
	}))
	defer server.Close()
	parent := t.TempDir()
	target := filepath.Join(parent, "CCBar.exe")
	if err = os.WriteFile(target, []byte("old version"), 0700); err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(parent, updateDirectoryPrefix)
	if err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(directory, "CCBar.exe")
	if err = downloadUpdate(context.Background(), server.URL, staged, image.Size()); err != nil {
		t.Fatal(err)
	}
	if err = replaceUpdateExecutable(staged, target); err != nil {
		t.Fatal(err)
	}
	replaced, err := os.Stat(target)
	if err != nil || replaced.Size() != image.Size() {
		t.Fatalf("更新文件替换: %v, %v", replaced, err)
	}
	if err = validateUpdateDirectory(directory, executable); err == nil {
		t.Fatal("更新操作接受了目录之外的程序")
	}
	if err = removeUpdateDirectory(directory, target); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("临时目录清理: %v", err)
	}
}
