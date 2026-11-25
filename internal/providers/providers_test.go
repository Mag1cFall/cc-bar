//go:build windows

package providers

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
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
	readAntigravityKeyring = func() ([]byte, error) {
		return []byte(`{"token":{"access_token":"accB-live","token_type":"Bearer","refresh_token":"1//accB","expiry":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano) + `"},"auth_method":"consumer"}`), nil
	}
	switched, err := forceRefreshAntigravity(context.Background(), &model.Credential{AccessToken: "accA-stale", RefreshToken: "1//accA", Email: "account-a@example.test", Source: antigravityKeyringSource})
	if err != nil || switched == nil || switched.AccessToken != "accB-live" || switched.Email != "" {
		t.Fatalf("外部换号后不应继承旧账号邮箱: err=%v cred=%+v", err, switched)
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

	reset4h := time.Now().Add(4*time.Hour + 20*time.Minute).UTC().Format(time.RFC3339)
	reset5d := time.Now().Add(5 * 24 * time.Hour).UTC().Format(time.RFC3339)
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

	geminiOnlyRoot, _ := DecodeObject([]byte(`{
		"models": {
			"gemini-3.8-flash-high": {"quotaInfo": {"resetTime": "` + reset4h + `"}},
			"gemini-3.8-pro-high": {"quotaInfo": {"remainingFraction": 0.65, "resetTime": "` + reset5d + `"}}
		}
	}`))
	geminiOnly := ParseAntigravity(geminiOnlyRoot)
	if geminiOnly.PrimaryLimit == nil || geminiOnly.PrimaryLimit.Window.UsedPercent != 100 || geminiOnly.SecondaryLimit == nil || math.Abs(geminiOnly.SecondaryLimit.Window.UsedPercent-35) > 0.01 || geminiOnly.GeminiWindow != nil || geminiOnly.GeminiWeekly != nil {
		t.Fatalf("仅 Gemini models 兜底升格或缺失剩余比例处理异常: %+v", geminiOnly)
	}
}
