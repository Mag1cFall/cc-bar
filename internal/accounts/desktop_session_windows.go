package accounts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/Mag1cFall/cc-bar/internal/providers"
	_ "modernc.org/sqlite"
)

var desktopSessionPaths = []string{"Local State", "Network"}

// desktopCookieState 记录 Cookie 数据库与 WAL 的实际写入时间
type desktopCookieState struct {
	cookiesWrittenAt time.Time
	walWrittenAt     time.Time
}

// readDesktopCookieState 检查 Cookie 持久化文件与未完成的回滚日志
func readDesktopCookieState(directory string) (desktopCookieState, bool) {
	var state desktopCookieState
	for _, name := range []string{"Cookies", "Cookies-wal"} {
		info, err := os.Stat(filepath.Join(directory, "Network", name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			return state, false
		}
		if name == "Cookies" && info.Size() > 0 {
			state.cookiesWrittenAt = info.ModTime()
		} else if name == "Cookies-wal" && info.Size() > 32 {
			state.walWrittenAt = info.ModTime()
		}
	}
	journal, err := os.Stat(filepath.Join(directory, "Network", "Cookies-journal"))
	if err != nil && !os.IsNotExist(err) || err == nil && journal.Size() != 0 {
		return state, false
	}
	return state, !state.cookiesWrittenAt.IsZero()
}

// persisted 在 Cookie 库被运行中的浏览器锁定时等待实际刷盘
func (baseline *desktopCookieState) persisted(directory string) bool {
	current, committed := readDesktopCookieState(directory)
	if !committed {
		return false
	}
	if desktopWebSession(directory) {
		return true
	}
	if baseline.cookiesWrittenAt.IsZero() {
		*baseline = current
		return false
	}
	return current.cookiesWrittenAt.After(baseline.cookiesWrittenAt) || current.walWrittenAt.After(baseline.walWrittenAt)
}

// desktopWebSession 检查原生 Cookie 库是否包含网页登录凭据
func desktopWebSession(directory string) bool {
	path := filepath.Join(directory, "Network", "Cookies")
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	file.Close()
	database, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		return false
	}
	defer database.Close()
	var present bool
	err = database.QueryRow(`SELECT EXISTS(SELECT 1 FROM cookies WHERE name IN ('sessionKey', 'sessionKeyV2', 'sessionKeyV3') AND host_key IN ('.claude.ai', 'claude.ai', '.claude.com', 'claude.com') AND (length(encrypted_value) > 0 OR length(value) > 0))`).Scan(&present)
	return err == nil && present
}

// desktopApp 描述当前安装的 Desktop 及其原生会话目录
type desktopApp struct {
	Executable string `json:"executable"`
	Directory  string `json:"directory"`
	ProcessID  int    `json:"processId"`
}

// findDesktop 优先保留当前正在使用的 Desktop 安装和会话目录
func findDesktop(ctx context.Context) (*desktopApp, error) {
	script := `$ErrorActionPreference = 'Stop'
$processes = @(Get-CimInstance Win32_Process -Filter "Name='claude.exe'")
$desktop = @($processes | Where-Object { $_.ExecutablePath -and (Test-Path -LiteralPath (Join-Path (Split-Path $_.ExecutablePath) 'resources/app.asar')) })
$root = $desktop | Where-Object { $_.ParentProcessId -notin @($desktop.ProcessId) } | Select-Object -First 1
$executable = $root.ExecutablePath
$directory = $null
foreach ($process in $desktop) {
    if ($process.CommandLine -match '--user-data-dir=(?:"([^"]+)"|(\S+))') {
        $directory = if ($Matches[1]) { $Matches[1] } else { $Matches[2] }
        break
    }
}
if (-not $executable) {
    $candidates = @((Join-Path $env:LOCALAPPDATA 'Programs/Claude/Claude.exe'), (Join-Path $env:LOCALAPPDATA 'AnthropicClaude/Claude.exe'))
    if (Test-Path -LiteralPath (Join-Path $env:LOCALAPPDATA 'Programs')) {
        $candidates += @(Get-ChildItem -LiteralPath (Join-Path $env:LOCALAPPDATA 'Programs') -Directory -Filter 'Claude*' | ForEach-Object { Join-Path $_.FullName 'claude.exe' })
    }
    $packages = @(Get-AppxPackage -Name '*Claude*')
    $candidates += @($packages | ForEach-Object { Join-Path $_.InstallLocation 'app/Claude.exe' })
    $executable = $candidates | Where-Object { Test-Path -LiteralPath $_ } | Select-Object -First 1
}
if (-not $directory) {
    $candidates = @((Join-Path $env:APPDATA 'Claude'))
    if (Test-Path -LiteralPath (Join-Path $env:LOCALAPPDATA 'Packages')) {
        $candidates += @(Get-ChildItem -LiteralPath (Join-Path $env:LOCALAPPDATA 'Packages') -Directory -Filter 'Claude_*' | ForEach-Object { Join-Path $_.FullName 'LocalCache/Roaming/Claude' })
    }
    $directory = $candidates | Where-Object { Test-Path -LiteralPath (Join-Path $_ 'config.json') } | Sort-Object { (Get-Item -LiteralPath (Join-Path $_ 'config.json')).LastWriteTimeUtc } -Descending | Select-Object -First 1
    if (-not $directory) { $directory = Join-Path $env:APPDATA 'Claude' }
}
if ($executable -and $directory) {
    @{ executable = $executable; directory = $directory; processId = [int]$root.ProcessId } | ConvertTo-Json -Compress
}`
	command := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	output, err := command.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("读取 Claude Desktop 安装: %w", err)
	}
	if len(strings.TrimSpace(string(output))) == 0 {
		return nil, nil
	}
	var desktop desktopApp
	if err := json.Unmarshal(output, &desktop); err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(desktop.Executable), "resources", "app.asar")); err != nil {
		return nil, errors.New("Claude Desktop 安装目录有误")
	}
	return &desktop, nil
}

