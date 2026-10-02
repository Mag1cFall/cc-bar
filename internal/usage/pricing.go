package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Mag1cFall/cc-bar/internal/model"
)

// Price 保存每百万令牌的四类单价并沿用既有价格缓存格式
type Price struct{ Input, Output, CacheRead, CacheWrite float64 }

var builtin = map[string]Price{
	"claude-fable-5.1": {10, 50, .25, 12.5}, "claude-fable-5-1": {10, 50, .25, 12.5}, "claude-fable-5": {10, 50, 1, 12.5},
	"claude-opus-5.5": {4, 20, .2, 5}, "claude-opus-5-5": {4, 20, .2, 5}, "claude-opus-5": {5, 25, .5, 6.25},
	"claude-opus-4-8": {5, 25, .5, 6.25}, "claude-opus-4-7": {5, 25, .5, 6.25}, "claude-opus-4-6": {5, 25, .5, 6.25}, "claude-opus-4-5": {5, 25, .5, 6.25},
	"claude-opus-4-1": {15, 75, 1.5, 18.75}, "claude-opus-4": {15, 75, 1.5, 18.75}, "claude-sonnet-5": {2, 10, .2, 2.5},
	"claude-sonnet-4-7": {3, 15, .3, 3.75}, "claude-sonnet-4-6": {3, 15, .3, 3.75}, "claude-sonnet-4-5": {3, 15, .3, 3.75}, "claude-sonnet-4": {3, 15, .3, 3.75},
	"claude-haiku-4-5": {1, 5, .1, 1.25}, "claude-haiku-4": {.8, 4, .08, 1}, "claude-3-5-sonnet": {3, 15, .3, 3.75}, "claude-3-5-haiku": {.8, 4, .08, 1}, "claude-3-opus": {15, 75, 1.5, 18.75},
	"gpt-6-astra": {10, 50, 1, 12.5}, "gpt-6-sol": {2, 10, .2, 2.5}, "gpt-6-luna": {.1, .5, .01, .125},
	"gpt-5.6": {5, 30, .5, 6.25}, "gpt-5.6-sol": {5, 30, .5, 6.25}, "gpt-5.6-terra": {2, 12, .2, 2.5}, "gpt-5.6-luna": {.2, 1.2, .02, .25}, "gpt-5.6-cyber": {12.5, 75, 1.25, 15.625},
	"gpt-5.5": {5, 30, .5, 0}, "gpt-5.5-codex": {5, 30, .5, 0}, "gpt-5.5-pro": {30, 180, 30, 0},
	"gpt-5.4": {2.5, 15, .25, 0}, "gpt-5.4-codex": {2.5, 15, .25, 0}, "gpt-5.4-mini": {.25, 2, .025, 0},
	"gpt-5.3": {1.25, 10, .125, 0}, "gpt-5.3-codex": {1.25, 10, .125, 0}, "gpt-5.2": {1.25, 10, .125, 0}, "gpt-5.2-codex": {1.25, 10, .125, 0}, "gpt-5.1": {1.25, 10, .125, 0},
	"gpt-5": {1.25, 10, .125, 0}, "gpt-5-codex": {1.25, 10, .125, 0}, "gpt-5-mini": {.25, 2, .025, 0}, "gpt-5-nano": {.05, .4, .005, 0}, "codex-mini-latest": {1.5, 6, .375, 0},
	"gpt-4o": {2.5, 10, 1.25, 0}, "gpt-4o-mini": {.15, .6, .075, 0}, "o3": {2, 8, 1, 0}, "o1": {15, 60, 7.5, 0}, "o1-mini": {1.1, 4.4, .55, 0}, "o3-mini": {1.1, 4.4, .55, 0}, "o4-mini": {1.1, 4.4, .55, 0},
	"composer-2.5": {3, 15, .3, 3.75}, "cursor-composer-2-5": {3, 15, .3, 3.75}, "grok-4-6": {2, 6, .5, 0}, "grok-4-5": {2, 6, .5, 0}, "gemini-3.1-pro": {1.25, 5, .3125, 0}, "gemini-3.7-flash": {.1, .4, .025, 0},
	"deepseek-v4-pro": {.435, .87, .003625, 0}, "deepseek-flash": {.14, .28, .0028, 0}, "deepseek-v4.1-flash": {.14, .28, .0028, 0}, "deepseek-v4-flash": {.14, .28, .0028, 0}, "deepseek-v4-flash-vision-exp": {.14, .28, .0028, 0},
	"deepseek-v3.2": {.28, .42, .028, 0}, "deepseek-v3.1": {.55, 1.67, .055, 0}, "deepseek-v3": {.28, 1.11, .028, 0}, "deepseek-chat": {.27, 1.10, .07, 0}, "deepseek-reasoner": {.55, 2.19, .14, 0},
}

