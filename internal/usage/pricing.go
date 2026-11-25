package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Mag1cFall/cc-bar/internal/model"
)

// Price 保存每百万令牌的四类单价并沿用既有价格缓存格式
type Price struct{ Input, Output, CacheRead, CacheWrite float64 }

type pricePeriod struct {
	from  time.Time
	to    time.Time
	price Price
}

type contextTier struct {
	threshold int64
	inclusive bool
	long      Price
}

func (tier contextTier) exceeded(inputTotal int64) bool {
	if tier.inclusive {
		return inputTotal >= tier.threshold
	}
	return inputTotal > tier.threshold
}

func utc(y int, m time.Month, d, h int) time.Time {
	return time.Date(y, m, d, h, 0, 0, 0, time.UTC)
}

func matchPeriod(periods []pricePeriod, at time.Time) (Price, bool) {
	for _, p := range periods {
		if (p.from.IsZero() || !at.Before(p.from)) && (p.to.IsZero() || at.Before(p.to)) {
			return p.price, true
		}
	}
	return Price{}, false
}

var (
	dsGA    = utc(2026, 8, 16, 16)
	dsSep10 = utc(2026, 9, 10, 4)
)

// isDeepSeekPeak 判断UTC时刻是否位于DeepSeek工作日高峰窗口
func isDeepSeekPeak(at time.Time) bool {
	u := at.UTC()
	wd, h := u.Weekday(), u.Hour()
	if wd == time.Saturday || wd == time.Sunday {
		return false
	}
	return (h >= 1 && h < 4) || (h >= 6 && h < 10)
}

// deepseekV4Price 计算DeepSeek V4系列按历史阶段与峰谷规则确定的单价
func deepseekV4Price(name string, at time.Time) (Price, bool) {
	var p Price
	switch name {
	case "deepseek-v4-pro":
		if at.Before(dsGA) {
			return Price{.435, .87, .003625, 0}, true
		}
		p = Price{1.32, 3.96, .044, 0}
	case "deepseek-flash", "deepseek-v4.1-flash", "deepseek-v4-flash", "deepseek-v4-flash-vision-exp":
		if at.Before(dsGA) {
			return Price{.14, .28, .0028, 0}, true
		}
		if at.Before(dsSep10) {
			p = Price{.44, 1.32, .014, 0}
		} else {
			p = Price{.30, 1.20, .006, 0}
		}
	default:
		return Price{}, false
	}
	if !isDeepSeekPeak(at) {
		p = Price{p.Input * .5, p.Output * .5, p.CacheRead * .5, p.CacheWrite * .5}
	}
	return p, true
}

var (
	gpt56SolPeriods = []pricePeriod{
		{to: utc(2026, 8, 21, 0), price: Price{5, 30, .5, 6.25}},
		{from: utc(2026, 8, 21, 0), to: utc(2026, 12, 1, 0), price: Price{4, 20, .4, 5}},
	}
	gpt56SolFastPeriods = []pricePeriod{
		{to: utc(2026, 8, 21, 0), price: Price{10, 60, 1, 12.5}},
		{from: utc(2026, 8, 21, 0), to: utc(2026, 12, 1, 0), price: Price{8, 40, .8, 10}},
	}
	gemini4PromoPeriods     = []pricePeriod{{to: utc(2026, 12, 1, 0), price: Price{2, 10, .1, 0}}}
	gemini3FlashPromoPeriod = []pricePeriod{{to: utc(2027, 1, 1, 0), price: Price{.75, 3.75, .075, 0}}}
)

var timedStandard = map[string][]pricePeriod{
	"gpt-5.6":              gpt56SolPeriods,
	"gpt-5.6-sol":          gpt56SolPeriods,
	"gpt-5.6-terra":        {{to: utc(2026, 7, 30, 0), price: Price{2.5, 15, .25, 3.125}}},
	"gpt-5.6-luna":         {{to: utc(2026, 7, 30, 0), price: Price{1, 6, .1, 1.25}}},
	"gemini-4-carbon":      gemini4PromoPeriods,
	"gemini-4-argon":       gemini4PromoPeriods,
	"gemini-4-argon-build": gemini4PromoPeriods,
	"gemini-3.8-flash":     gemini3FlashPromoPeriod,
	"gemini-3.7-flash":     gemini3FlashPromoPeriod,
	"gemini-3.6-flash":     gemini3FlashPromoPeriod,
}

