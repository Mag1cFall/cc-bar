package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Mag1cFall/cc-bar/internal/model"
	"github.com/Mag1cFall/cc-bar/internal/providers"
	"github.com/Mag1cFall/cc-bar/internal/secrets"
)

type savedCodex struct {
	CodexAccount
	ProtectedToken        string `json:"protectedToken"`
	ProtectedRefreshToken string `json:"protectedRefreshToken,omitempty"`
}
type parsedCodex struct {
	id, accountID, userID, email, plan, access, refresh string
	personal                                            bool
}

func text(root map[string]any, key string) string {
	value, _ := root[key].(string)
	return value
}

func objectValue(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

// parseCodex 提取可导入账号并按身份去重
func parseCodex(textJSON string) ([]parsedCodex, error) {
	var value any
	if err := json.Unmarshal([]byte(strings.TrimSpace(textJSON)), &value); err != nil {
		return nil, errors.New("粘贴内容不是合法 JSON")
	}
	var items []any
	if list, ok := value.([]any); ok {
		items = list
	} else {
		items = []any{value}
	}
	var result []parsedCodex
	var lastError error
	for _, item := range items {
		root := objectValue(item)
		if root == nil {
			lastError = errors.New("JSON 必须包含账号对象")
			continue
		}
		tokens := objectValue(root["tokens"])
		if tokens == nil && text(root, "personal_access_token") != "" && text(root, "access_token") == "" {
			result = append(result, parsedCodex{access: strings.TrimSpace(text(root, "personal_access_token")), personal: true})
			continue
		}
		if tokens == nil && text(root, "access_token") != "" {
			tokens = root
		}
		if tokens == nil {
			lastError = errors.New("JSON 中缺少 tokens 字段")
			continue
		}
		access := text(tokens, "access_token")
		if access == "" {
			lastError = errors.New("tokens.access_token 缺失")
			continue
		}
		idToken := text(tokens, "id_token")
		accountID := text(tokens, "account_id")
		if accountID == "" {
			accountID = providers.Claim(access, "chatgpt_account_id")
		}
		if accountID == "" {
			accountID = providers.Claim(idToken, "chatgpt_account_id")
		}
		if accountID == "" {
			lastError = errors.New("JWT 缺少 chatgpt_account_id")
			continue
		}
		userID := providers.Claim(access, "chatgpt_user_id")
		if userID == "" {
			userID = providers.Claim(idToken, "chatgpt_user_id")
		}
		id := accountID
		if userID != "" {
			id += ":" + userID
		}
		plan := providers.Claim(idToken, "chatgpt_plan_type")
		if plan == "" {
			plan = providers.Claim(access, "chatgpt_plan_type")
		}
		parsed := parsedCodex{id: id, accountID: accountID, userID: userID, email: providers.Claim(idToken, "email"), plan: plan, access: access, refresh: text(tokens, "refresh_token")}
		for i := len(result) - 1; i >= 0; i-- {
			if result[i].id == id {
				result = append(result[:i], result[i+1:]...)
			}
		}
		result = append(result, parsed)
	}
	if len(result) == 0 {
		if lastError == nil {
			lastError = errors.New("JSON 未包含账号")
		}
		return nil, lastError
	}
	return result, nil
}

// PreviewCodex 解析导入身份且隐藏令牌
func PreviewCodex(textJSON string) ([]CodexPreview, error) {
	parsed, err := parseCodex(textJSON)
	if err != nil {
		return nil, err
	}
	result := []CodexPreview{}
	for _, entry := range parsed {
		result = append(result, CodexPreview{Email: entry.email, Plan: entry.plan, AccountID: entry.accountID, UserID: entry.userID, PersonalToken: entry.personal})
	}
	return result, nil
}

// saveCodexLocked 将加密令牌保存在原有元数据文件
func (store *Store) saveCodexLocked() error {
	entries := []savedCodex{}
	for _, entry := range store.codex {
		persisted := entry
		persisted.Snapshot = nil
		persisted.Error = ""
		persisted.IsRefreshing = false
		persisted.DisplayName = ""
		entries = append(entries, savedCodex{CodexAccount: persisted, ProtectedToken: entry.ProtectedToken, ProtectedRefreshToken: entry.ProtectedRefreshToken})
	}
	return providers.WriteJSON(filepath.Join(store.dataDir, "imported-codex.json"), entries)
}
func (store *Store) codexIndex(id string) int {
	for i := range store.codex {
		if store.codex[i].ID == id {
			return i
		}
	}
	return -1
}
func displayName(entry CodexAccount) string {
	if entry.Alias != "" {
		return entry.Alias
	}
	if entry.Email != "" {
		return strings.Split(entry.Email, "@")[0]
	}
	return entry.ID
}

// ListCodex 返回原有顺序的账号展示数据
func (store *Store) ListCodex() []CodexAccount {
	store.mu.Lock()
	defer store.mu.Unlock()
	result := append([]CodexAccount{}, store.codex...)
	for i := range result {
		result[i].DisplayName = displayName(result[i])
		result[i].ProtectedToken = ""
		result[i].ProtectedRefreshToken = ""
	}
	return result
}

// ImportCodex 支持 auth.json 数组和 PAT 导入
func (store *Store) ImportCodex(ctx context.Context, textJSON string, visible bool) (int, error) {
	entries, err := parseCodex(textJSON)
	if err != nil {
		return 0, err
	}
	type prepared struct {
		account  CodexAccount
		snapshot *model.QuotaSnapshot
	}
	var changes []prepared
	for _, entry := range entries {
		var snapshot *model.QuotaSnapshot
		if entry.personal {
			credential := &model.Credential{AccessToken: entry.access, IsPersonalAccessToken: true, Source: "导入"}
			var identity *model.Credential
			snapshot, identity, err = providers.FetchCodexIdentity(ctx, credential)
			if err != nil {
				return 0, err
			}
			if identity.AccountID == "" {
				return 0, errors.New("usage 响应缺少 account_id")
			}
			entry.accountID, entry.userID, entry.email, entry.plan = identity.AccountID, identity.UserID, identity.Email, identity.SubscriptionType
			entry.id = entry.accountID
			if entry.userID != "" {
				entry.id += ":" + entry.userID
			}
		}
		protected, err := secrets.Protect(entry.access)
		if err != nil {
			return 0, err
		}
		protectedRefresh := ""
		if entry.refresh != "" {
			protectedRefresh, err = secrets.Protect(entry.refresh)
			if err != nil {
				return 0, err
			}
		}
		changes = append(changes, prepared{
			account: CodexAccount{
				ID:                    entry.id,
				AccountID:             entry.accountID,
				Email:                 entry.email,
				PlanType:              entry.plan,
				ProtectedToken:        protected,
				ProtectedRefreshToken: protectedRefresh,
				VisibleInPopover:      visible,
				IsPersonalAccessToken: entry.personal,
			},
			snapshot: snapshot,
		})
	}
	store.mu.Lock()
	for _, change := range changes {
		account := change.account
		index := store.codexIndex(account.ID)
		if index >= 0 {
			previous := store.codex[index]
			account.Alias = previous.Alias
			if account.Email == "" {
				account.Email = previous.Email
			}
			if account.PlanType == "" {
				account.PlanType = previous.PlanType
			}
			account.Snapshot = change.snapshot
			store.codex[index] = account
		} else {
			account.Snapshot = change.snapshot
			store.codex = append(store.codex, account)
		}
	}
	err = store.saveCodexLocked()
	store.mu.Unlock()
	store.notify()
	return len(changes), err
}

// UpdateCodex 修改昵称与弹窗可见性
func (store *Store) UpdateCodex(id, alias string, visible bool) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	index := store.codexIndex(id)
	if index < 0 {
		return errors.New("account not found")
	}
	store.codex[index].Alias = strings.TrimSpace(alias)
	store.codex[index].VisibleInPopover = visible
	return store.saveCodexLocked()
}

