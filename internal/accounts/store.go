package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Mag1cFall/cc-bar/internal/model"
	"github.com/Mag1cFall/cc-bar/internal/providers"
	"github.com/google/uuid"
)

// Store 管理官方 Claude 登录与导入 Codex 账号
type Store struct {
	mu            sync.Mutex
	refreshMu     sync.Mutex
	dataDir, home string
	claude        []ClaudeProfile
	codex         []CodexAccount
	Changed       func()
}

// New 加载原有 Windows 账号数据
func New(dataDir, home string) (*Store, error) {
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return nil, err
		}
	}
	if err := loadUserClaudeDirectory(home); err != nil {
		return nil, err
	}
	if dataDir == "" {
		dataDir = filepath.Join(os.Getenv("LOCALAPPDATA"), "CCBar")
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, err
	}
	store := &Store{dataDir: dataDir, home: home, claude: []ClaudeProfile{}, codex: []CodexAccount{}}
	if data, err := os.ReadFile(filepath.Join(dataDir, "claude-accounts.json")); err == nil {
		if err = json.Unmarshal(data, &store.claude); err != nil {
			return nil, fmt.Errorf("claude-accounts.json: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if data, err := os.ReadFile(filepath.Join(dataDir, "imported-codex.json")); err == nil {
		var entries []savedCodex
		if err = json.Unmarshal(data, &entries); err != nil {
			return nil, fmt.Errorf("imported-codex.json: %w", err)
		}
		for _, entry := range entries {
			account := entry.CodexAccount
			account.ProtectedToken, account.ProtectedRefreshToken = entry.ProtectedToken, entry.ProtectedRefreshToken
			store.codex = append(store.codex, account)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	for i := range store.claude {
		store.reloadClaudeLocked(&store.claude[i])
		store.claude[i].IsLoggingIn = false
	}
	return store, nil
}

func (store *Store) notify() {
	if store.Changed != nil {
		store.Changed()
	}
}
func (store *Store) saveClaudeLocked() error {
	items := append([]ClaudeProfile(nil), store.claude...)
	for i := range items {
		items[i].IsLoggingIn = false
		items[i].IsActive = false
		items[i].Error = ""
		items[i].HistoryShared = false
	}
	return providers.WriteJSON(filepath.Join(store.dataDir, "claude-accounts.json"), items)
}
func sameDirectory(a, b string) bool {
	first, err := filepath.Abs(a)
	if err != nil {
		return false
	}
	second, err := filepath.Abs(b)
	if err != nil {
		return false
	}
	if strings.EqualFold(filepath.Clean(first), filepath.Clean(second)) {
		return true
	}
	first, err = longPath(first)
	if err != nil {
		return false
	}
	second, err = longPath(second)
	return err == nil && strings.EqualFold(first, second)
}
func (store *Store) claudeIndex(id string) int {
	for i := range store.claude {
		if store.claude[i].ID == id {
			return i
		}
	}
	return -1
}
func (store *Store) profilePath(profile ClaudeProfile) string {
	if profile.UsesDefaultConfig {
		return filepath.Join(store.home, ".claude.json")
	}
	return filepath.Join(profile.ConfigDirectory, ".claude.json")
}
func (store *Store) readClaude(profile ClaudeProfile) (*model.Credential, error) {
	return providers.ReadClaude(profile.ConfigDirectory, store.profilePath(profile))
}
func (store *Store) isManaged(profile ClaudeProfile) bool {
	return profile.ID != "" && !strings.ContainsAny(profile.ID, "/\\") && sameDirectory(profile.ConfigDirectory, filepath.Join(store.dataDir, "claude-accounts", profile.ID))
}

// reloadClaudeLocked 读取官方轮换并保留未更新凭据的失效状态
func (store *Store) reloadClaudeLocked(profile *ClaudeProfile) {
	directory, _ := providers.ClaudePaths(store.home)
	profile.IsActive = sameDirectory(profile.ConfigDirectory, directory) && profile.UsesDefaultConfig == (os.Getenv("CLAUDE_CONFIG_DIR") == "")
	profile.HistoryShared = profile.UsesDefaultConfig || historyLinked(profile.ConfigDirectory, filepath.Join(store.home, ".claude"))
	credential, err := store.readClaude(*profile)
	if profile.DesktopLinked && store.isManaged(*profile) {
		desktop, desktopErr := providers.ReadClaudeDesktop(&model.Credential{AccountUUID: profile.AccountUUID, OrganizationUUID: profile.OrganizationUUID})
		matchingDesktop := desktopErr == nil && desktop != nil && desktop.Source == "Claude Desktop Code" && desktop.AccountUUID == profile.AccountUUID
		if matchingDesktop {
			newerLogin := credential == nil || desktop.ExpiresAt != nil && (credential.ExpiresAt == nil || desktop.ExpiresAt.After(*credential.ExpiresAt))
			if newerLogin {
				if err = store.writeDesktopClaude(*profile, desktop); err == nil {
					credential = desktop
				}
			}
		}
	}
	if err != nil {
		profile.NeedsLogin = true
		profile.Error = "登录信息读取失败"
		return
	}
	if credential == nil {
		profile.NeedsLogin = true
		return
	}
	if profile.AccountUUID != "" && credential.AccountUUID != "" && profile.AccountUUID != credential.AccountUUID {
		profile.Snapshot = nil
		profile.BackoffUntil = nil
	}
	if credential.Email != "" {
		profile.Email = credential.Email
	}
	if credential.AccountUUID != "" {
		profile.AccountUUID = credential.AccountUUID
	}
	if credential.OrganizationUUID != "" {
		profile.OrganizationUUID = credential.OrganizationUUID
	}
	if credential.SubscriptionType != "" {
		profile.Plan = credential.SubscriptionType
	}
	if info, err := os.Stat(filepath.Join(profile.ConfigDirectory, ".credentials.json")); err == nil {
		written := info.ModTime()
		if profile.CredentialsUpdatedAt == nil || !profile.CredentialsUpdatedAt.Equal(written) {
			profile.NeedsLogin = false
			profile.Error = ""
		}
		profile.CredentialsUpdatedAt = &written
	}
	if credential.ExpiresAt != nil && !credential.ExpiresAt.After(time.Now()) {
		profile.NeedsLogin = true
		profile.Error = "启动 Claude Code 更新登录"
	}
}

// ListClaude 返回无凭据的账号状态
func (store *Store) ListClaude() []ClaudeProfile {
	store.mu.Lock()
	defer store.mu.Unlock()
	for i := range store.claude {
		store.reloadClaudeLocked(&store.claude[i])
	}
	return append([]ClaudeProfile{}, store.claude...)
}

// SaveCurrentClaude 将当前官方登录加入列表
func (store *Store) SaveCurrentClaude() (*ClaudeProfile, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	directory, _ := providers.ClaudePaths(store.home)
	usesDefault := os.Getenv("CLAUDE_CONFIG_DIR") == ""
	profile := ClaudeProfile{ID: uuid.NewString(), ConfigDirectory: directory, UsesDefaultConfig: usesDefault}
	index := -1
	for i, item := range store.claude {
		if sameDirectory(item.ConfigDirectory, directory) && item.UsesDefaultConfig == usesDefault {
			index = i
			profile = item
			break
		}
	}
	credential, err := store.readClaude(profile)
	if err != nil {
		return nil, err
	}
	if credential == nil || credential.ExpiresAt != nil && !credential.ExpiresAt.After(time.Now()) {
		desktop, desktopErr := providers.ReadClaudeDesktop(credential)
		if desktopErr != nil {
			return nil, desktopErr
		}
		if desktop != nil {
			profile, index, err = store.saveDesktopClaude(desktop)
			if err != nil {
				return nil, err
			}
			credential = desktop
		} else if credential == nil {
			return nil, errors.New("请先在 Claude Code 或 Claude Desktop 完成登录")
		}
	}
	if profile.Name == "" {
		profile.Name = strings.Split(credential.Email, "@")[0]
		if profile.Name == "" {
			profile.Name = "Claude 账号"
		}
	}
	store.reloadClaudeLocked(&profile)
	if index < 0 {
		store.claude = append(store.claude, profile)
	} else {
		store.claude[index] = profile
	}
	if err := store.saveClaudeLocked(); err != nil {
		return nil, err
	}
	return &profile, nil
}

// AddClaude 为新账号创建独立官方目录
func (store *Store) AddClaude(name string) (*ClaudeProfile, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	profile := ClaudeProfile{ID: strings.ReplaceAll(uuid.NewString(), "-", ""), Name: strings.TrimSpace(name), NeedsLogin: true}
	if profile.Name == "" {
		profile.Name = "新账号"
	}
	profile.ConfigDirectory = filepath.Join(store.dataDir, "claude-accounts", profile.ID)
	if err := os.MkdirAll(profile.ConfigDirectory, 0700); err != nil {
		return nil, err
	}
	if err := prepareHistory(profile.ConfigDirectory, filepath.Join(store.home, ".claude")); err != nil {
		return nil, err
	}
	profile.HistoryShared = true
	store.claude = append(store.claude, profile)
	if err := store.saveClaudeLocked(); err != nil {
		return nil, err
	}
	return &profile, nil
}

// RenameClaude 修改账号名称
func (store *Store) RenameClaude(id, name string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	index := store.claudeIndex(id)
	if index < 0 {
		return errors.New("account not found")
	}
	if name = strings.TrimSpace(name); name == "" {
		return errors.New("account name is empty")
	}
	store.claude[index].Name = name
	return store.saveClaudeLocked()
}

// RemoveClaude 移除账号列表并保留官方登录与会话文件
func (store *Store) RemoveClaude(id string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	index := store.claudeIndex(id)
	if index < 0 {
		return errors.New("account not found")
	}
	store.claude = append(store.claude[:index], store.claude[index+1:]...)
	return store.saveClaudeLocked()
}

// RefreshClaude 遵守额度退避并隔离切换期间的旧响应
func (store *Store) RefreshClaude(ctx context.Context) error {
	store.refreshMu.Lock()
	defer store.refreshMu.Unlock()
	profiles := store.ListClaude()
	var failures []error
	for _, profile := range profiles {
		backingOff := profile.BackoffUntil != nil && profile.BackoffUntil.After(time.Now())
		recentlyFetched := profile.Snapshot != nil && profile.Snapshot.FetchedAt.After(time.Now().Add(-time.Minute))
		if profile.NeedsLogin || profile.IsLoggingIn || backingOff || recentlyFetched {
			continue
		}
		credential, err := store.readClaude(profile)
		if err != nil || credential == nil {
			continue
		}
		if credential.ExpiresAt != nil && credential.ExpiresAt.Before(time.Now()) {
			store.mu.Lock()
			if index := store.claudeIndex(profile.ID); index >= 0 {
				store.claude[index].Error = "启动 Claude Code 更新登录"
			}
			store.mu.Unlock()
			continue
		}
		snapshot, fetchError := providers.Fetch(ctx, model.Claude, credential)
		current, _ := store.readClaude(profile)
		if current == nil || current.AccountUUID != credential.AccountUUID || current.AccessToken != credential.AccessToken {
			continue
		}
		store.mu.Lock()
		index := store.claudeIndex(profile.ID)
		if index >= 0 {
			target := &store.claude[index]
			if fetchError == nil {
				target.Snapshot = preserveReset(snapshot, target.Snapshot)
				target.Error = ""
				target.BackoffUntil = nil
			} else {
				target.Error = fetchError.Error()
				var failure *providers.Error
				if errors.As(fetchError, &failure) {
					if failure.StatusCode == 429 {
						duration := 10 * time.Minute
						if failure.RetryAfter > duration {
							duration = failure.RetryAfter
						}
						until := time.Now().Add(duration)
						target.BackoffUntil = &until
					}
					if failure.IsAuthFailure() {
						target.NeedsLogin = true
					}
				}
			}
			if err := store.saveClaudeLocked(); err != nil {
				failures = append(failures, err)
			}
		}
		store.mu.Unlock()
		if fetchError != nil {
			failures = append(failures, fetchError)
		}
	}
	store.notify()
	return errors.Join(failures...)
}

func preserveReset(next, previous *model.QuotaSnapshot) *model.QuotaSnapshot {
	if next == nil || previous == nil {
		return next
	}
	prior := map[string]model.QuotaLimit{}
	for _, entry := range previous.AllLimits() {
		prior[entry.ID] = entry
	}
	update := func(limit *model.QuotaLimit) {
		if limit == nil {
			return
		}
		if old, ok := prior[limit.ID]; ok && limit.Window.ResetsAt == nil && old.Window.ResetsAt != nil && old.Window.ResetsAt.After(time.Now()) {
			limit.Window.ResetsAt = old.Window.ResetsAt
		}
	}
	update(next.PrimaryLimit)
	update(next.SecondaryLimit)
	for i := range next.AuxiliaryLimits {
		update(&next.AuxiliaryLimits[i])
	}
	for i := range next.ModelLimits {
		update(&next.ModelLimits[i])
	}
	return next
}