var timedCodexFast = map[string][]pricePeriod{
	"gpt-5.6":     gpt56SolFastPeriods,
	"gpt-5.6-sol": gpt56SolFastPeriods,
}

var contextTiers = map[string]contextTier{
	"claude-haiku-5-5": {threshold: 100000, long: Price{.5, 2.5, .05, .625}},
	"claude-haiku-5.5": {threshold: 100000, long: Price{.5, 2.5, .05, .625}},
	"gemini-3.1-pro":   {threshold: 200000, long: Price{4, 18, .4, 0}},
	"gemini-3-pro":     {threshold: 200000, long: Price{4, 18, .4, 0}},
	"gemini-2.5-pro":   {threshold: 200000, long: Price{2.5, 15, .25, 0}},
	"grok-4-7":         {threshold: 200000, inclusive: true, long: Price{4, 12, 1, 0}},
	"grok-4.7":         {threshold: 200000, inclusive: true, long: Price{4, 12, 1, 0}},
	"grok-4-6":         {threshold: 200000, inclusive: true, long: Price{4, 12, 1, 0}},
	"grok-4.6":         {threshold: 200000, inclusive: true, long: Price{4, 12, 1, 0}},
	"grok-4-5":         {threshold: 200000, inclusive: true, long: Price{4, 12, .6, 0}},
	"grok-4.5":         {threshold: 200000, inclusive: true, long: Price{4, 12, .6, 0}},
}

