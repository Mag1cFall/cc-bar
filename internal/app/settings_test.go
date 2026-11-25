package app

import (
	"encoding/json"
	"testing"

	"github.com/Mag1cFall/cc-bar/internal/model"
)

// TestSavedSettings 验证现有设置读取及新来源默认值
func TestSavedSettings(t *testing.T) {
	settings := model.DefaultSettings()
	data := []byte(`{"Providers":{"Codex":{"Enabled":true,"MenuBar":true},"Claude":{"Enabled":false}},"UsageVisibility":{"Pi":true},"LaunchAtLogin":true}`)
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	if !settings.Providers[model.Codex].Enabled || settings.Providers[model.Claude].Enabled || !settings.UsageVisibility[model.UsagePi] || !settings.UsageVisibility[model.UsageOmp] {
		t.Fatalf("设置读取错误: %+v", settings)
	}
	copy := cloneSettings(settings)
	copy.UsageVisibility[model.UsagePi] = false
	if !settings.UsageVisibility[model.UsagePi] {
		t.Fatal("设置快照改变了原始值")
	}
}

// TestSameAccountAndCacheMatches 验证无邮箱凭据补全邮箱与换号时的身份匹配
func TestSameAccountAndCacheMatches(t *testing.T) {
	discovered := &model.Credential{Source: "keyring:gemini:antigravity", AccessToken: "at-1", RefreshToken: "rt-1"}
	enriched := &model.Credential{Source: "keyring:gemini:antigravity", AccessToken: "at-1", RefreshToken: "rt-1", Email: "user@example.com"}
	switched := &model.Credential{Source: "keyring:gemini:antigravity", AccessToken: "at-2", RefreshToken: "rt-2"}
	if !sameAccount(discovered, enriched) {
		t.Fatal("same keyring account with backfilled email should match")
	}
	if sameAccount(enriched, switched) {
		t.Fatal("different refresh tokens should not match")
	}
	if sameAccount(discovered, nil) {
		t.Fatal("logged-out credential should not match")
	}
	cached := cachedProvider{
		AccountKey: "user@example.com",
		Source:     "keyring:gemini:antigravity",
		Email:      "user@example.com",
		Token:      "rt-1",
		Snapshot:   &model.QuotaSnapshot{App: model.Antigravity},
	}
	if !cacheMatches(cached, discovered) {
		t.Fatal("cache should match keyring source before email backfill")
	}
	if !cacheMatches(cached, enriched) {
		t.Fatal("cache should match explicit email")
	}
	if cacheMatches(cached, switched) {
		t.Fatal("cache should reject switched account with different token")
	}
}
