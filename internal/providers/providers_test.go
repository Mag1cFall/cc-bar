//go:build windows

package providers

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
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