var builtin = map[string]Price{
	"claude-fable-5.2": {8, 40, .2, 10}, "claude-fable-5-2": {8, 40, .2, 10}, "claude-mythos-5.2": {8, 40, .2, 10}, "claude-mythos-5-2": {8, 40, .2, 10},
	"claude-fable-5.1": {10, 50, .25, 12.5}, "claude-fable-5-1": {10, 50, .25, 12.5}, "claude-mythos-5.1": {10, 50, .25, 12.5}, "claude-mythos-5-1": {10, 50, .25, 12.5},
	"claude-fable-5": {10, 50, 1, 12.5}, "claude-mythos-5": {10, 50, 1, 12.5},
	"claude-opus-5.5": {4, 20, .2, 5}, "claude-opus-5-5": {4, 20, .2, 5},
	"claude-opus-5": {5, 25, .5, 6.25}, "claude-opus-4-8": {5, 25, .5, 6.25}, "claude-opus-4-7": {5, 25, .5, 6.25}, "claude-opus-4-6": {5, 25, .5, 6.25}, "claude-opus-4-5": {5, 25, .5, 6.25},
	"claude-opus-4-1": {15, 75, 1.5, 18.75}, "claude-opus-4": {15, 75, 1.5, 18.75}, "claude-3-opus": {15, 75, 1.5, 18.75},
	"claude-sonnet-5.5": {2, 10, .1, 2.5}, "claude-sonnet-5-5": {2, 10, .1, 2.5}, "claude-sonnet-5": {2, 10, .2, 2.5},
	"claude-sonnet-4-7": {3, 15, .3, 3.75}, "claude-sonnet-4-6": {3, 15, .3, 3.75}, "claude-sonnet-4-5": {3, 15, .3, 3.75}, "claude-sonnet-4": {3, 15, .3, 3.75}, "claude-3-5-sonnet": {3, 15, .3, 3.75},
	"claude-haiku-5.5": {.1, .5, .01, .125}, "claude-haiku-5-5": {.1, .5, .01, .125},
	"claude-haiku-4-5": {1, 5, .1, 1.25}, "claude-haiku-4": {.8, 4, .08, 1}, "claude-3-5-haiku": {.8, 4, .08, 1},
	"gpt-6.2-astra": {8, 40, .4, 10}, "gpt-6.2-sol": {2, 10, .1, 2.5},
	"gpt-6.1-sol": {2, 10, .1, 2.5}, "gpt-6.1-terra": {1, 5, .05, 1.25},
	"gpt-6-astra": {10, 50, 1, 12.5}, "gpt-6-sol": {2, 10, .2, 2.5}, "gpt-6-luna": {.1, .5, .01, .125}, "gpt-6-cyber": {20, 100, 2, 25},
	"gpt-5.6": {5, 30, .5, 6.25}, "gpt-5.6-sol": {5, 30, .5, 6.25}, "gpt-5.6-terra": {2, 12, .2, 2.5}, "gpt-5.6-luna": {.2, 1.2, .02, .25}, "gpt-5.6-cyber": {12.5, 75, 1.25, 15.625},
	"gpt-daybreak-blue-latest": {4, 20, .4, 5}, "daybreak-blue-latest": {4, 20, .4, 5},
	"gpt-daybreak-red-latest": {12.5, 75, 1.25, 15.625}, "daybreak-red-latest": {12.5, 75, 1.25, 15.625},
	"gpt-5.5": {5, 30, .5, 0}, "gpt-5.5-codex": {5, 30, .5, 0}, "gpt-5.5-pro": {30, 180, 0, 0}, "gpt-5.4-pro": {30, 180, 0, 0},
	"gpt-5.4": {2.5, 15, .25, 0}, "gpt-5.4-codex": {2.5, 15, .25, 0}, "gpt-5.4-mini": {.75, 4.5, .075, 0}, "gpt-5.4-nano": {.2, 1.25, .02, 0},
	"gpt-5.3": {1.75, 14, .175, 0}, "gpt-5.3-codex": {1.75, 14, .175, 0}, "gpt-5.2": {1.75, 14, .175, 0}, "gpt-5.2-codex": {1.75, 14, .175, 0}, "gpt-5.2-pro": {21, 168, 0, 0},
	"gpt-5.1": {1.25, 10, .125, 0}, "gpt-5.1-codex": {1.25, 10, .125, 0}, "gpt-5.1-codex-max": {1.25, 10, .125, 0}, "gpt-5": {1.25, 10, .125, 0}, "gpt-5-codex": {1.25, 10, .125, 0},
	"gpt-5-mini": {.25, 2, .025, 0}, "gpt-5.1-codex-mini": {.25, 2, .025, 0}, "gpt-5-nano": {.05, .4, .005, 0}, "gpt-5-pro": {15, 120, 0, 0}, "codex-mini-latest": {1.5, 6, .375, 0},
	"gpt-4.1": {2, 8, .5, 0}, "gpt-4.1-mini": {.4, 1.6, .1, 0}, "gpt-4o": {2.5, 10, 1.25, 0}, "gpt-4o-mini": {.15, .6, .075, 0},
	"o3": {2, 8, .5, 0}, "o3-pro": {20, 80, 0, 0}, "o1": {15, 60, 7.5, 0}, "o1-mini": {1.1, 4.4, .55, 0}, "o3-mini": {1.1, 4.4, .55, 0}, "o4-mini": {1.1, 4.4, .275, 0},
	"gemini-4-neon": {3, 15, .15, 0}, "gemini-4-carbon": {4, 20, .2, 0}, "gemini-4-argon": {4, 20, .2, 0}, "gemini-4-argon-build": {4, 20, .2, 0}, "gemini-4-hydrogen": {1.5, 7.5, .075, 0},
	"gemini-3.8-flash": {1.5, 7.5, .15, 0}, "gemini-3.7-flash": {1.5, 7.5, .15, 0}, "gemini-3.6-flash": {1.5, 7.5, .15, 0},
	"gemini-3.5-flash": {1.5, 9, .15, 0}, "gemini-3.5-flash-lite": {.3, 2.5, .03, 0}, "gemini-3.1-flash-lite": {.25, 1.5, .025, 0},
	"gemini-3.1-pro": {2, 12, .2, 0}, "gemini-3-pro": {2, 12, .2, 0}, "gemini-3-flash": {.5, 3, .05, 0},
	"gemini-2.5-pro": {1.25, 10, .125, 0}, "gemini-2.5-flash": {.3, 2.5, .03, 0}, "gemini-2.5-flash-lite": {.1, .4, .01, 0},
	"composer-2.5": {.5, 2.5, .2, 0}, "cursor-composer-2-5": {.5, 2.5, .2, 0}, "composer-2.5-fast": {3, 15, .5, 0},
	"grok-4-7": {2, 6, .5, 0}, "grok-4.7": {2, 6, .5, 0}, "grok-4-6": {2, 6, .5, 0}, "grok-4.6": {2, 6, .5, 0}, "grok-4-5": {2, 6, .3, 0}, "grok-4.5": {2, 6, .3, 0},
	"gpt-oss-120b": {.15, .6, .075, 0},
	"deepseek-v4-pro": {1.32, 3.96, .044, 0}, "deepseek-flash": {.3, 1.2, .006, 0}, "deepseek-v4.1-flash": {.3, 1.2, .006, 0}, "deepseek-v4-flash": {.3, 1.2, .006, 0}, "deepseek-v4-flash-vision-exp": {.3, 1.2, .006, 0},
	"deepseek-v3.2": {.28, .42, .028, 0}, "deepseek-chat": {.28, .42, .028, 0}, "deepseek-reasoner": {.28, .42, .028, 0},
	"deepseek-v3.1": {.55, 1.67, .055, 0}, "deepseek-v3": {.28, 1.11, .028, 0},
}

