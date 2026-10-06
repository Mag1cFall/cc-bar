package accounts

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/Mag1cFall/cc-bar/internal/providers"
	"github.com/google/uuid"
)

// desktopCodeDirectory 定位账号的原生 Code 记录目录
func desktopCodeDirectory(directory string, profile ClaudeProfile) (string, error) {
	for _, identity := range []string{profile.AccountUUID, profile.OrganizationUUID} {
		if _, err := uuid.Parse(identity); err != nil {
			return "", errors.New("Desktop 账号或组织标识有误")
		}
	}
	root, err := resolveDirectory(directory)
	if err != nil {
		return "", err
	}
	for _, part := range []string{"claude-code-sessions", profile.AccountUUID, profile.OrganizationUUID} {
		root = filepath.Join(root, part)
		info, err := os.Lstat(root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if !info.IsDir() || info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return "", errors.New("Desktop Code 记录目录包含链接或非目录文件")
		}
	}
	return root, nil
}

// syncDesktopCodeHistory 将最新本地会话与删除标记同步到所选账号
func syncDesktopCodeHistory(directory string, selected ClaudeProfile, profiles []ClaudeProfile) error {
	target, err := desktopCodeDirectory(directory, selected)
	if err != nil {
		return err
	}
	files := map[string]os.FileInfo{}
	sources := map[string]string{}
	for _, profile := range append(profiles, selected) {
		if !profile.DesktopSaved {
			continue
		}
		if profile.DesktopDirectory != "" && !sameDirectory(profile.DesktopDirectory, directory) {
			continue
		}
		source, err := desktopCodeDirectory(directory, profile)
		if err != nil {
			return err
		}
		entries, err := os.ReadDir(source)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			name := entry.Name()
			if !(strings.HasPrefix(name, "local_") && strings.HasSuffix(name, ".json")) && !strings.HasPrefix(name, "deleted_") {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return errors.New("Desktop Code 会话文件包含链接")
			}
			if current := files[entry.Name()]; current == nil || info.ModTime().After(current.ModTime()) {
				files[entry.Name()], sources[entry.Name()] = info, filepath.Join(source, entry.Name())
			}
		}
	}
	for name, source := range sources {
		destination := filepath.Join(target, name)
		if strings.HasPrefix(name, "local_") {
			marker := files["deleted_"+strings.TrimSuffix(strings.TrimPrefix(name, "local_"), ".json")]
			if marker != nil && !marker.ModTime().Before(files[name].ModTime()) {
				if err := os.Remove(destination); err != nil && !os.IsNotExist(err) {
					return err
				}
				continue
			}
		}
		if strings.HasPrefix(name, "deleted_") {
			if session := files["local_"+strings.TrimPrefix(name, "deleted_")+".json"]; session != nil && session.ModTime().After(files[name].ModTime()) {
				if err := os.Remove(destination); err != nil && !os.IsNotExist(err) {
					return err
				}
				continue
			}
		}
		if sameDirectory(source, destination) {
			continue
		}
		data, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(name, "local_") {
			if err := providers.WriteBytes(destination, data); err != nil {
				return err
			}
			modified := files[name].ModTime()
			if err := os.Chtimes(destination, modified, modified); err != nil {
				return err
			}
			continue
		}
		var record struct {
			CLISessionID  string `json:"cliSessionId"`
			RemoteTarget  any    `json:"remoteTarget"`
			SSHConfig     any    `json:"sshConfig"`
			MovedToCloud  any    `json:"movedToCloud"`
			BridgeSession any    `json:"bridgeSessionId"`
		}
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		if record.CLISessionID == "" || record.RemoteTarget != nil || record.SSHConfig != nil || record.MovedToCloud != nil || record.BridgeSession != nil {
			continue
		}
		if info, err := os.Lstat(destination); err == nil && !info.Mode().IsRegular() {
			return errors.New("Desktop Code 目标会话文件包含链接")
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := providers.WriteBytes(destination, data); err != nil {
			return err
		}
		modified := files[name].ModTime()
		if err := os.Chtimes(destination, modified, modified); err != nil {
			return err
		}
	}
	return nil
}
