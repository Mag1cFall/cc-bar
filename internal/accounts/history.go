package accounts

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/Mag1cFall/cc-bar/internal/providers"
)

// sharedDirectories 是始终在共享目录建立的记录目录
var sharedDirectories = []string{"projects", "tasks", "plans", "todos", "session-env", "file-history"}

// sharedFiles 是始终在共享目录建立的文件及其初始内容
var sharedFiles = map[string][]byte{"history.jsonl": {}, "settings.json": []byte("{}\n")}

// accountEntries 是只属于单个账号配置目录的条目
var accountEntries = map[string]bool{".credentials.json": true, ".claude.json": true, ".claude.json.backup": true, "backups": true, ".ccbar-imports": true, profileBaseName: true}

// profileBaseName 是账号目录中记录上次同步结果的文件名
const profileBaseName = ".ccbar-profile-base.json"

// sharedProfileKeys 是在默认与账号 .claude.json 之间同步的全局偏好与项目状态
var sharedProfileKeys = []string{"projects", "mcpServers", "hasCompletedOnboarding", "lastOnboardingVersion", "lastReleaseNotesSeen", "theme", "editorMode", "verbose", "autoCompactEnabled", "preferredNotifChannel", "githubRepoPaths"}

// historyMutex 串行化共享记录的合并与链接
var historyMutex sync.Mutex

// sharedEntries 返回两侧除账号私有条目外的全部条目及其是否为目录
func sharedEntries(directory, shared string) (map[string]bool, error) {
	entries := map[string]bool{}
	for _, name := range sharedDirectories {
		entries[name] = true
	}
	for name := range sharedFiles {
		entries[name] = false
	}
	for _, root := range []string{shared, directory} {
		items, err := os.ReadDir(root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			name := item.Name()
			if _, known := entries[name]; known || accountEntries[name] || strings.Contains(name, ".tmp") || strings.HasSuffix(name, ".lock") {
				continue
			}
			entries[name] = item.IsDir() || item.Type()&(os.ModeSymlink|os.ModeIrregular) != 0
		}
	}
	return entries, nil
}

// historyLinked 检查账号目录是否引用同一份会话、历史与配置
func historyLinked(directory, shared string) bool {
	entries, err := sharedEntries(directory, shared)
	if err != nil {
		return false
	}
	for name, isDirectory := range entries {
		if isDirectory {
			current, err := resolveDirectory(filepath.Join(directory, name))
			if err != nil {
				return false
			}
			target, err := resolveDirectory(filepath.Join(shared, name))
			if err != nil || !sameDirectory(current, target) {
				return false
			}
			continue
		}
		first, err := os.Stat(filepath.Join(directory, name))
		if err != nil {
			return false
		}
		second, err := os.Stat(filepath.Join(shared, name))
		if err != nil || !os.SameFile(first, second) {
			return false
		}
	}
	return true
}

// mergeLines 追加缺少的记录并保留其他账号的硬链接
func mergeLines(target, source string) error {
	left, err := os.ReadFile(target)
	if os.IsNotExist(err) {
		return copyHistoryFile(target, source)
	}
	if err != nil {
		return err
	}
	right, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if bytes.Equal(left, right) {
		return nil
	}
	seen := map[string]bool{}
	var output bytes.Buffer
	for index, data := range [][]byte{left, right} {
		scanner := bufio.NewScanner(bytes.NewReader(data))
		scanner.Buffer(make([]byte, 64<<10), 32<<20)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" || seen[line] {
				continue
			}
			seen[line] = true
			if index == 1 {
				output.WriteString(line)
				output.WriteByte('\n')
			}
		}
		if err := scanner.Err(); err != nil {
			return err
		}
	}
	if output.Len() == 0 {
		return nil
	}
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if len(left) > 0 && left[len(left)-1] != '\n' {
		if _, err := file.Write([]byte{'\n'}); err != nil {
			return err
		}
	}
	_, err = file.Write(output.Bytes())
	return err
}

// copyHistoryFile 复制尚不存在的共享文件
func copyHistoryFile(target, source string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return providers.WriteBytes(target, data)
}

