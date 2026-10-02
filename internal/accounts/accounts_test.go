//go:build windows

package accounts

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mag1cFall/cc-bar/internal/model"
	"github.com/Mag1cFall/cc-bar/internal/providers"
	"github.com/Mag1cFall/cc-bar/internal/secrets"
)

// TestSharedHistory 验证切换账号后项目与历史持续写入同一文件
func TestSharedHistory(t *testing.T) {
	home := t.TempDir()
	shared := filepath.Join(home, ".claude")
	first := filepath.Join(home, "first")
	second := filepath.Join(home, "second")
	if err := providers.WriteBytes(filepath.Join(first, "projects", "project", "session.jsonl"), []byte("{\"id\":1}\n")); err != nil {
		t.Fatal(err)
	}
	if err := providers.WriteBytes(filepath.Join(first, "history.jsonl"), []byte("{\"sessionId\":\"first\"}\n")); err != nil {
		t.Fatal(err)
	}
	if err := prepareHistory(first, shared); err != nil {
		t.Fatal(err)
	}
	if err := providers.WriteBytes(filepath.Join(second, "history.jsonl"), []byte("{\"sessionId\":\"second\"}\n")); err != nil {
		t.Fatal(err)
	}
	if err := prepareHistory(second, shared); err != nil {
		t.Fatal(err)
	}
	if !historyLinked(first, shared) || !historyLinked(second, shared) {
		t.Fatal("account history links do not share the same file")
	}
	file, err := os.OpenFile(filepath.Join(second, "history.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString("{\"sessionId\":\"continued\"}\n")
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(first, "history.jsonl"))
	if err != nil || !strings.Contains(string(data), "first") || !strings.Contains(string(data), "second") || !strings.Contains(string(data), "continued") {
		t.Fatalf("shared history failed: %v", err)
	}
	projectData, err := os.ReadFile(filepath.Join(second, "projects", "project", "session.jsonl"))
	if err != nil || string(projectData) != "{\"id\":1}\n" {
		t.Fatalf("shared conversation failed: %v", err)
	}
	if err := prepareHistory(first, shared); err != nil {
		t.Fatal(err)
	}
}

// TestClaudeCommandRouting 验证包装脚本参数账号凭据与共享记录的端到端隔离
func TestClaudeCommandRouting(t *testing.T) {
	home := t.TempDir()
	shared := filepath.Join(home, ".claude")
	entry := filepath.Join(home, "local CLI", "claude.ps1")
	program := `param([Parameter(ValueFromRemainingArguments=$true)][string[]]$CliArguments)
$ErrorActionPreference = 'Stop'
$directory = $env:CLAUDE_CONFIG_DIR
$credential = Get-Content -LiteralPath (Join-Path $directory '.credentials.json') -Raw | ConvertFrom-Json
$record = [ordered]@{
    arguments = @($CliArguments)
    directory = $directory
    credential = $credential.claudeAiOauth.accessToken
    apiOverride = $env:ANTHROPIC_AUTH_TOKEN
    localPreference = $env:CCBAR_LOCAL_PREFERENCE
}
[IO.File]::AppendAllText((Join-Path $directory 'history.jsonl'), (($record | ConvertTo-Json -Compress) + [Environment]::NewLine))
[IO.File]::WriteAllText($env:CCBAR_CLI_RESULT, ($record | ConvertTo-Json -Compress))
exit 0
`
	if err := providers.WriteBytes(entry, []byte(program)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "wrong-account-token")
	t.Setenv("CCBAR_LOCAL_PREFERENCE", "preserved")
	for _, name := range []string{"first", "second"} {
		profile := ClaudeProfile{ConfigDirectory: filepath.Join(home, name)}
		if err := prepareHistory(profile.ConfigDirectory, shared); err != nil {
			t.Fatal(err)
		}
		if err := providers.WriteJSON(filepath.Join(profile.ConfigDirectory, ".credentials.json"), map[string]any{"claudeAiOauth": map[string]string{"accessToken": name + "-token"}}); err != nil {
			t.Fatal(err)
		}
		resultPath := filepath.Join(home, name+"-result.json")
		command := claudeCommand(context.Background(), ClaudeCLI{Executable: entry}, "auth", "status", "argument with spaces", "apostrophe's value")
		command.Env = append(cliEnvironment(profile), "CCBAR_CLI_RESULT="+resultPath)
		showConsole(command)
		if err := command.Run(); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(resultPath)
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Arguments       []string `json:"arguments"`
			Directory       string   `json:"directory"`
			Credential      string   `json:"credential"`
			APIOverride     string   `json:"apiOverride"`
			LocalPreference string   `json:"localPreference"`
		}
		if err = json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		if result.Credential != name+"-token" || result.Directory != profile.ConfigDirectory || result.APIOverride != "" || result.LocalPreference != "preserved" {
			t.Fatalf("账号或 local 环境路由错误: %+v", result)
		}
		if len(result.Arguments) != 4 || result.Arguments[2] != "argument with spaces" || result.Arguments[3] != "apostrophe's value" {
			t.Fatalf("启动参数发生改变: %+v", result.Arguments)
		}
	}
	data, err := os.ReadFile(filepath.Join(shared, "history.jsonl"))
	if err != nil || !strings.Contains(string(data), "first-token") || !strings.Contains(string(data), "second-token") {
		t.Fatalf("两个独立账号的写入未出现在共享索引: %v", err)
	}
}

