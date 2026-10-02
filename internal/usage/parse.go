package usage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Mag1cFall/cc-bar/internal/model"
)

type node map[string]any

// jsonDecoder 保留JSON整数精度
func jsonDecoder(data []byte) *json.Decoder {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return decoder
}

// parse 读取单条JSON日志并识别UTF8标记
func parse(data []byte) (node, error) {
	var value node
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})))
	decoder.UseNumber()
	err := decoder.Decode(&value)
	return value, err
}

// object 获取JSON对象
func object(value any) node {
	if result, ok := value.(map[string]any); ok {
		return result
	}
	if result, ok := value.(node); ok {
		return result
	}
	return nil
}
func obj(value node, key string) node {
	return object(value[key])
}

func str(value node, key string) string {
	result, _ := value[key].(string)
	return result
}

func has(value node, key string) bool {
	result, found := value[key]
	return found && result != nil
}

func flag(value node, key string) bool {
	result, _ := value[key].(bool)
	return result
}

// number 读取数字与远端接口使用的数字字符串
func number(value node, key string) float64 {
	switch result := value[key].(type) {
	case json.Number:
		v, _ := result.Float64()
		return v
	case float64:
		return result
	case string:
		v, _ := strconv.ParseFloat(result, 64)
		return v
	}
	return 0
}

