//go:build windows

package providers

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mag1cFall/cc-bar/internal/model"
	"github.com/Mag1cFall/cc-bar/internal/secrets"
)

// TestCredentialFormats 验证原有 Claude CLI 与桌面 DPAPI AES 数据
func TestCredentialFormats(t *testing.T) {
	directory := t.TempDir()
	expiry := time.Now().Add(time.Hour)
	if err := WriteJSON(filepath.Join(directory, ".credentials.json"), object{"claudeAiOauth": object{"accessToken": "fixture-access", "subscriptionType": "max", "expiresAt": expiry.UnixMilli()}}); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(filepath.Join(directory, ".claude.json"), object{"oauthAccount": object{"emailAddress": "fixture@example.test", "accountUuid": "account", "organizationUuid": "organization"}}); err != nil {
		t.Fatal(err)
	}
	cli, err := ReadClaude(directory, "")
	if err != nil || cli == nil || cli.AccountUUID != "account" || cli.OrganizationUUID != "organization" || cli.Email != "fixture@example.test" {
		t.Fatalf("CLI credential failed: %v", err)
	}
	key := []byte("0123456789abcdef0123456789abcdef")
	wrapped, err := secrets.ProtectBytes(key)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	nonce := []byte("0123456789ab")
	cache, _ := json.Marshal(object{"acct:account|9d1c250a-e61b-44d9-88ed-5944d1962f5e:organization:https://api.anthropic.com:user:inference user:file_upload user:profile user:sessions:claude_code": object{"token": "fixture-desktop-access", "refreshToken": "fixture-desktop-refresh", "expiresAt": expiry.UnixMilli(), "subscriptionType": "max", "rateLimitTier": "default_claude_max_20x"}})
	encrypted := append([]byte("v10"), nonce...)
	encrypted = append(encrypted, gcm.Seal(nil, nonce, cache, nil)...)
	if err := WriteJSON(filepath.Join(directory, "Local State"), object{"os_crypt": object{"encrypted_key": base64.StdEncoding.EncodeToString(append([]byte("DPAPI"), wrapped...))}}); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(filepath.Join(directory, "config.json"), object{"lastKnownAccountUuid": "account", "oauth:tokenCacheV2": base64.StdEncoding.EncodeToString(encrypted)}); err != nil {
		t.Fatal(err)
	}
	desktop, err := ReadClaudeDesktopDirectory(directory, cli)
	if err != nil || desktop == nil || desktop.AccessToken != "fixture-desktop-access" || desktop.RefreshToken != "fixture-desktop-refresh" || desktop.AccountUUID != "account" || desktop.Source != "Claude Desktop Code" || len(desktop.Scopes) != 4 || desktop.Scopes[0] != "user:inference" || desktop.RateLimitTier != "default_claude_max_20x" {
		t.Fatalf("desktop credential failed: %v", err)
	}
}

type fixtureTransport func(*http.Request) (*http.Response, error)

func (transport fixtureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

// TestQuotaProtocol 验证请求身份、限流及全部窗口解析
func TestQuotaProtocol(t *testing.T) {
	previous := HTTPClient
	defer func() { HTTPClient = previous }()
	HTTPClient = &http.Client{Transport: fixtureTransport(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "Bearer fixture-token" || request.Header.Get("ChatGPT-Account-Id") != "account" {
			t.Fatal("quota request identity missing")
		}
		return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": []string{"600"}}, Body: io.NopCloser(strings.NewReader("limited"))}, nil
	})}
	_, _, err := FetchCodexIdentity(context.Background(), &model.Credential{AccessToken: "fixture-token", AccountID: "account"})
	failure, ok := err.(*Error)
	if !ok || failure.StatusCode != 429 || failure.RetryAfter != 10*time.Minute {
		t.Fatalf("rate limit metadata missing: %v", err)
	}
	root, _ := DecodeObject([]byte(`{"five_hour":{"utilization":2,"resets_at":"2030-01-01T00:00:00Z"},"seven_day":{"utilization":3},"limits":[{"kind":"session","percent":4,"is_active":false},{"kind":"weekly_scoped","percent":5,"scope":{"model":{"id":"opus","display_name":"Opus"}}}]}`))
	claude := ParseClaude(root)
	if claude.PrimaryLimit == nil || claude.PrimaryLimit.Window.UsedPercent != 4 || claude.PrimaryLimit.Window.ResetsAt == nil || claude.PrimaryLimit.IsActive == nil || *claude.PrimaryLimit.IsActive || len(claude.ModelLimits) != 1 || claude.SecondaryLimit == nil {
		t.Fatal("Claude limits were lost")
	}
	gravity, _ := DecodeObject([]byte(`{"groups":[{"displayName":"Gemini","buckets":[{"bucketId":"gemini-5h","remainingFraction":0.8}]},{"displayName":"Claude","buckets":[{"bucketId":"claude-weekly","remainingFraction":0.5}]}]}`))
	parsed := ParseAntigravity(gravity)
	if parsed.PrimaryLimit == nil || parsed.PrimaryLimit.Window.UsedPercent != 20 || len(parsed.AuxiliaryLimits) != 1 || parsed.AuxiliaryLimits[0].Window.UsedPercent != 50 {
		t.Fatal("Antigravity grouped limits were lost")
	}
}

