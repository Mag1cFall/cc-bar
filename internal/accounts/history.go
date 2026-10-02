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

	"github.com/Mag1cFall/cc-bar/internal/providers"
)

var sharedDirectories = []string{"projects", "tasks", "plans", "todos", "session-env"}

// historyLinked 检查账号目录是否引用同一份会话与历史
func historyLinked(directory, shared string) bool {
	for _, name := range sharedDirectories {
		current, err := resolveDirectory(filepath.Join(directory, name))
		if err != nil {
			return false
		}
		target, err := resolveDirectory(filepath.Join(shared, name))
		if err != nil || !sameDirectory(current, target) {
			return false
		}
	}
	for _, name := range []string{"history.jsonl", "settings.json"} {
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

// prepareHistory 合并已有会话后建立共享目录与历史索引
func prepareHistory(directory, shared string) error {
	if sameDirectory(directory, shared) {
		return nil
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	for _, name := range sharedDirectories {
		if err := shareDirectory(directory, shared, name); err != nil {
			return err
		}
	}
	if err := shareFile(directory, shared, "history.jsonl", nil); err != nil {
		return err
	}
	if err := shareFile(directory, shared, "settings.json", []byte("{}\n")); err != nil {
		return err
	}
	return shareMCPPreferences(directory, shared)
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

// shareFile 合并历史或设置并保留同一份文件的硬链接
func shareFile(directory, shared, name string, initial []byte) error {
	target := filepath.Join(shared, name)
	if _, err := os.Stat(target); os.IsNotExist(err) {
		if err := providers.WriteBytes(target, initial); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	local := filepath.Join(directory, name)
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
		} else {
			if err := mergeJSONFile(target, local, shared, filepath.Base(directory), name); err != nil {
				return err
			}
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

// shareMCPPreferences 仅同步 MCP 定义并保持各账号 OAuth 身份独立
func shareMCPPreferences(directory, shared string) error {
	defaultPath := filepath.Join(filepath.Dir(shared), ".claude.json")
	profilePath := filepath.Join(directory, ".claude.json")
	read := func(path string) (map[string]any, error) {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		if err != nil {
			return nil, err
		}
		value, err := providers.DecodeObject(data)
		if err == nil && value == nil {
			err = errors.New("Claude profile must be a JSON object")
		}
		return value, err
	}
	current, err := read(defaultPath)
	if err != nil {
		return err
	}
	incoming, err := read(profilePath)
	if err != nil {
		return err
	}
	servers, currentServers := current["mcpServers"].(map[string]any)
	profileServers, incomingServers := incoming["mcpServers"].(map[string]any)
	if !currentServers && !incomingServers {
		return nil
	}
	if servers == nil {
		servers = map[string]any{}
	}
	if currentServers && incomingServers && !reflect.DeepEqual(servers, profileServers) {
		currentData, err := json.MarshalIndent(servers, "", "  ")
		if err != nil {
			return err
		}
		profileData, err := json.MarshalIndent(profileServers, "", "  ")
		if err != nil {
			return err
		}
		if err := preserveImportedFile(shared, "shared", "mcp-servers.json", currentData); err != nil {
			return err
		}
		if err := preserveImportedFile(shared, filepath.Base(directory), "mcp-servers.json", profileData); err != nil {
			return err
		}
	}
	merged := mergeJSONValue(servers, profileServers, false)
	current["mcpServers"] = merged
	incoming["mcpServers"] = merged
	if err := providers.WriteJSON(defaultPath, current); err != nil {
		return err
	}
	return providers.WriteJSON(profilePath, incoming)
}
