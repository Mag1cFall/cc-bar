package accounts

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Mag1cFall/cc-bar/internal/model"
	"github.com/Mag1cFall/cc-bar/internal/providers"
	"github.com/google/uuid"
)

// saveDesktopClaude 将桌面已有 Code 登录保存为独立 CLI 账号
func (store *Store) saveDesktopClaude(credential *model.Credential) (ClaudeProfile, int, error) {
	if credential.Source != "Claude Desktop Code" || !slices.Contains(credential.Scopes, "user:inference") {
		return ClaudeProfile{}, -1, errors.New("请在 Claude Desktop 的 Code 页面打开一次会话，再保存登录")
	}
	profile := ClaudeProfile{ID: strings.ReplaceAll(uuid.NewString(), "-", ""), DesktopLinked: true, AccountUUID: credential.AccountUUID, OrganizationUUID: credential.OrganizationUUID}
	profile.ConfigDirectory = filepath.Join(store.dataDir, "claude-accounts", profile.ID)
	index := -1
	for existingIndex, existing := range store.claude {
		if existing.DesktopLinked && existing.AccountUUID == credential.AccountUUID && existing.OrganizationUUID == credential.OrganizationUUID && store.isManaged(existing) {
			profile, index = existing, existingIndex
			break
		}
	}
	if err := prepareHistory(profile.ConfigDirectory, filepath.Join(store.home, ".claude")); err != nil {
		return ClaudeProfile{}, -1, err
	}
	if err := store.writeDesktopClaude(profile, credential); err != nil {
		return ClaudeProfile{}, -1, err
	}
	return profile, index, nil
}

func (store *Store) writeDesktopClaude(profile ClaudeProfile, credential *model.Credential) error {
	if !store.isManaged(profile) {
		return errors.New("账号目录位置有误")
	}
	oauth := map[string]any{
		"accessToken":      credential.AccessToken,
		"refreshToken":     credential.RefreshToken,
		"scopes":           credential.Scopes,
		"subscriptionType": credential.SubscriptionType,
		"rateLimitTier":    credential.RateLimitTier,
	}
	if credential.ExpiresAt != nil {
		oauth["expiresAt"] = credential.ExpiresAt.UnixMilli()
	}
	if err := providers.WriteJSON(filepath.Join(profile.ConfigDirectory, ".credentials.json"), map[string]any{"claudeAiOauth": oauth}); err != nil {
		return err
	}
	path := store.profilePath(profile)
	identity := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		identity, err = providers.DecodeObject(data)
		if err != nil || identity == nil {
			return errors.New("账号身份文件格式有误")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	account, _ := identity["oauthAccount"].(map[string]any)
	if account == nil {
		if data, err := os.ReadFile(filepath.Join(store.home, ".claude.json")); err == nil {
			if defaults, err := providers.DecodeObject(data); err == nil {
				if cached, ok := defaults["oauthAccount"].(map[string]any); ok && cached["accountUuid"] == credential.AccountUUID {
					account = cached
				}
			}
		}
	}
	if account == nil {
		account = map[string]any{}
	}
	account["accountUuid"] = credential.AccountUUID
	account["organizationUuid"] = credential.OrganizationUUID
	account["emailAddress"] = credential.Email
	identity["oauthAccount"] = account
	return providers.WriteJSON(path, identity)
}
