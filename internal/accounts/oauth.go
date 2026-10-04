package accounts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Mag1cFall/cc-bar/internal/model"
	"github.com/Mag1cFall/cc-bar/internal/providers"
	"github.com/google/uuid"
)

// loginAttempt 保存单次登录的取消上下文及界面进度
type loginAttempt struct {
	ClaudeLogin
	ctx            context.Context
	cancel         context.CancelFunc
	desktop        *desktopApp
	previous       *ClaudeProfile
	desktopChanged bool
	done           chan struct{}
}

// LoginStatus 返回登录与账号切换的当前进度
func (store *Store) LoginStatus() *ClaudeLogin {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.login == nil {
		return nil
	}
	status := store.login.ClaudeLogin
	return &status
}

func (store *Store) loginStage(attempt *loginAttempt, stage string, failure error) {
	store.mu.Lock()
	if store.login != attempt {
		store.mu.Unlock()
		return
	}
	attempt.Stage = stage
	if slices.Contains([]string{"done", "error", "cancelled"}, stage) {
		attempt.URL = ""
	}
	if failure != nil {
		attempt.Error = failure.Error()
	}
	store.mu.Unlock()
	store.notify()
}

// beginLogin 建立单次账号操作并发布初始进度
func (store *Store) beginLogin(id, mode string, timeout time.Duration) (*loginAttempt, error) {
	store.mu.Lock()
	if store.login != nil && store.login.Stage != "done" && store.login.Stage != "error" && store.login.Stage != "cancelled" {
		store.mu.Unlock()
		return nil, errors.New("登录或切换正在进行")
	}
	if id != "" && store.claudeIndex(id) < 0 {
		store.mu.Unlock()
		return nil, errors.New("账号未找到")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	attempt := &loginAttempt{ClaudeLogin: ClaudeLogin{ID: uuid.NewString(), AccountID: id, Stage: "starting", Mode: mode}, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	store.login = attempt
	store.mu.Unlock()
	store.notify()
	return attempt, nil
}

// restorePreviousDesktop 恢复操作前保存的登录并重新打开原安装
func (store *Store) restorePreviousDesktop(attempt *loginAttempt) error {
	if attempt.desktop == nil {
		return nil
	}
	store.loginStage(attempt, "cancelling", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := attempt.desktop.close(ctx); err != nil {
		return err
	}
	if attempt.desktopChanged {
		saved := ""
		if attempt.previous != nil {
			saved = store.desktopSession(*attempt.previous)
		}
		if err := restoreDesktopSession(attempt.desktop.Directory, saved); err != nil {
			return err
		}
	}
	return attempt.desktop.launch()
}

// finishLogin 在实际保存或恢复完成后发布终态
func (store *Store) finishLogin(attempt *loginAttempt, failure error) {
	if failure != nil {
		restoreErr := store.restorePreviousDesktop(attempt)
		if errors.Is(failure, context.Canceled) && restoreErr == nil {
			store.loginStage(attempt, "cancelled", nil)
		} else {
			store.loginStage(attempt, "error", errors.Join(failure, restoreErr))
		}
	} else {
		store.loginStage(attempt, "done", nil)
		if store.LoginCompleted != nil {
			store.LoginCompleted()
		}
	}
	close(attempt.done)
}

// BeginClaudeLogin 直接开始原生登录并在软件内展示进度
func (store *Store) BeginClaudeLogin(id string, openBrowser func(string) error) (*ClaudeLogin, error) {
	attempt, err := store.beginLogin(id, "", 10*time.Minute)
	if err != nil {
		return nil, err
	}
	go func() {
		defer attempt.cancel()
		desktop, err := store.desktopApp(attempt.ctx)
		if err == nil && desktop != nil {
			attempt.desktop = desktop
			err = store.loginDesktop(attempt)
		} else if err == nil {
			err = store.loginOAuth(attempt, openBrowser)
		}
		store.finishLogin(attempt, err)
	}()
	return store.LoginStatus(), nil
}

// CancelClaudeLogin 取消当前授权并释放回调端口
func (store *Store) CancelClaudeLogin() {
	store.mu.Lock()
	if store.login != nil {
		if !slices.Contains([]string{"done", "error", "cancelled"}, store.login.Stage) {
			store.login.Stage = "cancelling"
		}
		store.login.cancel()
	}
	store.mu.Unlock()
	store.notify()
}

// StopLogin 等待取消后的 Desktop 会话恢复完成
func (store *Store) StopLogin() {
	store.mu.Lock()
	attempt := store.login
	store.mu.Unlock()
	if attempt != nil {
		attempt.cancel()
		<-attempt.done
	}
}

// loginOAuth 使用系统浏览器与本机回调完成官方 PKCE 授权
func (store *Store) loginOAuth(attempt *loginAttempt, openBrowser func(string) error) error {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("准备登录回调: %w", err)
	}
	defer listener.Close()
	verifier := randomOAuthValue()
	state := randomOAuthValue()
	challenge := sha256.Sum256([]byte(verifier))
	redirect := "http://localhost:" + fmt.Sprint(listener.Addr().(*net.TCPAddr).Port) + "/callback"
	authorize, _ := url.Parse("https://claude.com/cai/oauth/authorize")
	authorize.RawQuery = url.Values{
		"code": {"true"}, "response_type": {"code"}, "client_id": {providers.ClaudeOAuthClientID},
		"redirect_uri": {redirect}, "scope": {"user:profile user:inference user:sessions:claude_code"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"}, "state": {state},
	}.Encode()
	code := make(chan string, 1)
	failure := make(chan error, 1)
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: oauthCallback(state, code, failure)}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	store.mu.Lock()
	attempt.Mode, attempt.URL = "oauth", authorize.String()
	store.mu.Unlock()
	store.loginStage(attempt, "browser", nil)
	if err := openBrowser(authorize.String()); err != nil {
		return err
	}
	select {
	case value := <-code:
		store.loginStage(attempt, "exchanging", nil)
		credential, err := providers.ExchangeClaudeCode(attempt.ctx, value, verifier, state, redirect)
		if err != nil {
			return err
		}
		return store.finishClaudeLogin(attempt, credential)
	case err := <-failure:
		return err
	case <-attempt.ctx.Done():
		return attempt.ctx.Err()
	}
}

func randomOAuthValue() string {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(value)
}

// oauthCallback 校验授权状态并自动接收浏览器返回的登录结果
func oauthCallback(state string, code chan<- string, failure chan<- error) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("Content-Type", "text/html; charset=utf-8")
		if request.Method != http.MethodGet || request.URL.Path != "/callback" {
			http.NotFound(response, request)
			return
		}
		if subtle.ConstantTimeCompare([]byte(request.URL.Query().Get("state")), []byte(state)) != 1 {
			http.Error(response, "登录回调校验失败", http.StatusBadRequest)
			return
		}
		if request.URL.Query().Get("error") != "" {
			select {
			case failure <- errors.New("浏览器授权已取消"):
			default:
			}
		} else if value := request.URL.Query().Get("code"); value != "" {
			select {
			case code <- value:
			default:
			}
		} else {
			http.Error(response, "登录回调缺少授权结果", http.StatusBadRequest)
			return
		}
		fmt.Fprint(response, "<!doctype html><html lang=zh-CN><meta charset=utf-8><title>CCBar 登录</title><h1>授权已返回 CCBar</h1><p>请回到 CCBar 查看登录结果，此页面可以关闭</p></html>")
	})
}