var codexFast = map[string]Price{
	"gpt-6.2-astra": {16, 80, .8, 20}, "gpt-6.2-sol": {4, 20, .2, 5},
	"gpt-6.1-sol": {4, 20, .2, 5}, "gpt-6.1-terra": {2, 10, .1, 2.5},
	"gpt-6-astra": {20, 100, 2, 25}, "gpt-6-sol": {4, 20, .4, 5}, "gpt-6-luna": {.2, 1, .02, .25},
	"gpt-5.6": {10, 60, 1, 12.5}, "gpt-5.6-sol": {10, 60, 1, 12.5}, "gpt-5.6-terra": {4, 24, .4, 5}, "gpt-5.6-luna": {.4, 2.4, .04, .5},
	"gpt-5.5": {12.5, 75, 1.25, 0}, "gpt-5.5-codex": {12.5, 75, 1.25, 0},
	"gpt-5.4": {5, 30, .5, 0}, "gpt-5.4-codex": {5, 30, .5, 0}, "gpt-5.4-mini": {1.5, 9, .15, 0},
	"gpt-5.3-codex": {3.5, 28, .35, 0}, "gpt-5.2": {3.5, 28, .35, 0}, "gpt-5.1": {2.5, 20, .25, 0}, "gpt-5": {2.5, 20, .25, 0}, "gpt-5-mini": {.45, 3.6, .045, 0},
	"gpt-4.1": {3.5, 14, .875, 0}, "gpt-4.1-mini": {.7, 2.8, .175, 0}, "gpt-4o": {4.25, 17, 2.125, 0}, "gpt-4o-mini": {.25, 1, .125, 0}, "o3": {3.5, 14, .875, 0}, "o4-mini": {2, 8, .5, 0},
}

var claudeFast = map[string]Price{
	"claude-fable-5.2": {16, 80, .4, 20}, "claude-fable-5-2": {16, 80, .4, 20}, "claude-mythos-5.2": {16, 80, .4, 20}, "claude-mythos-5-2": {16, 80, .4, 20},
	"claude-opus-5.5": {8, 40, .4, 10}, "claude-opus-5-5": {8, 40, .4, 10}, "claude-opus-5": {10, 50, 1, 12.5}, "claude-opus-4-8": {10, 50, 1, 12.5},
	"claude-opus-4-7": {30, 150, 3, 37.5}, "claude-opus-4-6": {30, 150, 3, 37.5},
}