// prepareHistory 将账号私有条目以外的记录与配置合并到共享目录并建立链接
func prepareHistory(directory, shared string) error {
	historyMutex.Lock()
	defer historyMutex.Unlock()
	if sameDirectory(directory, shared) {
		return nil
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	entries, err := sharedEntries(directory, shared)
	if err != nil {
		return err
	}
	_, baseErr := os.Stat(filepath.Join(directory, profileBaseName))
	adopted := baseErr == nil
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if entries[name] {
			err = shareDirectory(directory, shared, name)
		} else {
			err = shareFile(directory, shared, name, sharedFiles[name], adopted)
		}
		if err != nil {
			return err
		}
	}
	return shareProfilePreferences(directory, shared)
}

// shareDirectory 合并账号记录并建立普通用户可用的目录联接
func shareDirectory(directory, shared, name string) error {
	target := filepath.Join(shared, name)
	if err := os.MkdirAll(target, 0700); err != nil {
		return err
	}
	targetResolved, err := resolveDirectory(target)
	if err != nil {
		return err
	}
	local := filepath.Join(directory, name)
	if info, err := os.Lstat(local); err == nil {
		resolved, resolveError := resolveDirectory(local)
		if resolveError != nil {
			return resolveError
		}
		if !sameDirectory(resolved, targetResolved) {
			if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
				return fmt.Errorf("shared directory link points elsewhere: %s", local)
			}
			if err := mergeRecordDirectory(local, target, shared, filepath.Base(directory), name); err != nil {
				return err
			}
			if err := os.Remove(local); err != nil {
				return err
			}
			if err := createDirectoryLink(local, target); err != nil {
				return err
			}
		}
	} else if os.IsNotExist(err) {
		if err := createDirectoryLink(local, target); err != nil {
			return err
		}
	} else {
		return err
	}
	return nil
}

// shareFile 首次接入时合并历史或设置，之后以最后修改的一份为准，并保留同一份文件的硬链接
func shareFile(directory, shared, name string, initial []byte, adopted bool) error {
	target := filepath.Join(shared, name)
	local := filepath.Join(directory, name)
	if _, err := os.Stat(target); os.IsNotExist(err) {
		if _, err := os.Lstat(local); err == nil {
			if err := os.Rename(local, target); err != nil {
				return err
			}
		} else if initial == nil {
			return nil
		} else if err := providers.WriteBytes(target, initial); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if first, err := os.Stat(local); err == nil {
		second, err := os.Stat(target)
		if err != nil {
			return err
		}
		if os.SameFile(first, second) {
			return nil
		}
		if name == "history.jsonl" {
			if err := mergeLines(target, local); err != nil {
				return err
			}
		} else if !adopted {
			if err := mergeJSONFile(target, local, shared, filepath.Base(directory), name); err != nil {
				return err
			}
		} else if err := keepNewerFile(target, local, shared, filepath.Base(directory), name, first, second); err != nil {
			return err
		}
		if err := os.Remove(local); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Link(target, local); err != nil {
		return fmt.Errorf("share %s: %w", name, err)
	}
	return nil
}

// keepNewerFile 以最后修改的一份作为共享内容并保留另一份原文
func keepNewerFile(target, local, shared, accountName, name string, localInfo, targetInfo os.FileInfo) error {
	current, err := os.ReadFile(target)
	if err != nil {
		return err
	}
	incoming, err := os.ReadFile(local)
	if err != nil {
		return err
	}
	if bytes.Equal(current, incoming) {
		return nil
	}
	if localInfo.ModTime().After(targetInfo.ModTime()) {
		if err := preserveImportedFile(shared, "shared", name, current); err != nil {
			return err
		}
		return writeLinkedFile(target, incoming)
	}
	return preserveImportedFile(shared, accountName, name, incoming)
}

// mergeRecordDirectory 将独立记录迁入共享目录并完整保留冲突文件
func mergeRecordDirectory(source, target, shared, accountName, folderName string) error {
	sourceRoot, err := resolveDirectory(source)
	if err != nil {
		return err
	}
	if !sameDirectory(source, sourceRoot) {
		return errors.New("source records is an unexpected link")
	}
	var directories []string
	err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkError error) error {
		if walkError != nil {
			return walkError
		}
		if entry.Type()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return fmt.Errorf("nested record link: %s", path)
		}
		relative, err := filepath.Rel(source, path)
		if err != nil || filepath.IsAbs(relative) || relative == ".." {
			return errors.New("invalid history path")
		}
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			directories = append(directories, path)
			return os.MkdirAll(destination, 0700)
		}
		if _, err := os.Stat(destination); os.IsNotExist(err) {
			if err := copyHistoryFile(destination, path); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if filepath.Ext(path) == ".jsonl" {
			if err := mergeLines(destination, path); err != nil {
				return err
			}
		} else if filepath.Ext(path) == ".json" {
			if err := mergeJSONFile(destination, path, shared, accountName, filepath.Join(folderName, relative)); err != nil {
				return err
			}
		} else {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			current, err := os.ReadFile(destination)
			if err != nil {
				return err
			}
			if !bytes.Equal(data, current) {
				if err := preserveImportedFile(shared, accountName, filepath.Join(folderName, relative), data); err != nil {
					return err
				}
			}
		}
		return os.Remove(path)
	})
	if err != nil {
		return err
	}
	for i := len(directories) - 1; i > 0; i-- {
		if err := os.Remove(directories[i]); err != nil {
			return err
		}
	}
	return nil
}

