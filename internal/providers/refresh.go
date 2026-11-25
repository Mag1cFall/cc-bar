package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Mag1cFall/cc-bar/internal/model"
)

var (
	refreshGate            sync.Mutex
	antigravityRefreshGate sync.Mutex
)

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
	return !credential.IsPersonalAccessToken && (credential.AccessToken == "" || credential.ExpiresAt == nil || !credential.ExpiresAt.After(time.Now().Add(5*time.Minute)))
}

// EnsureFresh 仅刷新 Codex 与 Antigravity 的官方登录
func EnsureFresh(ctx context.Context, app model.QuotaApp, credential *model.Credential) (*model.Credential, error) {
	if !NeedsRefresh(credential) {
		return credential, nil
	}
	if app == model.Codex {
		refreshGate.Lock()
		defer refreshGate.Unlock()
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

// forceRefreshAntigravity 在收到认证失效后加锁强制刷新 Antigravity 令牌
func forceRefreshAntigravity(ctx context.Context, credential *model.Credential) (*model.Credential, error) {
	antigravityRefreshGate.Lock()
	defer antigravityRefreshGate.Unlock()
	return refreshAntigravityLocked(ctx, credential, true)
}

// refreshAntigravity 从本机官方组件读取客户端信息并保存续期结果
func refreshAntigravity(ctx context.Context, credential *model.Credential) (*model.Credential, error) {
	antigravityRefreshGate.Lock()
	defer antigravityRefreshGate.Unlock()
	return refreshAntigravityLocked(ctx, credential, false)
}

type antigravityOAuthPair struct {
	id     string
	secret string
}

type scannedOAuthFile struct {
	size    int64
	modTime int64
	paired  map[string]string
	ids     []string
	secrets []string
}

var (
	antigravityClientMu    sync.Mutex
	antigravityClientCache = map[string]antigravityOAuthPair{}
	oauthScanMu            sync.Mutex
	oauthScanCache         = map[string]scannedOAuthFile{}
	antigravityBinaryPaths = defaultAntigravityBinaryPaths
	antigravityIDPattern   = regexp.MustCompile(`[0-9]{10,}-[a-z0-9]+\.apps\.googleusercontent\.com`)
	antigravitySecPattern  = regexp.MustCompile(`GOCSPX-[A-Za-z0-9_-]{28}`)
)

// antigravityHomeForSource 从文件路径反推主目录并在系统凭据库来源下回退当前用户目录
func antigravityHomeForSource(source string) string {
	if source != "" && source != antigravityKeyringSource {
		directory := filepath.Dir(source)
		for {
			if strings.EqualFold(filepath.Base(directory), ".gemini") {
				return filepath.Dir(directory)
			}
			parent := filepath.Dir(directory)
			if parent == directory {
				break
			}
			directory = parent
		}
	}
	home, _ := os.UserHomeDir()
	return home
}

func refreshAntigravityLocked(ctx context.Context, credential *model.Credential, force bool) (*model.Credential, error) {
	home := antigravityHomeForSource(credential.Source)
	raw, rawErr := readAntigravityRaw(credential.Source)
	canPersist := rawErr == nil || os.IsNotExist(rawErr)
	fresh := parseAntigravityCredential(raw, credential.Source)
	if fresh != nil && fresh.AccessToken != "" && !NeedsRefresh(fresh) && (!force || fresh.AccessToken != credential.AccessToken) {
		if fresh.Email == "" && fresh.RefreshToken == credential.RefreshToken {
			fresh.Email = credential.Email
		}
		return fresh, nil
	}
	refresh := credential.RefreshToken
	if fresh != nil && fresh.RefreshToken != "" {
		refresh = fresh.RefreshToken
	}
	if refresh == "" {
		return nil, &Error{Kind: "tokenRefreshFailed", Detail: "Antigravity refresh token missing"}
	}
	authMethod := str(raw, "auth_method")
	wifProvider := str(raw, "wif_provider")
	applyRefreshed := func(root object) (*model.Credential, error) {
		access := str(root, "access_token")
		if access == "" {
			return nil, nil
		}
		expiry := time.Now().Add(time.Duration(firstValue(num(root, "expires_in"), 3600)) * time.Second)
		issuedRefresh := str(root, "refresh_token")
		idToken := str(root, "id_token")
		if canPersist {
			source := raw
			if source == nil {
				source = object{}
			}
			nestedFormat := credential.Source == antigravityKeyringSource || obj(source["token"]) != nil || filepath.Base(credential.Source) == "jetski-standalone-oauth-token"
			if nestedFormat {
				body := obj(source["token"])
				if body == nil {
					body = object{}
					source["token"] = body
				}
				body["access_token"] = access
				if str(body, "token_type") == "" {
					body["token_type"] = "Bearer"
				}
				if issuedRefresh != "" {
					body["refresh_token"] = issuedRefresh
				} else if str(body, "refresh_token") == "" {
					body["refresh_token"] = refresh
				}
				body["expiry"] = expiry.UTC().Format(time.RFC3339Nano)
			} else {
				source["access_token"] = access
				source["expiry_date"] = expiry.UnixMilli()
				if issuedRefresh != "" {
					source["refresh_token"] = issuedRefresh
				}
				if idToken != "" {
					source["id_token"] = idToken
				}
			}
			if credential.Source == antigravityKeyringSource {
				data, err := json.Marshal(source)
				if err != nil {
					return nil, err
				}
				if err := writeAntigravityKeyring(data); err != nil {
					return nil, err
				}
			} else if credential.Source != "" {
				if err := WriteJSON(credential.Source, source); err != nil {
					return nil, err
				}
			}
		}
		updated := *credential
		if fresh != nil && fresh.RefreshToken != "" && fresh.RefreshToken != credential.RefreshToken {
			updated.Email = fresh.Email
		}
		updated.AccessToken = access
		updated.RefreshToken = refresh
		if issuedRefresh != "" {
			updated.RefreshToken = issuedRefresh
		}
		updated.ExpiresAt = &expiry
		if updated.Email == "" {
			updated.Email = Claim(idToken, "email")
		}
		if updated.Email == "" && fresh != nil {
			updated.Email = fresh.Email
		}
		return &updated, nil
	}
	if wifProvider != "" {
		const wifClientID = "a2a6f0e3e-3ff0-462b-869a-47277df4c806"
		root, err := postForm(ctx, "https://sts.googleapis.com/v1/oauthtoken", url.Values{
			"grant_type":    {"refresh_token"},
			"refresh_token": {refresh},
			"client_id":     {wifClientID},
		})
		if err != nil {
			return nil, err
		}
		updated, err := applyRefreshed(root)
		if err != nil || updated != nil {
			return updated, err
		}
		return nil, &Error{Kind: "tokenRefreshFailed", Detail: "Antigravity WIF access token missing"}
	}
	const googleClientDomain = ".apps." + "googleusercontent.com"
	const nativeID = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep" + googleClientDomain
	const cloudID = "884354919052-36trc1jjb3tguiac32ov6cod268c5blh" + googleClientDomain
	const geminiID = "681255809395-oo8ft2oprdrnp9e3aqf6av3hmdib135j" + googleClientDomain
	exchangePair := func(item antigravityOAuthPair) (object, error) {
		form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {item.id}}
		if item.secret != "" {
			form.Set("client_secret", item.secret)
		}
		return postForm(ctx, "https://oauth2.googleapis.com/token", form)
	}
	var lastError error
	antigravityClientMu.Lock()
	cached, hasCached := antigravityClientCache[authMethod]
	antigravityClientMu.Unlock()
	if hasCached {
		root, err := exchangePair(cached)
		if err == nil && str(root, "access_token") != "" {
			return applyRefreshed(root)
		}
		if err != nil {
			lastError = err
			if ctx.Err() != nil {
				return nil, err
			}
		}
	}
	paired, extractedIDs, extractedSecrets := googleClientSecrets(home)
	clientIDs := []string{nativeID, geminiID, cloudID}
	if authMethod == "gcp" {
		clientIDs = []string{cloudID, nativeID, geminiID}
	}
	clientIDs = append(clientIDs, extractedIDs...)
	candidates := make([]antigravityOAuthPair, 0, len(clientIDs)*(len(extractedSecrets)+2))
	seenID := map[string]bool{}
	for _, id := range clientIDs {
		if id == "" || seenID[id] {
			continue
		}
		seenID[id] = true
		if secret := paired[id]; secret != "" {
			candidates = append(candidates, antigravityOAuthPair{id: id, secret: secret})
		}
		for _, secret := range extractedSecrets {
			candidates = append(candidates, antigravityOAuthPair{id: id, secret: secret})
		}
		candidates = append(candidates, antigravityOAuthPair{id: id})
	}
	seen := map[antigravityOAuthPair]bool{}
	if hasCached {
		seen[cached] = true
	}
	for _, item := range candidates {
		if seen[item] {
			continue
		}
		seen[item] = true
		root, err := exchangePair(item)
		if err != nil {
			lastError = err
			if ctx.Err() != nil {
				break
			}
			continue
		}
		if str(root, "access_token") != "" {
			antigravityClientMu.Lock()
			antigravityClientCache[authMethod] = item
			antigravityClientMu.Unlock()
			return applyRefreshed(root)
		}
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

func defaultAntigravityBinaryPaths(home string) (binaries, bundles []string) {
	addBinary := func(path string) {
		if path == "" {
			return
		}
		for _, existing := range binaries {
			if strings.EqualFold(existing, path) {
				return
			}
		}
		binaries = append(binaries, path)
	}
	addBinary(strings.TrimSpace(os.Getenv("AGY_BIN")))
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		addBinary(filepath.Join(local, "agy", "bin", "agy.exe"))
	}
	if resolved, err := exec.LookPath("agy"); err == nil {
		addBinary(resolved)
	}
	if installDir := findAntigravityInstallDir(); installDir != "" {
		addBinary(filepath.Join(installDir, "resources", "bin", "language_server.exe"))
	}
	if home != "" {
		addBinary(filepath.Join(home, ".gemini", "bin", "agy.exe"))
		addBinary(filepath.Join(home, ".gemini", "antigravity", "bin", "agy.exe"))
	}
	bundleDirs := []string{}
	if home != "" {
		bundleDirs = append(bundleDirs, filepath.Join(home, ".gemini", "node_modules", "@google", "gemini-cli", "bundle"))
	}
	if appData := os.Getenv("APPDATA"); appData != "" {
		bundleDirs = append(bundleDirs, filepath.Join(appData, "npm", "node_modules", "@google", "gemini-cli", "bundle"))
	}
	for _, directory := range bundleDirs {
		items, _ := filepath.Glob(filepath.Join(directory, "*.js"))
		bundles = append(bundles, items...)
	}
	return binaries, bundles
}

// googleClientSecrets 流式扫描本机官方组件提取去重后的 OAuth 参数并按文件元信息缓存
func googleClientSecrets(home string) (map[string]string, []string, []string) {
	binaries, bundles := antigravityBinaryPaths(home)
	paired := map[string]string{}
	var ids, secrets []string
	seenID, seenSecret := map[string]bool{}, map[string]bool{}
	merge := func(entry scannedOAuthFile) {
		for k, v := range entry.paired {
			if paired[k] == "" {
				paired[k] = v
			}
		}
		for _, id := range entry.ids {
			if !seenID[id] {
				seenID[id] = true
				ids = append(ids, id)
			}
		}
		for _, secret := range entry.secrets {
			if !seenSecret[secret] {
				seenSecret[secret] = true
				secrets = append(secrets, secret)
			}
		}
	}
	for _, path := range binaries {
		if entry, ok := scanOAuthFile(path); ok {
			merge(entry)
		}
	}
	for _, path := range bundles {
		if entry, ok := scanOAuthFile(path); ok {
			merge(entry)
		}
	}
	return paired, ids, secrets
}

// scanOAuthFile 分块流式扫描单个组件文件以提取 client ID 与精确 35 字符密钥
func scanOAuthFile(path string) (scannedOAuthFile, bool) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return scannedOAuthFile{}, false
	}
	oauthScanMu.Lock()
	if cached, ok := oauthScanCache[path]; ok && cached.size == info.Size() && cached.modTime == info.ModTime().UnixNano() {
		oauthScanMu.Unlock()
		return cached, true
	}
	oauthScanMu.Unlock()
	file, err := os.Open(path)
	if err != nil {
		return scannedOAuthFile{}, false
	}
	defer file.Close()
	const chunkSize = 4 << 20
	const overlapSize = 256
	buffer := make([]byte, chunkSize+overlapSize)
	entry := scannedOAuthFile{size: info.Size(), modTime: info.ModTime().UnixNano(), paired: map[string]string{}}
	seenID, seenSecret := map[string]bool{}, map[string]bool{}
	carry := 0
	for {
		n, readErr := file.Read(buffer[carry : carry+chunkSize])
		if n > 0 {
			window := buffer[:carry+n]
			idRanges := antigravityIDPattern.FindAllIndex(window, -1)
			for _, loc := range idRanges {
				value := string(window[loc[0]:loc[1]])
				if !seenID[value] {
					seenID[value] = true
					entry.ids = append(entry.ids, value)
				}
			}
			secRanges := antigravitySecPattern.FindAllIndex(window, -1)
			for _, loc := range secRanges {
				secret := string(window[loc[0]:loc[1]])
				if !seenSecret[secret] {
					seenSecret[secret] = true
					entry.secrets = append(entry.secrets, secret)
				}
				distance := 512
				nearest := ""
				for _, idLoc := range idRanges {
					delta := idLoc[0] - loc[0]
					if delta < 0 {
						delta = -delta
					}
					if delta < distance {
						distance = delta
						nearest = string(window[idLoc[0]:idLoc[1]])
					}
				}
				if nearest != "" && entry.paired[nearest] == "" {
					entry.paired[nearest] = secret
				}
			}
			if len(window) > overlapSize {
				copy(buffer, window[len(window)-overlapSize:])
				carry = overlapSize
			} else {
				copy(buffer, window)
				carry = len(window)
			}
		}
		if readErr != nil {
			break
		}
	}
	oauthScanMu.Lock()
	oauthScanCache[path] = entry
	oauthScanMu.Unlock()
	return entry, true
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