// desktopApp 将 Desktop 操作限定到当前 Windows 用户的真实主目录
func (store *Store) desktopApp(ctx context.Context) (*desktopApp, error) {
	home, err := os.UserHomeDir()
	if err != nil || !sameDirectory(home, store.home) {
		return nil, err
	}
	return findDesktop(ctx)
}

// close 等待指定 Desktop 进程退出并释放会话文件
func (desktop *desktopApp) close(ctx context.Context) error {
	script := fmt.Sprintf(`$ErrorActionPreference = 'Stop'
$rootId = %d
$expected = '%s'
$all = @(Get-CimInstance Win32_Process)
$matching = @($all | Where-Object { $_.ExecutablePath -ieq $expected })
$root = $matching | Where-Object { $_.ProcessId -eq $rootId } | Select-Object -First 1
if (-not $root) {
    $root = $matching | Where-Object { $_.ParentProcessId -notin @($matching.ProcessId) } | Select-Object -First 1
}
if (-not $root) { exit 0 }
$rootId = $root.ProcessId
$owned = @($rootId)
do {
    $children = @($all | Where-Object { $_.ParentProcessId -in $owned -and $_.ProcessId -notin $owned -and $_.ExecutablePath -ieq $expected })
    $owned += @($children | Select-Object -ExpandProperty ProcessId)
} while ($children.Count -gt 0)
$main = Get-Process -Id $rootId -ErrorAction SilentlyContinue
if ($main -and -not $main.HasExited -and $main.Path -ieq $expected) {
    try {
        if ($main.CloseMainWindow()) {
            $deadline = [DateTime]::UtcNow.AddSeconds(5)
            do {
                $main.Refresh()
                if ($main.HasExited -or $main.MainWindowHandle -eq [IntPtr]::Zero) { break }
                if ($main.WaitForExit(50)) { break }
            } while ([DateTime]::UtcNow -lt $deadline)
        }
    } catch { if (-not $main.HasExited) { throw } }
}
$processes = @($owned | ForEach-Object { Get-Process -Id $_ -ErrorAction SilentlyContinue })
$closing = @(foreach ($process in $processes) {
    try {
        if ($process.HasExited -or $process.Path -ine $expected) { continue }
        $process.Kill()
    } catch { if (-not $process.HasExited) { throw } }
    $process
})
foreach ($process in $closing) {
    if (-not $process.WaitForExit(10000)) { throw "Desktop process $($process.Id) did not exit" }
}`, desktop.ProcessID, strings.ReplaceAll(desktop.Executable, "'", "''"))
	command := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if output, err := command.CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("关闭 Claude Desktop: %w %s", err, strings.TrimSpace(string(output)))
	}
	desktop.ProcessID = 0
	return nil
}

// launch 直接运行原 Desktop 可执行文件并沿用其原生登录入口
func (desktop *desktopApp) launch() error {
	command := exec.Command(desktop.Executable)
	command.Env = desktopEnvironment()
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := command.Start(); err != nil {
		return err
	}
	desktop.ProcessID = command.Process.Pid
	go command.Wait()
	return nil
}

// execDesktopCode 用当前 Desktop 的深链接初始化 Code 登录
func execDesktopCode(executable string) *exec.Cmd {
	command := exec.Command(executable, "claude://code/new")
	command.Env = desktopEnvironment()
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return command
}

// desktopEnvironment 保持 Desktop 的共享记录目录独立于 CLI 账号变量
func desktopEnvironment() []string {
	var result []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.EqualFold(key, "CLAUDE_CONFIG_DIR") {
			result = append(result, entry)
		}
	}
	return result
}

