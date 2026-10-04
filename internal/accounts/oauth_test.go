package accounts

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Mag1cFall/cc-bar/internal/providers"
	"github.com/Mag1cFall/cc-bar/internal/secrets"
	"github.com/google/uuid"
)

// oauthTransport 将登录测试限定到本机隔离的授权响应
type oauthTransport func(*http.Request) (*http.Response, error)

func (transport oauthTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

// TestNativeOAuth 验证自动回调身份命名去重取消及凭据隔离
func TestNativeOAuth(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := t.TempDir()
	store, err := New(filepath.Join(home, "ccbar"), home)
	if err != nil {
		t.Fatal(err)
	}
	defer store.StopLogin()
	originalClient := providers.HTTPClient
	defer func() { providers.HTTPClient = originalClient }()
	var challenge, state string
	completeGrant := true
	providers.HTTPClient = &http.Client{Transport: oauthTransport(func(request *http.Request) (*http.Response, error) {
		var body string
		switch request.URL.Path {
		case "/v1/oauth/token":
			var fields map[string]string
			if err := json.NewDecoder(request.Body).Decode(&fields); err != nil {
				return nil, err
			}
			computed := sha256.Sum256([]byte(fields["code_verifier"]))
			if fields["code"] != "returned-code" || fields["state"] != state || base64.RawURLEncoding.EncodeToString(computed[:]) != challenge || fields["client_id"] != providers.ClaudeOAuthClientID {
				return nil, fmt.Errorf("OAuth 请求未携带对应的 PKCE 与状态")
			}
			body = `{"access_token":"private-access","refresh_token":"private-refresh","expires_in":3600,"scope":"user:profile user:inference","account":{"uuid":"account-first","email_address":"first@example.test"},"organization":{"uuid":"org-first"}}`
			if !completeGrant {
				body = strings.Replace(body, "user:profile user:inference", "user:profile", 1)
			}
		case "/api/oauth/profile":
			if request.Header.Get("Authorization") != "Bearer private-access" {
				return nil, fmt.Errorf("身份请求未使用授权返回的令牌")
			}
			body = `{"account":{"uuid":"account-first","email":"first@example.test"},"organization":{"uuid":"org-first","organization_type":"claude_max"}}`
		default:
			return nil, fmt.Errorf("测试访问了额外接口: %s", request.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}
	var stages []string
	var stagesMu sync.Mutex
	store.Changed = func() {
		if progress := store.LoginStatus(); progress != nil {
			stagesMu.Lock()
			stages = append(stages, progress.Stage)
			stagesMu.Unlock()
		}
	}
	openBrowser := func(address string) error {
		authorize, err := url.Parse(address)
		if err != nil {
			return err
		}
		query := authorize.Query()
		challenge, state = query.Get("code_challenge"), query.Get("state")
		if query.Get("code_challenge_method") != "S256" || query.Get("client_id") != providers.ClaudeOAuthClientID {
			return fmt.Errorf("授权地址未使用官方客户端及 PKCE")
		}
		callback, _ := url.Parse(query.Get("redirect_uri"))
		callback.RawQuery = url.Values{"code": {"returned-code"}, "state": {state}}.Encode()
		response, err := http.Get(callback.String())
		if err == nil {
			response.Body.Close()
		}
		return err
	}
	for i := 0; i < 2; i++ {
		if _, err := store.BeginClaudeLogin("", openBrowser); err != nil {
			t.Fatal(err)
		}
		waitLogin(t, store)
		if status := store.LoginStatus(); status.Stage != "done" {
			t.Fatalf("登录没有完成: %+v", status)
		}
	}
	profiles := store.ListClaude()
	if len(profiles) != 1 || profiles[0].Name != "first" || !profiles[0].IsActive || !profiles[0].HistoryShared || profiles[0].NeedsLogin {
		t.Fatalf("身份命名去重或共享记录有误: %+v", profiles)
	}
	credentialsPath := filepath.Join(profiles[0].ConfigDirectory, ".credentials.json")
	savedCredentials, _ := os.ReadFile(credentialsPath)
	completeGrant = false
	if _, err := store.BeginClaudeLogin("", openBrowser); err != nil {
		t.Fatal(err)
	}
	waitLogin(t, store)
	currentCredentials, _ := os.ReadFile(credentialsPath)
	if store.LoginStatus().Stage != "error" || len(store.ListClaude()) != 1 || !bytes.Equal(savedCredentials, currentCredentials) {
		t.Fatal("半套授权被保存或覆盖了完整登录")
	}
	completeGrant = true
	if err := store.SwitchClaude(profiles[0].ID); err != nil || store.LoginStatus().Stage != "done" || !store.ListClaude()[0].IsActive {
		t.Fatalf("CLI 账号切换或完成状态有误: %v", err)
	}
	encoded, _ := json.Marshal(store.LoginStatus())
	if bytes.Contains(encoded, []byte("private-access")) || bytes.Contains(encoded, []byte("private-refresh")) {
		t.Fatal("登录进度包含凭据")
	}
	stagesMu.Lock()
	observed := strings.Join(stages, ",")
	stagesMu.Unlock()
	for _, expected := range []string{"starting", "browser", "exchanging", "verifying", "saving", "done"} {
		if !strings.Contains(observed, expected) {
			t.Fatalf("缺少登录阶段 %s: %s", expected, observed)
		}
	}
	opened := make(chan string, 1)
	if _, err := store.BeginClaudeLogin("", func(address string) error { opened <- address; return nil }); err != nil {
		t.Fatal(err)
	}
	address := <-opened
	store.CancelClaudeLogin()
	waitLogin(t, store)
	if store.LoginStatus().Stage != "cancelled" || len(store.ListClaude()) != 1 {
		t.Fatal("取消登录留下了额外账号")
	}
	authorize, _ := url.Parse(address)
	client := &http.Client{Timeout: time.Second}
	if response, err := client.Get(authorize.Query().Get("redirect_uri")); err == nil {
		response.Body.Close()
		t.Fatal("取消登录后回调端口仍在监听")
	}
	store.mu.Lock()
	store.claude[0].DesktopSaved = true
	firstSession := store.desktopSession(store.claude[0])
	partial := ClaudeProfile{ID: "2c18cff7-6c11-4d27-b55f-cffef091151b", Name: "partial"}
	partial.ConfigDirectory = filepath.Join(store.dataDir, "claude-accounts", partial.ID)
	store.claude = append(store.claude, partial)
	err = store.saveClaudeLocked()
	store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := providers.WriteJSON(filepath.Join(firstSession, "config.json"), map[string]any{"lastKnownAccountUuid": "account-first"}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(firstSession, "Network"), 0700); err != nil {
		t.Fatal(err)
	}
	cookies, err := sql.Open("sqlite", filepath.Join(firstSession, "Network", "Cookies"))
	if err != nil {
		t.Fatal(err)
	}
	_, cookieErr := cookies.Exec(`CREATE TABLE cookies (name TEXT, host_key TEXT, encrypted_value BLOB, value TEXT); INSERT INTO cookies VALUES ('sessionKey', '.claude.ai', X'01', '')`)
	cookies.Close()
	if cookieErr != nil {
		t.Fatal(cookieErr)
	}
	if err := prepareHistory(partial.ConfigDirectory, filepath.Join(home, ".claude")); err != nil {
		t.Fatal(err)
	}
	if err := providers.WriteBytes(filepath.Join(partial.ConfigDirectory, ".credentials.json"), []byte("partial-token")); err != nil {
		t.Fatal(err)
	}
	if err := providers.WriteJSON(filepath.Join(partial.ConfigDirectory, ".claude.json"), map[string]any{"oauthAccount": map[string]any{"accountUuid": "partial"}, "preference": "retained"}); err != nil {
		t.Fatal(err)
	}
	if err := providers.WriteBytes(filepath.Join(store.desktopSession(partial), "config.json"), []byte("partial-session")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", partial.ConfigDirectory)
	reloaded, err := New(store.dataDir, home)
	if err != nil || len(reloaded.ListClaude()) != 1 {
		t.Fatalf("半套授权仍在账号列表中: %v", err)
	}
	if _, err := os.Stat(filepath.Join(partial.ConfigDirectory, ".credentials.json")); !os.IsNotExist(err) {
		t.Fatal("半套授权凭据仍被保留")
	}
	if _, err := os.Stat(store.desktopSession(partial)); !os.IsNotExist(err) || os.Getenv("CLAUDE_CONFIG_DIR") != profiles[0].ConfigDirectory {
		t.Fatal("半套会话仍被保留或默认账号指向已清理的登录")
	}
	identityData, _ := os.ReadFile(filepath.Join(partial.ConfigDirectory, ".claude.json"))
	identity, _ := providers.DecodeObject(identityData)
	if identity["preference"] != "retained" || identity["oauthAccount"] != nil || !historyLinked(partial.ConfigDirectory, filepath.Join(home, ".claude")) {
		t.Fatal("清理授权修改了偏好或共享会话")
	}
}

func waitLogin(t *testing.T, store *Store) {
	t.Helper()
	store.mu.Lock()
	completed := store.login.done
	store.mu.Unlock()
	select {
	case <-completed:
	case <-time.After(5 * time.Second):
		t.Fatal("登录过程未按时结束")
	}
}

// TestOAuthCallback 验证错误状态和取消不会冒充成功授权
func TestOAuthCallback(t *testing.T) {
	code := make(chan string, 1)
	failure := make(chan error, 1)
	handler := oauthCallback("expected", code, failure)
	for _, path := range []string{"/callback?state=wrong&code=bad", "/callback?state=expected"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusBadRequest || len(code) != 0 || len(failure) != 0 {
			t.Fatal("无效回调改变了登录状态")
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/callback?state=expected&error=access_denied", nil))
	if len(code) != 0 || len(failure) != 1 {
		t.Fatal("拒绝授权被当作登录成功")
	}
}

// TestDesktopSessions 验证网页及 Code 会话同时切换并保持偏好和对话
func TestDesktopSessions(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "live")
	first, second := filepath.Join(root, "first"), filepath.Join(root, "second")
	seed := func(identity string) {
		t.Helper()
		if err := providers.WriteJSON(filepath.Join(live, "config.json"), map[string]any{"lastKnownAccountUuid": identity, "oauth:tokenCacheV2": identity + "-code", "windowWidth": 1600}); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"Local State", "Network/Cookies", "Local Storage/session", "Session Storage/current", "claude-code-sessions/project/conversation.json"} {
			if err := providers.WriteBytes(filepath.Join(live, name), []byte(identity+":"+name)); err != nil {
				t.Fatal(err)
			}
		}
	}
	seed("first")
	if err := saveDesktopSession(live, first); err != nil {
		t.Fatal(err)
	}
	if err := restoreDesktopSession(live, ""); err != nil {
		t.Fatal(err)
	}
	if desktopIdentity(live) != "" {
		t.Fatal("新的原生登录仍带有旧账号")
	}
	seed("second")
	if err := saveDesktopSession(live, second); err != nil {
		t.Fatal(err)
	}
	for _, target := range []struct{ path, identity string }{{first, "first"}, {second, "second"}, {first, "first"}} {
		if err := restoreDesktopSession(live, target.path); err != nil {
			t.Fatal(err)
		}
		if desktopIdentity(live) != target.identity {
			t.Fatal("Desktop 身份没有随切换更新")
		}
		for _, name := range []string{"Local State", "Network/Cookies"} {
			data, err := os.ReadFile(filepath.Join(live, name))
			if err != nil || string(data) != target.identity+":"+name {
				t.Fatalf("网页会话未与 Code 账号一起切换: %s", name)
			}
		}
		config, _ := os.ReadFile(filepath.Join(live, "config.json"))
		if !bytes.Contains(config, []byte("windowWidth")) || !bytes.Contains(config, []byte(target.identity+"-code")) {
			t.Fatal("偏好或 Code 缓存丢失")
		}
	}
	conversation, err := os.ReadFile(filepath.Join(live, "claude-code-sessions", "project", "conversation.json"))
	if err != nil || !strings.HasPrefix(string(conversation), "second:") {
		t.Fatal("切换修改了账号会话文件")
	}
	for _, name := range []string{"Local Storage/session", "Session Storage/current"} {
		data, err := os.ReadFile(filepath.Join(live, name))
		if err != nil || string(data) != "second:"+name {
			t.Fatalf("切换覆盖了共享页面状态: %s", name)
		}
	}
	outside := filepath.Join(root, "outside")
	if err := providers.WriteBytes(filepath.Join(outside, "keep.txt"), []byte("retained")); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(second, "Network", "linked")
	if err := createDirectoryLink(link, outside); err != nil {
		t.Fatal(err)
	}
	restoreErr := restoreDesktopSession(live, second)
	if restoreErr == nil {
		t.Fatal("含有目录联接的会话被覆盖到当前账号")
	}
	data, err := os.ReadFile(filepath.Join(live, "Network", "Cookies"))
	if err != nil || string(data) != "first:Network/Cookies" || desktopIdentity(live) != "first" {
		t.Fatalf("失败的切换修改了原登录: cookies=%q identity=%q read=%v restore=%v", string(data), desktopIdentity(live), err, restoreErr)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(outside, "keep.txt")); err != nil || string(data) != "retained" {
		t.Fatal("会话切换修改了链接指向的文件")
	}
	profiles := []ClaudeProfile{
		{AccountUUID: uuid.NewString(), OrganizationUUID: uuid.NewString(), DesktopSaved: true},
		{AccountUUID: uuid.NewString(), OrganizationUUID: uuid.NewString(), DesktopSaved: true},
	}
	firstRecords, _ := desktopCodeDirectory(live, profiles[0])
	secondRecords, _ := desktopCodeDirectory(live, profiles[1])
	name := "local_" + uuid.NewString() + ".json"
	record := map[string]any{"cliSessionId": uuid.NewString(), "title": "first title"}
	if err := providers.WriteJSON(filepath.Join(firstRecords, name), record); err != nil {
		t.Fatal(err)
	}
	if err := providers.WriteJSON(filepath.Join(secondRecords, "scheduled-tasks.json"), map[string]any{"account": "second"}); err != nil {
		t.Fatal(err)
	}
	if err := syncDesktopCodeHistory(live, profiles[1], profiles); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(secondRecords, name))
	if err != nil || !bytes.Contains(data, []byte("first title")) {
		t.Fatal("Desktop 本地会话未随账号切换共享")
	}
	record["title"] = "latest title"
	if err := providers.WriteJSON(filepath.Join(secondRecords, name), record); err != nil {
		t.Fatal(err)
	}
	updated := time.Now().Add(time.Second)
	if err := os.Chtimes(filepath.Join(secondRecords, name), updated, updated); err != nil {
		t.Fatal(err)
	}
	if err := syncDesktopCodeHistory(live, profiles[0], profiles); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(firstRecords, name))
	if err != nil || !bytes.Contains(data, []byte("latest title")) {
		t.Fatal("Desktop 会话标题更新未同步回原账号")
	}
	if _, err := os.Stat(filepath.Join(firstRecords, "scheduled-tasks.json")); !os.IsNotExist(err) {
		t.Fatal("Desktop 账号定时任务被混入共享会话")
	}
}

// TestDesktopClose 验证已退出与过期进程编号不会阻塞登录
func TestDesktopClose(t *testing.T) {
	for _, processID := range []int{0, os.Getpid()} {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		desktop := desktopApp{Executable: filepath.Join(t.TempDir(), "claude.exe"), ProcessID: processID}
		err := desktop.close(ctx)
		cancel()
		if err != nil || desktop.ProcessID != 0 {
			t.Fatalf("已退出的 Desktop 阻塞后续登录: 原进程=%d 错误=%v", processID, err)
		}
	}
}

// TestDesktopCookies 验证原生支持的三种网页登录 Cookie
func TestDesktopCookies(t *testing.T) {
	directory := t.TempDir()
	if err := os.MkdirAll(filepath.Join(directory, "Network"), 0700); err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", filepath.Join(directory, "Network", "Cookies"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(`CREATE TABLE cookies (name TEXT, host_key TEXT, encrypted_value BLOB, value TEXT)`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sessionKey", "sessionKeyV2", "sessionKeyV3"} {
		if _, err := database.Exec(`DELETE FROM cookies; INSERT INTO cookies VALUES (?, '.claude.ai', X'01', '')`, name); err != nil {
			t.Fatal(err)
		}
		if !desktopWebSession(directory) {
			t.Errorf("已登录的 %s 被判为缺少网页登录", name)
		}
	}
}

// TestDesktopCookieRead 核对当前 Cookie 类型与账号身份并沿用原登录
func TestDesktopCookieRead(t *testing.T) {
	if os.Getenv("CCBAR_LIVE_READ") != "1" {
		t.Skip("本机 Cookie 读取按需运行")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	desktop, err := findDesktop(ctx)
	if err != nil || desktop == nil {
		t.Fatalf("读取原生目录: %v", err)
	}
	if err := desktop.close(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := desktop.launch(); err != nil {
			t.Error(err)
		}
	}()
	data, err := os.ReadFile(filepath.Join(desktop.Directory, "Network", "Cookies"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "Cookies")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	rows, err := database.Query(`SELECT name, host_key, length(encrypted_value) > 0 OR length(value) > 0 FROM cookies WHERE name LIKE 'sessionKey%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, domain string
		var present bool
		if err := rows.Scan(&name, &domain, &present); err != nil {
			t.Fatal(err)
		}
		t.Logf("Cookie类型=%s 域=%s 有凭据=%t", name, domain, present)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !desktopWebSession(desktop.Directory) {
		t.Fatal("已登录的 Desktop 缺少识别到的网页凭据")
	}
	credential, err := providers.ReadClaudeDesktopDirectory(desktop.Directory, nil)
	if err != nil || credential == nil {
		t.Fatalf("读取 Code 身份: %v", err)
	}
	t.Logf("Desktop=%s Code=%s 身份一致=%t", desktopIdentity(desktop.Directory), credential.AccountUUID, desktopIdentity(desktop.Directory) == credential.AccountUUID)
}

// TestInstalledDesktopRead 验证本机实际安装与 OAuth 身份读取而保持登录原样
func TestInstalledDesktopRead(t *testing.T) {
	if os.Getenv("CCBAR_LIVE_READ") != "1" {
		t.Skip("本机读取检查按需运行")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	desktop, err := findDesktop(ctx)
	if err != nil || desktop == nil {
		t.Fatalf("读取 Desktop 安装: %v", err)
	}
	credential, err := providers.ReadClaudeDesktopDirectory(desktop.Directory, nil)
	if err != nil || credential == nil {
		t.Fatalf("读取 Desktop 登录: %v", err)
	}
	if err := providers.FetchClaudeProfile(ctx, credential); err != nil {
		t.Fatalf("核对官方账号资料: %v", err)
	}
	web := desktopWebSession(desktop.Directory)
	if !web {
		data, _ := os.ReadFile(filepath.Join(os.Getenv("LOCALAPPDATA"), "CCBar", "claude-accounts.json"))
		var saved []ClaudeProfile
		if json.Unmarshal(data, &saved) == nil {
			for _, profile := range saved {
				if profile.AccountUUID == credential.AccountUUID {
					web = desktopWebSession(filepath.Join(os.Getenv("LOCALAPPDATA"), "CCBar", "claude-desktop", profile.ID))
				}
			}
		}
	}
	if !web || credential.RefreshToken == "" {
		t.Fatalf("Desktop 登录未包含完整授权: 网页=%t Code刷新凭据=%t", web, credential.RefreshToken != "")
	}
	t.Logf("安装=%s 网页=%t Code=%t 身份匹配=%t 邮箱已读取=%t", filepath.Base(filepath.Dir(desktop.Executable)), web, credential.Source == "Claude Desktop Code", credential.AccountUUID == desktopIdentity(desktop.Directory), credential.Email != "")
	if os.Getenv("CCBAR_LIVE_SWITCH") != "1" {
		return
	}
	store, err := New(filepath.Join(os.Getenv("LOCALAPPDATA"), "CCBar"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.StopLogin()
	baseline := credential.AccountUUID
	current, err := store.SaveCurrentClaude()
	if err != nil || current == nil || !store.HasDesktopSession(*current) {
		t.Fatalf("保存当前完整登录: %v", err)
	}
	completion, err := store.beginLogin(current.ID, "desktop", 45*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	completion.desktop, err = store.desktopApp(completion.ctx)
	if err == nil {
		completion.previous = current
		err = store.finishClaudeLogin(completion, credential)
	}
	store.finishLogin(completion, err)
	completion.cancel()
	if err != nil || store.LoginStatus().Stage != "done" {
		t.Fatalf("完整授权的实际保存失败: %v", err)
	}
	if secondEmail := os.Getenv("CCBAR_LIVE_SECOND"); secondEmail != "" {
		acceptDesktopAccounts(t, store, *current, secondEmail)
		return
	}
	if err := store.SwitchClaude(current.ID); err != nil {
		t.Fatalf("同步 Desktop 与 CLI 默认登录: %v", err)
	}
	if _, err := store.BeginClaudeLogin("", func(string) error { return fmt.Errorf("原生登录意外进入 CLI-only 授权") }); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for status := store.LoginStatus(); status.Stage != "browser"; status = store.LoginStatus() {
		if status.Stage == "error" || status.Stage == "done" || time.Now().After(deadline) {
			store.CancelClaudeLogin()
			store.StopLogin()
			t.Fatalf("原生登录准备阶段有误: %s %s", status.Stage, status.Error)
		}
		time.Sleep(100 * time.Millisecond)
	}
	store.CancelClaudeLogin()
	store.StopLogin()
	if status := store.LoginStatus(); status.Stage != "cancelled" {
		t.Fatalf("取消未完成恢复: %s %s", status.Stage, status.Error)
	}
	if desktopIdentity(desktop.Directory) != baseline || !store.HasDesktopSession(*current) {
		t.Fatal("取消改变了原账号的 Desktop 登录")
	}
	restored, err := providers.ReadClaudeDesktopDirectory(desktop.Directory, nil)
	if err != nil || restored == nil {
		t.Fatalf("恢复后的 Code 登录读取失败: %v", err)
	}
	verify, cancelVerify := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelVerify()
	if err := providers.FetchClaudeProfile(verify, restored); err != nil || restored.AccountUUID != baseline {
		t.Fatalf("恢复后的官方身份核对失败: %v", err)
	}
	for _, profile := range store.ListClaude() {
		if !store.HasDesktopSession(profile) {
			t.Fatal("半套授权仍出现在账号列表")
		}
	}
	t.Logf("本机保存完整登录=true 完整授权提交=true 同步默认账号=true 取消恢复=true 官方身份一致=true 完整账号数=%d", len(store.ListClaude()))
}

// acceptDesktopAccounts 对两个实际授权的账号执行来回切换
func acceptDesktopAccounts(t *testing.T, store *Store, first ClaudeProfile, secondEmail string) {
	t.Helper()
	defer func() {
		if err := store.SwitchClaude(first.ID); err != nil {
			t.Errorf("验收后恢复起始账号: %v", err)
		}
	}()
	findSecond := func() *ClaudeProfile {
		for _, profile := range store.ListClaude() {
			if profile.ID != first.ID && strings.HasPrefix(profile.Email, secondEmail) && store.HasDesktopSession(profile) {
				return &profile
			}
		}
		return nil
	}
	second := findSecond()
	if second == nil {
		store.Changed = func() {
			status := store.LoginStatus()
			if status != nil {
				t.Logf("第二账号授权阶段=%s", status.Stage)
			}
		}
		if _, err := store.BeginClaudeLogin("", func(string) error { return fmt.Errorf("双账号验收应使用原生 Desktop 授权") }); err != nil {
			t.Fatal(err)
		}
		store.mu.Lock()
		done := store.login.done
		store.mu.Unlock()
		<-done
		store.Changed = nil
		if status := store.LoginStatus(); status.Stage != "done" {
			t.Fatalf("第二账号完整授权失败: %s %s", status.Stage, status.Error)
		}
		second = findSecond()
		if second == nil {
			t.Fatalf("新授权未保存目标账号 %s", secondEmail)
		}
	}
	for index, profile := range []ClaudeProfile{*second, first, *second, first} {
		if err := store.SwitchClaude(profile.ID); err != nil {
			t.Fatalf("第 %d 次切换到 %s: %v", index+1, profile.Email, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		desktop, err := store.desktopApp(ctx)
		if err != nil || desktop == nil {
			cancel()
			t.Fatalf("读取切换后的 Desktop: %v", err)
		}
		credential, err := providers.ReadClaudeDesktopDirectory(desktop.Directory, nil)
		if err == nil && credential != nil {
			err = providers.FetchClaudeProfile(ctx, credential)
		}
		cancel()
		if err != nil || credential == nil || credential.AccountUUID != profile.AccountUUID || credential.Email != profile.Email || desktopIdentity(desktop.Directory) != profile.AccountUUID {
			t.Fatalf("第 %d 次切换的实际身份未匹配 %s: %v", index+1, profile.Email, err)
		}
		var active *ClaudeProfile
		for _, item := range store.ListClaude() {
			if item.IsActive {
				active = &item
			}
		}
		if active == nil || active.ID != profile.ID || !active.HistoryShared || !store.HasDesktopSession(profile) {
			t.Fatal("CLI 默认账号、共享记录或 Desktop 会话未随切换保持一致")
		}
		records := readDesktopCodeWindow(t, desktop, profile)
		t.Logf("第 %d 次实际切换=%s Desktop身份=true Code官方身份=true CLI默认=true Desktop实际列表=%d", index+1, profile.Email, records)
	}
}

// readDesktopCodeWindow 只读核对原生窗口中实际呈现的本地会话标题
func readDesktopCodeWindow(t *testing.T, desktop *desktopApp, profile ClaudeProfile) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := desktop.close(ctx); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(desktop.Executable, "--force-renderer-accessibility")
	command.Env = desktopEnvironment()
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	go command.Wait()
	directory, err := desktopCodeDirectory(desktop.Directory, profile)
	if err != nil {
		t.Fatal(err)
	}
	script := `$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName UIAutomationClient, UIAutomationTypes
$records = @(Get-ChildItem -LiteralPath $env:CCBAR_RECORDS -Filter 'local_*.json' -File | ForEach-Object {
    try { Get-Content -LiteralPath $_.FullName -Raw -Encoding UTF8 | ConvertFrom-Json }
    catch { throw ('Invalid history metadata: ' + $_.Exception.GetType().Name) }
})
$deadline = (Get-Date).AddSeconds(18)
do {
    $matched = 0
    $desktop = Get-Process -Name claude -ErrorAction SilentlyContinue | Where-Object { $_.Path -ieq $env:CCBAR_DESKTOP -and $_.MainWindowHandle -ne [IntPtr]::Zero } | Select-Object -First 1
    if ($desktop) {
        $root = [System.Windows.Automation.AutomationElement]::FromHandle($desktop.MainWindowHandle)
        $elements = $root.FindAll([System.Windows.Automation.TreeScope]::Descendants, [System.Windows.Automation.Condition]::TrueCondition)
        $names = @($elements | ForEach-Object { $_.Current.Name } | Where-Object { $_ })
        foreach ($record in $records) {
            if ($record.title -and ($names | Where-Object { $_.IndexOf($record.title, [StringComparison]::Ordinal) -ge 0 })) { $matched++ }
        }
    }
    if ($matched -eq $records.Count -and $records.Count -gt 0) { break }
    Start-Sleep -Milliseconds 300
} while ((Get-Date) -lt $deadline)
@{ expected = $records.Count; matched = $matched } | ConvertTo-Json -Compress`
	reader := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	reader.Env = append(os.Environ(), "CCBAR_RECORDS="+directory, "CCBAR_DESKTOP="+desktop.Executable)
	reader.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	output, err := reader.CombinedOutput()
	if err != nil {
		t.Fatalf("只读核对 Desktop 会话列表: %v %s", err, output)
	}
	var counts struct {
		Expected int `json:"expected"`
		Matched  int `json:"matched"`
	}
	if err := json.Unmarshal(output, &counts); err != nil || counts.Expected == 0 || counts.Matched != counts.Expected {
		t.Fatalf("Desktop 实际会话列表不完整: %s %v", output, err)
	}
	return counts.Matched
}

// TestDesktopWebIdentity 核对保存的网页会话在服务端对应的实际账号
func TestDesktopWebIdentity(t *testing.T) {
	if os.Getenv("CCBAR_LIVE_READ") != "1" {
		t.Skip("本机网页身份检查按需运行")
	}
	store, err := New(filepath.Join(os.Getenv("LOCALAPPDATA"), "CCBar"), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range store.ListClaude() {
		directory := store.desktopSession(profile)
		stateData, err := os.ReadFile(filepath.Join(directory, "Local State"))
		if err != nil {
			t.Fatal(err)
		}
		state, err := providers.DecodeObject(stateData)
		if err != nil {
			t.Fatal(err)
		}
		crypt := state["os_crypt"].(map[string]any)
		wrapped, err := base64.StdEncoding.DecodeString(crypt["encrypted_key"].(string))
		if err != nil {
			t.Fatal(err)
		}
		key, err := secrets.UnprotectBytes(wrapped[5:])
		if err != nil {
			t.Fatal(err)
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			t.Fatal(err)
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			t.Fatal(err)
		}
		database, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(directory, "Network", "Cookies"))+"?mode=ro")
		if err != nil {
			t.Fatal(err)
		}
		var version int
		if err := database.QueryRow(`SELECT value FROM meta WHERE key='version'`).Scan(&version); err != nil {
			t.Fatal(err)
		}
		rows, err := database.Query(`SELECT name, value, encrypted_value FROM cookies WHERE host_key IN ('.claude.ai', 'claude.ai') AND (name LIKE 'sessionKey%' OR name = 'lastActiveOrg')`)
		if err != nil {
			t.Fatal(err)
		}
		request, _ := http.NewRequest(http.MethodGet, "https://claude.ai/api/bootstrap", nil)
		request.Header.Set("Accept", "application/json")
		request.Header.Set("User-Agent", "Mozilla/5.0 Chrome/138.0.0.0 Safari/537.36")
		for rows.Next() {
			var name, value string
			var encrypted []byte
			if err := rows.Scan(&name, &value, &encrypted); err != nil {
				t.Fatal(err)
			}
			if len(encrypted) > 31 {
				plain, err := gcm.Open(nil, encrypted[3:15], encrypted[15:], nil)
				if err != nil {
					t.Fatal(err)
				}
				if version >= 24 {
					plain = plain[32:]
				}
				value = string(plain)
			}
			request.AddCookie(&http.Cookie{Name: name, Value: value})
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		database.Close()
		response, err := providers.HTTPClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			t.Fatalf("%s 网页身份查询返回 HTTP %d", profile.Email, response.StatusCode)
		}
		var bootstrap struct {
			Account struct {
				UUID  string `json:"uuid"`
				Email string `json:"email_address"`
			} `json:"account"`
		}
		err = json.NewDecoder(response.Body).Decode(&bootstrap)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("网页账号=%s 已保存账号=%s 实际身份一致=%t", bootstrap.Account.Email, profile.Email, bootstrap.Account.UUID == profile.AccountUUID)
		if bootstrap.Account.UUID != profile.AccountUUID {
			t.Error("网页会话与 Code 账号不一致")
		}
	}
}