// TestClaudePlan 验证套餐按官方资料识别升级且不改动调用方凭据
func TestClaudePlan(t *testing.T) {
	previous := HTTPClient
	defer func() { HTTPClient = previous }()
	profiles := 0
	profileStatus := 200
	HTTPClient = &http.Client{Transport: fixtureTransport(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/api/oauth/usage":
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"limits":[{"kind":"session","percent":4}]}`))}, nil
		case "/api/oauth/profile":
			profiles++
			return &http.Response{StatusCode: profileStatus, Body: io.NopCloser(strings.NewReader(`{"account":{"uuid":"account","email":"user@example.com"},"organization":{"uuid":"organization","organization_type":"claude_max","subscription_type":null,"billing_type":"stripe_subscription","rate_limit_tier":"default_claude_max_20x"}}`))}, nil
		}
		t.Fatalf("unexpected request %s", request.URL)
		return nil, nil
	})}
	credential := &model.Credential{AccessToken: "plan-token", SubscriptionType: "pro", RateLimitTier: "default_claude_ai"}
	upgraded, err := Fetch(context.Background(), model.Claude, credential)
	if err != nil || upgraded.PlanType != "max 20x" {
		t.Fatalf("Claude 升级后的套餐未识别: %v %+v", err, upgraded)
	}
	if credential.AccountUUID != "" || credential.SubscriptionType != "pro" {
		t.Fatalf("套餐查询改动了调用方凭据: %+v", credential)
	}
	profileStatus = 429
	again, err := Fetch(context.Background(), model.Claude, &model.Credential{AccessToken: "plan-token", SubscriptionType: "pro", RateLimitTier: "default_claude_ai"})
	if err != nil || again.PlanType != "max 20x" || profiles != 1 {
		t.Fatalf("套餐在一小时内重复查询或回退: 查询=%d %v %+v", profiles, err, again)
	}
	claudePlans.Lock()
	entry := claudePlans.entries["plan-token"]
	entry.checkedAt = time.Now().Add(-2 * time.Hour)
	claudePlans.entries["plan-token"] = entry
	claudePlans.Unlock()
	for range 2 {
		limited, err := Fetch(context.Background(), model.Claude, &model.Credential{AccessToken: "plan-token", SubscriptionType: "pro", RateLimitTier: "default_claude_ai"})
		if err != nil || limited.PlanType != "max 20x" || profiles != 2 {
			t.Fatalf("资料查询失败后套餐回退或未退避: 查询=%d %v %+v", profiles, err, limited)
		}
	}
}

// TestReadAntigravityPriorityAndFormats 验证系统凭据库优先级、标记文件切换与多路径回退
func TestReadAntigravityPriorityAndFormats(t *testing.T) {
	previousRead := readAntigravityKeyring
	defer func() { readAntigravityKeyring = previousRead }()
	home := t.TempDir()
	expiry := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	keyringBlob := []byte(`{"token":{"access_token":"keyring-access","token_type":"Bearer","refresh_token":"1//keyring-refresh","expiry":"` + expiry + `"},"auth_method":"consumer"}`)
	readAntigravityKeyring = func() ([]byte, error) { return keyringBlob, nil }
	cliPath := filepath.Join(home, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	if err := WriteJSON(cliPath, object{"token": object{"access_token": "cli-access", "refresh_token": "1//cli-refresh", "expiry": expiry}, "id_token_email": "cli@example.test"}); err != nil {
		t.Fatal(err)
	}
	credential, err := ReadAntigravity(home)
	if err != nil || credential == nil || credential.AccessToken != "keyring-access" || credential.RefreshToken != "1//keyring-refresh" || credential.Source != antigravityKeyringSource {
		t.Fatalf("应优先读取 Windows 凭据管理器: %v %+v", err, credential)
	}
	jetskiPath := filepath.Join(home, ".gemini", "jetski-standalone-oauth-token")
	if err := WriteJSON(jetskiPath, object{"token": object{"access_token": "jetski-access", "refresh_token": "1//jetski-refresh", "expiry": expiry}}); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(home, ".gemini", "cache", "antigravity-keyring-unavailable")
	if err := WriteBytes(markerPath, nil); err != nil {
		t.Fatal(err)
	}
	credential, err = ReadAntigravity(home)
	if err != nil || credential == nil || credential.AccessToken != "jetski-access" || credential.Source != jetskiPath {
		t.Fatalf("标记文件有效期内应优先读取 jetski 文件: %v %+v", err, credential)
	}
	_ = os.Remove(markerPath)
	_ = os.Remove(jetskiPath)
	readAntigravityKeyring = func() ([]byte, error) { return nil, nil }
	credential, err = ReadAntigravity(home)
	if err != nil || credential == nil || credential.AccessToken != "cli-access" || credential.Email != "cli@example.test" || credential.Source != cliPath {
		t.Fatalf("凭据库为空时应回退读取 agy CLI 凭据文件: %v %+v", err, credential)
	}
	_ = os.Remove(cliPath)
	oauthPath := filepath.Join(home, ".gemini", "oauth_creds.json")
	if err := WriteJSON(oauthPath, object{"access_token": "legacy-access", "refresh_token": "1//legacy-refresh", "expiry_date": time.Now().Add(time.Hour).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	credential, err = ReadAntigravity(home)
	if err != nil || credential == nil || credential.AccessToken != "legacy-access" || credential.Source != oauthPath {
		t.Fatalf("最后应回退读取 oauth_creds.json: %v %+v", err, credential)
	}
}

// TestRefreshAntigravityKeyringAndStreamingSecrets 验证远距粘连二进制密钥提取、续期写回凭据库与缓存复用
func TestRefreshAntigravityKeyringAndStreamingSecrets(t *testing.T) {
	previousHTTP := HTTPClient
	previousRead := readAntigravityKeyring
	previousWrite := writeAntigravityKeyring
	previousPaths := antigravityBinaryPaths
	defer func() {
		HTTPClient = previousHTTP
		readAntigravityKeyring = previousRead
		writeAntigravityKeyring = previousWrite
		antigravityBinaryPaths = previousPaths
		antigravityClientMu.Lock()
		antigravityClientCache = map[string]antigravityOAuthPair{}
		antigravityClientMu.Unlock()
		oauthScanMu.Lock()
		oauthScanCache = map[string]scannedOAuthFile{}
		oauthScanMu.Unlock()
	}()
	antigravityClientMu.Lock()
	antigravityClientCache = map[string]antigravityOAuthPair{}
	antigravityClientMu.Unlock()
	oauthScanMu.Lock()
	oauthScanCache = map[string]scannedOAuthFile{}
	oauthScanMu.Unlock()

	wantClientID := "1071006060591-tmhssin2h21lcre235vtolojh4g403ep" + ".apps." + "googleusercontent.com"
	wrongSecret := "GOCSPX" + "-" + strings.Repeat("A", 28)
	rightSecret := "GOCSPX" + "-" + strings.Repeat("B", 28)
	binPath := filepath.Join(t.TempDir(), "agy.exe")
	var binaryPayload []byte
	binaryPayload = append(binaryPayload, []byte(wrongSecret+"https")...)
	binaryPayload = append(binaryPayload, []byte(rightSecret+"cloudcode-pa")...)
	binaryPayload = append(binaryPayload, make([]byte, 800000)...)
	binaryPayload = append(binaryPayload, []byte(wantClientID)...)
	if err := os.WriteFile(binPath, binaryPayload, 0600); err != nil {
		t.Fatal(err)
	}
	antigravityBinaryPaths = func(string) ([]string, []string) { return []string{binPath}, nil }

	expired := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	stored := []byte(`{"token":{"access_token":"old-access","token_type":"Bearer","refresh_token":"1//fixture-refresh","expiry":"` + expired + `"},"auth_method":"consumer"}`)
	readAntigravityKeyring = func() ([]byte, error) { return stored, nil }
	writeAntigravityKeyring = func(data []byte) error {
		stored = append([]byte(nil), data...)
		return nil
	}

	tokenCalls := 0
	HTTPClient = &http.Client{Transport: fixtureTransport(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host != "oauth2.googleapis.com" {
			t.Fatalf("unexpected host %s", request.URL)
		}
		rawBody, _ := io.ReadAll(request.Body)
		values, _ := url.ParseQuery(string(rawBody))
		tokenCalls++
		if values.Get("client_id") == wantClientID && values.Get("client_secret") == rightSecret && values.Get("refresh_token") == "1//fixture-refresh" {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"access_token":"fresh-access","expires_in":3599}`))}, nil
		}
		return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader(`{"error":"invalid_client"}`))}, nil
	})}

	past := time.Now().Add(-time.Hour)
	refreshed, err := EnsureFresh(context.Background(), model.Antigravity, &model.Credential{
		AccessToken:  "old-access",
		RefreshToken: "1//fixture-refresh",
		ExpiresAt:    &past,
		Source:       antigravityKeyringSource,
	})
	if err != nil || refreshed == nil || refreshed.AccessToken != "fresh-access" || refreshed.ExpiresAt == nil || !refreshed.ExpiresAt.After(time.Now().Add(50*time.Minute)) {
		t.Fatalf("Antigravity 凭据续期失败: %v %+v", err, refreshed)
	}
	persisted, err := DecodeObject(stored)
	if err != nil || str(persisted, "auth_method") != "consumer" || str(obj(persisted["token"]), "access_token") != "fresh-access" {
		t.Fatalf("写回 Windows 凭据库格式异常: %v %s", err, string(stored))
	}
	beforeCalls := tokenCalls
	reused, err := EnsureFresh(context.Background(), model.Antigravity, &model.Credential{
		AccessToken:  "old-access",
		RefreshToken: "1//fixture-refresh",
		ExpiresAt:    &past,
		Source:       antigravityKeyringSource,
	})
	if err != nil || reused.AccessToken != "fresh-access" || tokenCalls != beforeCalls {
		t.Fatalf("凭据库已是新令牌时不应重复刷新: calls=%d->%d err=%v", beforeCalls, tokenCalls, err)
	}
}

