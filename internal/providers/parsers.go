package providers

import (
	"math"
	"strings"
	"time"

	"github.com/Mag1cFall/cc-bar/internal/model"
)

func limit(id, name string, kind model.LimitKind, window *model.QuotaWindow) *model.QuotaLimit {
	if window == nil {
		return nil
	}
	return &model.QuotaLimit{ID: id, DisplayName: name, Kind: kind, Window: *window}
}
func kind(window *model.QuotaWindow, fallback model.LimitKind) model.LimitKind {
	if window != nil && window.WindowSeconds != nil {
		if *window.WindowSeconds == 18000 {
			return model.FiveHour
		}
		if *window.WindowSeconds == 604800 {
			return model.Weekly
		}
	}
	return fallback
}
func standard(window *model.QuotaWindow) *model.QuotaLimit {
	if window == nil {
		return nil
	}
	value := kind(window, model.UnknownLimit)
	id := "unknown"
	if value == model.FiveHour {
		id = "five-hour"
	}
	if value == model.Weekly {
		id = "weekly"
	}
	return limit(id, "", value, window)
}
func codexWindow(value any) *model.QuotaWindow {
	root := obj(value)
	if root == nil {
		return nil
	}
	reset := date(root["reset_at"])
	if reset == nil {
		if seconds := num(root, "reset_after_seconds"); seconds != nil {
			value := time.Now().Add(time.Duration(*seconds * float64(time.Second)))
			reset = &value
		}
	}
	window := &model.QuotaWindow{UsedPercent: val(root, "used_percent"), ResetsAt: reset}
	if seconds := num(root, "limit_window_seconds"); seconds != nil {
		window.WindowSeconds = integer(int(*seconds))
	}
	return window
}

// ParseCodex 保留主窗口与模型独立额度
func ParseCodex(root map[string]any) *model.QuotaSnapshot {
	rate := obj(root["rate_limit"])
	result := &model.QuotaSnapshot{
		App:             model.Codex,
		PlanType:        str(root, "plan_type"),
		PrimaryLimit:    standard(codexWindow(rate["primary_window"])),
		SecondaryLimit:  standard(codexWindow(rate["secondary_window"])),
		AuxiliaryLimits: []model.QuotaLimit{},
		ModelLimits:     []model.QuotaLimit{},
		FetchedAt:       time.Now(),
	}
	for _, row := range rows(root["additional_rate_limits"]) {
		entry := obj(row)
		details := obj(entry["rate_limit"])
		if details == nil {
			details = obj(entry["details"])
		}
		if details == nil {
			details = entry
		}
		name := str(entry, "limit_name", "name", "normal_model_slug")
		id := str(entry, "limit_id", "id", "normal_model_slug")
		if id == "" {
			id = name
		}
		for _, suffix := range []string{"primary", "secondary"} {
			if window := codexWindow(details[suffix+"_window"]); window != nil {
				result.ModelLimits = append(result.ModelLimits, *limit("model:"+id+":"+suffix, name, kind(window, model.ModelWeekly), window))
			}
		}
	}
	return result
}

func claudeWindow(value any) *model.QuotaWindow {
	root := obj(value)
	percent := num(root, "utilization")
	if percent == nil {
		return nil
	}
	return &model.QuotaWindow{UsedPercent: *percent, ResetsAt: date(root["resets_at"])}
}
func mergeWindow(preferred, old *model.QuotaWindow) *model.QuotaWindow {
	if preferred == nil {
		return old
	}
	if old != nil {
		if preferred.ResetsAt == nil {
			preferred.ResetsAt = old.ResetsAt
		}
		if preferred.WindowSeconds == nil {
			preferred.WindowSeconds = old.WindowSeconds
		}
	}
	return preferred
}

