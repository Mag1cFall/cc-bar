package browser

import (
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

//go:embed runtime.cab
var archive embed.FS

var runtimeName = regexp.MustCompile(`^Microsoft\.WebView2\.FixedVersionRuntime\.[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+\.(x64|arm64)$`)

// Ready 判断当前架构运行库是否已完整释放
func Ready(dataDirectory string) bool {
	path, err := runtimeDirectory(dataDirectory)
	return err == nil && complete(path)
}

// Prepare 首次释放固定运行库并复用已完成的本地文件
func Prepare(dataDirectory string) (string, error) {
	destination, err := runtimeDirectory(dataDirectory)
	if err != nil {
		return "", err
	}
	if complete(destination) {
		return destination, cleanOldRuntimes(destination)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("读取运行库用户: %w", err)
	}
	name, err := windows.UTF16PtrFromString(`Local\CCBar.FixedRuntime.` + user.User.Sid.String())
	if err != nil {
		return "", err
	}
	mutex, err := windows.CreateMutex(nil, false, name)
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return "", fmt.Errorf("创建运行库准备锁: %w", err)
	}
	defer windows.CloseHandle(mutex)
	state, err := windows.WaitForSingleObject(mutex, 180000)
	if err != nil {
		return "", fmt.Errorf("等待运行库准备: %w", err)
	}
	if state != windows.WAIT_OBJECT_0 && state != windows.WAIT_ABANDONED {
		return "", errors.New("运行库准备等待超时")
	}
	defer windows.ReleaseMutex(mutex)
	if complete(destination) {
		return destination, cleanOldRuntimes(destination)
	}
	root := filepath.Dir(destination)
	if err = checkLocalPath(root); err != nil {
		return "", err
	}
	if err = os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("创建运行库目录: %w", err)
	}
	staging := filepath.Join(root, ".extracting")
	cabinet := filepath.Join(root, ".runtime.cab")
	for _, path := range []string{staging, cabinet, destination} {
		if err = removeRuntimePath(root, path); err != nil {
			return "", err
		}
	}
	if err = os.Mkdir(staging, 0o700); err != nil {
		return "", fmt.Errorf("创建运行库展开目录: %w", err)
	}
	defer removeRuntimePath(root, staging)
	defer removeRuntimePath(root, cabinet)
	if err = writeCabinet(cabinet); err != nil {
		return "", err
	}
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return "", err
	}
	if err = runHidden(filepath.Join(system, "expand.exe"), cabinet, "-F:*", staging); err != nil {
		return "", fmt.Errorf("展开固定运行库: %w", err)
	}
	extracted := filepath.Join(staging, filepath.Base(destination))
	if !complete(extracted) {
		return "", errors.New("固定运行库展开结果不完整或架构不匹配")
	}
	// Windows 10 的 AppContainer 渲染进程需要运行库读取权限
	if err = runHidden(filepath.Join(system, "icacls.exe"), extracted, "/grant", "*S-1-15-2-2:(OI)(CI)(RX)", "*S-1-15-2-1:(OI)(CI)(RX)"); err != nil {
		return "", fmt.Errorf("设置运行库读取权限: %w", err)
	}
	if err = checkLocalPath(destination); err != nil {
		return "", err
	}
	if err = os.Rename(extracted, destination); err != nil {
		return "", fmt.Errorf("保存固定运行库: %w", err)
	}
	return destination, cleanOldRuntimes(destination)
}

// cleanOldRuntimes 保留仍被使用的旧版目录并在下次启动重试清理
func cleanOldRuntimes(destination string) error {
	root := filepath.Dir(destination)
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != filepath.Base(destination) && runtimeName.MatchString(entry.Name()) {
			path := filepath.Join(root, entry.Name())
			if err = checkRuntimeDeletion(path); err == nil {
				err = removeRuntimePath(root, path)
			}
			if err != nil && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, windows.ERROR_SHARING_VIOLATION) && !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
				return fmt.Errorf("清理旧运行库: %w", err)
			}
		}
	}
	return nil
}