// stripDateSuffix 在末段为8位日期或YYYY-MM-DD时原地裁除版本后缀
func stripDateSuffix(name string) string {
	n := len(name)
	isDigits := func(s string) bool {
		for i := 0; i < len(s); i++ {
			if s[i] < '0' || s[i] > '9' {
				return false
			}
		}
		return true
	}
	if n > 9 && name[n-9] == '-' && isDigits(name[n-8:]) {
		return name[:n-9]
	}
	if n > 11 && name[n-11] == '-' && name[n-6] == '-' && name[n-3] == '-' && isDigits(name[n-10:n-6]) && isDigits(name[n-5:n-3]) && isDigits(name[n-2:]) {
		return name[:n-11]
	}
	return name
}

// stripVariantSuffix 循环移除 Antigravity 等来源附带的推理档位与预览后缀
func stripVariantSuffix(name string) string {
	for {
		trimmed := name
		for _, suffix := range []string{"-tiered-xhigh", "-tiered-high", "-tiered-medium", "-tiered-low", "-xhigh", "-high", "-medium", "-low", "-thinking", "-preview"} {
			if strings.HasSuffix(trimmed, suffix) {
				trimmed = strings.TrimSuffix(trimmed, suffix)
				break
			}
		}
		if trimmed == name {
			return name
		}
		name = trimmed
	}
}

// NormalizeModel 移除提供商前缀和版本日期以匹配价格目录
func NormalizeModel(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	for {
		changed := false
		for _, prefix := range []string{"openai-codex/", "openai/", "anthropic/", "deepseek/", "opencode-go/", "commandcode/", "command-code/", "google/", "google-vertex/", "gemini/", "xai/"} {
			if strings.HasPrefix(name, prefix) {
				name = strings.TrimPrefix(name, prefix)
				changed = true
				break
			}
		}
		if !changed {
			break
		}
	}
	if at := strings.IndexByte(name, '@'); at >= 0 {
		name = name[:at]
	}
	return stripDateSuffix(name)
}

type priceSource struct {
	Etag            *string
	FetchedAt       *time.Time
	FailedAt        *time.Time
	StandardRates   map[string]Price
	CodexFastRates  map[string]Price
	ClaudeFastRates map[string]Price
}

type catalog struct {
	Version         int
	LiteLLM         priceSource
	ModelsDev       priceSource
	MissingAttempts map[string]time.Time
}

// Pricing 管理内置价格和两份在线目录
type Pricing struct {
	mu      sync.RWMutex
	refresh sync.Mutex
	active  catalog
	version uint64
	path    string
	client  *http.Client
}

// Version 返回价格目录内容变更版本号
func (pricing *Pricing) Version() uint64 {
	pricing.mu.RLock()
	defer pricing.mu.RUnlock()
	return pricing.version
}

// NewPricing 加载现有价格缓存并保留历史价格规则
func NewPricing(dataDir string) *Pricing {
	pricing := &Pricing{path: filepath.Join(dataDir, "pricing-catalog.json"), client: &http.Client{Timeout: 25 * time.Second}}
	if data, err := os.ReadFile(pricing.path); err == nil {
		_ = json.Unmarshal(data, &pricing.active)
	}
	if pricing.active.Version < 3 {
		for _, source := range []*priceSource{&pricing.active.LiteLLM, &pricing.active.ModelsDev} {
			source.Etag = nil
			source.FetchedAt = nil
		}
	}
	pricing.active.Version = 3
	for _, source := range []*priceSource{&pricing.active.LiteLLM, &pricing.active.ModelsDev} {
		if source.StandardRates == nil {
			source.StandardRates = map[string]Price{}
		}
		if source.CodexFastRates == nil {
			source.CodexFastRates = map[string]Price{}
		}
		if source.ClaudeFastRates == nil {
			source.ClaudeFastRates = map[string]Price{}
		}
	}
	if pricing.active.MissingAttempts == nil {
		pricing.active.MissingAttempts = map[string]time.Time{}
	}
	return pricing
}