// SaveCodexAccounts 保存昵称、可见性与界面排序
func (store *Store) SaveCodexAccounts(accounts []CodexAccount) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	result := make([]CodexAccount, 0, len(store.codex))
	seen := map[string]bool{}
	for _, update := range accounts {
		index := store.codexIndex(update.ID)
		if index < 0 || seen[update.ID] {
			continue
		}
		entry := store.codex[index]
		entry.Alias = strings.TrimSpace(update.Alias)
		entry.VisibleInPopover = update.VisibleInPopover
		result = append(result, entry)
		seen[entry.ID] = true
	}
	for _, entry := range store.codex {
		if !seen[entry.ID] {
			result = append(result, entry)
		}
	}
	store.codex = result
	return store.saveCodexLocked()
}

// RemoveCodex 删除保存的额度账号
func (store *Store) RemoveCodex(id string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	index := store.codexIndex(id)
	if index < 0 {
		return errors.New("account not found")
	}
	store.codex = append(store.codex[:index], store.codex[index+1:]...)
	return store.saveCodexLocked()
}

// codexCredential 解密所选账号并保存官方轮换后的令牌
func (store *Store) codexCredential(ctx context.Context, id string) (*model.Credential, error) {
	if id == "" || id == "current" {
		settings := model.DefaultSettings()
		credential, err := providers.Discover(model.Codex, settings)
		if err != nil {
			return nil, err
		}
		if credential == nil {
			return nil, errors.New("Codex 未登录")
		}
		return providers.EnsureFresh(ctx, model.Codex, credential)
	}
	store.mu.Lock()
	index := store.codexIndex(id)
	if index < 0 {
		store.mu.Unlock()
		return nil, errors.New("account not found")
	}
	entry := store.codex[index]
	store.mu.Unlock()
	access, err := secrets.Unprotect(entry.ProtectedToken)
	if err != nil {
		return nil, err
	}
	refresh := ""
	if entry.ProtectedRefreshToken != "" {
		refresh, err = secrets.Unprotect(entry.ProtectedRefreshToken)
		if err != nil {
			return nil, err
		}
	}
	credential := &model.Credential{
		AccessToken:           access,
		RefreshToken:          refresh,
		AccountID:             entry.AccountID,
		Email:                 entry.Email,
		SubscriptionType:      entry.PlanType,
		IsPersonalAccessToken: entry.IsPersonalAccessToken,
		ExpiresAt:             providers.TokenExpiry(access),
		Source:                "导入",
	}
	if separator := strings.IndexByte(entry.ID, ':'); separator >= 0 {
		credential.UserID = entry.ID[separator+1:]
	}
	if !providers.NeedsRefresh(credential) {
		return credential, nil
	}
	issued, err := providers.RefreshCodexToken(ctx, refresh)
	if err != nil {
		return nil, err
	}
	protected, err := secrets.Protect(issued.Access)
	if err != nil {
		return nil, err
	}
	protectedRefresh, err := secrets.Protect(issued.Refresh)
	if err != nil {
		return nil, err
	}
	store.mu.Lock()
	index = store.codexIndex(id)
	if index < 0 {
		store.mu.Unlock()
		return nil, errors.New("account was removed")
	}
	if store.codex[index].ProtectedToken != entry.ProtectedToken {
		store.mu.Unlock()
		return store.codexCredential(ctx, id)
	}
	store.codex[index].ProtectedToken, store.codex[index].ProtectedRefreshToken = protected, protectedRefresh
	err = store.saveCodexLocked()
	store.mu.Unlock()
	if err != nil {
		return nil, err
	}
	credential.AccessToken, credential.RefreshToken, credential.ExpiresAt = issued.Access, issued.Refresh, issued.ExpiresAt
	return credential, nil
}

