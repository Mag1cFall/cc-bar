package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Mag1cFall/cc-bar/internal/model"
)

const (
	defaultAntigravityVersion = "2.34.0"
	antigravityClientMetadata = `{"ideType":"ANTIGRAVITY","platform":"WINDOWS","pluginType":"GEMINI"}`
)

var (
	localDaemonClient     = &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}}
	fetchLocalAntigravity = fetchAntigravityDaemon
)

type gravityWindows struct {
	five, weekly, geminiFive, geminiWeek, claudeFive, claudeWeek *model.QuotaWindow
	argonFive, argonWeek                                         *model.QuotaWindow
	grouped                                                      bool
	plan                                                         string
}

func (target *gravityWindows) hasAny() bool {
	return target.five != nil || target.weekly != nil ||
		target.geminiFive != nil || target.geminiWeek != nil ||
		target.claudeFive != nil || target.claudeWeek != nil ||
		target.argonFive != nil || target.argonWeek != nil
}

func fraction(root object) *float64 {
	if value := firstNumber(num(root, "remainingFraction"), num(root, "remaining_fraction")); value != nil {
		return value
	}
	if nested := obj(root["remaining"]); nested != nil {
		if value := firstNumber(num(nested, "remainingFraction"), num(nested, "remaining_fraction")); value != nil {
			return value
		}
	}
	if value := num(root, "remaining"); value != nil {
		if *value > 1 {
			*value /= 100
		}
		return value
	}
	if value := num(root, "remainingPercent"); value != nil {
		*value /= 100
		return value
	}
	if value := num(root, "usedPercent"); value != nil {
		*value = 1 - *value/100
		return value
	}
	return nil
}

func gravityWindow(row object, weekly bool) *model.QuotaWindow {
	seconds := 18000
	if weekly {
		seconds = 604800
	}
	reset := gravityReset(row)
	value := fraction(row)
	if value == nil {
		if reset == nil {
			return nil
		}
		return &model.QuotaWindow{UsedPercent: 100, ResetsAt: reset, WindowSeconds: integer(seconds)}
	}
	return &model.QuotaWindow{UsedPercent: math.Max(0, math.Min(100, 100-*value*100)), ResetsAt: reset, WindowSeconds: integer(seconds)}
}

func gravityReset(row object) *time.Time {
	value := date(row["resetTime"])
	if value == nil {
		value = date(row["reset_time"])
	}
	return value
}

func adopt(target **model.QuotaWindow, window *model.QuotaWindow) {
	if window != nil && (*target == nil || window.UsedPercent > (*target).UsedPercent || window.UsedPercent == (*target).UsedPercent && (*target).ResetsAt == nil && window.ResetsAt != nil) {
		*target = window
	}
}

func (target *gravityWindows) merge(more *gravityWindows) {
	if more == nil {
		return
	}
	if target.five == nil {
		target.five = more.five
	}
	if target.weekly == nil {
		target.weekly = more.weekly
	}
	if target.geminiFive == nil {
		target.geminiFive = more.geminiFive
	}
	if target.geminiWeek == nil {
		target.geminiWeek = more.geminiWeek
	}
	if target.claudeFive == nil {
		target.claudeFive = more.claudeFive
	}
	if target.claudeWeek == nil {
		target.claudeWeek = more.claudeWeek
	}
	if target.argonFive == nil {
		target.argonFive = more.argonFive
	}
	if target.argonWeek == nil {
		target.argonWeek = more.argonWeek
	}
	target.grouped = target.grouped || more.grouped
	if target.plan == "" {
		target.plan = more.plan
	}
}

