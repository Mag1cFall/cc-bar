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

// parseRawTimestamp 解析RFC3339字符串或毫秒数字时间戳
func parseRawTimestamp(raw json.RawMessage) time.Time {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return time.Now()
	}
	text := string(raw)
	if raw[0] == '"' {
		var decoded string
		if json.Unmarshal(raw, &decoded) != nil || decoded == "" {
			return time.Now()
		}
		if at, err := time.Parse(time.RFC3339Nano, decoded); err == nil {
			return at
		}
		text = strings.TrimSpace(decoded)
	}
	if ms, err := strconv.ParseInt(text, 10, 64); err == nil && ms > 0 {
		return time.UnixMilli(ms)
	}
	if f, err := strconv.ParseFloat(text, 64); err == nil && f > 0 {
		return time.UnixMilli(int64(f))
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

// contentTitleRaw 按需从原始JSON切片提取首条用户消息标题
func contentTitleRaw(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	if raw[0] == '"' {
		var text string
		if json.Unmarshal(raw, &text) == nil {
			return cleanTitle(text)
		}
		return ""
	}
	if raw[0] == '[' {
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &parts) == nil {
			for _, part := range parts {
				if part.Type == "text" {
					if title := cleanTitle(part.Text); title != "" {
						return title
					}
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
	IsSubagentPath                                                 bool
	AllChain                                                       *bool
	CacheSeen                                                      bool
	Titles                                                         map[string]string
	Candidates                                                     map[string][]projectCandidate
	Tiers                                                          map[string]string
	TiersLoaded                                                    bool
}

type projectCandidate struct {
	Path, Container string
	Sidechain       bool
}

var (
	utf8BOM             = []byte{0xef, 0xbb, 0xbf}
	tokTokenCount       = []byte(`"token_count"`)
	tokSessionMeta      = []byte(`"session_meta"`)
	tokTurnContext      = []byte(`"turn_context"`)
	tokThreadSettings   = []byte(`"thread_settings_applied"`)
	tokUserMessage      = []byte(`"user_message"`)
	tokAssistant        = []byte(`"assistant"`)
	tokUser             = []byte(`"user"`)
	tokUsage            = []byte(`"usage"`)
	tokToolResult1      = []byte(`"type":"tool_result"`)
	tokToolResult2      = []byte(`"type": "tool_result"`)
	tokTypeText1        = []byte(`"type":"text"`)
	tokTypeText2        = []byte(`"type": "text"`)
	tokSession          = []byte(`"session"`)
	tokTitle            = []byte(`"title"`)
	tokModelChange      = []byte(`"model_change"`)
	tokServiceTier      = []byte(`"service_tier_change"`)
	tokSessionInfo      = []byte(`"session_info"`)
	tokHarnessToolRes   = []byte(`"toolResult"`)
)

type codexTokenDetails struct {
	CacheWriteTokens *int64 `json:"cache_write_tokens"`
}

type codexTokenUsage struct {
	InputTokens           int64              `json:"input_tokens"`
	CachedInputTokens     int64              `json:"cached_input_tokens"`
	CacheWriteTokens      *int64             `json:"cache_write_tokens"`
	OutputTokens          int64              `json:"output_tokens"`
	ReasoningOutputTokens int64              `json:"reasoning_output_tokens"`
	TotalTokens           int64              `json:"total_tokens"`
	InputTokensDetails    *codexTokenDetails `json:"input_tokens_details"`
	PromptTokensDetails   *codexTokenDetails `json:"prompt_tokens_details"`
	TokenDetails          *codexTokenDetails `json:"token_details"`
}

type codexLine struct {
	Type      string          `json:"type"`
	Timestamp json.RawMessage `json:"timestamp"`
	Payload   struct {
		ID             string `json:"id"`
		SessionID      string `json:"session_id"`
		Cwd            string `json:"cwd"`
		Model          string `json:"model"`
		Type           string `json:"type"`
		Message        string `json:"message"`
		ThreadSettings *struct {
			Model       string `json:"model"`
			ServiceTier string `json:"service_tier"`
		} `json:"thread_settings"`
		Info *struct {
			LastTokenUsage  *codexTokenUsage `json:"last_token_usage"`
			TotalTokenUsage *codexTokenUsage `json:"total_token_usage"`
		} `json:"info"`
	} `json:"payload"`
}

// parseCodexLine 通过字节预检与轻量结构体解析Codex日志并跳过无关大行
func (store *Store) parseCodexLine(line []byte, path string, offset int64, state *scanState) *model.UsageRow {
	line = bytes.TrimPrefix(line, utf8BOM)
	if !bytes.Contains(line, tokTokenCount) &&
		!bytes.Contains(line, tokSessionMeta) &&
		!bytes.Contains(line, tokTurnContext) &&
		!bytes.Contains(line, tokThreadSettings) &&
		(state.Title != "" || !bytes.Contains(line, tokUserMessage)) {
		return nil
	}
	var rec codexLine
	if err := json.Unmarshal(line, &rec); err != nil {
		return nil
	}
	switch rec.Type {
	case "session_meta":
		meta := coalesce(rec.Payload.ID, rec.Payload.SessionID)
		if state.Session == "" {
			state.Session = meta
		}
		if meta != "" {
			state.Emitting = meta
		}
		if meta == "" || meta == state.Session {
			state.Cwd = coalesce(rec.Payload.Cwd, state.Cwd)
		}
	case "turn_context":
		state.Model = coalesce(rec.Payload.Model, state.Model)
	case "event_msg":
		switch rec.Payload.Type {
		case "user_message":
			if state.Title == "" {
				state.Title = cleanTitle(rec.Payload.Message)
			}
		case "thread_settings_applied":
			if settings := rec.Payload.ThreadSettings; settings != nil {
				state.Model = coalesce(settings.Model, state.Model)
				switch strings.ToLower(settings.ServiceTier) {
				case "priority", "fast":
					state.Speed = "fast"
				case "default":
					state.Speed = "standard"
				default:
					state.Speed = "unknown"
				}
			} else {
				state.Speed = "unknown"
			}
		case "token_count":
			if rec.Payload.Info == nil || rec.Payload.Info.LastTokenUsage == nil || state.Session == "" {
				return nil
			}
			usage := rec.Payload.Info.LastTokenUsage
			signature := ""
			if total := rec.Payload.Info.TotalTokenUsage; total != nil {
				var cw int64
				if total.CacheWriteTokens != nil {
					cw = *total.CacheWriteTokens
				}
				if total.InputTokens != 0 || total.CachedInputTokens != 0 || cw != 0 || total.OutputTokens != 0 || total.ReasoningOutputTokens != 0 || total.TotalTokens != 0 {
					var b strings.Builder
					b.Grow(48)
					b.WriteString(strconv.FormatInt(total.InputTokens, 10))
					b.WriteByte(':')
					b.WriteString(strconv.FormatInt(total.CachedInputTokens, 10))
					b.WriteByte(':')
					b.WriteString(strconv.FormatInt(cw, 10))
					b.WriteByte(':')
					b.WriteString(strconv.FormatInt(total.OutputTokens, 10))
					b.WriteByte(':')
					b.WriteString(strconv.FormatInt(total.ReasoningOutputTokens, 10))
					b.WriteByte(':')
					b.WriteString(strconv.FormatInt(total.TotalTokens, 10))
					signature = b.String()
				}
			}
			if signature != "" && signature == state.Signature {
				return nil
			}
			if signature != "" {
				state.Signature = signature
			}
			read := max(usage.CachedInputTokens, 0)
			var write int64
			if usage.CacheWriteTokens != nil {
				write = max(*usage.CacheWriteTokens, 0)
			} else {
				for _, detail := range []*codexTokenDetails{usage.InputTokensDetails, usage.PromptTokensDetails, usage.TokenDetails} {
					if detail != nil && detail.CacheWriteTokens != nil {
						write = max(*detail.CacheWriteTokens, 0)
						break
					}
				}
			}
			input := max(usage.InputTokens-read-write, 0)
			output := max(usage.OutputTokens, 0)
			if input+output+read+write == 0 {
				return nil
			}
			state.CacheSeen = state.CacheSeen || write > 0
			key := fmt.Sprintf("codex:%s:%d", path, offset)
			if signature != "" && state.Emitting != "" {
				key = "codex:" + state.Emitting + "#" + signature
			}
			row := model.UsageRow{ID: key, App: model.UsageCodex, Conversation: "codex:" + state.Session, Model: NormalizeModel(state.Model), Speed: state.Speed, Time: parseRawTimestamp(rec.Timestamp), Input: input, Output: output, CacheRead: read, CacheWrite: write, Requests: 1}
			row.Cost = store.pricing.Cost(row)
			return &row
		}
	}
	return nil
}

type claudeUsage struct {
	InputTokens              int64  `json:"input_tokens"`
	OutputTokens             int64  `json:"output_tokens"`
	CacheReadInputTokens     int64  `json:"cache_read_input_tokens"`
	CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens"`
	Speed                    string `json:"speed"`
	CacheCreation            *struct {
		Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
		Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
}

type claudeLine struct {
	Type        string          `json:"type"`
	IsSidechain bool            `json:"isSidechain"`
	SessionID   string          `json:"sessionId"`
	Cwd         string          `json:"cwd"`
	GitBranch   string          `json:"gitBranch"`
	Timestamp   json.RawMessage `json:"timestamp"`
	Message     *struct {
		ID         string          `json:"id"`
		Model      string          `json:"model"`
		StopReason string          `json:"stop_reason"`
		Content    json.RawMessage `json:"content"`
		Usage      *claudeUsage    `json:"usage"`
	} `json:"message"`
}

// parseClaudeLine 通过字节预检跳过非用量与巨型工具回传行并精确提取助手用量
func (store *Store) parseClaudeLine(line []byte, state *scanState) *model.UsageRow {
	line = bytes.TrimPrefix(line, utf8BOM)
	if !state.Subtasks && state.IsSubagentPath {
		state.Subtasks = true
	}
	hasAssistant := bytes.Contains(line, tokAssistant)
	hasUser := bytes.Contains(line, tokUser)
	if !hasAssistant && !hasUser {
		return nil
	}
	if !hasAssistant {
		if state.Title != "" && (bytes.Contains(line, tokToolResult1) || bytes.Contains(line, tokToolResult2)) && !bytes.Contains(line, tokTypeText1) && !bytes.Contains(line, tokTypeText2) {
			return nil
		}
	} else if !hasUser && !bytes.Contains(line, tokUsage) {
		return nil
	}
	var rec claudeLine
	if err := json.Unmarshal(line, &rec); err != nil {
		return nil
	}
	sidechain := rec.IsSidechain
	state.Subtasks = state.Subtasks || sidechain || state.IsSubagentPath
	session := rec.SessionID
	if rec.Type == "user" {
		if !sidechain && rec.Message != nil && session != "" && (state.Titles[session] == "" || state.Title == "") {
			title := contentTitleRaw(rec.Message.Content)
			if title != "" {
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
	if rec.Type != "assistant" || rec.Message == nil || rec.Message.Usage == nil {
		return nil
	}
	usage := rec.Message.Usage
	id := rec.Message.ID
	output := max(usage.OutputTokens, 0)
	if id == "" || session == "" || output == 0 {
		return nil
	}
	state.Session = session
	cwd := rec.Cwd
	if state.Cwd == "" || (state.AllChain != nil && *state.AllChain && !sidechain) {
		state.Cwd = cwd
	}
	all := sidechain
	if state.AllChain != nil {
		all = all && *state.AllChain
	}
	state.AllChain = &all
	state.Branch = coalesce(state.Branch, rec.GitBranch)
	if cwd != "" {
		state.Candidates[session] = append(state.Candidates[session], projectCandidate{Path: cwd, Sidechain: sidechain})
	}
	if rec.Message.StopReason == "" {
		return nil
	}
	var write1h, write int64
	if usage.CacheCreation != nil {
		write1h = max(usage.CacheCreation.Ephemeral1h, 0)
		write = max(usage.CacheCreation.Ephemeral5m, 0) + write1h
	}
	if usage.CacheCreationInputTokens != nil {
		write = max(*usage.CacheCreationInputTokens, 0)
		write1h = min(write1h, write)
	}
	speed := usage.Speed
	if speed != "fast" && speed != "standard" {
		speed = "unknown"
	}
	row := model.UsageRow{ID: "claude:" + id, App: model.UsageClaude, Conversation: "claude:" + session, Model: NormalizeModel(coalesce(rec.Message.Model, "unknown")), Speed: speed, Time: parseRawTimestamp(rec.Timestamp), Input: max(usage.InputTokens, 0), Output: output, CacheRead: max(usage.CacheReadInputTokens, 0), CacheWrite: write, CacheWrite1h: write1h, Requests: 1}
	row.Cost = store.pricing.Cost(row)
	return &row
}

type harnessCost struct {
	Input      *float64 `json:"input"`
	Output     *float64 `json:"output"`
	CacheRead  *float64 `json:"cacheRead"`
	CacheWrite *float64 `json:"cacheWrite"`
	Total      *float64 `json:"total"`
}

type harnessUsage struct {
	Input         int64        `json:"input"`
	Output        int64        `json:"output"`
	CacheRead     int64        `json:"cacheRead"`
	CacheWrite    int64        `json:"cacheWrite"`
	TotalTokens   int64        `json:"totalTokens"`
	Cost          *harnessCost `json:"cost"`
	Orchestration *struct {
		Input     int64 `json:"input"`
		Output    int64 `json:"output"`
		CacheRead int64 `json:"cacheRead"`
	} `json:"orchestration"`
}

type harnessMessage struct {
	Role      string          `json:"role"`
	Model     string          `json:"model"`
	Provider  string          `json:"provider"`
	Timestamp json.RawMessage `json:"timestamp"`
	Content   json.RawMessage `json:"content"`
	Usage     *harnessUsage   `json:"usage"`
}

type harnessLine struct {
	Type        string          `json:"type"`
	ID          string          `json:"id"`
	Title       string          `json:"title"`
	Name        string          `json:"name"`
	Cwd         string          `json:"cwd"`
	Model       string          `json:"model"`
	Provider    string          `json:"provider"`
	Timestamp   json.RawMessage `json:"timestamp"`
	ServiceTier json.RawMessage `json:"serviceTier"`
	Usage       *harnessUsage   `json:"usage"`
	Message     json.RawMessage `json:"message"`
}

func rawString(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '"' {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}

func rawInt64(raw json.RawMessage) int64 {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return 0
	}
	text := string(raw)
	if raw[0] == '"' {
		var decoded string
		if json.Unmarshal(raw, &decoded) != nil {
			return 0
		}
		text = strings.TrimSpace(decoded)
	}
	if ms, err := strconv.ParseInt(text, 10, 64); err == nil {
		return ms
	}
	if f, err := strconv.ParseFloat(text, 64); err == nil {
		return int64(f)
	}
	return 0
}

func (state *scanState) ensureTiers() map[string]string {
	if state.TiersLoaded {
		return state.Tiers
	}
	state.TiersLoaded = true
	if state.Signature == "" {
		return nil
	}
	var tiers map[string]string
	if json.Unmarshal([]byte(state.Signature), &tiers) == nil {
		state.Tiers = tiers
	}
	return state.Tiers
}

// parseHarnessLine 通过字节预检跳过Pi与OMP工具结果行并提取助手及后台用量
func (store *Store) parseHarnessLine(line []byte, state *scanState, app model.UsageApp) *model.UsageRow {
	line = bytes.TrimPrefix(line, utf8BOM)
	hasUsage := bytes.Contains(line, tokUsage)
	if !hasUsage &&
		!bytes.Contains(line, tokSession) &&
		!bytes.Contains(line, tokTitle) &&
		!bytes.Contains(line, tokModelChange) &&
		!bytes.Contains(line, tokServiceTier) &&
		!bytes.Contains(line, tokSessionInfo) &&
		(state.Title != "" || !bytes.Contains(line, tokUser)) {
		return nil
	}
	if !hasUsage && bytes.Contains(line, tokHarnessToolRes) &&
		!bytes.Contains(line, tokSession) &&
		!bytes.Contains(line, tokTitle) &&
		!bytes.Contains(line, tokModelChange) &&
		!bytes.Contains(line, tokServiceTier) &&
		!bytes.Contains(line, tokSessionInfo) {
		return nil
	}
	var rec harnessLine
	if err := json.Unmarshal(line, &rec); err != nil {
		return nil
	}
	kind := rec.Type
	if kind == "title" || kind == "title_change" {
		state.Title = coalesce(cleanTitle(rec.Title), state.Title)
		return nil
	}
	if kind == "session" {
		state.Session = coalesce(rec.ID, state.Session)
		state.Cwd = coalesce(rec.Cwd, state.Cwd)
		state.Title = coalesce(state.Title, cleanTitle(rec.Title))
		return nil
	}
	if kind == "session_info" {
		state.Title = coalesce(cleanTitle(rec.Name), state.Title)
		return nil
	}
	if kind == "model_change" {
		state.Model = coalesce(rec.Model, state.Model)
		return nil
	}
	if kind == "service_tier_change" {
		tiers := map[string]string{}
		rawTier := bytes.TrimSpace(rec.ServiceTier)
		if len(rawTier) > 0 && rawTier[0] == '{' {
			_ = json.Unmarshal(rawTier, &tiers)
		} else if tier := rawString(rawTier); tier != "" {
			for _, provider := range []string{"openai", "anthropic", "google"} {
				tiers[provider] = tier
			}
		}
		encoded, _ := json.Marshal(tiers)
		state.Signature = string(encoded)
		state.Tiers = tiers
		state.TiersLoaded = true
		return nil
	}
	if kind != "message" && kind != "model_usage" && kind != "compaction" && kind != "branch_summary" {
		return nil
	}
	tsStr := rawString(rec.Timestamp)
	if rec.ID == "" || tsStr == "" {
		return nil
	}
	usage := rec.Usage
	modelName, providerName := rec.Model, rec.Provider
	msgTsRaw := rec.Timestamp
	if kind == "message" {
		rawMsg := bytes.TrimSpace(rec.Message)
		if len(rawMsg) == 0 || rawMsg[0] != '{' {
			return nil
		}
		var msg harnessMessage
		if err := json.Unmarshal(rawMsg, &msg); err != nil {
			return nil
		}
		if msg.Role == "user" {
			if state.Title == "" {
				state.Title = contentTitleRaw(msg.Content)
			}
			return nil
		}
		if msg.Role != "assistant" {
			return nil
		}
		usage = msg.Usage
		modelName, providerName = msg.Model, msg.Provider
		msgTsRaw = msg.Timestamp
	}
	if usage == nil || state.Session == "" {
		return nil
	}
	if modelName != "" {
		state.Model = modelName
		if providerName != "" {
			state.Model = providerName + "/" + modelName
		}
	}
	prefix := "pi:"
	if app == model.UsageOmp {
		prefix = "omp:"
	}
	speed := "standard"
	if kind == "message" {
		if tiers := state.ensureTiers(); len(tiers) > 0 {
			provider := strings.ToLower(Provider(app, state.Model))
			switch tiers[provider] {
			case "priority", "fast":
				speed = "fast"
			case "flex", "scale", "ultrafast":
				speed = tiers[provider]
			}
		}
	}
	row := model.UsageRow{ID: prefix + rec.ID + "@" + tsStr, App: app, Conversation: prefix + state.Session, Model: state.Model, Speed: speed, Time: parseRawTimestamp(rec.Timestamp), Input: max(usage.Input, 0), Output: max(usage.Output, 0), CacheRead: max(usage.CacheRead, 0), CacheWrite: max(usage.CacheWrite, 0), Requests: 1}
	if app == model.UsageOmp && usage.Orchestration != nil {
		row.Input += max(usage.Orchestration.Input, 0)
		row.Output += max(usage.Orchestration.Output, 0)
		row.CacheRead += max(usage.Orchestration.CacheRead, 0)
	}
	if milliseconds := rawInt64(msgTsRaw); milliseconds > 0 {
		row.Time = time.UnixMilli(milliseconds)
	}
	cost := 0.0
	hasTotalCost := false
	if usage.Cost != nil && usage.Cost.Total != nil {
		cost = *usage.Cost.Total
		hasTotalCost = true
	}
	if app != model.UsageOmp && row.Input+row.Output+row.CacheRead+row.CacheWrite == 0 && usage.TotalTokens == 0 && cost <= 0 {
		return nil
	}
	if cost > 0 || (app == model.UsageOmp && hasTotalCost) {
		row.Cost = &cost
		if usage.Cost != nil && (usage.Cost.Input != nil || usage.Cost.Output != nil || usage.Cost.CacheRead != nil || usage.Cost.CacheWrite != nil) {
			part := &model.CostBreakdown{}
			if usage.Cost.Input != nil {
				part.Input = *usage.Cost.Input
			}
			if usage.Cost.Output != nil {
				part.Output = *usage.Cost.Output
			}
			if usage.Cost.CacheRead != nil {
				part.CacheRead = *usage.Cost.CacheRead
			}
			if usage.Cost.CacheWrite != nil {
				part.CacheWrite = *usage.Cost.CacheWrite
			}
			row.ReportedCosts = part
		}
	} else {
		row.Cost = store.pricing.Cost(row)
	}
	return &row
}
