package browser

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// TestRuntimePreparation 验证离线释放、并发复用、沙箱权限与定向清理
func TestRuntimePreparation(t *testing.T) {
	directory := t.TempDir()
	settings := filepath.Join(directory, "settings.json")
	if err := os.WriteFile(settings, []byte(`{"theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(directory, "Runtime", "Microsoft.WebView2.FixedVersionRuntime.1.0.0.0.x64")
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	if Ready(directory) {
		t.Fatal("运行库释放前已经就绪")
	}
	var group sync.WaitGroup
	paths := make([]string, 2)
	errors := make([]error, 2)
	started := time.Now()
	for index := range paths {
		group.Add(1)
		go func() {
			defer group.Done()
			paths[index], errors[index] = Prepare(directory)
		}()
	}
	group.Wait()
	for _, err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if paths[0] != paths[1] || !Ready(directory) {
		t.Fatal("并发运行库准备结果不一致")
	}
	link := filepath.Join(t.TempDir(), "userdata")
	if output, err := exec.Command("cmd.exe", "/c", "mklink", "/J", link, directory).CombinedOutput(); err != nil {
		t.Fatalf("创建用户目录联接: %v %s", err, output)
	}
	if linked, err := Prepare(link); err != nil || linked != paths[0] || !Ready(link) {
		t.Fatalf("用户目录重定向: %s %v", linked, err)
	}
	t.Logf("首次离线释放与双实例准备: %s", time.Since(started))
	for _, name := range []string{old, filepath.Join(directory, "Runtime", ".extracting"), filepath.Join(directory, "Runtime", ".runtime.cab")} {
		if _, err := os.Stat(name); !os.IsNotExist(err) {
			t.Fatalf("旧运行库或临时文件仍然存在: %s", name)
		}
	}
	if contents, err := os.ReadFile(settings); err != nil || string(contents) != `{"theme":"dark"}` {
		t.Fatal("运行库释放影响了应用数据")
	}
	for _, name := range []string{paths[0], filepath.Join(paths[0], "msedgewebview2.exe")} {
		descriptor, err := windows.GetNamedSecurityInfo(name, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		permissions := descriptor.String()
		if !strings.Contains(permissions, ";;;AC)") || !strings.Contains(permissions, ";;;S-1-15-2-2)") {
			t.Fatalf("运行库 AppContainer 读取权限缺失: %s", permissions)
		}
	}
	info, err := os.Stat(filepath.Join(paths[0], "msedgewebview2.exe"))
	if err != nil {
		t.Fatal(err)
	}
	started = time.Now()
	path, err := Prepare(directory)
	if err != nil || path != paths[0] {
		t.Fatalf("热启动复用失败: %v", err)
	}
	t.Logf("已释放运行库复用: %s", time.Since(started))
	current, err := os.Stat(filepath.Join(path, "msedgewebview2.exe"))
	if err != nil || !info.ModTime().Equal(current.ModTime()) {
		t.Fatal("热启动重新释放了运行库")
	}
	if removeRuntimePath(filepath.Join(directory, "Runtime"), directory) == nil {
		t.Fatal("运行库清理允许访问应用数据根目录")
	}
	if err = os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	lockedFile := filepath.Join(old, "running.dll")
	otherFile := filepath.Join(old, "resources.pak")
	for _, name := range []string{lockedFile, otherFile} {
		if err = os.WriteFile(name, []byte("retained"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	name, err := windows.UTF16PtrFromString(lockedFile)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Prepare(directory); err != nil {
		windows.CloseHandle(handle)
		t.Fatalf("正在退出的旧版阻止新运行库启动: %v", err)
	}
	for _, name := range []string{lockedFile, otherFile} {
		if _, err = os.Stat(name); err != nil {
			windows.CloseHandle(handle)
			t.Fatalf("正在使用的旧目录被部分清理: %v", err)
		}
	}
	windows.CloseHandle(handle)
	if _, err = Prepare(directory); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("热启动未清理已经释放的旧版")
	}
	architecture := map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
	library := filepath.Join(path, "EBWebView", architecture, "EmbeddedBrowserWebView.dll")
	if err = os.Rename(library, library+".missing"); err != nil {
		t.Fatal(err)
	}
	if Ready(directory) {
		t.Fatal("缺少当前架构浏览器 DLL 仍然被判定就绪")
	}
}