// TestParseAntigravityRealResponses 验证真实分组、套餐、Argon 独立桶、周额度归零及 models 兜底解析
func TestParseAntigravityRealResponses(t *testing.T) {
	realSummary, err := DecodeObject([]byte(`{
		"currentTier": {"id": "free-tier", "name": "Antigravity"},
		"paidTier": {"id": "g1-ultra-tier", "name": "Google AI Ultra"},
		"groups": [
			{
				"displayName": "Gemini Models",
				"buckets": [
					{"bucketId": "gemini-weekly", "window": "weekly", "resetTime": "2026-11-30T20:03:15Z", "remainingFraction": 0.68173766},
					{"bucketId": "gemini-5h", "window": "5h", "resetTime": "2026-11-25T21:32:02Z", "remainingFraction": 0.93401057}
				]
			},
			{
				"displayName": "Claude and GPT models",
				"buckets": [
					{"bucketId": "3p-weekly", "window": "weekly", "resetTime": "2026-12-02T02:52:50Z", "remainingFraction": 1},
					{"bucketId": "3p-5h", "window": "5h", "resetTime": "2026-11-25T22:07:12Z", "remainingFraction": 1}
				]
			},
			{
				"displayName": "Gemini 4 Argon",
				"buckets": [
					{"bucketId": "gemini-argon-wtu-weekly", "window": "weekly", "resetTime": "2026-12-01T04:34:17Z", "remainingFraction": 0.7396149}
				]
			}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	snap := ParseAntigravity(realSummary)
	if snap.PlanType != "Google AI Ultra" {
		t.Fatalf("应优先采用 paidTier.name: %q", snap.PlanType)
	}
	if snap.PrimaryLimit == nil || snap.PrimaryLimit.DisplayName != "Gemini 5H" || math.Abs(snap.PrimaryLimit.Window.UsedPercent-6.598943) > 0.01 {
		t.Fatalf("Gemini 5H 解析错误: %+v", snap.PrimaryLimit)
	}
	if snap.SecondaryLimit == nil || snap.SecondaryLimit.DisplayName != "Gemini WK" || math.Abs(snap.SecondaryLimit.Window.UsedPercent-31.826234) > 0.01 {
		t.Fatalf("Gemini WK 解析错误: %+v", snap.SecondaryLimit)
	}
	if len(snap.AuxiliaryLimits) != 2 || snap.AuxiliaryLimits[0].DisplayName != "Claude 5H" || snap.AuxiliaryLimits[1].DisplayName != "Claude WK" {
		t.Fatalf("Claude 组解析错误: %+v", snap.AuxiliaryLimits)
	}
	if len(snap.ModelLimits) != 1 || snap.ModelLimits[0].DisplayName != "Argon WK" || math.Abs(snap.ModelLimits[0].Window.UsedPercent-26.03851) > 0.01 {
		t.Fatalf("Argon 独立周额度应进入 ModelLimits 且不污染 Gemini WK: %+v", snap.ModelLimits)
	}

	freeRoot, _ := DecodeObject([]byte(`{"currentTier": {"id": "free-tier", "name": "Antigravity"}}`))
	if plan := ParseAntigravity(freeRoot).PlanType; plan != "free-tier" {
		t.Fatalf("免费版应优先取 currentTier.id 而非重复服务名: %q", plan)
	}

	weeklyExhausted, _ := DecodeObject([]byte(`{
		"groups": [
			{
				"displayName": "Gemini Models",
				"buckets": [
					{"bucketId": "gemini-weekly", "window": "weekly", "resetTime": "2026-12-01T00:00:00Z"},
					{"bucketId": "gemini-5h", "window": "5h", "resetTime": "2026-11-26T05:00:00Z", "remainingFraction": 1}
				]
			},
			{
				"displayName": "Claude and GPT models",
				"buckets": [
					{"bucketId": "3p-weekly", "window": "weekly", "remainingFraction": 0.5},
					{"bucketId": "3p-5h", "window": "5h", "remainingFraction": 1, "disabled": true}
				]
			}
		]
	}`))
	exhausted := ParseAntigravity(weeklyExhausted)
	if exhausted.PrimaryLimit == nil || exhausted.PrimaryLimit.DisplayName != "Gemini WK" || exhausted.PrimaryLimit.Window.UsedPercent != 100 || exhausted.SecondaryLimit != nil {
		t.Fatalf("周额度耗尽后应过滤同组 5h 桶并升格 Gemini WK: primary=%+v secondary=%+v", exhausted.PrimaryLimit, exhausted.SecondaryLimit)
	}
	if len(exhausted.AuxiliaryLimits) != 1 || exhausted.AuxiliaryLimits[0].DisplayName != "Claude WK" {
		t.Fatalf("disabled 桶应被忽略: %+v", exhausted.AuxiliaryLimits)
	}

	reset4h := time.Now().Add(4*time.Hour + 20*time.Minute).UTC().Format(time.RFC3339)
	modelsFallback, _ := DecodeObject([]byte(`{
		"models": {
			"tab_flash_lite_preview": {"quotaInfo": {"remainingFraction": 1}},
			"gemini-3.8-flash-high": {"quotaInfo": {"remainingFraction": 0.936, "resetTime": "` + reset4h + `"}},
			"claude-opus-5-5-low": {"quotaInfo": {"remainingFraction": 0.8, "resetTime": "` + reset4h + `"}}
		}
	}`))
	fallback := ParseAntigravity(modelsFallback)
	if fallback.PrimaryLimit == nil || math.Abs(fallback.PrimaryLimit.Window.UsedPercent-20) > 0.01 || fallback.GeminiWindow == nil || math.Abs(fallback.GeminiWindow.UsedPercent-6.4) > 0.01 {
		t.Fatalf("models 兜底不应被 2h 阈值误杀且应保留第三方与 Gemini: primary=%+v gemini=%+v", fallback.PrimaryLimit, fallback.GeminiWindow)
	}
}

// TestFetchAntigravityProtocol 验证新版 UA、project 请求体、401 强制续期与 userinfo 邮箱回填
func TestFetchAntigravityProtocol(t *testing.T) {
	previousHTTP := HTTPClient
	previousRead := readAntigravityKeyring
	previousWrite := writeAntigravityKeyring
	previousPaths := antigravityBinaryPaths
	previousLocal := fetchLocalAntigravity
	defer func() {
		HTTPClient = previousHTTP
		readAntigravityKeyring = previousRead
		writeAntigravityKeyring = previousWrite
		antigravityBinaryPaths = previousPaths
		fetchLocalAntigravity = previousLocal
		antigravityClientMu.Lock()
		antigravityClientCache = map[string]antigravityOAuthPair{}
		antigravityClientMu.Unlock()
	}()
	fetchLocalAntigravity = func(context.Context, string) (object, error) { return nil, os.ErrNotExist }
	antigravityBinaryPaths = func(string) ([]string, []string) { return nil, nil }
	antigravityClientMu.Lock()
	antigravityClientCache = map[string]antigravityOAuthPair{
		"consumer": {id: "1071006060591-tmhssin2h21lcre235vtolojh4g403ep" + ".apps." + "googleusercontent.com", secret: "GOCSPX" + "-" + strings.Repeat("C", 28)},
	}
	antigravityClientMu.Unlock()

	future := time.Now().Add(time.Hour)
	stored := []byte(`{"token":{"access_token":"stale-token","token_type":"Bearer","refresh_token":"1//ref","expiry":"` + future.UTC().Format(time.RFC3339Nano) + `"},"auth_method":"consumer"}`)
	readAntigravityKeyring = func() ([]byte, error) { return stored, nil }
	writeAntigravityKeyring = func(data []byte) error {
		stored = append([]byte(nil), data...)
		return nil
	}

	var called []string
	HTTPClient = &http.Client{Transport: fixtureTransport(func(request *http.Request) (*http.Response, error) {
		called = append(called, request.URL.Host+request.URL.Path)
		rawBody, _ := io.ReadAll(request.Body)
		switch {
		case request.URL.Host == "oauth2.googleapis.com":
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"access_token":"live-token","expires_in":3599}`))}, nil
		case request.URL.Path == "/v1internal:loadCodeAssist":
			if !strings.HasPrefix(request.Header.Get("User-Agent"), "antigravity/hub/2.") {
				t.Fatalf("应使用 2.x 官方 UA，实际为: %q", request.Header.Get("User-Agent"))
			}
			if request.Header.Get("Authorization") == "Bearer stale-token" {
				return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader(`{"error":{"status":"UNAUTHENTICATED"}}`))}, nil
			}
			if !strings.Contains(string(rawBody), `"ideType":"ANTIGRAVITY"`) {
				t.Fatalf("loadCodeAssist 缺少官方 metadata: %s", string(rawBody))
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"cloudaicompanionProject":"aicode-consumers","paidTier":{"id":"g1-ultra-tier","name":"Google AI Ultra"}}`))}, nil
		case request.URL.Path == "/v1internal:retrieveUserQuotaSummary":
			if !strings.Contains(string(rawBody), `"project":"aicode-consumers"`) {
				t.Fatalf("retrieveUserQuotaSummary 缺少 project 参数: %s", string(rawBody))
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"groups":[{"displayName":"Gemini Models","buckets":[{"bucketId":"gemini-5h","window":"5h","remainingFraction":0.9},{"bucketId":"gemini-weekly","window":"weekly","remainingFraction":0.7}]},{"displayName":"Claude and GPT models","buckets":[{"bucketId":"3p-5h","window":"5h","remainingFraction":1}]}]}`))}, nil
		case request.URL.Host == "www.googleapis.com" && request.URL.Path == "/oauth2/v3/userinfo":
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"email":"fixture-agy@example.test"}`))}, nil
		}
		t.Fatalf("unexpected request: %s", request.URL)
		return nil, nil
	})}

	credential := &model.Credential{
		AccessToken:  "stale-token",
		RefreshToken: "1//ref",
		ExpiresAt:    &future,
		Source:       antigravityKeyringSource,
	}
	snapshot, err := Fetch(context.Background(), model.Antigravity, credential)
	if err != nil {
		t.Fatalf("Fetch Antigravity 失败: %v", err)
	}
	if credential.AccessToken != "live-token" || credential.Email != "fixture-agy@example.test" {
		t.Fatalf("401 续期或邮箱回填未生效: %+v", credential)
	}
	if snapshot.PlanType != "Google AI Ultra" || snapshot.PrimaryLimit == nil || snapshot.PrimaryLimit.DisplayName != "Gemini 5H" || snapshot.SecondaryLimit == nil || len(snapshot.AuxiliaryLimits) != 1 {
		t.Fatalf("Antigravity 完整快照解析异常: %+v", snapshot)
	}
	for _, item := range called {
		if strings.HasSuffix(item, ":fetchAvailableModels") || strings.HasSuffix(item, ":retrieveUserQuota") {
			t.Fatalf("已有分组摘要时不应冗余请求兜底端点: %v", called)
		}
	}
}

// TestRefreshAntigravitySafety 验证换号不继承旧邮箱、读错不覆写、写回失败不重试及全量二进制扫描
func TestRefreshAntigravitySafety(t *testing.T) {
	previousHTTP := HTTPClient
	previousRead := readAntigravityKeyring
	previousWrite := writeAntigravityKeyring
	previousPaths := antigravityBinaryPaths
	defer func() {
		HTTPClient = previousHTTP
		readAntigravityKeyring = previousRead
		writeAntigravityKeyring = previousWrite
		antigravityBinaryPaths = previousPaths
		antigravityClientMu.Lock()
		antigravityClientCache = map[string]antigravityOAuthPair{}
		antigravityClientMu.Unlock()
		oauthScanMu.Lock()
		oauthScanCache = map[string]scannedOAuthFile{}
		oauthScanMu.Unlock()
	}()
	antigravityClientMu.Lock()
	antigravityClientCache = map[string]antigravityOAuthPair{}
	antigravityClientMu.Unlock()
	oauthScanMu.Lock()
	oauthScanCache = map[string]scannedOAuthFile{}
	oauthScanMu.Unlock()

	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	readAntigravityKeyring = func() ([]byte, error) {
		return []byte(`{"token":{"access_token":"accB-live","token_type":"Bearer","refresh_token":"1//accB","expiry":"` + future + `"},"auth_method":"consumer"}`), nil
	}
	switched, err := forceRefreshAntigravity(context.Background(), &model.Credential{
		AccessToken:  "accA-stale",
		RefreshToken: "1//accA",
		Email:        "account-a@example.test",
		Source:       antigravityKeyringSource,
	})
	if err != nil || switched == nil || switched.AccessToken != "accB-live" || switched.Email != "" {
		t.Fatalf("外部换号后不应继承旧账号邮箱: err=%v cred=%+v", err, switched)
	}

	dir := t.TempDir()
	secretA := "GOCSPX" + "-" + strings.Repeat("1", 28)
	secretB := "GOCSPX" + "-" + strings.Repeat("2", 28)
	binA := filepath.Join(dir, "agy-a.exe")
	binB := filepath.Join(dir, "agy-b.exe")
	if err := os.WriteFile(binA, []byte(secretA), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binB, []byte(secretB), 0600); err != nil {
		t.Fatal(err)
	}
	antigravityBinaryPaths = func(string) ([]string, []string) { return []string{binA, binB}, nil }
	_, _, secretsFound := googleClientSecrets(dir)
	if len(secretsFound) != 2 {
		t.Fatalf("应扫描全部官方二进制而非首个命中即停止: %v", secretsFound)
	}

	readAntigravityKeyring = func() ([]byte, error) { return nil, errors.New("keyring busy") }
	writeCalled := false
	writeAntigravityKeyring = func([]byte) error {
		writeCalled = true
		return nil
	}
	HTTPClient = &http.Client{Transport: fixtureTransport(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"access_token":"mem-only","expires_in":3599}`))}, nil
	})}
	past := time.Now().Add(-time.Hour)
	memCred, err := EnsureFresh(context.Background(), model.Antigravity, &model.Credential{
		AccessToken:  "old",
		RefreshToken: "1//ref",
		ExpiresAt:    &past,
		Source:       antigravityKeyringSource,
	})
	if err != nil || memCred == nil || memCred.AccessToken != "mem-only" || writeCalled {
		t.Fatalf("读原凭据失败时不应覆写 keyring: err=%v cred=%+v wrote=%t", err, memCred, writeCalled)
	}

	antigravityClientMu.Lock()
	antigravityClientCache = map[string]antigravityOAuthPair{}
	antigravityClientMu.Unlock()
	expired := past.UTC().Format(time.RFC3339Nano)
	readAntigravityKeyring = func() ([]byte, error) {
		return []byte(`{"token":{"access_token":"old","token_type":"Bearer","refresh_token":"1//ref","expiry":"` + expired + `"},"auth_method":"consumer"}`), nil
	}
	writeAntigravityKeyring = func([]byte) error { return errors.New("write denied") }
	tokenCalls := 0
	HTTPClient = &http.Client{Transport: fixtureTransport(func(request *http.Request) (*http.Response, error) {
		tokenCalls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"access_token":"new-token","expires_in":3599}`))}, nil
	})}
	if _, err := EnsureFresh(context.Background(), model.Antigravity, &model.Credential{
		AccessToken:  "old",
		RefreshToken: "1//ref",
		ExpiresAt:    &past,
		Source:       antigravityKeyringSource,
	}); err == nil || tokenCalls != 1 {
		t.Fatalf("写回失败后不应继续穷举其他 client pair: calls=%d err=%v", tokenCalls, err)
	}
}

// TestFetchAntigravityHostFallbackAndDaemon 验证额度全错切备用域名、耗尽兜底与多守护进程遍历
func TestFetchAntigravityHostFallbackAndDaemon(t *testing.T) {
	previousHTTP := HTTPClient
	previousLocal := fetchLocalAntigravity
	previousDaemonClient := localDaemonClient
	previousRead := readAntigravityKeyring
	defer func() {
		HTTPClient = previousHTTP
		fetchLocalAntigravity = previousLocal
		localDaemonClient = previousDaemonClient
		readAntigravityKeyring = previousRead
	}()
	fetchLocalAntigravity = func(context.Context, string) (object, error) { return nil, os.ErrNotExist }
	readAntigravityKeyring = func() ([]byte, error) { return nil, nil }

	HTTPClient = &http.Client{Transport: fixtureTransport(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Host {
		case "daily-cloudcode-pa.googleapis.com":
			if request.URL.Path == "/v1internal:loadCodeAssist" {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"cloudaicompanionProject":"proj"}`))}, nil
			}
			return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader(`{"error":"unavailable"}`))}, nil
		case "cloudcode-pa.googleapis.com":
			if request.URL.Path == "/v1internal:loadCodeAssist" {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"cloudaicompanionProject":"proj"}`))}, nil
			}
			if request.URL.Path == "/v1internal:retrieveUserQuotaSummary" {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"groups":[{"displayName":"Gemini Models","buckets":[{"bucketId":"gemini-5h","window":"5h","remainingFraction":0.6}]}]}`))}, nil
			}
		case "www.googleapis.com":
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"email":"backup@example.test"}`))}, nil
		}
		t.Fatalf("unexpected request: %s", request.URL)
		return nil, nil
	})}
	future := time.Now().Add(time.Hour)
	snap, err := Fetch(context.Background(), model.Antigravity, &model.Credential{
		AccessToken: "live",
		ExpiresAt:   &future,
		Source:      antigravityKeyringSource,
	})
	if err != nil || snap == nil || snap.PrimaryLimit == nil || math.Abs(snap.PrimaryLimit.Window.UsedPercent-40) > 0.01 {
		t.Fatalf("主域名额度全错时应切换备用域名而非返回空快照: err=%v snap=%+v", err, snap)
	}

	resetAt := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	bucketRoot, _ := DecodeObject([]byte(`{"buckets":[{"modelId":"gemini-2.5-pro","resetTime":"` + resetAt + `"}]}`))
	bucketSnap := ParseAntigravity(bucketRoot)
	if bucketSnap.GeminiWindow == nil || bucketSnap.GeminiWindow.UsedPercent != 100 {
		t.Fatalf("buckets 缺少 remainingFraction 且含 resetTime 应视作 100%% 已用: %+v", bucketSnap)
	}
	modelRoot, _ := DecodeObject([]byte(`{"models":{"claude-opus":{"quotaInfo":{"resetTime":"` + resetAt + `"}}}}`))
	modelSnap := ParseAntigravity(modelRoot)
	if modelSnap.PrimaryLimit == nil || modelSnap.PrimaryLimit.Window.UsedPercent != 100 {
		t.Fatalf("models 缺少 remainingFraction 且含 resetTime 应视作 100%% 已用: %+v", modelSnap)
	}

	home := t.TempDir()
	daemonDir := filepath.Join(home, ".gemini", "antigravity", "daemon")
	livePath := filepath.Join(daemonDir, "hub_live.json")
	deadPath := filepath.Join(daemonDir, "hub_dead.json")
	if err := WriteJSON(livePath, object{"lsVersion": "2.35.0", "httpPort": 18081, "csrfToken": "csrf-live"}); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(deadPath, object{"lsVersion": "2.35.0", "httpPort": 18082, "csrfToken": "csrf-dead"}); err != nil {
		t.Fatal(err)
	}
	older := time.Now().Add(-time.Minute)
	newer := time.Now()
	_ = os.Chtimes(livePath, older, older)
	_ = os.Chtimes(deadPath, newer, newer)
	localDaemonClient = &http.Client{Transport: fixtureTransport(func(request *http.Request) (*http.Response, error) {
		if request.URL.Port() == "18082" {
			return nil, errors.New("connection refused")
		}
		if request.URL.Port() == "18081" && request.Header.Get("X-Codeium-Csrf-Token") == "csrf-live" {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"groups":[{"displayName":"Gemini Models","buckets":[{"bucketId":"gemini-5h","window":"5h","remainingFraction":0.75}]}]}`))}, nil
		}
		return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}
	daemonRoot, err := fetchAntigravityDaemon(context.Background(), home)
	if err != nil || len(rows(daemonRoot["groups"])) != 1 {
		t.Fatalf("应跳过失联的最新 daemon 文件并命中存活 daemon: err=%v root=%+v", err, daemonRoot)
	}
}

