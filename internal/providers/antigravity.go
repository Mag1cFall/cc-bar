package providers

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/Mag1cFall/cc-bar/internal/model"
)

type gravityWindows struct {
	five, weekly, geminiFive, geminiWeek, claudeFive, claudeWeek *model.QuotaWindow
	grouped                                                      bool
	plan                                                         string
}

func fraction(root object) *float64 {
	if value := firstNumber(num(root, "remainingFraction"), num(root, "remaining_fraction")); value != nil {
		return value
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
	value := fraction(row)
	if value == nil {
		return nil
	}
	seconds := 18000
	if weekly {
		seconds = 604800
	}
	return &model.QuotaWindow{UsedPercent: math.Max(0, math.Min(100, 100-*value*100)), ResetsAt: gravityReset(row), WindowSeconds: integer(seconds)}
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
	target.grouped = target.grouped || more.grouped
	if target.plan == "" {
		target.plan = more.plan
	}
}

// parseGravity 汇总不同版本响应中的模型额度窗口
func parseGravity(root object) *gravityWindows {
	result := &gravityWindows{plan: str(obj(root["paidTier"]), "name", "id")}
	if result.plan == "" {
		result.plan = str(obj(root["currentTier"]), "name", "id")
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
	for _, value := range rows(root["groups"]) {
		group := obj(value)
		groupName := strings.ToLower(str(group, "displayName"))
		for _, value := range rows(group["buckets"]) {
			bucket := obj(value)
			if disabled := boolean(bucket, "disabled"); disabled != nil && *disabled {
				continue
			}
			id, tag, name := strings.ToLower(str(bucket, "bucketId")), strings.ToLower(str(bucket, "window")), strings.ToLower(str(bucket, "displayName"))
			gemini := strings.HasPrefix(id, "gemini") || !strings.HasPrefix(id, "3p") && !strings.HasPrefix(id, "third") && !strings.HasPrefix(id, "claude") && !strings.HasPrefix(id, "gpt") && strings.Contains(groupName, "gemini")
			weekly := strings.Contains(id, "week") || strings.Contains(tag, "week") || strings.Contains(name, "week")
			window := gravityWindow(bucket, weekly)
			if gemini {
				if weekly {
					adopt(&result.geminiWeek, window)
				} else {
					adopt(&result.geminiFive, window)
				}
			} else {
				if weekly {
					adopt(&result.claudeWeek, window)
				} else {
					adopt(&result.claudeFive, window)
				}
			}
		}
	}
	result.grouped = result.claudeFive != nil || result.claudeWeek != nil || result.geminiFive != nil || result.geminiWeek != nil
	parseDict := func(dict object) *gravityWindows {
		more := &gravityWindows{}
		for key, value := range dict {
			row := obj(value)
			lower := strings.ToLower(key)
			weekly := strings.Contains(lower, "week")
			window := gravityWindow(row, weekly)
			if strings.Contains(lower, "gemini") {
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
			gemini := strings.Contains(id, "gemini") || strings.Contains(id, "tab_") || strings.Contains(id, "chat_")
			third := strings.Contains(id, "claude") || strings.Contains(id, "gpt") || strings.Contains(id, "opus") || strings.Contains(id, "sonnet")
			if !gemini && !third && !weekly {
				continue
			}
			remaining := fraction(row)
			if remaining == nil || *remaining >= 1 && gravityReset(row) == nil {
				continue
			}
			window := gravityWindow(row, weekly)
			if gemini {
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
		for key, value := range models {
			row := obj(value)
			if nested := obj(row["quotaInfo"]); nested != nil {
				row = nested
			}
			remaining := fraction(row)
			reset := gravityReset(row)
			if remaining == nil || reset == nil || reset.Sub(time.Now()) >= 2*time.Hour {
				continue
			}
			window := gravityWindow(row, false)
			window.WindowSeconds = nil
			id := strings.ToLower(key)
			if strings.Contains(id, "gemini") || strings.Contains(id, "tab_") || strings.Contains(id, "chat_") {
				adopt(&more.geminiFive, window)
			} else {
				more.five = window
			}
		}
		if more.geminiFive != nil {
			more.five = more.geminiFive
		}
		result.merge(more)
	}
	result.merge(parseModels(rows(root["availableModels"])))
	if result.five == nil && result.weekly == nil && result.geminiFive == nil && result.geminiWeek == nil && result.claudeFive == nil && result.claudeWeek == nil {
		result.merge(parseDict(obj(root["quotaInfo"])))
	}
	return result
}

// gravitySnapshot 将 Gemini 与 Claude 的额度分开呈现
func gravitySnapshot(value *gravityWindows) *model.QuotaSnapshot {
	result := &model.QuotaSnapshot{App: model.Antigravity, PlanType: value.plan, AuxiliaryLimits: []model.QuotaLimit{}, ModelLimits: []model.QuotaLimit{}, FetchedAt: time.Now()}
	if value.grouped {
		primary := value.geminiFive
		if primary == nil {
			primary = value.geminiWeek
		}
		if primary != nil {
			tag := kind(primary, model.FiveHour)
			id, name := "gemini-5h", "Gemini 5H"
			if tag == model.Weekly {
				id, name = "gemini-weekly", "Gemini WK"
			}
			result.PrimaryLimit = limit(id, name, tag, primary)
			if value.geminiFive != nil {
				result.SecondaryLimit = limit("gemini-weekly", "Gemini WK", model.Weekly, value.geminiWeek)
			}
		} else {
			primary = value.five
			if primary == nil {
				primary = value.weekly
			}
			result.PrimaryLimit = standard(primary)
			if value.five != nil {
				result.SecondaryLimit = limit("weekly", "", model.Weekly, value.weekly)
			}
		}
		for _, item := range []*model.QuotaLimit{limit("claude-5h", "Claude 5H", model.FiveHour, value.claudeFive), limit("claude-weekly", "Claude WK", model.Weekly, value.claudeWeek)} {
			if item != nil {
				result.AuxiliaryLimits = append(result.AuxiliaryLimits, *item)
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
	}
	return result
}

// ParseAntigravity 解析官方分组、模型和旧额度响应
func ParseAntigravity(root map[string]any) *model.QuotaSnapshot {
	return gravitySnapshot(parseGravity(root))
}

func fetchAntigravity(ctx context.Context, credential *model.Credential) (*model.QuotaSnapshot, error) {
	var lastError error
	for _, host := range []string{"daily-cloudcode-pa.googleapis.com", "cloudcode-pa.googleapis.com"} {
		base := "https://" + host + "/v1internal:"
		body := []byte(`{}`)
		if !strings.HasPrefix(host, "daily") {
			body = []byte(`{"cloudaicompanionProject":"aicode-consumers","metadata":{"ideName":"antigravity"}}`)
		}
		post := func(method string, body []byte) (object, error) {
			return requestJSON(ctx, http.MethodPost, base+method, credential, body, map[string]string{"User-Agent": "antigravity/1.0 windows/x64 google-api-nodejs-client/10.3.0", "X-Goog-Api-Client": "gl-node/20.0.0"})
		}
		root, err := post("loadCodeAssist", body)
		if err != nil {
			lastError = err
			var failure *Error
			if errors.As(err, &failure) && failure.IsAuthFailure() {
				return nil, err
			}
			continue
		}
		result := parseGravity(root)
		if summary, err := post("retrieveUserQuotaSummary", []byte(`{}`)); err == nil {
			result.merge(parseGravity(summary))
		}
		complete := (result.geminiFive != nil || result.geminiWeek != nil) && (result.claudeFive != nil || result.claudeWeek != nil)
		if !complete {
			for _, method := range []string{"fetchAvailableModels", "retrieveUserQuota"} {
				if details, err := post(method, []byte(`{}`)); err == nil {
					result.merge(parseGravity(details))
				}
			}
		}
		if credential.Email == "" {
			if user, err := requestJSON(ctx, http.MethodGet, "https://www.googleapis.com/oauth2/v3/userinfo", credential, nil, map[string]string{"X-Goog-Api-Client": "gl-node/20.0.0"}); err == nil {
				credential.Email = str(user, "email")
			}
		}
		return gravitySnapshot(result), nil
	}
	return nil, lastError
}