// parseGravity 汇总不同版本响应中的模型额度窗口
func parseGravity(root object) *gravityWindows {
	result := &gravityWindows{plan: str(obj(root["paidTier"]), "name", "id")}
	if result.plan == "" {
		result.plan = str(obj(root["currentTier"]), "id", "name")
	}
	if result.plan == "" {
		result.plan = str(root, "currentTier")
	}
	if result.plan == "" {
		for _, row := range rows(root["allowedTiers"]) {
			if result.plan = str(obj(row), "id"); result.plan != "" {
				break
			}
		}
	}
	if result.plan == "" {
		result.plan = str(obj(root["tier"]), "id")
	}
	groupRows := rows(root["groups"])
	if groupRows == nil {
		if wrapped := obj(root["response"]); wrapped != nil {
			groupRows = rows(wrapped["groups"])
		}
	}
	for _, value := range groupRows {
		group := obj(value)
		groupName := strings.ToLower(str(group, "displayName"))
		buckets := rows(group["buckets"])
		weeklyOut := false
		for _, item := range buckets {
			bucket := obj(item)
			if disabled := boolean(bucket, "disabled"); disabled != nil && *disabled {
				continue
			}
			id, tag, name := strings.ToLower(str(bucket, "bucketId")), strings.ToLower(str(bucket, "window")), strings.ToLower(str(bucket, "displayName"))
			if !(strings.Contains(id, "week") || strings.Contains(tag, "week") || strings.Contains(name, "week")) {
				continue
			}
			remaining := fraction(bucket)
			if remaining != nil && *remaining <= 0 || remaining == nil && gravityReset(bucket) != nil {
				weeklyOut = true
				break
			}
		}
		for _, item := range buckets {
			bucket := obj(item)
			if disabled := boolean(bucket, "disabled"); disabled != nil && *disabled {
				continue
			}
			id, tag, name := strings.ToLower(str(bucket, "bucketId")), strings.ToLower(str(bucket, "window")), strings.ToLower(str(bucket, "displayName"))
			weekly := strings.Contains(id, "week") || strings.Contains(tag, "week") || strings.Contains(name, "week")
			if weeklyOut && !weekly {
				continue
			}
			window := gravityWindow(bucket, weekly)
			if window == nil {
				continue
			}
			argon := strings.Contains(id, "argon") || strings.Contains(groupName, "argon")
			gemini := !argon && (strings.HasPrefix(id, "gemini") || !strings.HasPrefix(id, "3p") && !strings.HasPrefix(id, "third") && !strings.HasPrefix(id, "claude") && !strings.HasPrefix(id, "gpt") && strings.Contains(groupName, "gemini"))
			switch {
			case argon:
				if weekly {
					adopt(&result.argonWeek, window)
				} else {
					adopt(&result.argonFive, window)
				}
			case gemini:
				if weekly {
					adopt(&result.geminiWeek, window)
				} else {
					adopt(&result.geminiFive, window)
				}
			default:
				if weekly {
					adopt(&result.claudeWeek, window)
				} else {
					adopt(&result.claudeFive, window)
				}
			}
		}
	}
	result.grouped = result.claudeFive != nil || result.claudeWeek != nil || result.geminiFive != nil || result.geminiWeek != nil || result.argonFive != nil || result.argonWeek != nil
	parseDict := func(dict object) *gravityWindows {
		more := &gravityWindows{}
		for key, value := range dict {
			row := obj(value)
			lower := strings.ToLower(key)
			weekly := strings.Contains(lower, "week")
			window := gravityWindow(row, weekly)
			if strings.Contains(lower, "argon") {
				if weekly {
					adopt(&more.argonWeek, window)
				} else {
					adopt(&more.argonFive, window)
				}
			} else if strings.Contains(lower, "gemini") {
				if weekly {
					adopt(&more.geminiWeek, window)
				} else {
					adopt(&more.geminiFive, window)
				}
			} else if weekly {
				adopt(&more.weekly, window)
			} else {
				adopt(&more.five, window)
			}
		}
		return more
	}
	result.merge(parseDict(obj(root["quota"])))
	if buckets := rows(root["buckets"]); buckets != nil {
		more := &gravityWindows{}
		for _, value := range buckets {
			row := obj(value)
			id := strings.ToLower(str(row, "modelId", "model"))
			bucketID := strings.ToLower(str(row, "bucketId"))
			weekly := strings.Contains(bucketID, "week")
			argon := strings.Contains(id, "argon") || strings.Contains(bucketID, "argon")
			gemini := !argon && (strings.Contains(id, "gemini") || strings.Contains(id, "tab_") || strings.Contains(id, "chat_"))
			third := strings.Contains(id, "claude") || strings.Contains(id, "gpt") || strings.Contains(id, "opus") || strings.Contains(id, "sonnet")
			if !argon && !gemini && !third && !weekly {
				continue
			}
			remaining := fraction(row)
			reset := gravityReset(row)
			if reset == nil && (remaining == nil || *remaining >= 1) {
				continue
			}
			window := gravityWindow(row, weekly)
			switch {
			case argon:
				if weekly {
					adopt(&more.argonWeek, window)
				} else {
					adopt(&more.argonFive, window)
				}
			case gemini:
				if weekly {
					adopt(&more.geminiWeek, window)
				} else {
					adopt(&more.geminiFive, window)
				}
			case weekly:
				adopt(&more.weekly, window)
			default:
				adopt(&more.five, window)
			}
		}
		if more.five != nil {
			result.five = more.five
		}
		if more.weekly != nil {
			result.weekly = more.weekly
		}
		if more.geminiFive != nil {
			result.geminiFive = more.geminiFive
		}
		if more.geminiWeek != nil {
			result.geminiWeek = more.geminiWeek
		}
		if more.argonFive != nil {
			result.argonFive = more.argonFive
		}
		if more.argonWeek != nil {
			result.argonWeek = more.argonWeek
		}
	}
	parseModels := func(models []any) *gravityWindows {
		more := &gravityWindows{}
		for _, value := range models {
			row := obj(value)
			id := strings.ToLower(str(row, "modelId", "name"))
			window := gravityWindow(row, strings.Contains(id, "week"))
			if window == nil {
				continue
			}
			if strings.Contains(id, "gemini") {
				if more.geminiFive == nil {
					more.geminiFive = window
				} else if more.geminiWeek == nil {
					more.geminiWeek = window
				}
			} else if more.five == nil {
				more.five = window
			} else if more.weekly == nil {
				more.weekly = window
			}
		}
		return more
	}
	if rows(root["buckets"]) == nil {
		result.merge(parseModels(rows(root["models"])))
	}
	if models := obj(root["models"]); models != nil {
		more := &gravityWindows{}
		now := time.Now()
		for key, value := range models {
			entry := obj(value)
			if internal := boolean(entry, "isInternal"); internal != nil && *internal {
				continue
			}
			if disabled := boolean(entry, "disabled"); disabled != nil && *disabled {
				continue
			}
			row := entry
			if nested := obj(entry["quotaInfo"]); nested != nil {
				row = nested
			}
			remaining := fraction(row)
			reset := gravityReset(row)
			if reset == nil && (remaining == nil || *remaining >= 1) {
				continue
			}
			id := strings.ToLower(key)
			if strings.HasPrefix(id, "tab_") || strings.HasPrefix(id, "tab-") || strings.HasPrefix(id, "chat_") {
				continue
			}
			weekly := strings.Contains(id, "week") || reset != nil && reset.Sub(now) > 24*time.Hour
			window := gravityWindow(row, weekly)
			switch {
			case strings.Contains(id, "argon"):
				if weekly {
					adopt(&more.argonWeek, window)
				} else {
					adopt(&more.argonFive, window)
				}
			case strings.Contains(id, "gemini"):
				if weekly {
					adopt(&more.geminiWeek, window)
				} else {
					adopt(&more.geminiFive, window)
				}
			case strings.Contains(id, "claude") || strings.Contains(id, "gpt") || strings.Contains(id, "opus") || strings.Contains(id, "sonnet") || strings.Contains(id, "oss"):
				if weekly {
					adopt(&more.weekly, window)
				} else {
					adopt(&more.five, window)
				}
			}
		}
		if more.five == nil && more.geminiFive != nil {
			more.five = more.geminiFive
			more.geminiFive = nil
		}
		if more.weekly == nil && more.geminiWeek != nil {
			more.weekly = more.geminiWeek
			more.geminiWeek = nil
		}
		result.merge(more)
	}
	result.merge(parseModels(rows(root["availableModels"])))
	if !result.hasAny() {
		result.merge(parseDict(obj(root["quotaInfo"])))
	}
	return result
}

