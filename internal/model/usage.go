package model

import "time"

// UsageTotals 汇总完整的令牌类别和已知费用
type UsageTotals struct {
	Input        int64   `json:"input"`
	Output       int64   `json:"output"`
	CacheRead    int64   `json:"cacheRead"`
	CacheWrite   int64   `json:"cacheWrite"`
	CacheWrite1h int64   `json:"cacheWrite1h"`
	Requests     int64   `json:"requests"`
	Cost         float64 `json:"cost"`
	Tokens       int64   `json:"tokens"`
	CacheHitRate float64 `json:"cacheHitRate"`
}

// Add 累加并更新总令牌与缓存命中率
func (totals *UsageTotals) Add(value UsageTotals) {
	totals.Input += value.Input
	totals.Output += value.Output
	totals.CacheRead += value.CacheRead
	totals.CacheWrite += value.CacheWrite
	totals.CacheWrite1h += value.CacheWrite1h
	totals.Requests += value.Requests
	totals.Cost += value.Cost
	totals.Tokens = totals.Input + totals.Output + totals.CacheRead + totals.CacheWrite
	if denominator := totals.Input + totals.CacheRead + totals.CacheWrite; denominator > 0 {
		totals.CacheHitRate = 100 * float64(totals.CacheRead) / float64(denominator)
	}
}

// UsageRow 保存一次请求及其发生时的精确计费上下文
type UsageRow struct {
	ID            string         `json:"id"`
	App           UsageApp       `json:"app"`
	Conversation  string         `json:"conversation"`
	Model         string         `json:"model"`
	Speed         string         `json:"speed"`
	Time          time.Time      `json:"time"`
	Input         int64          `json:"input"`
	Output        int64          `json:"output"`
	CacheRead     int64          `json:"cacheRead"`
	CacheWrite    int64          `json:"cacheWrite"`
	CacheWrite1h  int64          `json:"cacheWrite1h"`
	Requests      int64          `json:"requests"`
	Cost          *float64       `json:"cost"`
	ReportedCosts *CostBreakdown `json:"reportedCosts,omitempty"`
}

// Totals 返回事件的完整统计
func (row UsageRow) Totals() UsageTotals {
	result := UsageTotals{Input: row.Input, Output: row.Output, CacheRead: row.CacheRead, CacheWrite: row.CacheWrite, CacheWrite1h: row.CacheWrite1h, Requests: row.Requests}
	if row.Cost != nil {
		result.Cost = *row.Cost
	}
	result.Add(UsageTotals{})
	return result
}

// UsageQuery 指定统计时间范围与图表粒度
type UsageQuery struct {
	AllowedApps map[UsageApp]bool `json:"-"`
	Service     *UsageApp         `json:"service"`
	Grain       string            `json:"grain"`
	From        string            `json:"from"`
	To          string            `json:"to"`
	Compare     bool              `json:"compare"`
}

// UsageGroup 表示服务或提供商中的模型档位归并
type UsageGroup struct {
	ID             string       `json:"id"`
	Name           string       `json:"name"`
	App            *UsageApp    `json:"app,omitempty"`
	Model          string       `json:"model,omitempty"`
	Provider       string       `json:"provider,omitempty"`
	Speed          string       `json:"speed,omitempty"`
	Totals         UsageTotals  `json:"totals"`
	PreviousTotals UsageTotals  `json:"previousTotals"`
	Models         []UsageGroup `json:"models"`
}

// UsageBucket 保存一个日周月分桶及服务拆分
type UsageBucket struct {
	At       time.Time    `json:"at"`
	Totals   UsageTotals  `json:"totals"`
	Services []UsageGroup `json:"services"`
}

// FastUsageSummary 保存快速档位的原始及额度等效令牌
type FastUsageSummary struct {
	Totals            UsageTotals `json:"totals"`
	StandardTotals    UsageTotals `json:"standardTotals"`
	EquivalentTokens  *float64    `json:"equivalentTokens"`
	UnknownMultiplier bool        `json:"unknownMultiplier"`
	MultiplierText    string      `json:"multiplierText"`
}

// UsageOverview 提供总览环比及图表所需的全部数据
type UsageOverview struct {
	Totals         UsageTotals      `json:"totals"`
	PreviousTotals UsageTotals      `json:"previousTotals"`
	Services       []UsageGroup     `json:"services"`
	Models         []UsageGroup     `json:"models"`
	Providers      []UsageGroup     `json:"providers"`
	Buckets        []UsageBucket    `json:"buckets"`
	Fast           FastUsageSummary `json:"fast"`
}

// ConversationQuery 提供全局搜索项目筛选和稳定分页
type ConversationQuery struct {
	AllowedApps map[UsageApp]bool `json:"-"`
	App         *UsageApp         `json:"app"`
	From        string            `json:"from"`
	To          string            `json:"to"`
	Search      string            `json:"search"`
	Project     *string           `json:"project"`
	Sort        string            `json:"sort"`
	Offset      int               `json:"offset"`
	Limit       int               `json:"limit"`
}

// ConversationSummary 描述一个对话的完整聚合与项目位置
type ConversationSummary struct {
	ID              string      `json:"id"`
	Title           string      `json:"title"`
	App             UsageApp    `json:"app"`
	Project         string      `json:"project"`
	ProjectStatus   string      `json:"projectStatus"`
	Cwd             string      `json:"cwd"`
	Branch          string      `json:"branch"`
	Subtasks        bool        `json:"subtasks"`
	CacheWriteAvail bool        `json:"cacheWriteAvail"`
	StartedAt       time.Time   `json:"startedAt"`
	EndedAt         time.Time   `json:"endedAt"`
	Models          []string    `json:"models"`
	Speed           string      `json:"speed"`
	Totals          UsageTotals `json:"totals"`
}

// ProjectSummary 保存项目筛选所需名称与数量
type ProjectSummary struct {
	ID     string    `json:"id"`
	Name   string    `json:"name"`
	Status string    `json:"status"`
	Count  int64     `json:"count"`
	LastAt time.Time `json:"lastAt"`
}

// ConversationPage 返回分页后的对话及全局项目清单
type ConversationPage struct {
	Items    []ConversationSummary `json:"items"`
	Total    int64                 `json:"total"`
	Projects []ProjectSummary      `json:"projects"`
}

// CostBreakdown 按请求价格拆分四类费用
type CostBreakdown struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

// ConversationDetail 保存完整事件模型拆分及费用明细
type ConversationDetail struct {
	Conversation ConversationSummary `json:"conversation"`
	Events       []UsageRow          `json:"events"`
	Models       []UsageGroup        `json:"models"`
	Costs        CostBreakdown       `json:"costs"`
}

// UsageStatus 保存扫描进度与远端查询结果
type UsageStatus struct {
	Revision       uint64     `json:"revision"`
	IsScanning     bool       `json:"isScanning"`
	FilesCompleted int        `json:"filesCompleted"`
	FilesTotal     int        `json:"filesTotal"`
	LastScanAt     *time.Time `json:"lastScanAt"`
	Error          string     `json:"error"`
	CursorError    string     `json:"cursorError"`
}

// UsageName 返回统计服务的显示名称
func UsageName(app UsageApp) string {
	names := [...]string{"Codex", "Claude Code", "Cursor", "Pi", "OpenCode", "DSH", "Oh My Pi"}
	if app < UsageCodex || app > UsageOmp {
		return "Unknown"
	}
	return names[app]
}
