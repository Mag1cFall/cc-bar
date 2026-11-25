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
	if !settings.Providers[model.Codex].Enabled || settings.Providers[model.Claude].Enabled || !settings.UsageVisibility[model.UsagePi] || !settings.UsageVisibility[model.UsageOmp] || !settings.UsageVisibility[model.UsageAntigravity] {
		t.Fatalf("设置读取错误: %+v", settings)
	}
	disabled := model.DefaultSettings()
	if err := json.Unmarshal([]byte(`{"UsageVisibility":{"Antigravity":false}}`), &disabled); err != nil {
		t.Fatal(err)
	}
	if disabled.UsageVisibility[model.UsageAntigravity] {
		t.Fatalf("显式关闭 Antigravity 用量统计未生效: %+v", disabled.UsageVisibility)
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

// TestInheritEmail 验证刷新前沿用同账号已有邮箱且换号不串号
func TestInheritEmail(t *testing.T) {
	previous := &model.Credential{Source: "keyring:gemini:antigravity", AccessToken: "at-1", RefreshToken: "rt-1", Email: "user@example.com"}
	cached := cachedProvider{
		AccountKey: "user@example.com",
		Source:     "keyring:gemini:antigravity",
		Email:      "user@example.com",
		Token:      "rt-1",
		Snapshot:   &model.QuotaSnapshot{App: model.Antigravity},
	}

	warm := &model.Credential{Source: "keyring:gemini:antigravity", AccessToken: "at-1", RefreshToken: "rt-1"}
	inheritEmail(warm, previous, cachedProvider{})
	if warm.Email != "user@example.com" {
		t.Fatalf("应从内存中同账号状态继承邮箱: %+v", warm)
	}

	cold := &model.Credential{Source: "keyring:gemini:antigravity", AccessToken: "at-1", RefreshToken: "rt-1"}
	inheritEmail(cold, nil, cached)
	if cold.Email != "user@example.com" {
		t.Fatalf("冷启动时应从匹配的缓存继承邮箱: %+v", cold)
	}

	discoveredOnly := &model.Credential{Source: "keyring:gemini:antigravity", AccessToken: "at-1", RefreshToken: "rt-1"}
	emptyPrev := &model.Credential{Source: "keyring:gemini:antigravity", AccessToken: "at-1", RefreshToken: "rt-1"}
	inheritEmail(discoveredOnly, emptyPrev, cached)
	if discoveredOnly.Email != "user@example.com" {
		t.Fatalf("旧状态无邮箱时应回退继承缓存邮箱: %+v", discoveredOnly)
	}

	switched := &model.Credential{Source: "keyring:gemini:antigravity", AccessToken: "at-2", RefreshToken: "rt-2"}
	inheritEmail(switched, previous, cached)
	if switched.Email != "" {
		t.Fatalf("换号后不应继承旧账号或旧缓存邮箱: %+v", switched)
	}

	explicit := &model.Credential{Source: "keyring:gemini:antigravity", AccessToken: "at-1", RefreshToken: "rt-1", Email: "fresh@example.com"}
	inheritEmail(explicit, previous, cached)
	if explicit.Email != "fresh@example.com" {
		t.Fatalf("已有邮箱时不应被覆盖: %+v", explicit)
	}
}