// gravitySnapshot 将 Gemini、Claude 与独立模型组额度分开呈现
func gravitySnapshot(value *gravityWindows) *model.QuotaSnapshot {
	result := &model.QuotaSnapshot{App: model.Antigravity, PlanType: value.plan, AuxiliaryLimits: []model.QuotaLimit{}, ModelLimits: []model.QuotaLimit{}, FetchedAt: time.Now()}
	appendArgon := func() {
		for _, item := range []*model.QuotaLimit{limit("argon-5h", "Argon 5H", model.FiveHour, value.argonFive), limit("argon-weekly", "Argon WK", model.Weekly, value.argonWeek)} {
			if item != nil {
				result.ModelLimits = append(result.ModelLimits, *item)
			}
		}
	}
	if value.grouped {
		switch {
		case value.geminiFive != nil || value.geminiWeek != nil:
			primary := value.geminiFive
			if primary == nil {
				primary = value.geminiWeek
			}
			tag := kind(primary, model.FiveHour)
			id, name := "gemini-5h", "Gemini 5H"
			if tag == model.Weekly {
				id, name = "gemini-weekly", "Gemini WK"
			}
			result.PrimaryLimit = limit(id, name, tag, primary)
			if value.geminiFive != nil {
				result.SecondaryLimit = limit("gemini-weekly", "Gemini WK", model.Weekly, value.geminiWeek)
			}
			for _, item := range []*model.QuotaLimit{limit("claude-5h", "Claude 5H", model.FiveHour, value.claudeFive), limit("claude-weekly", "Claude WK", model.Weekly, value.claudeWeek)} {
				if item != nil {
					result.AuxiliaryLimits = append(result.AuxiliaryLimits, *item)
				}
			}
			appendArgon()
		case value.claudeFive != nil || value.claudeWeek != nil:
			primary := value.claudeFive
			if primary == nil {
				primary = value.claudeWeek
			}
			tag := kind(primary, model.FiveHour)
			id, name := "claude-5h", "Claude 5H"
			if tag == model.Weekly {
				id, name = "claude-weekly", "Claude WK"
			}
			result.PrimaryLimit = limit(id, name, tag, primary)
			if value.claudeFive != nil {
				result.SecondaryLimit = limit("claude-weekly", "Claude WK", model.Weekly, value.claudeWeek)
			}
			appendArgon()
		case value.argonFive != nil || value.argonWeek != nil:
			primary := value.argonFive
			if primary == nil {
				primary = value.argonWeek
			}
			tag := kind(primary, model.FiveHour)
			id, name := "argon-5h", "Argon 5H"
			if tag == model.Weekly {
				id, name = "argon-weekly", "Argon WK"
			}
			result.PrimaryLimit = limit(id, name, tag, primary)
			if value.argonFive != nil {
				result.SecondaryLimit = limit("argon-weekly", "Argon WK", model.Weekly, value.argonWeek)
			}
		}
	} else {
		primary := value.five
		if primary == nil {
			primary = value.weekly
		}
		result.PrimaryLimit = standard(primary)
		if result.PrimaryLimit != nil && result.PrimaryLimit.Kind == model.UnknownLimit {
			result.PrimaryLimit.Kind = model.FiveHour
		}
		if value.five != nil {
			result.SecondaryLimit = limit("weekly", "", model.Weekly, value.weekly)
		}
		result.GeminiWindow, result.GeminiWeekly = value.geminiFive, value.geminiWeek
		appendArgon()
	}
	return result
}

