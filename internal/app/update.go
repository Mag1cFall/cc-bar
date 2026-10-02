package app

import (
	"context"
	"debug/pe"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

const updateRepository = "Mag1cFall/cc-bar"
const updateDirectoryPrefix = ".ccbar-update-"
const maximumUpdateSize = 256 << 20

var updateInstallGate sync.Mutex
var updateStartupError string

// ReleaseUpdate 表示自有 Windows 仓库的最新正式版本
type ReleaseUpdate struct {
	CurrentVersion string `json:"currentVersion"`
	LatestVersion  string `json:"latestVersion,omitempty"`
	Status         string `json:"status"`
	ReleaseURL     string `json:"releaseUrl,omitempty"`
	Notes          string `json:"notes,omitempty"`
	Size           int64  `json:"size,omitempty"`
	InstallError   string `json:"installError,omitempty"`
}

type githubRelease struct {
	Tag        string `json:"tag_name"`
	URL        string `json:"html_url"`
	Notes      string `json:"body"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

// CheckForUpdates 查询正式 Release 并返回可安装的 Windows 版本
func (service *Service) CheckForUpdates(ctx context.Context) (ReleaseUpdate, error) {
	result, _, err := findUpdate(ctx, service.version)
	result.InstallError = updateStartupError
	return result, err
}

func findUpdate(ctx context.Context, current string) (ReleaseUpdate, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	result := ReleaseUpdate{CurrentVersion: current, Status: "unpublished"}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+updateRepository+"/releases/latest", nil)
	if err != nil {
		return result, "", err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "CCBar/"+current)
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return result, "", fmt.Errorf("检查更新: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return result, "", nil
	}
	if response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusTooManyRequests {
		return result, "", errors.New("GitHub 请求暂时受限，请稍后重试")
	}
	if response.StatusCode != http.StatusOK {
		return result, "", fmt.Errorf("检查更新: GitHub 返回 %d", response.StatusCode)
	}
	var release githubRelease
	if err = json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&release); err != nil {
		return result, "", fmt.Errorf("读取发布信息: %w", err)
	}
	return selectUpdate(release, current)
}

func selectUpdate(release githubRelease, current string) (ReleaseUpdate, string, error) {
	result := ReleaseUpdate{CurrentVersion: current, Status: "unpublished"}
	if release.Draft || release.Prerelease {
		return result, "", nil
	}
	newer, err := newerVersion(release.Tag, current)
	if err != nil {
		return result, "", err
	}
	for _, asset := range release.Assets {
		if asset.Name != "CCBar.exe" {
			continue
		}
		parsed, parseErr := url.Parse(asset.URL)
		expectedPath := "/" + updateRepository + "/releases/download/" + release.Tag + "/CCBar.exe"
		if parseErr != nil || parsed.Scheme != "https" || parsed.Host != "github.com" || parsed.Path != expectedPath || parsed.RawQuery != "" || asset.Size <= 0 || asset.Size > maximumUpdateSize {
			return result, "", errors.New("发布版的 Windows 下载信息有误")
		}
		result.LatestVersion = strings.TrimPrefix(release.Tag, "v")
		result.ReleaseURL = "https://github.com/" + updateRepository + "/releases/tag/" + url.PathEscape(release.Tag)
		result.Notes = release.Notes
		result.Size = asset.Size
		result.Status = "current"
		if newer {
			result.Status = "available"
		}
		return result, asset.URL, nil
	}
	return result, "", nil
}

func newerVersion(candidate, current string) (bool, error) {
	parse := func(value string) ([3]int, error) {
		var version [3]int
		parts := strings.Split(strings.TrimPrefix(value, "v"), ".")
		if len(parts) != len(version) {
			return version, fmt.Errorf("版本号格式有误: %s", value)
		}
		for index, part := range parts {
			if part == "" || strings.Trim(part, "0123456789") != "" {
				return version, fmt.Errorf("版本号格式有误: %s", value)
			}
			number, err := strconv.Atoi(part)
			if err != nil {
				return version, err
			}
			version[index] = number
		}
		return version, nil
	}
	latest, err := parse(candidate)
	if err != nil {
		return false, err
	}
	installed, err := parse(current)
	if err != nil {
		return false, err
	}
	for index := range latest {
		if latest[index] != installed[index] {
			return latest[index] > installed[index], nil
		}
	}
	return false, nil
}

// InstallUpdate 下载新版本并让更新助手在退出后替换当前 EXE
func (service *Service) InstallUpdate(ctx context.Context) error {
	if !updateInstallGate.TryLock() {
		return errors.New("更新正在安装中")
	}
	defer updateInstallGate.Unlock()
	result, downloadURL, err := findUpdate(ctx, service.version)
	if err != nil {
		return err
	}
	if result.Status != "available" {
		return errors.New("当前没有可安装的新版本")
	}
	target, err := os.Executable()
	if err != nil {
		return err
	}
	target, err = filepath.EvalSymlinks(target)
	if err != nil {
		return err
	}
	directory, err := os.MkdirTemp(filepath.Dir(target), updateDirectoryPrefix)
	if err != nil {
		return fmt.Errorf("请将 CCBar 放在有写入权限的目录后重试: %w", err)
	}
	staged := filepath.Join(directory, "CCBar.exe")
	started := false
	defer func() {
		if !started {
			_ = removeUpdateDirectory(directory, target)
		}
	}()
	if err = downloadUpdate(ctx, downloadURL, staged, result.Size); err != nil {
		return err
	}
	command := exec.Command(staged, "--apply-update", strconv.Itoa(os.Getpid()), target)
	command.Dir = filepath.Dir(target)
	if err = command.Start(); err != nil {
		return fmt.Errorf("启动更新助手: %w", err)
	}
	_ = command.Process.Release()
	started = true
	time.AfterFunc(300*time.Millisecond, service.desktop.Quit)
	return nil
}

func downloadUpdate(ctx context.Context, address, destination string, expectedSize int64) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("下载更新: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("下载更新: GitHub 返回 %d", response.StatusCode)
	}
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
	if err != nil {
		return err
	}
	length, copyErr := io.Copy(file, io.LimitReader(response.Body, expectedSize+1))
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("下载更新: %w", copyErr)
	}
	if closeErr != nil {
		return closeErr
	}
	if length != expectedSize {
		return errors.New("更新文件下载不完整，请重新下载")
	}
	image, err := pe.Open(destination)
	if err != nil {
		return fmt.Errorf("读取 Windows 更新文件: %w", err)
	}
	defer image.Close()
	if image.Machine != pe.IMAGE_FILE_MACHINE_AMD64 {
		return errors.New("发布文件与 Windows x64 平台不匹配")
	}
	return nil
}

// HandleUpdateArguments 在桌面启动前完成原位替换及临时文件清理
func HandleUpdateArguments(arguments []string) (bool, error) {
	if len(arguments) == 0 {
		return false, nil
	}
	if arguments[0] == "--update-error" && len(arguments) == 2 {
		updateStartupError = arguments[1]
		return false, nil
	}
	if arguments[0] != "--apply-update" && arguments[0] != "--cleanup-update" {
		return false, nil
	}
	if len(arguments) != 3 {
		return true, errors.New("更新助手参数有误")
	}
	processID, err := strconv.Atoi(arguments[1])
	if err != nil || processID <= 0 {
		return true, errors.New("更新进程信息有误")
	}
	executable, err := os.Executable()
	if err != nil {
		return true, err
	}
	if arguments[0] == "--cleanup-update" {
		if err = validateUpdateDirectory(arguments[2], executable); err != nil {
			return true, err
		}
		if err = waitForUpdateProcess(processID); err != nil {
			updateStartupError = err.Error()
			return false, nil
		}
		if err = removeUpdateDirectory(arguments[2], executable); err != nil {
			updateStartupError = "清理更新临时文件: " + err.Error()
		}
		return false, nil
	}
	target := arguments[2]
	directory := filepath.Dir(executable)
	if err = validateUpdateDirectory(directory, target); err != nil {
		return true, err
	}
	if err = waitForUpdateProcess(processID); err == nil {
		err = replaceUpdateExecutable(executable, target)
	}
	if err != nil {
		command := exec.Command(target, "--update-error", "安装更新: "+err.Error())
		command.Dir = filepath.Dir(target)
		_ = command.Start()
		return true, err
	}
	command := exec.Command(target, "--cleanup-update", strconv.Itoa(os.Getpid()), directory)
	command.Dir = filepath.Dir(target)
	if err = command.Start(); err != nil {
		return true, fmt.Errorf("启动新版本: %w", err)
	}
	_ = command.Process.Release()
	return true, nil
}

func waitForUpdateProcess(processID int) error {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(processID))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return nil
	}
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	result, err := windows.WaitForSingleObject(handle, 90_000)
	if err != nil {
		return err
	}
	if result != windows.WAIT_OBJECT_0 {
		return errors.New("应用退出耗时过长，请退出后重新安装更新")
	}
	return nil
}

func validateUpdateDirectory(directory, target string) error {
	if !filepath.IsAbs(directory) || !filepath.IsAbs(target) || !strings.HasPrefix(filepath.Base(directory), updateDirectoryPrefix) {
		return errors.New("更新文件目录有误")
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(directory)
	if err != nil {
		return err
	}
	buffer := make([]uint16, 32768)
	length, err := windows.GetLongPathName(name, &buffer[0], uint32(len(buffer)))
	if err != nil || length >= uint32(len(buffer)) {
		return errors.New("读取更新目录的位置失败")
	}
	expected := filepath.Clean(windows.UTF16ToString(buffer[:length]))
	if !strings.EqualFold(resolved, expected) {
		return errors.New("更新临时目录的实际位置有误")
	}
	targetInfo, err := os.Lstat(target)
	if err != nil || !targetInfo.Mode().IsRegular() {
		return errors.New("当前程序文件的位置有误")
	}
	parentInfo, err := os.Stat(filepath.Dir(directory))
	if err != nil {
		return err
	}
	targetParent, err := os.Stat(filepath.Dir(target))
	if err != nil || !os.SameFile(parentInfo, targetParent) {
		return errors.New("更新文件与当前程序须位于同一目录")
	}
	return nil
}

func removeUpdateDirectory(directory, target string) error {
	if err := validateUpdateDirectory(directory, target); err != nil {
		return err
	}
	return os.RemoveAll(directory)
}

func replaceUpdateExecutable(source, target string) error {
	if err := validateUpdateDirectory(filepath.Dir(source), target); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.CreateTemp(filepath.Dir(target), ".ccbar-new-*.exe")
	if err != nil {
		return err
	}
	defer os.Remove(output.Name())
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	for attempt := 0; attempt < 15; attempt++ {
		err = os.Rename(output.Name(), target)
		if err == nil {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return err
}
