//go:build windows

package accounts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// longPath 展开 Windows 短文件名并保留目录联接原本的位置
func longPath(path string) (string, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	buffer := make([]uint16, 32768)
	length, err := windows.GetLongPathName(name, &buffer[0], uint32(len(buffer)))
	if err != nil {
		return "", err
	}
	if length >= uint32(len(buffer)) {
		return "", fmt.Errorf("directory path is too long")
	}
	return filepath.Clean(windows.UTF16ToString(buffer[:length])), nil
}

// resolveDirectory 解析 Windows 目录联接和符号链接的最终位置
func resolveDirectory(path string) (string, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	handle, err := windows.CreateFile(name, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)
	buffer := make([]uint16, 32768)
	length, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], uint32(len(buffer)), 0)
	if err != nil {
		return "", err
	}
	if length >= uint32(len(buffer)) {
		return "", fmt.Errorf("resolved directory path is too long")
	}
	resolved := windows.UTF16ToString(buffer[:length])
	if strings.HasPrefix(resolved, `\\?\UNC\`) {
		resolved = `\\` + strings.TrimPrefix(resolved, `\\?\UNC\`)
	} else {
		resolved = strings.TrimPrefix(resolved, `\\?\`)
	}
	return filepath.Clean(resolved), nil
}

// createDirectoryLink 用普通用户可用的目录联接共享项目
func createDirectoryLink(path, target string) error {
	command := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "New-Item -ItemType Junction -Path $env:CCBAR_LINK -Target $env:CCBAR_TARGET -ErrorAction Stop | Out-Null")
	command.Env = append(os.Environ(), "CCBAR_LINK="+path, "CCBAR_TARGET="+target)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	var output bytes.Buffer
	command.Stderr = &output
	if err := command.Run(); err != nil {
		return fmt.Errorf("create projects junction: %w: %s", err, output.String())
	}
	return nil
}
func showConsole(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}

// findPowerShellClaude 读取用户函数别名脚本与 PATH 命令的实际解析顺序
func findPowerShellClaude() (ClaudeCLI, bool, error) {
	for _, name := range []string{"pwsh.exe", "powershell.exe"} {
		shell, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		query := "$command = Get-Command claude -ErrorAction SilentlyContinue; if ($command) { Write-Output ('CCBAR_CLAUDE:' + (@{ path = $command.Source; kind = [string]$command.CommandType } | ConvertTo-Json -Compress)) }"
		command := exec.CommandContext(ctx, shell, "-NoLogo", "-NonInteractive", "-Command", query)
		showConsole(command)
		output, runErr := command.Output()
		cancel()
		if runErr != nil {
			return ClaudeCLI{}, false, fmt.Errorf("读取 Claude 启动命令: %w", runErr)
		}
		marker := bytes.LastIndex(output, []byte("CCBAR_CLAUDE:"))
		if marker < 0 {
			continue
		}
		var resolved struct {
			Path string `json:"path"`
			Kind string `json:"kind"`
		}
		if err = json.Unmarshal(bytes.TrimSpace(output[marker+len("CCBAR_CLAUDE:"):]), &resolved); err != nil {
			return ClaudeCLI{}, false, err
		}
		if resolved.Kind == "Function" || resolved.Kind == "Alias" {
			resolved.Path = "claude"
		}
		return ClaudeCLI{Executable: resolved.Path, PowerShell: shell}, true, nil
	}
	return ClaudeCLI{}, false, nil
}

// Command 使用同一终端入口并保留用户的函数别名和脚本行为
func (cli ClaudeCLI) Command(ctx context.Context, args ...string) *exec.Cmd {
	quote := func(value string) string {
		return "'" + strings.ReplaceAll(value, "'", "''") + "'"
	}
	if cli.PowerShell != "" {
		invocation := "& 'claude'"
		for _, argument := range args {
			invocation += " " + quote(argument)
		}
		return exec.CommandContext(ctx, cli.PowerShell, "-NoLogo", "-Command", invocation+"; exit $LASTEXITCODE")
	}
	if strings.EqualFold(filepath.Ext(cli.Executable), ".ps1") {
		return exec.CommandContext(ctx, "powershell.exe", append([]string{"-NoLogo", "-NoProfile", "-File", cli.Executable}, args...)...)
	}
	return exec.CommandContext(ctx, cli.Executable, args...)
}

// claudeCommand 让已有 CLI 获得独立控制台与有效标准句柄
func claudeCommand(ctx context.Context, cli ClaudeCLI, args ...string) *exec.Cmd {
	quote := func(value string) string {
		return "'" + strings.ReplaceAll(value, "'", "''") + "'"
	}
	direct := cli.Command(ctx, args...)
	launch := "Start-Process -FilePath " + quote(direct.Path)
	if len(direct.Args) > 1 {
		arguments := make([]string, len(direct.Args)-1)
		for index, argument := range direct.Args[1:] {
			arguments[index] = syscall.EscapeArg(argument)
		}
		launch += " -ArgumentList " + quote(strings.Join(arguments, " "))
	}
	script := "$ErrorActionPreference = 'Stop'; $process = " + launch + " -Wait -PassThru; exit $process.ExitCode"
	return exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
}

// loadUserClaudeDirectory 在启动时读取 Windows 用户选择的默认账号
func loadUserClaudeDirectory(home string) error {
	userHome, err := os.UserHomeDir()
	if err != nil || !sameDirectory(home, userHome) || os.Getenv("CLAUDE_CONFIG_DIR") != "" {
		return err
	}
	key, err := registry.OpenKey(registry.CURRENT_USER, "Environment", registry.QUERY_VALUE)
	if err == registry.ErrNotExist {
		return nil
	}
	if err != nil {
		return err
	}
	defer key.Close()
	directory, _, err := key.GetStringValue("CLAUDE_CONFIG_DIR")
	if err == registry.ErrNotExist {
		return nil
	}
	if err != nil {
		return err
	}
	return os.Setenv("CLAUDE_CONFIG_DIR", directory)
}

// setUserClaudeDirectory 保存新终端默认目录并通知桌面环境
func setUserClaudeDirectory(directory string) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, "Environment", registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	if directory == "" {
		err = key.DeleteValue("CLAUDE_CONFIG_DIR")
		if err == registry.ErrNotExist {
			err = nil
		}
	} else {
		err = key.SetStringValue("CLAUDE_CONFIG_DIR", directory)
	}
	if err != nil {
		return err
	}
	if directory == "" {
		err = os.Unsetenv("CLAUDE_CONFIG_DIR")
	} else {
		err = os.Setenv("CLAUDE_CONFIG_DIR", directory)
	}
	if err != nil {
		return err
	}
	message := windows.NewLazySystemDLL("user32.dll").NewProc("SendMessageTimeoutW")
	text, _ := windows.UTF16PtrFromString("Environment")
	var result uintptr
	message.Call(0xffff, 0x001a, 0, uintptr(unsafe.Pointer(text)), 0x0002, 2000, uintptr(unsafe.Pointer(&result)))
	return nil
}