// ParseAntigravity 解析官方分组、模型和旧额度响应
func ParseAntigravity(root map[string]any) *model.QuotaSnapshot {
	return gravitySnapshot(parseGravity(root))
}

type antigravityDaemon struct {
	version string
	port    int
	csrf    string
	modTime time.Time
}

// antigravityDaemons 按修改时间从新到旧读取全部本机 Antigravity 守护进程信息
func antigravityDaemons(home string) []antigravityDaemon {
	if home == "" {
		return nil
	}
	files, err := filepath.Glob(filepath.Join(home, ".gemini", "antigravity", "daemon", "hub_*.json"))
	if err != nil || len(files) == 0 {
		return nil
	}
	daemons := make([]antigravityDaemon, 0, len(files))
	for _, path := range files {
		info, statErr := os.Stat(path)
		if statErr != nil || info.IsDir() {
			continue
		}
		root, readErr := readObject(path)
		if readErr != nil {
			continue
		}
		daemons = append(daemons, antigravityDaemon{
			version: str(root, "lsVersion"),
			port:    int(val(root, "httpPort")),
			csrf:    str(root, "csrfToken"),
			modTime: info.ModTime(),
		})
	}
	sort.Slice(daemons, func(i, j int) bool { return daemons[i].modTime.After(daemons[j].modTime) })
	return daemons
}