// desktopIdentity 读取 Desktop 当前账号的原生标识
func desktopIdentity(directory string) string {
	data, err := os.ReadFile(filepath.Join(directory, "config.json"))
	if err != nil {
		return ""
	}
	config, err := providers.DecodeObject(data)
	if err != nil {
		return ""
	}
	identity, _ := config["lastKnownAccountUuid"].(string)
	return identity
}

// desktopAuthConfig 提取账号状态并保留全局窗口和偏好设置
func desktopAuthConfig(config map[string]any) map[string]any {
	result := map[string]any{}
	for key, value := range config {
		if strings.HasPrefix(key, "oauth:") || key == "lastKnownAccountUuid" {
			result[key] = value
		}
	}
	return result
}

// copySessionFile 复制固定会话范围内的文件并拒绝目录联接
func copySessionFile(source, destination string) error {
	info, err := os.Lstat(source)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return errors.New("Desktop 会话目录包含链接")
	}
	if info.IsDir() {
		if err := os.MkdirAll(destination, 0700); err != nil {
			return err
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := copySessionFile(filepath.Join(source, entry.Name()), filepath.Join(destination, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	return errors.Join(copyErr, output.Close())
}

// checkSessionFiles 在覆盖前检查整个会话目录的读取及链接状态
func checkSessionFiles(path string) error {
	return filepath.WalkDir(path, func(_ string, entry fs.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !entry.IsDir() && !entry.Type().IsRegular() {
			return errors.New("Desktop 会话目录包含链接")
		}
		return nil
	})
}

// sessionPath 固定 Desktop 会话操作的路径与链接边界
func sessionPath(directory, name string) (string, error) {
	if !slices.Contains(desktopSessionPaths, name) {
		return "", errors.New("Desktop 会话文件名称有误")
	}
	root, err := resolveDirectory(directory)
	if err != nil {
		return "", err
	}
	path := filepath.Join(root, name)
	if info, err := os.Lstat(path); err == nil && !info.IsDir() && !info.Mode().IsRegular() {
		return "", errors.New("Desktop 会话文件包含链接")
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	return path, nil
}

// saveDesktopSession 保存网页登录和 Code 凭据
func saveDesktopSession(directory, saved string) error {
	if err := os.MkdirAll(saved, 0700); err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(directory, "config.json"))
	if err != nil {
		return err
	}
	config, err := providers.DecodeObject(data)
	if err != nil {
		return err
	}
	for _, name := range desktopSessionPaths {
		for _, root := range []string{directory, saved} {
			path, err := sessionPath(root, name)
			if err != nil {
				return err
			}
			if err := checkSessionFiles(path); err != nil {
				return err
			}
		}
	}
	for _, name := range desktopSessionPaths {
		source, err := sessionPath(directory, name)
		if err != nil {
			return err
		}
		target, err := sessionPath(saved, name)
		if err != nil {
			return err
		}
		if err := os.RemoveAll(target); err != nil {
			return err
		}
		if err := copySessionFile(source, target); err != nil {
			return err
		}
	}
	return providers.WriteJSON(filepath.Join(saved, "config.json"), desktopAuthConfig(config))
}

// restoreDesktopSession 恢复账号的完整登录状态或准备新的原生登录
func restoreDesktopSession(directory, saved string) error {
	for _, name := range desktopSessionPaths {
		if _, err := sessionPath(directory, name); err != nil {
			return err
		}
		if saved != "" {
			path, err := sessionPath(saved, name)
			if err != nil {
				return err
			}
			if err := checkSessionFiles(path); err != nil {
				return err
			}
		}
	}
	data, err := os.ReadFile(filepath.Join(directory, "config.json"))
	if err != nil {
		return err
	}
	config, err := providers.DecodeObject(data)
	if err != nil {
		return err
	}
	var auth map[string]any
	if saved != "" {
		data, err := os.ReadFile(filepath.Join(saved, "config.json"))
		if err != nil {
			return err
		}
		if auth, err = providers.DecodeObject(data); err != nil {
			return err
		}
	}
	for key := range desktopAuthConfig(config) {
		delete(config, key)
	}
	for key, value := range desktopAuthConfig(auth) {
		config[key] = value
	}
	for _, name := range desktopSessionPaths {
		target, err := sessionPath(directory, name)
		if err != nil {
			return err
		}
		if err := os.RemoveAll(target); err != nil {
			return err
		}
		if saved != "" {
			source, err := sessionPath(saved, name)
			if err != nil {
				return err
			}
			if err := copySessionFile(source, target); err != nil {
				return err
			}
		}
	}
	return providers.WriteJSON(filepath.Join(directory, "config.json"), config)
}
