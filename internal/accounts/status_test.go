package accounts

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mag1cFall/cc-bar/internal/model"
	"github.com/Mag1cFall/cc-bar/internal/providers"
	"github.com/google/uuid"
)

// statusStore 创建已有额度和固定凭据更新时间的独立账号
func statusStore(t *testing.T, expires time.Time, refresh string) *Store {
	t.Helper()
	home := t.TempDir()
	data := filepath.Join(home, "ccbar")
	profile := ClaudeProfile{ID: uuid.NewString(), Snapshot: &model.QuotaSnapshot{FetchedAt: time.Now().Add(-time.Hour)}}
	profile.ConfigDirectory = filepath.Join(data, "claude-accounts", profile.ID)
	credential := &model.Credential{
		AccessToken: "fixture-access", RefreshToken: refresh,
		Scopes: []string{"user:profile", "user:inference"}, ExpiresAt: &expires,
		AccountUUID: "fixture-account", OrganizationUUID: "fixture-org", Email: "fixture@example.test",
	}
	store := &Store{dataDir: data, home: home, claude: []ClaudeProfile{profile}}
	if err := store.writeDesktopClaude(profile, credential); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(profile.ConfigDirectory, ".credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	written := info.ModTime()
	store.claude[0].CredentialsUpdatedAt = &written
	return store
}

// TestClaudeLoginState 验证可续期登录与真实缺失授权的状态区分
func TestClaudeLoginState(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	for _, item := range []struct {
		name, refresh, message       string
		expired, missing, needsLogin bool
	}{
		{name: "expired-refreshable", refresh: "fixture-refresh", message: "启动 Claude Code 更新登录", expired: true},
		{name: "updated-expiry-with-old-message", refresh: "fixture-refresh", message: "启动 Claude Code 更新登录"},
		{name: "persisted-expiry-status", refresh: "fixture-refresh", expired: true},
		{name: "expired-without-refresh", expired: true, needsLogin: true},
		{name: "confirmed-auth-rejection", refresh: "fixture-refresh", message: "HTTP 401: Unauthorized", needsLogin: true},
		{name: "expired-auth-rejection", refresh: "fixture-refresh", message: "HTTP 401: Unauthorized", expired: true, needsLogin: true},
		{name: "missing-credentials", refresh: "fixture-refresh", missing: true, needsLogin: true},
	} {
		t.Run(item.name, func(t *testing.T) {
			expires := time.Now().Add(time.Hour)
			if item.expired {
				expires = time.Now().Add(-time.Hour)
			}
			store := statusStore(t, expires, item.refresh)
			store.claude[0].NeedsLogin, store.claude[0].Error = true, item.message
			previous := store.claude[0].Snapshot
			if item.missing {
				if err := os.Remove(filepath.Join(store.claude[0].ConfigDirectory, ".credentials.json")); err != nil {
					t.Fatal(err)
				}
			}
			profile := store.ListClaude()[0]
			if profile.NeedsLogin != item.needsLogin || profile.Snapshot != previous {
				t.Fatalf("登录判定=%t，预期=%t，额度保留=%t", profile.NeedsLogin, item.needsLogin, profile.Snapshot == previous)
			}
			if !item.needsLogin && profile.Error != "" {
				t.Fatalf("仍保留过期错误：%s", profile.Error)
			}
		})
	}
}

// TestClaudeRefreshStatus 验证额度响应与网络错误不会混淆账号登录状态
func TestClaudeRefreshStatus(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	original := providers.HTTPClient
	t.Cleanup(func() { providers.HTTPClient = original })
	for _, item := range []struct {
		name, body          string
		status              int
		network, needsLogin bool
	}{
		{name: "success", status: 200, body: `{"five_hour":{"utilization":12}}`},
		{name: "authentication", status: 401, body: `{"error":{"type":"authentication_error"}}`, needsLogin: true},
		{name: "explicit-authentication-403", status: 403, body: `{"error":{"type":"authentication_error"}}`, needsLogin: true},
		{name: "proxy-401", status: 401, body: "<html>proxy</html>"},
		{name: "proxy-403", status: 403, body: "<html>proxy</html>"},
		{name: "permission", status: 403, body: `{"error":{"type":"permission_error"}}`},
		{name: "rate-limit", status: 429, body: `{"error":{"type":"rate_limit_error"}}`},
		{name: "timeout", network: true},
	} {
		t.Run(item.name, func(t *testing.T) {
			store := statusStore(t, time.Now().Add(time.Hour), "fixture-refresh")
			store.claude[0].NeedsLogin = true
			previous := store.claude[0].Snapshot
			providers.HTTPClient = &http.Client{Transport: oauthTransport(func(request *http.Request) (*http.Response, error) {
				if item.network {
					return nil, context.DeadlineExceeded
				}
				return &http.Response{StatusCode: item.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(item.body)), Request: request}, nil
			})}
			err := store.RefreshClaude(context.Background())
			profile := store.ListClaude()[0]
			if profile.NeedsLogin != item.needsLogin {
				t.Fatalf("登录判定=%t，预期=%t", profile.NeedsLogin, item.needsLogin)
			}
			if item.status == 200 {
				if err != nil || profile.Error != "" || profile.Snapshot == previous {
					t.Fatalf("额度成功未更新状态：%v", err)
				}
			} else if err == nil || profile.Snapshot != previous {
				t.Fatalf("失败响应未保留原有额度：%v", err)
			}
		})
	}
}