// antigravityUserAgent 生成携带当前官方客户端版本的请求头以避免服务端门控过滤 Gemini 额度组
func antigravityUserAgent(home string) string {
	version := defaultAntigravityVersion
	for _, daemon := range antigravityDaemons(home) {
		if daemon.version == "" {
			continue
		}
		if major, err := strconv.Atoi(strings.SplitN(daemon.version, ".", 2)[0]); err == nil && major >= 2 {
			version = daemon.version
			break
		}
	}
	return fmt.Sprintf("antigravity/hub/%s (aidev_client; os_type=windows; arch=%s)", version, runtime.GOARCH)
}

// fetchAntigravityDaemon 从运行中的本机 Antigravity 2.0 language_server 读取分组额度作为兜底
func fetchAntigravityDaemon(ctx context.Context, home string) (object, error) {
	var lastErr error = os.ErrNotExist
	for _, daemon := range antigravityDaemons(home) {
		if daemon.port <= 0 || daemon.csrf == "" {
			continue
		}
		endpoint := fmt.Sprintf("http://127.0.0.1:%d/exa.language_server_pb.LanguageServerService/RetrieveUserQuotaSummary", daemon.port)
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader([]byte(`{}`)))
		if err != nil {
			lastErr = err
			continue
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Connect-Protocol-Version", "1")
		request.Header.Set("X-Codeium-Csrf-Token", daemon.csrf)
		response, err := localDaemonClient.Do(request)
		if err != nil {
			lastErr = err
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, 4<<20))
		response.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			lastErr = &Error{Kind: "http", StatusCode: response.StatusCode, Detail: http.StatusText(response.StatusCode)}
			continue
		}
		root, decodeErr := DecodeObject(data)
		if decodeErr != nil {
			lastErr = decodeErr
			continue
		}
		return root, nil
	}
	return nil, lastErr
}

func subscriptionEmail(root object) string {
	raw := str(root, "manageSubscriptionUri")
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if email := strings.TrimSpace(parsed.Query().Get("Email")); email != "" {
		return email
	}
	return strings.TrimSpace(parsed.Query().Get("email"))
}