// runtimeDirectory 按应用数据目录与当前架构确定固定目录
func runtimeDirectory(dataDirectory string) (string, error) {
	architecture := map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
	if architecture == "" {
		return "", fmt.Errorf("固定运行库架构未支持: %s", runtime.GOARCH)
	}
	if strings.TrimSpace(dataDirectory) == "" {
		return "", errors.New("应用数据目录为空")
	}
	data, err := filepath.Abs(dataDirectory)
	if err != nil {
		return "", err
	}
	name, err := windows.UTF16PtrFromString(data)
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
		return "", errors.New("应用数据目录路径过长")
	}
	data = strings.TrimPrefix(windows.UTF16ToString(buffer[:length]), `\\?\`)
	if volume := filepath.VolumeName(data); len(volume) != 2 || volume[1] != ':' {
		return "", errors.New("固定运行库需要本地磁盘目录")
	}
	path := filepath.Join(data, "Runtime", "Microsoft.WebView2.FixedVersionRuntime."+Version+"."+architecture)
	if err = checkLocalPath(path); err != nil {
		return "", err
	}
	return path, nil
}

// complete 检查完整展开后的关键文件与原生目录
func complete(directory string) bool {
	architecture := map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
	for _, name := range []string{"msedgewebview2.exe", "msedge.dll", "icudtl.dat", "resources.pak", Version + ".manifest", filepath.Join("EBWebView", architecture, "EmbeddedBrowserWebView.dll")} {
		path := filepath.Join(directory, name)
		if checkLocalPath(path) != nil {
			return false
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return false
		}
	}
	return true
}

// checkRuntimeDeletion 先确认整棵旧版目录可删除再开始清理
func checkRuntimeDeletion(directory string) error {
	return filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := checkLocalPath(path); err != nil {
			return err
		}
		name, err := windows.UTF16PtrFromString(path)
		if err != nil {
			return err
		}
		attributes := uint32(windows.FILE_ATTRIBUTE_NORMAL)
		if entry.IsDir() {
			attributes |= windows.FILE_FLAG_BACKUP_SEMANTICS
		}
		handle, err := windows.CreateFile(name, windows.DELETE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, attributes, 0)
		if err != nil {
			return err
		}
		return windows.CloseHandle(handle)
	})
}

// checkLocalPath 检查运行库路径的现有父目录与重解析点
func checkLocalPath(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	volume := filepath.VolumeName(absolute)
	if len(volume) != 2 || volume[1] != ':' {
		return errors.New("固定运行库需要本地磁盘目录")
	}
	current := volume + string(filepath.Separator)
	for _, component := range strings.Split(strings.TrimPrefix(absolute, current), string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, fs.ErrNotExist) {
			return nil
		}
		if statErr != nil {
			return statErr
		}
		attributes, ok := info.Sys().(*syscall.Win32FileAttributeData)
		if info.Mode()&os.ModeSymlink != 0 || (ok && attributes.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0) {
			return fmt.Errorf("运行库路径包含重解析点: %s", current)
		}
	}
	return nil
}

// removeRuntimePath 仅移除运行库内的明确目录与临时文件
func removeRuntimePath(root, path string) error {
	relative, err := filepath.Rel(root, path)
	if err != nil || filepath.Dir(relative) != "." || (relative != ".extracting" && relative != ".runtime.cab" && !runtimeName.MatchString(relative)) {
		return errors.New("运行库清理路径越界")
	}
	if err = checkLocalPath(path); err != nil {
		return err
	}
	err = filepath.WalkDir(path, func(child string, entry fs.DirEntry, walkErr error) error {
		if errors.Is(walkErr, fs.ErrNotExist) {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		return checkLocalPath(child)
	})
	if err != nil {
		return err
	}
	return os.RemoveAll(path)
}

// writeCabinet 将嵌入的压缩包流式写入展开目录
func writeCabinet(path string) error {
	source, err := archive.Open("runtime.cab")
	if err != nil {
		return err
	}
	defer source.Close()
	destination, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(destination, source)
	closeErr := destination.Close()
	return errors.Join(err, closeErr)
}

// runHidden 调用系统工具并保留直接错误信息
func runHidden(executable string, arguments ...string) error {
	command := exec.Command(executable, arguments...)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