// integer 优先使用整数解析以保留毫秒和令牌计数
func integer(value node, key string) int64 {
	if result, ok := value[key].(json.Number); ok {
		if value, err := result.Int64(); err == nil {
			return value
		}
	}
	return int64(number(value, key))
}
func nonnegative(value node, key string) int64 { return max(integer(value, key), 0) }
func coalesce(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// occurred 读取日志的ISO时间或毫秒时间
func occurred(value node) time.Time {
	if timestamp := str(value, "timestamp"); timestamp != "" {
		if at, err := time.Parse(time.RFC3339Nano, timestamp); err == nil {
			return at
		}
	}
	if milliseconds := integer(value, "timestamp"); milliseconds > 0 {
		return time.UnixMilli(milliseconds)
	}
	return time.Now()
}

// cleanTitle 清理对话标题并过滤日志中的标签内容
func cleanTitle(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if strings.HasPrefix(value, "<") {
		return ""
	}
	characters := []rune(value)
	if len(characters) > 80 {
		characters = characters[:80]
	}
	return string(characters)
}

// contentTitle 从用户消息的文本段提取对话标题
func contentTitle(value any) string {
	if text, ok := value.(string); ok {
		return cleanTitle(text)
	}
	if parts, ok := value.([]any); ok {
		for _, raw := range parts {
			part := object(raw)
			if str(part, "type") == "text" {
				if title := cleanTitle(str(part, "text")); title != "" {
					return title
				}
			}
		}
	}
	return ""
}

type scanState struct {
	Length, Modified, Offset                                       int64
	Model, Speed, Signature, Session, Emitting, Title, Cwd, Branch string
	Subtasks                                                       bool
	AllChain                                                       *bool
	CacheSeen                                                      bool
	Titles                                                         map[string]string
	Candidates                                                     map[string][]projectCandidate
}

type projectCandidate struct {
	Path, Container string
	Sidechain       bool
}

// parseCodex 使用累计签名去重并拆开包含缓存的输入令牌
func (store *Store) parseCodex(root node, path string, offset int64, state *scanState) *model.UsageRow {
	payload := obj(root, "payload")
	switch str(root, "type") {
	case "session_meta":
		meta := coalesce(str(payload, "id"), str(payload, "session_id"))
		if state.Session == "" {
			state.Session = meta
		}
		if meta != "" {
			state.Emitting = meta
		}
		if meta == "" || meta == state.Session {
			state.Cwd = coalesce(str(payload, "cwd"), state.Cwd)
		}
	case "turn_context":
		state.Model = coalesce(str(payload, "model"), state.Model)
	case "event_msg":
		switch str(payload, "type") {
		case "user_message":
			if state.Title == "" {
				state.Title = cleanTitle(str(payload, "message"))
			}
		case "thread_settings_applied":
			settings := obj(payload, "thread_settings")
			state.Model = coalesce(str(settings, "model"), state.Model)
			switch strings.ToLower(str(settings, "service_tier")) {
			case "priority", "fast":
				state.Speed = "fast"
			case "default":
				state.Speed = "standard"
			default:
				state.Speed = "unknown"
			}
		case "token_count":
			info := obj(payload, "info")
			usage := obj(info, "last_token_usage")
			if usage == nil || state.Session == "" {
				return nil
			}
			total := obj(info, "total_token_usage")
			signature := ""
			values := []string{}
			nonzero := false
			for _, key := range []string{"input_tokens", "cached_input_tokens", "cache_write_tokens", "output_tokens", "reasoning_output_tokens", "total_tokens"} {
				value := integer(total, key)
				values = append(values, strconv.FormatInt(value, 10))
				nonzero = nonzero || value != 0
			}
			if nonzero {
				signature = strings.Join(values, ":")
			}
			if signature != "" && signature == state.Signature {
				return nil
			}
			if signature != "" {
				state.Signature = signature
			}
			read := nonnegative(usage, "cached_input_tokens")
			write := nonnegative(usage, "cache_write_tokens")
			if !has(usage, "cache_write_tokens") {
				for _, key := range []string{"input_tokens_details", "prompt_tokens_details", "token_details"} {
					detail := obj(usage, key)
					if has(detail, "cache_write_tokens") {
						write = nonnegative(detail, "cache_write_tokens")
						break
					}
				}
			}
			input := max(integer(usage, "input_tokens")-read-write, 0)
			output := nonnegative(usage, "output_tokens")
			if input+output+read+write == 0 {
				return nil
			}
			state.CacheSeen = state.CacheSeen || write > 0
			key := fmt.Sprintf("codex:%s:%d", path, offset)
			if signature != "" && state.Emitting != "" {
				key = "codex:" + state.Emitting + "#" + signature
			}
			row := model.UsageRow{ID: key, App: model.UsageCodex, Conversation: "codex:" + state.Session, Model: NormalizeModel(state.Model), Speed: state.Speed, Time: occurred(root), Input: input, Output: output, CacheRead: read, CacheWrite: write, Requests: 1}
			row.Cost = store.pricing.Cost(row)
			return &row
		}
	}
	return nil
}

// parseClaude 只记录完整助手请求并保留子任务和缓存TTL
func (store *Store) parseClaude(root node, path string, state *scanState) *model.UsageRow {
	sidechain := flag(root, "isSidechain")
	state.Subtasks = state.Subtasks || sidechain || strings.Contains(strings.ReplaceAll(path, "\\", "/"), "/subagents/")
	message := obj(root, "message")
	session := str(root, "sessionId")
	if str(root, "type") == "user" {
		if !sidechain {
			title := contentTitle(message["content"])
			if title != "" && session != "" {
				if state.Titles[session] == "" {
					state.Titles[session] = title
				}
				if state.Title == "" {
					state.Title = title
				}
			}
		}
		return nil
	}
	if str(root, "type") != "assistant" {
		return nil
	}
	usage := obj(message, "usage")
	id := str(message, "id")
	output := nonnegative(usage, "output_tokens")
	if id == "" || session == "" || usage == nil || output == 0 {
		return nil
	}
	state.Session = session
	cwd := str(root, "cwd")
	if state.Cwd == "" || (state.AllChain != nil && *state.AllChain && !sidechain) {
		state.Cwd = cwd
	}
	all := sidechain
	if state.AllChain != nil {
		all = all && *state.AllChain
	}
	state.AllChain = &all
	state.Branch = coalesce(state.Branch, str(root, "gitBranch"))
	if cwd != "" {
		state.Candidates[session] = append(state.Candidates[session], projectCandidate{Path: cwd, Sidechain: sidechain})
	}
	if str(message, "stop_reason") == "" {
		return nil
	}
	detail := obj(usage, "cache_creation")
	write1h := nonnegative(detail, "ephemeral_1h_input_tokens")
	write := nonnegative(detail, "ephemeral_5m_input_tokens") + write1h
	if has(usage, "cache_creation_input_tokens") {
		write = nonnegative(usage, "cache_creation_input_tokens")
		write1h = min(write1h, write)
	}
	speed := str(usage, "speed")
	if speed != "fast" && speed != "standard" {
		speed = "unknown"
	}
	row := model.UsageRow{ID: "claude:" + id, App: model.UsageClaude, Conversation: "claude:" + session, Model: NormalizeModel(coalesce(str(message, "model"), "unknown")), Speed: speed, Time: occurred(root), Input: nonnegative(usage, "input_tokens"), Output: output, CacheRead: nonnegative(usage, "cache_read_input_tokens"), CacheWrite: write, CacheWrite1h: write1h, Requests: 1}
	row.Cost = store.pricing.Cost(row)
	return &row
}

// parseHarness 读取Pi与OMP的助手后台模型用量及会话元数据
func (store *Store) parseHarness(root node, state *scanState, app model.UsageApp) *model.UsageRow {
	kind := str(root, "type")
	if kind == "title" || kind == "title_change" {
		state.Title = coalesce(cleanTitle(str(root, "title")), state.Title)
		return nil
	}
	if kind == "session" {
		state.Session = coalesce(str(root, "id"), state.Session)
		state.Cwd = coalesce(str(root, "cwd"), state.Cwd)
		state.Title = coalesce(state.Title, cleanTitle(str(root, "title")))
		return nil
	}
	if kind == "session_info" {
		state.Title = coalesce(cleanTitle(str(root, "name")), state.Title)
		return nil
	}
	if kind == "model_change" {
		state.Model = coalesce(str(root, "model"), state.Model)
		return nil
	}
	if kind == "service_tier_change" {
		tiers := obj(root, "serviceTier")
		if tiers == nil {
			tiers = node{}
			if tier := str(root, "serviceTier"); tier != "" {
				for _, provider := range []string{"openai", "anthropic", "google"} {
					tiers[provider] = tier
				}
			}
		}
		encoded, _ := json.Marshal(tiers)
		state.Signature = string(encoded)
		return nil
	}
	if kind != "message" && kind != "model_usage" && kind != "compaction" && kind != "branch_summary" {
		return nil
	}
	if str(root, "id") == "" || str(root, "timestamp") == "" {
		return nil
	}
	message := obj(root, "message")
	if kind == "message" && str(message, "role") == "user" {
		if state.Title == "" {
			state.Title = contentTitle(message["content"])
		}
		return nil
	}
	if kind == "message" && str(message, "role") != "assistant" {
		return nil
	}
	usage := obj(root, "usage")
	if kind == "message" {
		usage = obj(message, "usage")
	}
	if usage == nil || state.Session == "" {
		return nil
	}
	if kind != "message" {
		message = root
	}
	if name := str(message, "model"); name != "" {
		state.Model = name
		if provider := str(message, "provider"); provider != "" {
			state.Model = provider + "/" + name
		}
	}
	prefix := "pi:"
	if app == model.UsageOmp {
		prefix = "omp:"
	}
	speed := "standard"
	if tiers, err := parse([]byte(state.Signature)); err == nil && kind == "message" {
		provider := strings.ToLower(Provider(app, state.Model))
		switch str(tiers, provider) {
		case "priority", "fast":
			speed = "fast"
		case "flex", "scale", "ultrafast":
			speed = str(tiers, provider)
		}
	}
	row := model.UsageRow{ID: prefix + str(root, "id") + "@" + str(root, "timestamp"), App: app, Conversation: prefix + state.Session, Model: state.Model, Speed: speed, Time: occurred(root), Input: nonnegative(usage, "input"), Output: nonnegative(usage, "output"), CacheRead: nonnegative(usage, "cacheRead"), CacheWrite: nonnegative(usage, "cacheWrite"), Requests: 1}
	if app == model.UsageOmp {
		orchestration := obj(usage, "orchestration")
		row.Input += nonnegative(orchestration, "input")
		row.Output += nonnegative(orchestration, "output")
		row.CacheRead += nonnegative(orchestration, "cacheRead")
	}
	if milliseconds := integer(message, "timestamp"); milliseconds > 0 {
		row.Time = time.UnixMilli(milliseconds)
	}
	cost := number(obj(usage, "cost"), "total")
	if app != model.UsageOmp && row.Input+row.Output+row.CacheRead+row.CacheWrite == 0 && integer(usage, "totalTokens") == 0 && cost <= 0 {
		return nil
	}
	if cost > 0 || (app == model.UsageOmp && has(obj(usage, "cost"), "total")) {
		row.Cost = &cost
		parts := obj(usage, "cost")
		if has(parts, "input") || has(parts, "output") || has(parts, "cacheRead") || has(parts, "cacheWrite") {
			row.ReportedCosts = &model.CostBreakdown{
				Input: number(parts, "input"), Output: number(parts, "output"),
				CacheRead: number(parts, "cacheRead"), CacheWrite: number(parts, "cacheWrite"),
			}
		}
	} else {
		row.Cost = store.pricing.Cost(row)
	}
	return &row
}
