package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Mag1cFall/cc-bar/internal/model"
)

var refreshGate sync.Mutex

// IssuedToken 表示官方续期响应
type IssuedToken struct {
	Access, Refresh, IDToken string
	ExpiresAt                *time.Time
}

func postForm(ctx context.Context, endpoint string, form url.Values) (object, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := HTTPClient.Do(request)
	if err != nil {
		return nil, &Error{Kind: "network", Detail: err.Error()}
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &Error{Kind: "tokenRefreshFailed", StatusCode: response.StatusCode, Detail: http.StatusText(response.StatusCode)}
	}
	root, err := DecodeObject(data)
	if err != nil {
		return nil, &Error{Kind: "tokenRefreshFailed", Detail: "invalid token response"}
	}
	return root, nil
}

// RefreshCodexToken 使用官方 OAuth 刷新令牌
func RefreshCodexToken(ctx context.Context, refresh string) (*IssuedToken, error) {
	if refresh == "" {
		return nil, &Error{Kind: "tokenRefreshFailed", Detail: "refresh token missing"}
	}
	root, err := postForm(ctx, "https://auth.openai.com/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {"app_EMoamEEZ73f0CkXaXp7hrann"}, "scope": {"openid profile email"}})
	if err != nil {
		return nil, err
	}
	access := str(root, "access_token")
	if access == "" {
		return nil, &Error{Kind: "tokenRefreshFailed", Detail: "access token missing in response"}
	}
	issued := &IssuedToken{Access: access, Refresh: str(root, "refresh_token"), IDToken: str(root, "id_token"), ExpiresAt: TokenExpiry(access)}
	if issued.Refresh == "" {
		issued.Refresh = refresh
	}
	if issued.ExpiresAt == nil && val(root, "expires_in") > 0 {
		expiry := time.Now().Add(time.Duration(val(root, "expires_in")) * time.Second)
		issued.ExpiresAt = &expiry
	}
	return issued, nil
}

// NeedsRefresh 沿用过期前五分钟续期策略
func NeedsRefresh(credential *model.Credential) bool {
	return !credential.IsPersonalAccessToken && (credential.ExpiresAt == nil || !credential.ExpiresAt.After(time.Now().Add(5*time.Minute)))
}

// EnsureFresh 仅刷新 Codex 与 Antigravity 的官方登录
func EnsureFresh(ctx context.Context, app model.QuotaApp, credential *model.Credential) (*model.Credential, error) {
	if !NeedsRefresh(credential) {
		return credential, nil
	}
	refreshGate.Lock()
	defer refreshGate.Unlock()
	if app == model.Codex {
		fresh, err := ReadCodex(credential.Source)
		if err != nil {
			return nil, err
		}
		if fresh == nil {
			return nil, &Error{Kind: "missingToken", Detail: "Codex login was removed"}
		}
		if !NeedsRefresh(fresh) {
			return fresh, nil
		}
		credential = fresh
		refresh := credential.RefreshToken
		issued, err := RefreshCodexToken(ctx, refresh)
		if err != nil {
			return nil, err
		}
		result := *credential
		result.AccessToken, result.RefreshToken, result.ExpiresAt = issued.Access, issued.Refresh, issued.ExpiresAt
		root, err := readObject(credential.Source)
		if err != nil {
			return nil, fmt.Errorf("read token source: %w", err)
		}
		tokens := obj(root["tokens"])
		if tokens == nil {
			return nil, errors.New("token source has no tokens object")
		}
		if str(tokens, "refresh_token") != refresh {
			return ReadCodex(credential.Source)
		}
		tokens["access_token"], tokens["refresh_token"] = issued.Access, issued.Refresh
		if issued.IDToken != "" {
			tokens["id_token"] = issued.IDToken
		}
		now := time.Now().UTC()
		root["last_refresh"] = now.Format(time.RFC3339Nano)
		if err := WriteJSON(credential.Source, root); err != nil {
			return nil, fmt.Errorf("persist refreshed token: %w", err)
		}
		result.LastRefresh = &now
		return &result, nil
	}
	if app == model.Antigravity {
		return refreshAntigravity(ctx, credential)
	}
	return credential, nil
}

// refreshAntigravity 从本机官方组件读取客户端信息并保存续期结果
func refreshAntigravity(ctx context.Context, credential *model.Credential) (*model.Credential, error) {
	home := filepath.Dir(filepath.Dir(credential.Source))
	fresh, _ := ReadAntigravity(home)
	if fresh != nil && fresh.AccessToken != "" && !NeedsRefresh(fresh) {
		return fresh, nil
	}
	refresh := credential.RefreshToken
	if fresh != nil && fresh.RefreshToken != "" {
		refresh = fresh.RefreshToken
	}
	if refresh == "" {
		return nil, &Error{Kind: "tokenRefreshFailed", Detail: "Antigravity refresh token missing"}
	}
	const nativeID = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	const geminiID = "681255809395-oo8ft2oprdrnp9e3aqf6av3hmdib135j.apps.googleusercontent.com"
	secrets := googleClientSecrets(home)
	type candidate struct{ id, secret string }
	candidates := []candidate{{nativeID, secrets[nativeID]}, {geminiID, secrets[geminiID]}, {nativeID, ""}, {geminiID, ""}}
	for _, secret := range secrets {
		candidates = append(candidates, candidate{nativeID, secret}, candidate{geminiID, secret})
	}
	seen := map[candidate]bool{}
	var lastError error
	for _, item := range candidates {
		if seen[item] {
			continue
		}
		seen[item] = true
		form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {item.id}}
		if item.secret != "" {
			form.Set("client_secret", item.secret)
		}
		root, err := postForm(ctx, "https://oauth2.googleapis.com/token", form)
		if err != nil {
			lastError = err
			continue
		}
		access := str(root, "access_token")
		if access == "" {
			continue
		}
		expiry := time.Now().Add(time.Duration(firstValue(num(root, "expires_in"), 3600)) * time.Second)
		source, err := readObject(credential.Source)
		if err != nil {
			return nil, err
		}
		body := source
		if filepath.Base(credential.Source) == "jetski-standalone-oauth-token" {
			body = obj(source["token"])
			if body == nil {
				body = object{}
				source["token"] = body
			}
			body["expiry"] = expiry.UTC().Format(time.RFC3339Nano)
		} else {
			body["expiry_date"] = expiry.UnixMilli()
		}
		body["access_token"] = access
		if issued := str(root, "refresh_token"); issued != "" {
			body["refresh_token"] = issued
		}
		if err := WriteJSON(credential.Source, source); err != nil {
			return nil, err
		}
		return ReadAntigravity(home)
	}
	if lastError == nil {
		lastError = &Error{Kind: "tokenRefreshFailed", Detail: "Antigravity OAuth refresh failed"}
	}
	return nil, lastError
}
func firstValue(value *float64, fallback float64) float64 {
	if value != nil {
		return *value
	}
	return fallback
}