// ParseClaude 解析旧窗口与新分组协议
func ParseClaude(root map[string]any) *model.QuotaSnapshot {
	var session, weekly *model.QuotaWindow
	var sessionActive, weeklyActive *bool
	result := &model.QuotaSnapshot{App: model.Claude, AuxiliaryLimits: []model.QuotaLimit{}, ModelLimits: []model.QuotaLimit{}, FetchedAt: time.Now()}
	for _, value := range rows(root["limits"]) {
		row := obj(value)
		percent := num(row, "percent")
		if percent == nil {
			continue
		}
		tag := str(row, "kind")
		seconds := 604800
		if tag == "session" {
			seconds = 18000
		}
		window := &model.QuotaWindow{UsedPercent: *percent, ResetsAt: date(row["resets_at"]), WindowSeconds: integer(seconds)}
		active := boolean(row, "is_active")
		switch tag {
		case "session":
			session, sessionActive = window, active
		case "weekly_all":
			weekly, weeklyActive = window, active
		case "weekly_scoped":
			scope := obj(row["scope"])
			modelScope, surface := obj(scope["model"]), obj(scope["surface"])
			name := str(modelScope, "display_name")
			if name == "" {
				name = str(surface, "display_name", "name")
			}
			if name == "" {
				name = str(scope, "surface")
			}
			if name == "" {
				continue
			}
			id := str(modelScope, "id")
			if id == "" {
				id = str(surface, "id")
			}
			if id == "" {
				id = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(name)), " ", "-")
			}
			entry := *limit("model:"+strings.ToLower(id), name, model.ModelWeekly, window)
			entry.IsActive = active
			found := false
			for i := range result.ModelLimits {
				if result.ModelLimits[i].ID == entry.ID {
					result.ModelLimits[i] = entry
					found = true
					break
				}
			}
			if !found {
				result.ModelLimits = append(result.ModelLimits, entry)
			}
		}
	}
	session = mergeWindow(session, claudeWindow(root["five_hour"]))
	weekly = mergeWindow(weekly, claudeWindow(root["seven_day"]))
	for _, name := range []string{"Opus", "Sonnet"} {
		key := "seven_day_" + strings.ToLower(name)
		old := claudeWindow(root[key])
		if old == nil {
			continue
		}
		found := false
		for i := range result.ModelLimits {
			if strings.Contains(strings.ToLower(result.ModelLimits[i].DisplayName), strings.ToLower(name)) {
				if result.ModelLimits[i].Window.ResetsAt == nil {
					result.ModelLimits[i].Window.ResetsAt = old.ResetsAt
				}
				found = true
				break
			}
		}
		if !found {
			result.ModelLimits = append(result.ModelLimits, *limit("model:"+strings.ToLower(name), name, model.ModelWeekly, old))
		}
	}
	result.PrimaryLimit = limit("five-hour", "Current session", model.FiveHour, session)
	result.SecondaryLimit = limit("weekly", "All models", model.Weekly, weekly)
	if result.PrimaryLimit != nil {
		result.PrimaryLimit.IsActive = sessionActive
	}
	if result.SecondaryLimit != nil {
		result.SecondaryLimit.IsActive = weeklyActive
	}
	if result.PrimaryLimit == nil {
		result.PrimaryLimit = result.SecondaryLimit
		result.SecondaryLimit = nil
	}
	return result
}

