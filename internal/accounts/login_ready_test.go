package accounts

import (
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mag1cFall/cc-bar/internal/model"
	"github.com/Mag1cFall/cc-bar/internal/providers"
	"github.com/Mag1cFall/cc-bar/internal/secrets"
	"github.com/google/uuid"
	"golang.org/x/sys/windows"
)

// TestDesktopLoginReady 验证完整同账号 Code 凭据和当前身份
func TestDesktopLoginReady(t *testing.T) {
	directory := t.TempDir()
	if err := providers.WriteJSON(filepath.Join(directory, "config.json"), map[string]any{"lastKnownAccountUuid": "ready-account"}); err != nil {
		t.Fatal(err)
	}
	credential := &model.Credential{
		Source: "Claude Desktop Code", AccountUUID: "ready-account", AccessToken: "access", RefreshToken: "refresh",
		Scopes: []string{"user:profile", "user:inference"},
	}
	if !desktopLoginReady(directory, credential) {
		t.Fatal("已就绪的完整登录未立即进入保存")
	}
	for _, change := range []func(*model.Credential){
		func(value *model.Credential) { value.AccountUUID = "another-account" },
		func(value *model.Credential) { value.RefreshToken = "" },
		func(value *model.Credential) { value.Source = "Claude Desktop" },
	} {
		incomplete := *credential
		change(&incomplete)
		if desktopLoginReady(directory, &incomplete) {
			t.Fatal("不同账号或不完整的 Code 登录被接受")
		}
	}
}

// TestDesktopCookiePersistence 验证 Cookie 锁定时按真实刷盘与日志状态推进
func TestDesktopCookiePersistence(t *testing.T) {
	directory := t.TempDir()
	seedLoginCookies(t, directory)
	var baseline desktopCookieState
	if !baseline.persisted(directory) {
		t.Fatal("可读且完整的 Cookie 会话仍被等待")
	}
	path := filepath.Join(directory, "Network", "Cookies")
	widePath, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(widePath, windows.GENERIC_READ|windows.FILE_WRITE_ATTRIBUTES, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	if desktopWebSession(directory) {
		t.Fatal("测试 Cookie 文件未被独占锁定")
	}
	if baseline.persisted(directory) || baseline.persisted(directory) {
		t.Fatal("尚未建立写入变化的 Cookie 库被当作已刷盘")
	}
	written := windows.NsecToFiletime(baseline.cookiesWrittenAt.Add(time.Second).UnixNano())
	if err := windows.SetFileTime(handle, nil, nil, &written); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(directory, "Network", "Cookies-journal")
	if err := providers.WriteBytes(journal, []byte("pending transaction")); err != nil {
		t.Fatal(err)
	}
	if baseline.persisted(directory) {
		t.Fatal("未完成的回滚日志被当作已刷盘")
	}
	if err := os.Truncate(journal, 0); err != nil {
		t.Fatal(err)
	}
	if !baseline.persisted(directory) {
		t.Fatal("锁定的 Cookie 库完成写入后仍持续等待 SQL")
	}
	baseline, _ = readDesktopCookieState(directory)
	wal := filepath.Join(directory, "Network", "Cookies-wal")
	if err := providers.WriteBytes(wal, make([]byte, 64)); err != nil {
		t.Fatal(err)
	}
	if !baseline.persisted(directory) {
		t.Fatal("WAL 持久化写入未推进保存")
	}
}

// TestDesktopCheckpointReuse 验证再次保存已知账号直接复用资料且保留完整会话
func TestDesktopCheckpointReuse(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "desktop")
	profile := ClaudeProfile{ID: uuid.NewString(), AccountUUID: "cached-account", Name: "cached", Email: "cached@example.com", Plan: "claude_pro"}
	store := &Store{dataDir: filepath.Join(root, "data"), home: filepath.Join(root, "home"), claude: []ClaudeProfile{profile}}
	key := make([]byte, 32)
	protected, err := secrets.ProtectBytes(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := providers.WriteJSON(filepath.Join(directory, "Local State"), map[string]any{"os_crypt": map[string]any{"encrypted_key": base64.StdEncoding.EncodeToString(append([]byte("DPAPI"), protected...))}}); err != nil {
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
	cache, err := json.Marshal(map[string]any{"acct:cached-account|" + providers.ClaudeOAuthClientID + ":cached-org:user:profile user:inference": map[string]any{"token": "cached-access", "refreshToken": "cached-refresh", "expiresAt": time.Now().Add(time.Hour).UnixMilli()}})
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, gcm.NonceSize())
	encoded := base64.StdEncoding.EncodeToString(append(append([]byte("v10"), nonce...), gcm.Seal(nil, nonce, cache, nil)...))
	if err := providers.WriteJSON(filepath.Join(directory, "config.json"), map[string]any{"lastKnownAccountUuid": profile.AccountUUID, "oauth:tokenCacheV2": encoded}); err != nil {
		t.Fatal(err)
	}
	seedLoginCookies(t, directory)
	originalClient := providers.HTTPClient
	t.Cleanup(func() { providers.HTTPClient = originalClient })
	providers.HTTPClient = &http.Client{Transport: oauthTransport(func(*http.Request) (*http.Response, error) {
		t.Error("再次保存已知账号仍发起资料查询")
		return nil, errors.New("unexpected profile request")
	})}
	started := time.Now()
	saved, err := store.checkpointDesktop(&desktopApp{Directory: directory})
	if err != nil || saved == nil {
		t.Fatalf("复用资料保存失败: %v", err)
	}
	if saved.ID != profile.ID || saved.Email != profile.Email || saved.Plan != profile.Plan || saved.NeedsLogin || !store.HasDesktopSession(*saved) {
		t.Fatal("复用账号资料或完整会话状态有误")
	}
	t.Logf("已知账号完整保存耗时=%s", time.Since(started))
}

// seedLoginCookies 建立测试账号的原生 Cookie 数据库
func seedLoginCookies(t *testing.T, directory string) {
	t.Helper()
	path := filepath.Join(directory, "Network", "Cookies")
	if err := providers.WriteBytes(path, nil); err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := database.Exec(`CREATE TABLE cookies (name TEXT, host_key TEXT, encrypted_value BLOB, value TEXT); INSERT INTO cookies VALUES ('sessionKey', '.claude.ai', X'01', '')`)
	if err := errors.Join(writeErr, database.Close()); err != nil {
		t.Fatal(err)
	}
}