func fetchAntigravity(ctx context.Context, credential *model.Credential) (*model.QuotaSnapshot, error) {
	home := antigravityHomeForSource(credential.Source)
	ua := antigravityUserAgent(home)
	raw, _ := readAntigravityRaw(credential.Source)
	authMethod := str(raw, "auth_method")
	wifProvider := str(raw, "wif_provider")
	hosts := []string{"daily-cloudcode-pa.googleapis.com", "cloudcode-pa.googleapis.com"}
	if wifProvider != "" {
		hosts = []string{"cloudcode-pa.googleapis.com"}
	} else if authMethod == "gcp" {
		hosts = []string{"cloudcode-pa.googleapis.com", "daily-cloudcode-pa.googleapis.com"}
	}
	assistBody := []byte(`{"metadata":{"ideType":"ANTIGRAVITY"}}`)
	retriedRefresh := false
	var lastError error
	for _, host := range hosts {
		post := func(targetHost, method string, body []byte, extra map[string]string) (object, error) {
			headers := map[string]string{"User-Agent": ua}
			for k, v := range extra {
				headers[k] = v
			}
			return requestJSON(ctx, http.MethodPost, "https://"+targetHost+"/v1internal:"+method, credential, body, headers)
		}
		root, err := post(host, "loadCodeAssist", assistBody, nil)
		if err != nil {
			var failure *Error
			if errors.As(err, &failure) && failure.IsAuthFailure() {
				if !retriedRefresh && credential.RefreshToken != "" {
					retriedRefresh = true
					refreshed, refreshErr := forceRefreshAntigravity(ctx, credential)
					if refreshErr == nil && refreshed != nil {
						*credential = *refreshed
						root, err = post(host, "loadCodeAssist", assistBody, nil)
					}
				}
				if err != nil {
					lastError = err
					break
				}
			} else {
				lastError = err
				continue
			}
		}
		result := parseGravity(root)
		project := str(root, "cloudaicompanionProject")
		if credential.Email == "" {
			credential.Email = subscriptionEmail(root)
		}
		projectBody := []byte(`{}`)
		if project != "" {
			if encoded, marshalErr := json.Marshal(object{"project": project}); marshalErr == nil {
				projectBody = encoded
			}
		}
		quotaHost := host
		quotaHeaders := map[string]string{}
		if wifProvider != "" {
			quotaHost = "businessaicode.googleapis.com"
			if project != "" {
				quotaHeaders["X-Goog-User-Project"] = project
			}
		}
		quotaOK := false
		var quotaErr error
		if summary, summaryErr := post(quotaHost, "retrieveUserQuotaSummary", projectBody, quotaHeaders); summaryErr == nil {
			quotaOK = true
			result.merge(parseGravity(summary))
		} else {
			quotaErr = summaryErr
		}
		if !result.grouped {
			modelHeaders := map[string]string{"Client-Metadata": antigravityClientMetadata}
			for k, v := range quotaHeaders {
				modelHeaders[k] = v
			}
			if models, modelsErr := post(quotaHost, "fetchAvailableModels", projectBody, modelHeaders); modelsErr == nil {
				quotaOK = true
				result.merge(parseGravity(models))
			} else {
				quotaErr = modelsErr
			}
			if details, detailsErr := post(quotaHost, "retrieveUserQuota", projectBody, quotaHeaders); detailsErr == nil {
				quotaOK = true
				result.merge(parseGravity(details))
			} else {
				quotaErr = detailsErr
			}
		}
		if !result.hasAny() && !quotaOK {
			lastError = quotaErr
			continue
		}
		if !result.hasAny() {
			if local, localErr := fetchLocalAntigravity(ctx, home); localErr == nil && local != nil {
				result.merge(parseGravity(local))
			}
		}
		if credential.Email == "" {
			if user, userErr := requestJSON(ctx, http.MethodGet, "https://www.googleapis.com/oauth2/v3/userinfo", credential, nil, nil); userErr == nil {
				credential.Email = str(user, "email")
			}
		}
		return gravitySnapshot(result), nil
	}
	if local, localErr := fetchLocalAntigravity(ctx, home); localErr == nil && local != nil {
		if parsed := parseGravity(local); parsed.hasAny() {
			return gravitySnapshot(parsed), nil
		}
	}
	return nil, lastError
}