func (pricing *Pricing) rate(name string, app model.UsageApp, speed string) (Price, bool) {
	pricing.mu.RLock()
	defer pricing.mu.RUnlock()
	first, second := pricing.active.LiteLLM.StandardRates, pricing.active.ModelsDev.StandardRates
	if speed == "fast" {
		if app == model.UsageCodex {
			first, second = pricing.active.ModelsDev.CodexFastRates, pricing.active.LiteLLM.CodexFastRates
		} else if app == model.UsageClaude {
			first, second = pricing.active.ModelsDev.ClaudeFastRates, pricing.active.LiteLLM.ClaudeFastRates
		} else {
			return Price{}, false
		}
	}
	if price, found := first[name]; found {
		return price, true
	}
	price, found := second[name]
	return price, found
}

func (pricing *Pricing) resolveStandard(name string, app model.UsageApp, at time.Time, inputTotal int64) (Price, bool) {
	base := stripVariantSuffix(name)
	for _, key := range []string{name, base} {
		if key == "" {
			continue
		}
		if periods, ok := timedStandard[key]; ok {
			if price, matched := matchPeriod(periods, at); matched {
				return price, true
			}
		}
		if price, ok := deepseekV4Price(key, at); ok {
			return price, true
		}
		if tier, ok := contextTiers[key]; ok && tier.exceeded(inputTotal) {
			return tier.long, true
		}
		if key == base {
			break
		}
	}
	if online, ok := pricing.rate(name, app, "standard"); ok {
		return online, true
	}
	if price, found := builtin[name]; found {
		return price, true
	}
	if base != name {
		if online, ok := pricing.rate(base, app, "standard"); ok {
			return online, true
		}
		if price, found := builtin[base]; found {
			return price, true
		}
	}
	return Price{}, false
}

// canPrice 检查指定组合在常规或长上下文下是否具备可用单价
func (pricing *Pricing) canPrice(app model.UsageApp, name, speed string, at time.Time) bool {
	if speed == "unknown" {
		return false
	}
	probeShort := model.UsageRow{App: app, Model: name, Speed: speed, Time: at, Input: 1}
	probe250k := model.UsageRow{App: app, Model: name, Speed: speed, Time: at, Input: 250000}
	return pricing.Cost(probeShort) != nil || pricing.Cost(probe250k) != nil
}

// CostBreakdown 按官方阶段价、峰谷规则、长上下文阶梯与快速档位计算请求费用拆分
func (pricing *Pricing) CostBreakdown(row model.UsageRow) *model.CostBreakdown {
	if row.ReportedCosts != nil {
		return row.ReportedCosts
	}
	if row.App == model.UsagePi || row.App == model.UsageOmp || row.App == model.UsageAntigravity {
		switch Provider(row.App, row.Model) {
		case "OpenAI":
			row.App = model.UsageCodex
		case "Anthropic":
			row.App = model.UsageClaude
		}
	}
	name := NormalizeModel(row.Model)
	at := row.Time
	if at.IsZero() {
		at = time.Now()
	}
	inputTotal := row.Input + row.CacheRead + row.CacheWrite
	var price Price
	var found bool
	switch row.Speed {
	case "standard":
		price, found = pricing.resolveStandard(name, row.App, at, inputTotal)
	case "fast":
		switch row.App {
		case model.UsageCodex:
			if periods, ok := timedCodexFast[name]; ok {
				if p, matched := matchPeriod(periods, at); matched {
					price, found = p, true
					break
				}
			}
			if online, ok := pricing.rate(name, row.App, "fast"); ok {
				price, found = online, true
			} else {
				price, found = codexFast[name]
			}
		case model.UsageClaude:
			if name == "claude-opus-4-7" || name == "claude-opus-4-6" {
				price, found = claudeFast[name]
			} else if online, ok := pricing.rate(name, row.App, "fast"); ok {
				price, found = online, true
			} else {
				price, found = claudeFast[name]
			}
		default:
			return nil
		}
	default:
		return nil
	}
	if !found {
		return nil
	}
	write1h := min(max(row.CacheWrite1h, 0), row.CacheWrite)
	rate1h := price.CacheWrite
	if row.App == model.UsageClaude {
		rate1h = price.Input * 2
	}
	return &model.CostBreakdown{Input: float64(row.Input) * price.Input / 1e6, Output: float64(row.Output) * price.Output / 1e6, CacheRead: float64(row.CacheRead) * price.CacheRead / 1e6, CacheWrite: float64(row.CacheWrite-write1h)*price.CacheWrite/1e6 + float64(write1h)*rate1h/1e6}
}