func ratio(root object) *float64 {
	if enabled := boolean(root, "enabled"); enabled != nil && !*enabled {
		return nil
	}
	cap, used := num(root, "limit"), num(root, "used")
	if cap == nil || *cap <= 0 || used == nil {
		return nil
	}
	percent := *used / *cap * 100
	return &percent
}
func firstNumber(values ...*float64) *float64 {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

// ParseCursor 解析个人与团队计量及无限计划
func ParseCursor(root map[string]any) *model.QuotaSnapshot {
	individual, team := obj(root["individualUsage"]), obj(root["teamUsage"])
	plan, overall, pooled := obj(individual["plan"]), obj(individual["overall"]), obj(team["pooled"])
	start, reset := date(root["billingCycleStart"]), date(root["billingCycleEnd"])
	var seconds *int
	if start != nil && reset != nil && reset.After(*start) {
		seconds = integer(int(reset.Sub(*start).Seconds()))
	}
	total := firstNumber(num(plan, "totalPercentUsed"), ratio(plan), ratio(overall), ratio(pooled))
	if strings.EqualFold(str(root, "limitType"), "team") {
		total = firstNumber(ratio(pooled), total)
	}
	makeLimit := func(id, name string, percent *float64) *model.QuotaLimit {
		if percent == nil {
			return nil
		}
		return limit(id, name, model.UnknownLimit, &model.QuotaWindow{UsedPercent: *percent, ResetsAt: reset, WindowSeconds: seconds})
	}
	result := &model.QuotaSnapshot{
		App:             model.Cursor,
		PrimaryLimit:    makeLimit("cursor-total", "Total", total),
		SecondaryLimit:  makeLimit("cursor-auto", "Auto", num(plan, "autoPercentUsed")),
		IsUnlimited:     boolean(root, "isUnlimited"),
		PlanType:        formatPlan(str(root, "membershipType")),
		AuxiliaryLimits: []model.QuotaLimit{},
		ModelLimits:     []model.QuotaLimit{},
		FetchedAt:       time.Now(),
	}
	if api := makeLimit("cursor-api", "API", num(plan, "apiPercentUsed")); api != nil {
		result.AuxiliaryLimits = append(result.AuxiliaryLimits, *api)
	}
	return result
}
func formatPlan(raw string) string {
	words := strings.Fields(strings.NewReplacer("_", " ", "-", " ").Replace(raw))
	for i, word := range words {
		if word != "" {
			words[i] = strings.ToUpper(word[:1]) + strings.ToLower(word[1:])
		}
	}
	return strings.Join(words, " ")
}

// ParseCommandCode 解析五小时、周和 GOAT 月度额度
func ParseCommandCode(credits, subscription map[string]any) *model.QuotaSnapshot {
	plan := formatPlan(str(subscription, "planId"))
	lower := strings.ToLower(plan)
	for _, name := range []string{"GOAT", "Pro", "Team", "Enterprise"} {
		if strings.Contains(lower, strings.ToLower(name)) {
			plan = name
			break
		}
	}
	window := func(key, id, name string, kind model.LimitKind, seconds int) *model.QuotaLimit {
		row := obj(obj(credits["windowLimits"])[key])
		if row == nil {
			row = obj(credits[key])
		}
		cap := num(row, "cap")
		if cap == nil || *cap <= 0 {
			return nil
		}
		percent := math.Max(0, math.Min(100, val(row, "used") / *cap * 100))
		reset := date(row["resetAt"])
		if percent == 0 && (reset == nil || !reset.After(time.Now())) {
			value := time.Now().Add(time.Duration(seconds) * time.Second)
			reset = &value
		}
		return limit(id, name, kind, &model.QuotaWindow{UsedPercent: percent, ResetsAt: reset, WindowSeconds: integer(seconds)})
	}
	result := &model.QuotaSnapshot{
		App:             model.CommandCode,
		PlanType:        plan,
		PrimaryLimit:    window("fiveHour", "command-code-five-hour", "5HOUR", model.FiveHour, 18000),
		SecondaryLimit:  window("weekly", "command-code-weekly", "WEEKLY", model.Weekly, 604800),
		AuxiliaryLimits: []model.QuotaLimit{},
		ModelLimits:     []model.QuotaLimit{},
		FetchedAt:       time.Now(),
	}
	credit := firstNumber(num(obj(credits["credits"]), "monthlyCredits"), num(credits, "monthlyCredits"))
	if plan == "GOAT" && credit != nil {
		monthly := &model.QuotaWindow{
			UsedPercent:   math.Max(0, math.Min(100, (70-*credit)/70*100)),
			ResetsAt:      date(subscription["currentPeriodEnd"]),
			WindowSeconds: integer(2592000),
		}
		result.AuxiliaryLimits = append(result.AuxiliaryLimits, *limit("command-code-monthly", "MONTHLY", model.UnknownLimit, monthly))
	}
	return result
}