// mergeJSONValue 合并明确设置键和数组条目并采用较新文件的冲突值
func mergeJSONValue(current, incoming any, preferIncoming bool) any {
	left, leftObject := current.(map[string]any)
	right, rightObject := incoming.(map[string]any)
	if leftObject && rightObject {
		for key, value := range right {
			if previous, exists := left[key]; exists {
				left[key] = mergeJSONValue(previous, value, preferIncoming)
			} else {
				left[key] = value
			}
		}
		return left
	}
	leftArray, leftList := current.([]any)
	rightArray, rightList := incoming.([]any)
	if leftList && rightList {
		for _, candidate := range rightArray {
			found := false
			for _, existing := range leftArray {
				if reflect.DeepEqual(existing, candidate) {
					found = true
					break
				}
			}
			if !found {
				leftArray = append(leftArray, candidate)
			}
		}
		return leftArray
	}
	if preferIncoming {
		return incoming
	}
	return current
}

// mergeJSONFile 合并记录且保留两份不同的原始内容
func mergeJSONFile(target, source, shared, accountName, relative string) error {
	left, err := os.ReadFile(target)
	if err != nil {
		return err
	}
	right, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if bytes.Equal(left, right) {
		return nil
	}
	if err := preserveImportedFile(shared, accountName, relative, right); err != nil {
		return err
	}
	var current, incoming any
	if json.Unmarshal(left, &current) != nil || json.Unmarshal(right, &incoming) != nil {
		return nil
	}
	if err := preserveImportedFile(shared, "shared", relative, left); err != nil {
		return err
	}
	currentInfo, err := os.Stat(target)
	if err != nil {
		return err
	}
	incomingInfo, err := os.Stat(source)
	if err != nil {
		return err
	}
	merged := mergeJSONValue(current, incoming, incomingInfo.ModTime().After(currentInfo.ModTime()))
	data, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return err
	}
	return writeLinkedFile(target, data)
}

// preserveImportedFile 按账号保留有冲突的原始记录
func preserveImportedFile(shared, accountName, relative string, data []byte) error {
	path := filepath.Join(shared, ".ccbar-imports", accountName, relative)
	extension := filepath.Ext(path)
	stem := path[:len(path)-len(extension)]
	for number := 1; ; number++ {
		candidate := path
		if number > 1 {
			candidate = fmt.Sprintf("%s-%d%s", stem, number, extension)
		}
		existing, err := os.ReadFile(candidate)
		if os.IsNotExist(err) {
			return providers.WriteBytes(candidate, data)
		}
		if err != nil {
			return err
		}
		if bytes.Equal(existing, data) {
			return nil
		}
	}
}

// writeLinkedFile 更新文件内容而不拆断其他账号的硬链接
func writeLinkedFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	_, writeError := file.Write(data)
	closeError := file.Close()
	return errors.Join(writeError, closeError)
}

// cloneJSON 深拷贝 JSON 值
func cloneJSON(value any) any {
	data, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var copied any
	if json.Unmarshal(data, &copied) != nil {
		return value
	}
	return copied
}