// finishClaudeLogin 核对身份后保存账号并自动使用邮箱命名
func (store *Store) finishClaudeLogin(attempt *loginAttempt, credential *model.Credential) error {
	store.loginStage(attempt, "verifying", nil)
	if err := providers.FetchClaudeProfile(attempt.ctx, credential); err != nil {
		return err
	}
	if !hasCompleteCodeLogin(credential) {
		return errors.New("授权尚未包含完整 Code 凭据，请重新授权")
	}
	store.loginStage(attempt, "saving", nil)
	store.mu.Lock()
	index := store.claudeIndex(attempt.AccountID)
	if index < 0 {
		for i, profile := range store.claude {
			if profile.AccountUUID == credential.AccountUUID && (profile.OrganizationUUID == "" || profile.OrganizationUUID == credential.OrganizationUUID) {
				index = i
				break
			}
		}
	}
	profile := ClaudeProfile{ID: strings.ReplaceAll(uuid.NewString(), "-", ""), Name: strings.Split(credential.Email, "@")[0]}
	if index >= 0 {
		profile = store.claude[index]
		if profile.AccountUUID != "" && profile.AccountUUID != credential.AccountUUID {
			store.mu.Unlock()
			return errors.New("重新登录的账号与已保存账号不同，请通过添加账号保存")
		}
	}
	store.mu.Unlock()
	if profile.UsesDefaultConfig {
		profile.UsesDefaultConfig = false
	}
	if profile.Name == "" || profile.Name == "新账号" || strings.HasPrefix(profile.Name, "Claude "+credential.AccountUUID[:min(8, len(credential.AccountUUID))]) {
		profile.Name = strings.Split(credential.Email, "@")[0]
	}
	profile.ConfigDirectory = filepath.Join(store.dataDir, "claude-accounts", profile.ID)
	profile.AccountUUID, profile.OrganizationUUID = credential.AccountUUID, credential.OrganizationUUID
	profile.DesktopLinked = attempt.desktop != nil
	if attempt.desktop != nil {
		if err := attempt.desktop.close(attempt.ctx); err != nil {
			return err
		}
		if desktopIdentity(attempt.desktop.Directory) != credential.AccountUUID {
			return errors.New("Desktop 与 Code 登录的账号不同，请在 Code 授权中选择同一账号")
		}
		if !desktopWebSession(attempt.desktop.Directory) {
			return errors.New("Desktop 网页登录尚未保存，请等待登录完成后保存当前登录")
		}
		if err := saveDesktopSession(attempt.desktop.Directory, store.desktopSession(profile)); err != nil {
			return err
		}
		profile.DesktopSaved = true
		profile.DesktopDirectory = attempt.desktop.Directory
		if err := attempt.ctx.Err(); err != nil {
			return err
		}
		if err := syncDesktopCodeHistory(attempt.desktop.Directory, profile, store.ListClaude()); err != nil {
			return err
		}
		if err := attempt.desktop.launch(); err != nil {
			return err
		}
	}
	if err := prepareHistory(profile.ConfigDirectory, filepath.Join(store.home, ".claude")); err != nil {
		return err
	}
	if err := store.writeDesktopClaude(profile, credential); err != nil {
		return err
	}
	profile.Email, profile.AccountUUID, profile.OrganizationUUID = credential.Email, credential.AccountUUID, credential.OrganizationUUID
	profile.Plan, profile.NeedsLogin, profile.Error = credential.SubscriptionType, false, ""
	if err := attempt.ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if index < 0 {
		store.claude = append(store.claude, profile)
	} else {
		store.claude[index] = profile
	}
	attempt.AccountID = profile.ID
	if err := store.prunePartialClaude(); err != nil {
		return err
	}
	if err := store.saveClaudeLocked(); err != nil {
		return err
	}
	return store.setClaudeDirectory(profile.ConfigDirectory)
}