var longContext = map[string]Price{
	"gpt-6-astra": {20, 75, 2, 25}, "gpt-6-sol": {4, 15, .4, 5}, "gpt-6-luna": {.2, .75, .02, .25},
	"gpt-5.6": {10, 45, 1, 12.5}, "gpt-5.6-sol": {10, 45, 1, 12.5}, "gpt-5.6-terra": {4, 18, .4, 5}, "gpt-5.6-luna": {.4, 1.8, .04, .5}, "gpt-5.5": {10, 45, 1, 0}, "gpt-5.5-codex": {10, 45, 1, 0},
}

var codexFast = map[string]Price{
	"gpt-6-astra": {20, 100, 2, 25}, "gpt-6-sol": {4, 20, .4, 5}, "gpt-6-luna": {.2, 1, .02, .25},
	"gpt-5.6": {10, 60, 1, 12.5}, "gpt-5.6-sol": {10, 60, 1, 12.5}, "gpt-5.6-terra": {4, 24, .4, 5}, "gpt-5.6-luna": {.4, 2.4, .04, .5},
	"gpt-5.5": {12.5, 75, 1.25, 0}, "gpt-5.5-codex": {12.5, 75, 1.25, 0}, "gpt-5.4": {5, 30, .5, 0}, "gpt-5.4-codex": {5, 30, .5, 0},
}

var claudeFast = map[string]Price{
	"claude-opus-5.5": {8, 40, .4, 10}, "claude-opus-5-5": {8, 40, .4, 10}, "claude-opus-5": {10, 50, 1, 12.5}, "claude-opus-4-8": {10, 50, 1, 12.5}, "claude-opus-4-7": {30, 150, 3, 37.5}, "claude-opus-4-6": {30, 150, 3, 37.5},
}

var datedModel = regexp.MustCompile(`-(?:\d{8}|\d{4}-\d{2}-\d{2})$`)