// Cost 返回可确定的请求总费用
func (pricing *Pricing) Cost(row model.UsageRow) *float64 {
	part := pricing.CostBreakdown(row)
	if part == nil {
		return nil
	}
	cost := part.Input + part.Output + part.CacheRead + part.CacheWrite
	return &cost
}

// BillingEquivalentMultiplier 返回账号额度的快速档位倍率
func BillingEquivalentMultiplier(app model.UsageApp, name string) *float64 {
	if app == model.UsagePi || app == model.UsageOmp || app == model.UsageAntigravity {
		switch Provider(app, name) {
		case "OpenAI":
			app = model.UsageCodex
		case "Anthropic":
			app = model.UsageClaude
		}
	}
	name = NormalizeModel(name)
	var multiplier float64
	if app == model.UsageCodex {
		switch name {
		case "gpt-6.2-astra", "gpt-6.2-sol", "gpt-6.1-sol", "gpt-6.1-terra", "gpt-6-astra", "gpt-6-sol", "gpt-6-luna", "gpt-5.6", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.5-codex":
			multiplier = 2.5
		case "gpt-5.4", "gpt-5.4-codex":
			multiplier = 2
		}
	} else if app == model.UsageClaude {
		switch name {
		case "claude-fable-5.2", "claude-fable-5-2", "claude-mythos-5.2", "claude-mythos-5-2", "claude-opus-5.5", "claude-opus-5-5", "claude-opus-5", "claude-opus-4-8":
			multiplier = 2
		case "claude-opus-4-7", "claude-opus-4-6":
			multiplier = 6
		}
	}
	if multiplier == 0 {
		return nil
	}
	return &multiplier
}

type rateCandidate struct {
	key      string
	priority int
	price    Price
}

func resolveRates(candidates []rateCandidate) map[string]Price {
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].key != candidates[j].key {
			return candidates[i].key < candidates[j].key
		}
		return candidates[i].priority < candidates[j].priority
	})
	rates := map[string]Price{}
	for _, entry := range candidates {
		if NormalizeModel(entry.key) == entry.key {
			if _, found := rates[entry.key]; !found {
				rates[entry.key] = entry.price
			}
		}
	}
	for _, entry := range candidates {
		name := NormalizeModel(entry.key)
		if _, found := rates[name]; !found {
			rates[name] = entry.price
		}
	}
	return rates
}

func decodeCatalog(root node, lite bool) priceSource {
	standard := []rateCandidate{}
	codex := []rateCandidate{}
	claude := []rateCandidate{}
	if lite {
		for name, raw := range root {
			entry := object(raw)
			provider := str(entry, "litellm_provider")
			if provider != "anthropic" && provider != "openai" && provider != "deepseek" && provider != "gemini" {
				continue
			}
			priority := 0
			if provider == "gemini" && strings.HasPrefix(name, "gemini/") {
				name = strings.TrimPrefix(name, "gemini/")
				priority = 1
			}
			if strings.Contains(name, "/") {
				continue
			}
			if has(entry, "input_cost_per_token") && has(entry, "output_cost_per_token") {
				standard = append(standard, rateCandidate{name, priority, Price{number(entry, "input_cost_per_token") * 1e6, number(entry, "output_cost_per_token") * 1e6, number(entry, "cache_read_input_token_cost") * 1e6, number(entry, "cache_creation_input_token_cost") * 1e6}})
			}
			if provider == "openai" && has(entry, "input_cost_per_token_priority") && has(entry, "output_cost_per_token_priority") {
				codex = append(codex, rateCandidate{name, priority, Price{number(entry, "input_cost_per_token_priority") * 1e6, number(entry, "output_cost_per_token_priority") * 1e6, number(entry, "cache_read_input_token_cost_priority") * 1e6, number(entry, "cache_creation_input_token_cost_priority") * 1e6}})
			}
		}
	} else {
		for priority, provider := range []string{"anthropic", "openai", "deepseek", "google"} {
			for name, raw := range obj(obj(root, provider), "models") {
				entry := object(raw)
				cost := obj(entry, "cost")
				if has(cost, "input") && has(cost, "output") {
					standard = append(standard, rateCandidate{name, priority, Price{number(cost, "input"), number(cost, "output"), number(cost, "cache_read"), number(cost, "cache_write")}})
				}
				fast := obj(obj(obj(entry, "experimental"), "modes"), "fast")
				cost = obj(fast, "cost")
				if has(cost, "input") && has(cost, "output") {
					candidate := rateCandidate{name, priority, Price{number(cost, "input"), number(cost, "output"), number(cost, "cache_read"), number(cost, "cache_write")}}
					if provider == "openai" {
						codex = append(codex, candidate)
					} else if provider == "anthropic" {
						claude = append(claude, candidate)
					}
				}
			}
		}
	}
	return priceSource{StandardRates: resolveRates(standard), CodexFastRates: resolveRates(codex), ClaudeFastRates: resolveRates(claude)}
}