// RefreshCodex 拉取导入账号额度并遵守限流退避
func (store *Store) RefreshCodex(ctx context.Context) error {
	store.refreshMu.Lock()
	defer store.refreshMu.Unlock()
	store.mu.Lock()
	entries := append([]CodexAccount{}, store.codex...)
	store.mu.Unlock()
	var failures []error
	for _, entry := range entries {
		backingOff := entry.BackoffUntil != nil && entry.BackoffUntil.After(time.Now())
		recentlyFetched := entry.Snapshot != nil && entry.Snapshot.FetchedAt.After(time.Now().Add(-time.Minute))
		if backingOff || recentlyFetched {
			continue
		}
		store.mu.Lock()
		if index := store.codexIndex(entry.ID); index >= 0 {
			store.codex[index].IsRefreshing = true
		}
		store.mu.Unlock()
		credential, err := store.codexCredential(ctx, entry.ID)
		var snapshot *model.QuotaSnapshot
		var identity *model.Credential
		if err == nil {
			snapshot, identity, err = providers.FetchCodexIdentity(ctx, credential)
		}
		store.mu.Lock()
		index := store.codexIndex(entry.ID)
		if index >= 0 {
			target := &store.codex[index]
			target.IsRefreshing = false
			if err == nil {
				target.Snapshot = preserveReset(snapshot, target.Snapshot)
				target.Error = ""
				target.BackoffUntil = nil
				if identity.Email != "" {
					target.Email = identity.Email
				}
				if identity.SubscriptionType != "" {
					target.PlanType = identity.SubscriptionType
				}
			} else {
				target.Error = err.Error()
				var failure *providers.Error
				if errors.As(err, &failure) && failure.StatusCode == 429 {
					duration := 10 * time.Minute
					if failure.RetryAfter > duration {
						duration = failure.RetryAfter
					}
					until := time.Now().Add(duration)
					target.BackoffUntil = &until
				}
			}
		}
		if saveError := store.saveCodexLocked(); saveError != nil {
			failures = append(failures, saveError)
		}
		store.mu.Unlock()
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", entry.ID, err))
		}
	}
	store.notify()
	return errors.Join(failures...)
}
