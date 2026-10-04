package providers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Mag1cFall/cc-bar/internal/model"
)

// ClaudeOAuthClientID 标识官方 Code 的公开 OAuth 客户端
const ClaudeOAuthClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"

// ExchangeClaudeCode 兑换官方浏览器返回的 PKCE 授权码
func ExchangeClaudeCode(ctx context.Context, code, verifier, state, redirect string) (*model.Credential, error) {
	return claudeToken(ctx, map[string]string{
		"grant_type": "authorization_code", "client_id": ClaudeOAuthClientID,
		"code": code, "code_verifier": verifier, "state": state, "redirect_uri": redirect,
	})
}

// claudeToken 读取官方 OAuth 令牌与账号身份而隐藏响应中的敏感字段
func claudeToken(ctx context.Context, fields map[string]string) (*model.Credential, error) {
	body, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	root, err := requestJSON(ctx, http.MethodPost, "https://platform.claude.com/v1/oauth/token", nil, body, map[string]string{"User-Agent": "claude-code", "anthropic-beta": "oauth-2025-04-20"})
	if err != nil {
		return nil, err
	}
	credential := &model.Credential{
		AccessToken: str(root, "access_token"), RefreshToken: str(root, "refresh_token"),
		Scopes:      strings.Fields(str(root, "scope")),
		AccountUUID: str(obj(root["account"]), "uuid"), Email: str(obj(root["account"]), "email_address", "email"),
		OrganizationUUID: str(obj(root["organization"]), "uuid"),
	}
	if credential.AccessToken == "" || credential.RefreshToken == "" || val(root, "expires_in") <= 0 {
		return nil, errors.New("OAuth 响应缺少登录凭据")
	}
	expires := time.Now().Add(time.Duration(val(root, "expires_in")) * time.Second)
	credential.ExpiresAt = &expires
	return credential, nil
}

// FetchClaudeProfile 核对登录后的真实账号并读取邮箱与订阅信息
func FetchClaudeProfile(ctx context.Context, credential *model.Credential) error {
	root, err := requestJSON(ctx, http.MethodGet, "https://api.anthropic.com/api/oauth/profile", credential, nil, map[string]string{"anthropic-beta": "oauth-2025-04-20"})
	if err != nil {
		return err
	}
	account, organization := obj(root["account"]), obj(root["organization"])
	if account == nil {
		account = root
	}
	if value := str(account, "uuid", "account_uuid"); value != "" {
		if credential.AccountUUID != "" && credential.AccountUUID != value {
			return errors.New("登录账号与返回的身份不一致")
		}
		credential.AccountUUID = value
	}
	if value := str(account, "email", "email_address"); value != "" {
		credential.Email = value
	}
	if value := str(organization, "uuid", "organization_uuid"); value != "" {
		credential.OrganizationUUID = value
	}
	credential.SubscriptionType = str(organization, "subscription_type", "organization_type", "billing_type")
	credential.RateLimitTier = str(organization, "rate_limit_tier")
	if credential.AccountUUID == "" || credential.Email == "" || credential.OrganizationUUID == "" {
		return errors.New("登录后的账号资料尚未完整")
	}
	return nil
}