// Refresh 使用ETag更新两个价格源并持久化到既有缓存
func (pricing *Pricing) Refresh(ctx context.Context, force bool) error {
	pricing.refresh.Lock()
	defer pricing.refresh.Unlock()
	var errors []string
	for _, feed := range []struct {
		url  string
		lite bool
	}{{"https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json", true}, {"https://models.dev/api.json", false}} {
		pricing.mu.RLock()
		source := pricing.active.ModelsDev
		if feed.lite {
			source = pricing.active.LiteLLM
		}
		source.StandardRates = maps.Clone(source.StandardRates)
		source.CodexFastRates = maps.Clone(source.CodexFastRates)
		source.ClaudeFastRates = maps.Clone(source.ClaudeFastRates)
		pricing.mu.RUnlock()
		now := time.Now()
		if !force && ((source.FailedAt != nil && now.Sub(*source.FailedAt) < 30*time.Minute) || (source.FetchedAt != nil && now.Sub(*source.FetchedAt) < 24*time.Hour)) {
			continue
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, feed.url, nil)
		if err != nil {
			return err
		}
		request.Header.Set("Accept", "application/json")
		if source.Etag != nil {
			request.Header.Set("If-None-Match", *source.Etag)
		}
		response, err := pricing.client.Do(request)
		if err == nil {
			if response.StatusCode == http.StatusNotModified {
				source.FetchedAt = &now
				source.FailedAt = nil
			} else if response.StatusCode != http.StatusOK {
				err = fmt.Errorf("价格目录HTTP %d", response.StatusCode)
			} else {
				var root node
				err = json.NewDecoder(response.Body).Decode(&root)
				if err == nil {
					decoded := decodeCatalog(root, feed.lite)
					if len(decoded.StandardRates) == 0 || (len(source.StandardRates) >= 20 && len(decoded.StandardRates)*2 < len(source.StandardRates)) {
						err = fmt.Errorf("价格目录内容数量异常")
					} else {
						for key, value := range decoded.CodexFastRates {
							source.CodexFastRates[key] = value
						}
						for key, value := range decoded.ClaudeFastRates {
							source.ClaudeFastRates[key] = value
						}
						source.StandardRates = decoded.StandardRates
						etag := response.Header.Get("ETag")
						source.Etag = &etag
						source.FetchedAt = &now
						source.FailedAt = nil
					}
				}
			}
			response.Body.Close()
		}
		if err != nil {
			source.FailedAt = &now
			source.Etag = nil
			errors = append(errors, err.Error())
		}
		pricing.mu.Lock()
		if feed.lite {
			pricing.active.LiteLLM = source
		} else {
			pricing.active.ModelsDev = source
		}
		if err == nil {
			pricing.version++
		}
		data, marshalErr := json.Marshal(pricing.active)
		pricing.mu.Unlock()
		if marshalErr != nil {
			return marshalErr
		}
		if err = os.WriteFile(pricing.path, data, 0600); err != nil {
			return err
		}
	}
	if len(errors) > 0 {
		return fmt.Errorf("%s", strings.Join(errors, "; "))
	}
	return nil
}