// googleClientSecrets 提取官方客户端组件内相邻的 OAuth 参数
func googleClientSecrets(home string) map[string]string {
	files := []string{filepath.Join(home, ".gemini", "bin", "agy.exe"), filepath.Join(home, ".gemini", "antigravity", "bin", "agy.exe")}
	for _, directory := range []string{filepath.Join(home, ".gemini", "node_modules", "@google", "gemini-cli", "bundle"), filepath.Join(os.Getenv("APPDATA"), "npm", "node_modules", "@google", "gemini-cli", "bundle")} {
		items, _ := filepath.Glob(filepath.Join(directory, "*.js"))
		files = append(files, items...)
	}
	result := map[string]string{}
	idPattern := regexp.MustCompile(`[0-9]{10,}-[a-z0-9]+\.apps\.googleusercontent\.com`)
	secretPattern := regexp.MustCompile(`GOCSPX-[A-Za-z0-9_-]{20,}`)
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		ids := idPattern.FindAllIndex(data, -1)
		for _, location := range secretPattern.FindAllIndex(data, -1) {
			distance := 512
			nearest := ""
			for _, id := range ids {
				delta := id[0] - location[0]
				if delta < 0 {
					delta = -delta
				}
				if delta < distance {
					distance = delta
					nearest = string(data[id[0]:id[1]])
				}
			}
			if nearest != "" {
				result[nearest] = strings.Split(string(data[location[0]:location[1]]), "http")[0]
			}
		}
	}
	return result
}

// fetchCommandCode 并行查询订阅与额度并补充账号名称
func fetchCommandCode(ctx context.Context, credential *model.Credential) (*model.QuotaSnapshot, error) {
	get := func(path string) (object, error) {
		root, err := requestJSON(ctx, http.MethodGet, "https://api.commandcode.ai"+path, credential, nil, map[string]string{"User-Agent": "cc-bar"})
		if value := obj(root["data"]); value != nil {
			return value, err
		}
		return root, err
	}
	identity, err := get("/alpha/whoami")
	if err != nil {
		return nil, err
	}
	user := obj(identity["user"])
	if login := str(user, "userName"); login != "" {
		credential.Login = login
	}
	if email := str(user, "email"); email != "" {
		credential.Email = email
	}
	query := ""
	if org := str(obj(identity["org"]), "id"); org != "" {
		query = "?orgId=" + url.QueryEscape(org)
	}
	type result struct {
		root object
		err  error
	}
	channel := make(chan result, 1)
	go func() {
		root, err := get("/alpha/billing/subscriptions")
		channel <- result{root, err}
	}()
	credits, err := get("/alpha/billing/credits" + query)
	subscription := <-channel
	if err != nil {
		return nil, err
	}
	return ParseCommandCode(credits, subscription.root), nil
}