// NormalizeModel 移除提供商前缀和版本日期以匹配价格目录
func NormalizeModel(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	for {
		changed := false
		for _, prefix := range []string{"openai-codex/", "openai/", "anthropic/", "deepseek/", "opencode-go/", "commandcode/", "command-code/"} {
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
	return datedModel.ReplaceAllString(name, "")
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
	path    string
	client  *http.Client
}

// NewPricing 加载现有价格缓存并保留历史价格规则
func NewPricing(dataDir string) *Pricing {
	pricing := &Pricing{path: filepath.Join(dataDir, "pricing-catalog.json"), client: &http.Client{Timeout: 25 * time.Second}}
	if data, err := os.ReadFile(pricing.path); err == nil {
		_ = json.Unmarshal(data, &pricing.active)
	}
	pricing.active.Version = 2
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

// CostBreakdown 保留长上下文促销快速档位和缓存一小时的计费规则
func (pricing *Pricing) CostBreakdown(row model.UsageRow) *model.CostBreakdown {
	if row.ReportedCosts != nil {
		return row.ReportedCosts
	}
	if row.App == model.UsagePi || row.App == model.UsageOmp {
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
	long := row.Input+row.CacheRead+row.CacheWrite > 272000
	sol := name == "gpt-5.6" || name == "gpt-5.6-sol"
	promo := !at.Before(time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC))
	price, found := builtin[name]
	switch row.Speed {
	case "standard":
		if name == "deepseek-v4-pro" && !at.Before(time.Date(2026, 8, 16, 16, 0, 0, 0, time.UTC)) {
			price = Price{1.32, 3.96, .044, 0}
			found = true
		} else if name == "deepseek-flash" || name == "deepseek-v4-flash" || name == "deepseek-v4.1-flash" || name == "deepseek-v4-flash-vision-exp" {
			if !at.Before(time.Date(2026, 9, 10, 4, 0, 0, 0, time.UTC)) {
				price = Price{.30, 1.20, .006, 0}
			} else if !at.Before(time.Date(2026, 8, 16, 16, 0, 0, 0, time.UTC)) {
				price = Price{.44, 1.32, .014, 0}
			}
		} else if sol && promo {
			if long {
				price = Price{8, 30, .8, 10}
			} else {
				price = Price{4, 20, .4, 5}
			}
			found = true
		} else if long {
			if override, ok := longContext[name]; ok {
				price, found = override, true
			} else if online, ok := pricing.rate(name, row.App, row.Speed); ok {
				price, found = online, true
			}
		} else if _, fixed := longContext[name]; !fixed && name != "claude-sonnet-5" && name != "gpt-5.5-pro" {
			if online, ok := pricing.rate(name, row.App, row.Speed); ok {
				price, found = online, true
			}
		}
	case "fast":
		if row.App == model.UsageCodex {
			if long {
				return nil
			}
			price, found = codexFast[name]
			if sol && promo {
				price, found = Price{8, 40, .8, 10}, true
			}
		} else if row.App == model.UsageClaude {
			price, found = claudeFast[name]
		} else {
			return nil
		}
		fixed := name == "gpt-5.5" || name == "gpt-5.5-codex" || name == "claude-opus-4-7" || name == "claude-opus-4-6"
		if !fixed && !(row.App == model.UsageCodex && sol && promo) {
			if online, ok := pricing.rate(name, row.App, row.Speed); ok {
				price, found = online, true
			}
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
	if app == model.UsagePi || app == model.UsageOmp {
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
		case "gpt-6-astra", "gpt-6-sol", "gpt-6-luna", "gpt-5.6", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.5-codex":
			multiplier = 2.5
		case "gpt-5.4", "gpt-5.4-codex":
			multiplier = 2
		}
	} else if app == model.UsageClaude {
		switch name {
		case "claude-opus-5.5", "claude-opus-5-5", "claude-opus-5", "claude-opus-4-8":
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
			if strings.Contains(name, "/") || (provider != "anthropic" && provider != "openai" && provider != "deepseek") {
				continue
			}
			if has(entry, "input_cost_per_token") && has(entry, "output_cost_per_token") {
				standard = append(standard, rateCandidate{name, 0, Price{number(entry, "input_cost_per_token") * 1e6, number(entry, "output_cost_per_token") * 1e6, number(entry, "cache_read_input_token_cost") * 1e6, number(entry, "cache_creation_input_token_cost") * 1e6}})
			}
			if provider == "openai" && has(entry, "input_cost_per_token_priority") && has(entry, "output_cost_per_token_priority") {
				codex = append(codex, rateCandidate{name, 0, Price{number(entry, "input_cost_per_token_priority") * 1e6, number(entry, "output_cost_per_token_priority") * 1e6, number(entry, "cache_read_input_token_cost_priority") * 1e6, number(entry, "cache_creation_input_token_cost_priority") * 1e6}})
			}
		}
	} else {
		for priority, provider := range []string{"anthropic", "openai", "deepseek"} {
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
