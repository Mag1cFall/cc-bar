//go:build windows

package accounts

import (
	"bytes"
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
	"github.com/google/uuid"
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
		environment, err := cliEnvironment(profile)
		if err != nil {
			t.Fatal(err)
		}
		command.Env = append(environment, "CCBAR_CLI_RESULT="+resultPath)
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
		if result.Credential != name+"-token" || result.Directory != profile.ConfigDirectory || result.APIOverride != "" || result.LocalPreference != "" {
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
	profile := ClaudeProfile{ID: "228c9016-0cd2-42b4-b1d1-5545477a985d", DesktopLinked: true}
	profile.ConfigDirectory = filepath.Join(store.dataDir, "claude-accounts", profile.ID)
	if err := prepareHistory(profile.ConfigDirectory, shared); err != nil {
		t.Fatal(err)
	}
	if err := store.writeDesktopClaude(profile, desktop); err != nil {
		t.Fatalf("保存桌面 Code 登录: %v", err)
	}
	credential, err := store.readClaude(profile)
	if err != nil || credential == nil || credential.AccessToken != desktop.AccessToken || credential.RefreshToken != desktop.RefreshToken || len(credential.Scopes) != 2 || credential.AccountUUID != desktop.AccountUUID || credential.Email != desktop.Email || !historyLinked(profile.ConfigDirectory, shared) {
		t.Fatalf("桌面 Code 凭据或共享记录发生改变: %v", err)
	}
}

// TestSharedConfigEntries 验证账号私有条目以外的配置全部共享且缺失的共享目录可修复
func TestSharedConfigEntries(t *testing.T) {
	home := t.TempDir()
	shared := filepath.Join(home, ".claude")
	account := filepath.Join(home, "account")
	for path, value := range map[string]string{
		filepath.Join(shared, "CLAUDE.md"):                   "global rules",
		filepath.Join(shared, "skills", "probe", "SKILL.md"): "skill",
		filepath.Join(account, ".credentials.json"):          `{"claudeAiOauth":{"accessToken":"account-token"}}`,
		filepath.Join(account, "agents", "reviewer.md"):      "agent",
		filepath.Join(account, "notes.txt"):                  "account note",
	} {
		if err := providers.WriteBytes(path, []byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	if err := providers.WriteJSON(filepath.Join(home, ".claude.json"), map[string]any{
		"oauthAccount":           map[string]string{"emailAddress": "default@example.com"},
		"hasCompletedOnboarding": true,
		"projects":               map[string]any{"E:/default": map[string]any{"hasTrustDialogAccepted": true}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := providers.WriteJSON(filepath.Join(account, ".claude.json"), map[string]any{
		"oauthAccount": map[string]string{"emailAddress": "account@example.com"},
		"projects":     map[string]any{"E:/account": map[string]any{"allowedTools": []any{"Bash"}}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := prepareHistory(account, shared); err != nil {
		t.Fatal(err)
	}
	if !historyLinked(account, shared) {
		t.Fatal("account entries are not linked")
	}
	for name, want := range map[string]string{"CLAUDE.md": "global rules", filepath.Join("skills", "probe", "SKILL.md"): "skill", filepath.Join("agents", "reviewer.md"): "agent", "notes.txt": "account note"} {
		for _, root := range []string{account, shared} {
			data, err := os.ReadFile(filepath.Join(root, name))
			if err != nil || string(data) != want {
				t.Fatalf("%s in %s: %q %v", name, root, data, err)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(shared, ".credentials.json")); !os.IsNotExist(err) {
		t.Fatal("account credentials leaked into shared directory")
	}
	for path, email := range map[string]string{filepath.Join(home, ".claude.json"): "default@example.com", filepath.Join(account, ".claude.json"): "account@example.com"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		profile, err := providers.DecodeObject(data)
		if err != nil {
			t.Fatal(err)
		}
		projects, _ := profile["projects"].(map[string]any)
		if profile["oauthAccount"].(map[string]any)["emailAddress"] != email || profile["hasCompletedOnboarding"] != true || projects["E:/default"] == nil || projects["E:/account"] == nil {
			t.Fatalf("profile preferences not synchronized: %s", data)
		}
	}
	if err := os.RemoveAll(filepath.Join(shared, "plans")); err != nil {
		t.Fatal(err)
	}
	if historyLinked(account, shared) {
		t.Fatal("missing shared directory reported as linked")
	}
	if err := prepareHistory(account, shared); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(account, "plans", "plan.md"), []byte("plan"), 0600); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(shared, "plans", "plan.md")); err != nil || string(data) != "plan" || !historyLinked(account, shared) {
		t.Fatalf("restored plans link failed: %v", err)
	}
}

// TestReplacedSharedFile 验证被替换而断开的共享文件以最后修改的一份为准
func TestReplacedSharedFile(t *testing.T) {
	home := t.TempDir()
	shared := filepath.Join(home, ".claude")
	first, second := filepath.Join(home, "first"), filepath.Join(home, "second")
	if err := providers.WriteBytes(filepath.Join(shared, "CLAUDE.md"), []byte("old rules")); err != nil {
		t.Fatal(err)
	}
	if err := providers.WriteJSON(filepath.Join(shared, "settings.json"), map[string]any{"hooks": map[string]any{"PreToolUse": []any{"guard"}}, "model": "opus"}); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{first, second} {
		if err := prepareHistory(directory, shared); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-time.Hour)
	for _, path := range []string{filepath.Join(shared, "CLAUDE.md"), filepath.Join(shared, "settings.json")} {
		if err := os.Chtimes(path, past, past); err != nil {
			t.Fatal(err)
		}
	}
	if err := providers.WriteBytes(filepath.Join(first, "CLAUDE.md"), []byte("new rules")); err != nil {
		t.Fatal(err)
	}
	if err := providers.WriteJSON(filepath.Join(first, "settings.json"), map[string]any{"model": "opus"}); err != nil {
		t.Fatal(err)
	}
	if historyLinked(first, shared) {
		t.Fatal("replaced files reported as linked")
	}
	if err := prepareHistory(first, shared); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{shared, first, second} {
		rules, _ := os.ReadFile(filepath.Join(directory, "CLAUDE.md"))
		settings, _ := os.ReadFile(filepath.Join(directory, "settings.json"))
		if string(rules) != "new rules" || bytes.Contains(settings, []byte("hooks")) {
			t.Fatalf("newer replacement lost in %s: %q %s", directory, rules, settings)
		}
	}
	if !historyLinked(first, shared) || !historyLinked(second, shared) {
		t.Fatal("replaced files were not relinked")
	}
	if data, err := os.ReadFile(filepath.Join(shared, ".ccbar-imports", "shared", "CLAUDE.md")); err != nil || string(data) != "old rules" {
		t.Fatalf("older content was not preserved: %v", err)
	}
}

// TestProfilePreferenceDeletion 验证任一侧的删除与新增按上次同步结果传播
func TestProfilePreferenceDeletion(t *testing.T) {
	home := t.TempDir()
	shared := filepath.Join(home, ".claude")
	account := filepath.Join(home, "account")
	defaultPath, accountPath := filepath.Join(home, ".claude.json"), filepath.Join(account, ".claude.json")
	if err := providers.WriteJSON(defaultPath, map[string]any{"oauthAccount": "default", "mcpServers": map[string]any{"keep": "a", "drop": "b"}, "projects": map[string]any{"E:/one": map[string]any{}}}); err != nil {
		t.Fatal(err)
	}
	if err := providers.WriteJSON(accountPath, map[string]any{"oauthAccount": "account"}); err != nil {
		t.Fatal(err)
	}
	if err := prepareHistory(account, shared); err != nil {
		t.Fatal(err)
	}
	read := func(path string) map[string]any {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		value, err := providers.DecodeObject(data)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	profile := read(accountPath)
	delete(profile["mcpServers"].(map[string]any), "drop")
	if err := providers.WriteJSON(accountPath, profile); err != nil {
		t.Fatal(err)
	}
	global := read(defaultPath)
	global["projects"].(map[string]any)["E:/two"] = map[string]any{}
	if err := providers.WriteJSON(defaultPath, global); err != nil {
		t.Fatal(err)
	}
	if err := prepareHistory(account, shared); err != nil {
		t.Fatal(err)
	}
	for path, identity := range map[string]string{defaultPath: "default", accountPath: "account"} {
		value := read(path)
		servers := value["mcpServers"].(map[string]any)
		projects := value["projects"].(map[string]any)
		if value["oauthAccount"] != identity || servers["drop"] != nil || servers["keep"] != "a" || projects["E:/two"] == nil {
			t.Fatalf("preference changes not propagated in %s: %v", path, value)
		}
	}
	if err := os.Mkdir(defaultPath+".lock", 0700); err != nil {
		t.Fatal(err)
	}
	profile = read(accountPath)
	delete(profile["mcpServers"].(map[string]any), "keep")
	if err := providers.WriteJSON(accountPath, profile); err != nil {
		t.Fatal(err)
	}
	if err := prepareHistory(account, shared); err != nil {
		t.Fatal(err)
	}
	if read(defaultPath)["mcpServers"].(map[string]any)["keep"] != "a" {
		t.Fatal("profile written while Claude Code held its lock")
	}
	if _, err := os.Stat(defaultPath + ".lock"); err != nil {
		t.Fatal("Claude Code lock was removed")
	}
}

// TestDesktopDeletedSessions 验证删除的 Desktop 会话不会在切换后恢复
func TestDesktopDeletedSessions(t *testing.T) {
	directory := t.TempDir()
	first := ClaudeProfile{ID: "first", AccountUUID: uuid.NewString(), OrganizationUUID: uuid.NewString(), DesktopSaved: true}
	second := ClaudeProfile{ID: "second", AccountUUID: uuid.NewString(), OrganizationUUID: uuid.NewString(), DesktopSaved: true}
	firstRoot := filepath.Join(directory, "claude-code-sessions", first.AccountUUID, first.OrganizationUUID)
	secondRoot := filepath.Join(directory, "claude-code-sessions", second.AccountUUID, second.OrganizationUUID)
	past := time.Now().Add(-time.Hour)
	for path, data := range map[string]string{
		filepath.Join(firstRoot, "local_gone.json"):     `{"cliSessionId":"gone"}`,
		filepath.Join(firstRoot, "local_kept.json"):     `{"cliSessionId":"kept"}`,
		filepath.Join(secondRoot, "deleted_gone"):       "1",
		filepath.Join(firstRoot, "deleted_revived"):     "1",
		filepath.Join(secondRoot, "local_revived.json"): `{"cliSessionId":"revived"}`,
	} {
		if err := providers.WriteBytes(path, []byte(data)); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(filepath.Base(path), "local_") || filepath.Dir(path) == firstRoot {
			if err := os.Chtimes(path, past, past); err != nil {
				t.Fatal(err)
			}
		}
	}
	future := time.Now().Add(time.Minute)
	if err := os.Chtimes(filepath.Join(secondRoot, "local_revived.json"), future, future); err != nil {
		t.Fatal(err)
	}
	profiles := []ClaudeProfile{first, second}
	if err := syncDesktopCodeHistory(directory, first, profiles); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(firstRoot, "local_gone.json")); !os.IsNotExist(err) {
		t.Fatal("deleted session restored")
	}
	if _, err := os.Stat(filepath.Join(firstRoot, "deleted_gone")); err != nil {
		t.Fatal("deletion marker not synchronized")
	}
	if _, err := os.Stat(filepath.Join(firstRoot, "local_kept.json")); err != nil {
		t.Fatal("kept session removed")
	}
	if _, err := os.Stat(filepath.Join(firstRoot, "local_revived.json")); err != nil {
		t.Fatal("session used after deletion was not restored")
	}
	if _, err := os.Stat(filepath.Join(firstRoot, "deleted_revived")); !os.IsNotExist(err) {
		t.Fatal("stale deletion marker kept beside a newer session")
	}
	if err := syncDesktopCodeHistory(directory, second, profiles); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(secondRoot, "deleted_revived")); !os.IsNotExist(err) {
		t.Fatal("stale deletion marker copied to the other account")
	}
	if _, err := os.Stat(filepath.Join(secondRoot, "local_gone.json")); !os.IsNotExist(err) {
		t.Fatal("deleted session copied to the other account")
	}
	if _, err := os.Stat(filepath.Join(secondRoot, "local_kept.json")); err != nil {
		t.Fatal("kept session not shared")
	}
}

// TestRestoreSharedDirectory 验证 Claude Code 清理删除空的共享目录后为账号联接重建目标
func TestRestoreSharedDirectory(t *testing.T) {
	home := t.TempDir()
	dataDir := filepath.Join(home, "data")
	shared := filepath.Join(home, ".claude")
	profile := ClaudeProfile{ID: strings.ReplaceAll(uuid.NewString(), "-", "")}
	profile.ConfigDirectory = filepath.Join(dataDir, "claude-accounts", profile.ID)
	if err := prepareHistory(profile.ConfigDirectory, shared); err != nil {
		t.Fatal(err)
	}
	store := &Store{dataDir: dataDir, home: home, claude: []ClaudeProfile{profile}}
	for _, name := range []string{"plans", "notes"} {
		path := filepath.Join(shared, name)
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
		if err := store.RestoreSharedDirectory(path); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(profile.ConfigDirectory, "plans", "plan.md"), []byte("plan"), 0600); err != nil {
		t.Fatalf("account plans link still broken: %v", err)
	}
	if _, err := os.Stat(filepath.Join(shared, "notes")); !os.IsNotExist(err) {
		t.Fatal("directory without an account link was recreated")
	}
}
