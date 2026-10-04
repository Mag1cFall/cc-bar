package providers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Mag1cFall/cc-bar/internal/model"
)

// Error 保存可识别的额度错误而不泄露服务凭据
type Error struct {
	Kind       string
	StatusCode int
	RetryAfter time.Duration
	Detail     string
}

func (failure *Error) Error() string {
	if failure.StatusCode != 0 {
		return fmt.Sprintf("HTTP %d: %s", failure.StatusCode, failure.Detail)
	}
	return failure.Kind + ": " + failure.Detail
}

// IsAuthFailure 区分认证失效与网络或权限错误
func (failure *Error) IsAuthFailure() bool {
	return failure.Kind == "authentication" || failure.StatusCode == 401 && failure.Kind != "proxy"
}

// HTTPClient 复用连接并保留系统代理
var HTTPClient = &http.Client{Timeout: 30 * time.Second}

// requestJSON 统一处理请求、限流和受代理影响的响应
func requestJSON(ctx context.Context, method, url string, credential *model.Credential, body []byte, headers map[string]string) (object, error) {
	request, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	if len(body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	if credential != nil && credential.AccessToken != "" {
		request.Header.Set("Authorization", "Bearer "+credential.AccessToken)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := HTTPClient.Do(request)
	if err != nil {
		return nil, &Error{Kind: "network", Detail: err.Error()}
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		return nil, &Error{Kind: "transport", Detail: err.Error()}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail := http.StatusText(response.StatusCode)
		kind := "http"
		if strings.HasPrefix(strings.TrimSpace(string(data)), "<") {
			detail = "network proxy returned HTML"
			kind = "proxy"
		} else if root, err := DecodeObject(data); err == nil && str(obj(root["error"]), "type") == "authentication_error" {
			kind = "authentication"
		}
		failure := &Error{Kind: kind, StatusCode: response.StatusCode, Detail: detail}
		if parsed := num(object{"retry": response.Header.Get("Retry-After")}, "retry"); parsed != nil {
			failure.RetryAfter = time.Duration(*parsed * float64(time.Second))
		} else if date, err := http.ParseTime(response.Header.Get("Retry-After")); err == nil {
			failure.RetryAfter = time.Until(date)
		}
		return nil, failure
	}
	result, err := DecodeObject(data)
	if err != nil || result == nil {
		return nil, &Error{Kind: "decode", Detail: "response is not a JSON object"}
	}
	return result, nil
}

// Fetch 请求所选服务的全部额度
func Fetch(ctx context.Context, app model.QuotaApp, credential *model.Credential) (*model.QuotaSnapshot, error) {
	if credential == nil {
		return nil, &Error{Kind: "missingToken", Detail: "not signed in"}
	}
	if app == model.Codex || app == model.Antigravity {
		fresh, err := EnsureFresh(ctx, app, credential)
		if err != nil {
			return nil, err
		}
		*credential = *fresh
	}
	if credential.AccessToken == "" {
		return nil, &Error{Kind: "missingToken", Detail: "not signed in"}
	}
	var snapshot *model.QuotaSnapshot
	var err error
	switch app {
	case model.Codex:
		snapshot, _, err = FetchCodexIdentity(ctx, credential)
	case model.Claude:
		var root object
		root, err = requestJSON(ctx, http.MethodGet, "https://api.anthropic.com/api/oauth/usage", credential, nil, map[string]string{"anthropic-beta": "oauth-2025-04-20", "User-Agent": "claude-code/2.1.0"})
		if err == nil {
			snapshot = ParseClaude(root)
			snapshot.PlanType = credential.SubscriptionType
		}
	case model.Cursor:
		var root object
		root, err = requestJSON(ctx, http.MethodGet, "https://cursor.com/api/usage-summary", nil, nil, map[string]string{"Cookie": "WorkosCursorSessionToken=" + credential.AccountID + "%3A%3A" + credential.AccessToken})
		if err == nil {
			snapshot = ParseCursor(root)
		}
	case model.Antigravity:
		snapshot, err = fetchAntigravity(ctx, credential)
	case model.CommandCode:
		snapshot, err = fetchCommandCode(ctx, credential)
	default:
		err = errors.New("unknown provider")
	}
	if snapshot != nil {
		snapshot.FetchedAt = time.Now()
		if snapshot.AuxiliaryLimits == nil {
			snapshot.AuxiliaryLimits = []model.QuotaLimit{}
		}
		if snapshot.ModelLimits == nil {
			snapshot.ModelLimits = []model.QuotaLimit{}
		}
	}
	return snapshot, err
}

// codexHeaders 显式绑定 ChatGPT 工作区身份
func codexHeaders(credential *model.Credential) map[string]string {
	headers := map[string]string{"User-Agent": "codex-cli"}
	if credential.AccountID != "" {
		headers["ChatGPT-Account-Id"] = credential.AccountID
	}
	return headers
}

// FetchCodexIdentity 返回额度接口确认的账号身份
func FetchCodexIdentity(ctx context.Context, credential *model.Credential) (*model.QuotaSnapshot, *model.Credential, error) {
	root, err := requestJSON(ctx, http.MethodGet, "https://chatgpt.com/backend-api/wham/usage", credential, nil, codexHeaders(credential))
	if err != nil {
		return nil, nil, err
	}
	identity := *credential
	if value := str(root, "account_id"); value != "" {
		identity.AccountID = value
	}
	if value := str(root, "user_id"); value != "" {
		identity.UserID = value
	}
	if value := str(root, "email"); value != "" {
		identity.Email = value
	}
	snapshot := ParseCodex(root)
	if snapshot.PlanType == "" {
		snapshot.PlanType = credential.SubscriptionType
	}
	identity.SubscriptionType = snapshot.PlanType
	return snapshot, &identity, nil
}

// Statuses 并行读取官方状态并报告失败服务
func Statuses(ctx context.Context) (map[string]string, error) {
	result := map[string]string{}
	var lock sync.Mutex
	var failures []error
	var wait sync.WaitGroup
	for name, domain := range map[string]string{"Codex": "status.openai.com", "Claude Code": "status.claude.com", "Cursor": "status.cursor.com"} {
		wait.Add(1)
		go func(name, domain string) {
			defer wait.Done()
			root, err := requestJSON(ctx, http.MethodGet, "https://"+domain+"/api/v2/status.json", nil, nil, nil)
			lock.Lock()
			defer lock.Unlock()
			if err != nil {
				result[name] = "unknown"
				failures = append(failures, fmt.Errorf("%s: %w", name, err))
				return
			}
			result[name] = str(obj(root["status"]), "indicator")
		}(name, domain)
	}
	wait.Wait()
	return result, errors.Join(failures...)
}