// lockProfile 按 Claude Code 的目录锁约定锁定 .claude.json，已被占用时返回 false
func lockProfile(path string) (func(), bool, error) {
	lock := path + ".lock"
	if err := os.Mkdir(lock, 0700); err != nil {
		if os.IsExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return func() { os.Remove(lock) }, true, nil
}

// shareProfilePreferences 按上次同步结果三方合并全局偏好、项目状态与 MCP 定义并保持各账号 OAuth 身份独立
func shareProfilePreferences(directory, shared string) error {
	defaultPath := filepath.Join(filepath.Dir(shared), ".claude.json")
	profilePath := filepath.Join(directory, ".claude.json")
	for _, path := range []string{defaultPath, profilePath} {
		unlock, locked, err := lockProfile(path)
		if err != nil {
			return err
		}
		if !locked {
			return nil
		}
		defer unlock()
	}
	read := func(path string) (map[string]any, os.FileInfo, error) {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			return map[string]any{}, nil, nil
		}
		if err != nil {
			return nil, nil, err
		}
		value, err := providers.DecodeObject(data)
		if err == nil && value == nil {
			err = errors.New("Claude profile must be a JSON object")
		}
		if err != nil {
			return nil, nil, err
		}
		info, err := os.Stat(path)
		return value, info, err
	}
	current, currentInfo, err := read(defaultPath)
	if err != nil {
		return err
	}
	incoming, incomingInfo, err := read(profilePath)
	if err != nil {
		return err
	}
	basePath := filepath.Join(directory, profileBaseName)
	base, baseInfo, err := read(basePath)
	if err != nil {
		return err
	}
	if currentInfo == nil || incomingInfo == nil {
		base = map[string]any{}
	}
	preferIncoming := incomingInfo != nil && (currentInfo == nil || incomingInfo.ModTime().After(currentInfo.ModTime()))
	same := func(first any, hasFirst bool, second any, hasSecond bool) bool {
		return hasFirst == hasSecond && reflect.DeepEqual(first, second)
	}
	merged := map[string]any{}
	currentChanged, incomingChanged := false, false
	for _, key := range sharedProfileKeys {
		left, hasLeft := current[key]
		right, hasRight := incoming[key]
		previous, hasPrevious := base[key]
		value, hasValue := left, hasLeft
		switch {
		case same(left, hasLeft, right, hasRight):
		case same(previous, hasPrevious, left, hasLeft):
			value, hasValue = right, hasRight
		case same(previous, hasPrevious, right, hasRight):
		case !hasLeft:
			value, hasValue = right, hasRight
		case !hasRight:
		case baseInfo == nil:
			if key == "mcpServers" {
				if err := preserveMCPServers(shared, directory, left, right); err != nil {
					return err
				}
			}
			value = mergeJSONValue(cloneJSON(left), right, key != "mcpServers" && preferIncoming)
		case key == "projects" || key == "githubRepoPaths":
			value = mergeJSONValue(cloneJSON(left), right, preferIncoming)
		default:
			if key == "mcpServers" {
				if err := preserveMCPServers(shared, directory, left, right); err != nil {
					return err
				}
			}
			if preferIncoming {
				value = right
			}
		}
		if !hasValue {
			delete(current, key)
			delete(incoming, key)
			currentChanged = currentChanged || hasLeft
			incomingChanged = incomingChanged || hasRight
			continue
		}
		merged[key] = value
		if !same(left, hasLeft, value, true) {
			current[key], currentChanged = value, true
		}
		if !same(right, hasRight, value, true) {
			incoming[key], incomingChanged = value, true
		}
	}
	if currentChanged {
		if err := providers.WriteJSON(defaultPath, current); err != nil {
			return err
		}
	}
	if incomingChanged {
		if err := providers.WriteJSON(profilePath, incoming); err != nil {
			return err
		}
	}
	if baseInfo != nil && reflect.DeepEqual(base, merged) {
		return nil
	}
	return providers.WriteJSON(basePath, merged)
}

// preserveMCPServers 保留两侧不同的 MCP 定义原文
func preserveMCPServers(shared, directory string, current, incoming any) error {
	currentData, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return err
	}
	incomingData, err := json.MarshalIndent(incoming, "", "  ")
	if err != nil {
		return err
	}
	if err := preserveImportedFile(shared, "shared", "mcp-servers.json", currentData); err != nil {
		return err
	}
	return preserveImportedFile(shared, filepath.Base(directory), "mcp-servers.json", incomingData)
}
