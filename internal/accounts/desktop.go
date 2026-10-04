package accounts

import (
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/Mag1cFall/cc-bar/internal/model"
	"github.com/Mag1cFall/cc-bar/internal/providers"
)

// hasCompleteCodeLogin 检查可刷新且具备 Code 与账号查询权限的登录
func hasCompleteCodeLogin(credential *model.Credential) bool {
	if credential == nil || credential.AccessToken == "" || credential.RefreshToken == "" {
		return false
	}
	return slices.Contains(credential.Scopes, "user:inference") && slices.Contains(credential.Scopes, "user:profile")
}

// writeDesktopClaude 写入独立账号的 Code 凭据与身份
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