// TestParseAntigravityGeminiOnlyModelsNoDuplicate 验证仅含 Gemini 的 models 兜底不会重复生成主副限额与 Gemini 窗口
func TestParseAntigravityGeminiOnlyModelsNoDuplicate(t *testing.T) {
	reset4h := time.Now().Add(4 * time.Hour).UTC().Format(time.RFC3339)
	reset5d := time.Now().Add(5 * 24 * time.Hour).UTC().Format(time.RFC3339)
	bothRoot, err := DecodeObject([]byte(`{
		"models": {
			"gemini-3.8-flash-high": {"quotaInfo": {"remainingFraction": 0.9, "resetTime": "` + reset4h + `"}},
			"gemini-3.8-pro-high": {"quotaInfo": {"remainingFraction": 0.65, "resetTime": "` + reset5d + `"}}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	bothSnap := ParseAntigravity(bothRoot)
	if bothSnap.PrimaryLimit == nil || math.Abs(bothSnap.PrimaryLimit.Window.UsedPercent-10) > 0.01 {
		t.Fatalf("Gemini 5H 应升格为主限额: %+v", bothSnap.PrimaryLimit)
	}
	if bothSnap.SecondaryLimit == nil || math.Abs(bothSnap.SecondaryLimit.Window.UsedPercent-35) > 0.01 {
		t.Fatalf("Gemini 周额度应升格为副限额: %+v", bothSnap.SecondaryLimit)
	}
	if bothSnap.GeminiWindow != nil || bothSnap.GeminiWeekly != nil {
		t.Fatalf("升格后的 Gemini 窗口不应重复保留: window=%+v weekly=%+v", bothSnap.GeminiWindow, bothSnap.GeminiWeekly)
	}

	weeklyOnlyRoot, err := DecodeObject([]byte(`{
		"models": {
			"gemini-3.8-pro-high": {"quotaInfo": {"remainingFraction": 0.6, "resetTime": "` + reset5d + `"}}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	weeklyOnlySnap := ParseAntigravity(weeklyOnlyRoot)
	if weeklyOnlySnap.PrimaryLimit == nil || math.Abs(weeklyOnlySnap.PrimaryLimit.Window.UsedPercent-40) > 0.01 {
		t.Fatalf("仅含 Gemini 周额度时应升格为主限额: %+v", weeklyOnlySnap.PrimaryLimit)
	}
	if weeklyOnlySnap.SecondaryLimit != nil || weeklyOnlySnap.GeminiWindow != nil || weeklyOnlySnap.GeminiWeekly != nil {
		t.Fatalf("仅含 Gemini 周额度时不应生成重复窗口: secondary=%+v window=%+v weekly=%+v", weeklyOnlySnap.SecondaryLimit, weeklyOnlySnap.GeminiWindow, weeklyOnlySnap.GeminiWeekly)
	}
}