// TestCodexImport 验证旧数据格式和前端令牌隔离
func TestCodexImport(t *testing.T) {
	claims := map[string]any{"email": "example@example.test", "exp": time.Now().Add(time.Hour).Unix(), "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "account", "chatgpt_user_id": "user", "chatgpt_plan_type": "pro"}}
	data, _ := json.Marshal(claims)
	token := "fixture." + base64.RawURLEncoding.EncodeToString(data) + ".fixture"
	auth, _ := json.Marshal(map[string]any{"tokens": map[string]string{"access_token": token, "id_token": token, "refresh_token": "fixture-refresh"}})
	store, err := New(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if count, err := store.ImportCodex(context.Background(), string(auth), true); err != nil || count != 1 {
		t.Fatalf("import failed: %d %v", count, err)
	}
	if decrypted, err := secrets.Unprotect(store.codex[0].ProtectedToken); err != nil || decrypted != token {
		t.Fatal(err)
	}
	reloaded, err := New(store.dataDir, store.home)
	if err != nil {
		t.Fatal(err)
	}
	accounts := reloaded.ListCodex()
	if len(accounts) != 1 || accounts[0].ID != "account:user" || accounts[0].Email != "example@example.test" || !accounts[0].VisibleInPopover {
		t.Fatal("persisted account metadata changed")
	}
	public, _ := json.Marshal(accounts)
	if strings.Contains(string(public), token) || strings.Contains(string(public), "protectedToken") || strings.Contains(string(public), "fixture-refresh") {
		t.Fatal("account response contains credentials")
	}
	if err := reloaded.UpdateCodex("account:user", "Main", false); err != nil {
		t.Fatal(err)
	}
	if reloaded.ListCodex()[0].DisplayName != "Main" || reloaded.ListCodex()[0].VisibleInPopover {
		t.Fatal("account settings were not saved")
	}
}

// TestSharedPreferencesAndTasks 验证设置任务共享和账号身份隔离
func TestSharedPreferencesAndTasks(t *testing.T) {
	home := t.TempDir()
	shared := filepath.Join(home, ".claude")
	first := filepath.Join(home, "first")
	second := filepath.Join(home, "second")
	fixtures := map[string]any{
		filepath.Join(shared, "settings.json"): map[string]any{
			"permissions": map[string]any{"allow": []string{"Read"}, "deny": []string{"Bash(rm:*)"}},
			"env":         map[string]string{"ONE": "one"},
		},
		filepath.Join(first, "settings.json"): map[string]any{
			"permissions": map[string]any{"allow": []string{"Write"}, "ask": []string{"Bash"}},
			"env":         map[string]string{"TWO": "two"},
			"model":       "opus",
		},
		filepath.Join(second, "settings.json"): map[string]any{
			"permissions": map[string]any{"allow": []string{"Edit"}},
			"env":         map[string]string{"THREE": "three"},
			"effortLevel": "high",
		},
		filepath.Join(home, ".claude.json"): map[string]any{
			"oauthAccount": map[string]string{"accountUuid": "default"},
			"mcpServers":   map[string]any{"one": map[string]string{"command": "one"}},
		},
		filepath.Join(first, ".claude.json"): map[string]any{
			"oauthAccount": map[string]string{"accountUuid": "first"},
			"mcpServers":   map[string]any{"two": map[string]string{"command": "two"}},
		},
		filepath.Join(second, ".claude.json"): map[string]any{
			"oauthAccount": map[string]string{"accountUuid": "second"},
			"mcpServers":   map[string]any{"three": map[string]string{"command": "three"}},
		},
		filepath.Join(shared, "tasks", "session", "1.json"): map[string]string{"id": "1", "subject": "shared"},
		filepath.Join(first, "tasks", "session", "1.json"):  map[string]string{"id": "1", "description": "retained"},
	}
	for path, value := range fixtures {
		if err := providers.WriteJSON(path, value); err != nil {
			t.Fatal(err)
		}
	}
	for path, value := range map[string]string{
		filepath.Join(first, ".credentials.json"):  "first-private-token",
		filepath.Join(second, ".credentials.json"): "second-private-token",
		filepath.Join(shared, "plans", "plan.md"):  "shared-plan",
		filepath.Join(first, "plans", "plan.md"):   "retained-first-plan",
	} {
		if err := providers.WriteBytes(path, []byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	for _, directory := range []string{first, second, first} {
		if err := prepareHistory(directory, shared); err != nil {
			t.Fatal(err)
		}
	}
	if !historyLinked(first, shared) || !historyLinked(second, shared) {
		t.Fatal("shared preferences or records are not linked")
	}
	settingsData, _ := os.ReadFile(filepath.Join(second, "settings.json"))
	settings, err := providers.DecodeObject(settingsData)
	if err != nil {
		t.Fatal(err)
	}
	permissions := settings["permissions"].(map[string]any)
	variables := settings["env"].(map[string]any)
	if len(permissions["allow"].([]any)) != 3 || len(variables) != 3 || settings["model"] != "opus" || settings["effortLevel"] != "high" {
		t.Fatal("explicit settings keys were lost")
	}
	for _, name := range []string{"tasks", "plans", "todos", "session-env"} {
		path := filepath.Join(second, name, "continued.json")
		if err := providers.WriteBytes(path, []byte("continued")); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(first, name, "continued.json"))
		if err != nil || string(data) != "continued" {
			t.Fatalf("%s changes did not propagate: %v", name, err)
		}
	}
	conflictingPlan, err := os.ReadFile(filepath.Join(shared, ".ccbar-imports", "first", "plans", "plan.md"))
	if err != nil || string(conflictingPlan) != "retained-first-plan" {
		t.Fatal("conflicting non-JSONL record was lost")
	}
	for name, profilePath := range map[string]string{"default": filepath.Join(home, ".claude.json"), "first": filepath.Join(first, ".claude.json"), "second": filepath.Join(second, ".claude.json")} {
		data, _ := os.ReadFile(profilePath)
		profile, err := providers.DecodeObject(data)
		if err != nil || profile["oauthAccount"].(map[string]any)["accountUuid"] != name || len(profile["mcpServers"].(map[string]any)) != 3 {
			t.Fatalf("%s MCP preferences or OAuth identity changed", name)
		}
	}
	for _, directory := range []string{first, second} {
		data, err := os.ReadFile(filepath.Join(directory, ".credentials.json"))
		if err != nil || string(data) != filepath.Base(directory)+"-private-token" {
			t.Fatal("account credential changed during sharing")
		}
	}
	npmDirectory := filepath.Join(home, "npm")
	native := filepath.Join(npmDirectory, "node_modules", "@anthropic-ai", "claude-code", "bin", "claude.exe")
	if err := providers.WriteBytes(native, []byte("fixture")); err != nil {
		t.Fatal(err)
	}
	if err := providers.WriteBytes(filepath.Join(npmDirectory, "claude.cmd"), []byte("@echo off")); err != nil {
		t.Fatal(err)
	}
	originalPath := os.Getenv("PATH")
	t.Setenv("PATH", npmDirectory)
	t.Setenv("LOCALAPPDATA", home)
	cli, err := FindClaude(home)
	if err != nil || !sameDirectory(cli.Executable, filepath.Join(npmDirectory, "claude.cmd")) {
		t.Fatalf("npm Claude wrapper was not preserved: %v", err)
	}
	t.Setenv("PATH", originalPath)
	store, err := New(filepath.Join(home, "app"), home)
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(time.Hour)
	desktop := &model.Credential{AccessToken: "desktop-access", RefreshToken: "desktop-refresh", Scopes: []string{"user:inference", "user:profile"}, AccountUUID: "desktop-account", OrganizationUUID: "desktop-organization", Email: "desktop@example.test", SubscriptionType: "max", ExpiresAt: &expires, Source: "Claude Desktop Code"}
	profile, index, err := store.saveDesktopClaude(desktop)
	if err != nil || index != -1 {
		t.Fatalf("保存桌面 Code 登录: %v", err)
	}
	credential, err := store.readClaude(profile)
	if err != nil || credential == nil || credential.AccessToken != desktop.AccessToken || credential.RefreshToken != desktop.RefreshToken || len(credential.Scopes) != 2 || credential.AccountUUID != desktop.AccountUUID || credential.Email != desktop.Email || !historyLinked(profile.ConfigDirectory, shared) {
		t.Fatalf("桌面 Code 凭据或共享记录发生改变: %v", err)
	}
}