func (store *Store) desktopSession(profile ClaudeProfile) string {
	return filepath.Join(store.dataDir, "claude-desktop", profile.ID)
}

// loginDesktop 使用 Desktop 的原生登录同时建立网页与 Code 会话
func (store *Store) loginDesktop(attempt *loginAttempt) error {
	desktop := attempt.desktop
	if err := os.MkdirAll(desktop.Directory, 0700); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(desktop.Directory, "config.json")); os.IsNotExist(err) {
		if err := providers.WriteJSON(filepath.Join(desktop.Directory, "config.json"), map[string]any{}); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	store.mu.Lock()
	attempt.Mode = "desktop"
	store.mu.Unlock()
	store.loginStage(attempt, "closing", nil)
	if err := desktop.close(attempt.ctx); err != nil {
		return err
	}
	previous, err := store.checkpointDesktop(desktop)
	if err != nil {
		return err
	}
	attempt.previous = previous
	if err := attempt.ctx.Err(); err != nil {
		return err
	}
	attempt.desktopChanged = true
	if err := restoreDesktopSession(desktop.Directory, ""); err != nil {
		return err
	}
	if err := desktop.launch(); err != nil {
		return err
	}
	store.loginStage(attempt, "browser", nil)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	openedCode := false
	var webLoggedInAt time.Time
	for {
		if desktopIdentity(desktop.Directory) != "" && !openedCode {
			webLoggedInAt = time.Now()
			store.loginStage(attempt, "linking", nil)
			command := execDesktopCode(desktop.Executable)
			if err := command.Start(); err != nil {
				return err
			}
			go command.Wait()
			openedCode = true
		}
		credential, _ := providers.ReadClaudeDesktopDirectory(desktop.Directory, nil)
		if openedCode && credential != nil && credential.Source == "Claude Desktop Code" && hasCompleteCodeLogin(credential) && credential.AccountUUID == desktopIdentity(desktop.Directory) {
			store.loginStage(attempt, "persisting", nil)
			// Chromium 在 30 秒周期内持久化 Cookie
			flush := time.NewTimer(time.Until(webLoggedInAt.Add(32 * time.Second)))
			select {
			case <-attempt.ctx.Done():
				flush.Stop()
				return attempt.ctx.Err()
			case <-flush.C:
			}
			return store.finishClaudeLogin(attempt, credential)
		}
		select {
		case <-attempt.ctx.Done():
			return attempt.ctx.Err()
		case <-ticker.C:
		}
	}
}
