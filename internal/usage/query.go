package usage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Mag1cFall/cc-bar/internal/model"
)

// Provider 按模型前缀与来源服务归并厂商
func Provider(app model.UsageApp, name string) string {
	name = strings.ToLower(name)
	if strings.HasPrefix(name, "google/") || strings.HasPrefix(name, "google-vertex/") || strings.HasPrefix(name, "gemini-") {
		return "Google"
	}
	if strings.HasPrefix(name, "xai/") || strings.HasPrefix(name, "grok-") {
		return "xAI"
	}
	for _, entry := range []struct{ prefix, provider string }{{"openai-codex/", "OpenAI"}, {"openai/", "OpenAI"}, {"anthropic/", "Anthropic"}, {"deepseek/", "DeepSeek"}, {"opencode-go/", "OpenCode-Go"}, {"commandcode/", "Command Code"}, {"command-code/", "Command Code"}, {"claude-", "Anthropic"}, {"gpt-", "OpenAI"}, {"o1", "OpenAI"}, {"o3", "OpenAI"}, {"o4", "OpenAI"}, {"chatgpt-", "OpenAI"}, {"codex-", "OpenAI"}, {"deepseek-", "DeepSeek"}} {
		if strings.HasPrefix(name, entry.prefix) {
			return entry.provider
		}
	}
	if app == model.UsageCodex {
		return "OpenAI"
	}
	if app == model.UsageClaude {
		return "Anthropic"
	}
	return "Other"
}

// bucketStart 按本机日期和周一边界对齐图表分桶
func bucketStart(value time.Time, grain string) time.Time {
	value = value.In(time.Local)
	day := time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.Local)
	switch grain {
	case "week":
		return day.AddDate(0, 0, -(int(day.Weekday())+6)%7)
	case "month":
		return time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, time.Local)
	default:
		return day
	}
}

// nextBucket 使用日历加减以保留月长度和夏令时边界
func nextBucket(value time.Time, grain string, steps int) time.Time {
	switch grain {
	case "week":
		return value.AddDate(0, 0, 7*steps)
	case "month":
		return value.AddDate(0, steps, 0)
	default:
		return value.AddDate(0, 0, steps)
	}
}

// sortedGroups 按费用和稳定标识排列聚合结果
func sortedGroups(groups map[string]*model.UsageGroup) []model.UsageGroup {
	result := make([]model.UsageGroup, 0, len(groups))
	for _, entry := range groups {
		if entry.Models == nil {
			entry.Models = []model.UsageGroup{}
		}
		result = append(result, *entry)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Totals.Cost != result[j].Totals.Cost {
			return result[i].Totals.Cost > result[j].Totals.Cost
		}
		return result[i].ID < result[j].ID
	})
	return result
}

// group 获取同一服务模型和档位的聚合容器
func group(groups map[string]*model.UsageGroup, id, name string, app *model.UsageApp, modelName, provider, speed string) *model.UsageGroup {
	if found := groups[id]; found != nil {
		return found
	}
	entry := &model.UsageGroup{ID: id, Name: name, App: app, Model: modelName, Provider: provider, Speed: speed, Models: []model.UsageGroup{}}
	groups[id] = entry
	return entry
}

// sourceFilter 在数据库查询前限制服务集合
func sourceFilter(prefix string, app *model.UsageApp, allowed map[model.UsageApp]bool) string {
	if app != nil {
		return fmt.Sprintf(" AND %sapp=%d", prefix, *app)
	}
	if allowed == nil {
		return ""
	}
	apps := []string{}
	for candidate := model.UsageCodex; candidate <= model.UsageOmp; candidate++ {
		if allowed[candidate] {
			apps = append(apps, fmt.Sprint(candidate))
		}
	}
	if len(apps) == 0 {
		return " AND 0=1"
	}
	return " AND " + prefix + "app IN (" + strings.Join(apps, ",") + ")"
}

// Overview 在数据库按日模型预聚合后生成环比与日周月图表
func (store *Store) Overview(ctx context.Context, query model.UsageQuery) (model.UsageOverview, error) {
	result := model.UsageOverview{Services: []model.UsageGroup{}, Models: []model.UsageGroup{}, Providers: []model.UsageGroup{}, Buckets: []model.UsageBucket{}}
	result.Fast.MultiplierText = "—"
	from, to, err := bounds(query.From, query.To)
	if err != nil {
		return result, err
	}
	grain := query.Grain
	if grain != "week" && grain != "month" {
		grain = "day"
	}
	previousFrom := from
	if query.Compare {
		previousFrom = from.Add(-to.Sub(from))
	}
	chartFrom := bucketStart(from, grain)
	if from.UnixMilli() <= 0 {
		minimumSQL := "SELECT MIN(time) FROM usage WHERE time<?" + sourceFilter("", query.Service, query.AllowedApps)
		var first sql.NullInt64
		if err = store.db.QueryRowContext(ctx, minimumSQL, to.UnixMilli()).Scan(&first); err != nil {
			return result, err
		}
		if first.Valid {
			chartFrom = bucketStart(time.UnixMilli(first.Int64), grain)
		} else {
			chartFrom = nextBucket(bucketStart(to.Add(-time.Nanosecond), grain), grain, -13)
		}
	}
	if chartFrom.Equal(bucketStart(to.Add(-time.Nanosecond), grain)) {
		chartFrom = nextBucket(chartFrom, grain, -13)
	}
	readFrom := from
	if previousFrom.Before(readFrom) {
		readFrom = previousFrom
	}
	if chartFrom.Before(readFrom) {
		readFrom = chartFrom
	}
	services := map[string]*model.UsageGroup{}
	models := map[string]*model.UsageGroup{}
	providers := map[string]*model.UsageGroup{}
	buckets := map[int64]*model.UsageBucket{}
	bucketGroups := map[int64]map[string]*model.UsageGroup{}
	for at := chartFrom; at.Before(to); at = nextBucket(at, grain, 1) {
		buckets[at.Unix()] = &model.UsageBucket{At: at, Services: []model.UsageGroup{}}
		bucketGroups[at.Unix()] = map[string]*model.UsageGroup{}
	}
	rows, err := store.db.QueryContext(ctx, `SELECT app,model,speed,ccbar_day(time),
 CASE WHEN time>=? THEN 1 WHEN time>=? THEN 2 ELSE 0 END AS period,`+totalsSQL+` FROM usage
	 WHERE time>=? AND time<?`+sourceFilter("", query.Service, query.AllowedApps)+`
	 GROUP BY app,model,speed,4,period`,
		from.UnixMilli(), previousFrom.UnixMilli(), readFrom.UnixMilli(), to.UnixMilli())
	if err != nil {
		return result, err
	}
	defer rows.Close()
	equivalent := 0.0
	minimumMultiplier, maximumMultiplier := 0.0, 0.0
	for rows.Next() {
		var app model.UsageApp
		var name, speed string
		var day int64
		var period int
		var totals model.UsageTotals
		if err = rows.Scan(&app, &name, &speed, &day, &period, &totals.Input, &totals.Output, &totals.CacheRead, &totals.CacheWrite, &totals.CacheWrite1h, &totals.Requests, &totals.Cost); err != nil {
			return result, err
		}
		totals.Add(model.UsageTotals{})
		at := time.UnixMilli(day)
		serviceKey := fmt.Sprint(app)
		provider := Provider(app, name)
		modelKey := fmt.Sprintf("%d:%s:%s", app, name, speed)
		if !at.Before(chartFrom) {
			key := bucketStart(at, grain).Unix()
			if bucket := buckets[key]; bucket != nil {
				bucket.Totals.Add(totals)
				group(bucketGroups[key], serviceKey, model.UsageName(app), &app, "", provider, "").Totals.Add(totals)
			}
		}
		if period == 2 && query.Compare {
			result.PreviousTotals.Add(totals)
			group(services, serviceKey, model.UsageName(app), &app, "", provider, "").PreviousTotals.Add(totals)
			group(models, modelKey, name, &app, name, provider, speed).PreviousTotals.Add(totals)
			group(providers, provider, provider, nil, "", provider, "").PreviousTotals.Add(totals)
			continue
		}
		if period != 1 {
			continue
		}
		result.Totals.Add(totals)
		group(services, serviceKey, model.UsageName(app), &app, "", provider, "").Totals.Add(totals)
		group(models, modelKey, name, &app, name, provider, speed).Totals.Add(totals)
		group(providers, provider, provider, nil, "", provider, "").Totals.Add(totals)
		if speed == "fast" {
			result.Fast.Totals.Add(totals)
			if multiplier := BillingEquivalentMultiplier(app, name); multiplier != nil {
				equivalent += float64(totals.Tokens) * *multiplier
				if minimumMultiplier == 0 || *multiplier < minimumMultiplier {
					minimumMultiplier = *multiplier
				}
				if *multiplier > maximumMultiplier {
					maximumMultiplier = *multiplier
				}
			} else {
				result.Fast.UnknownMultiplier = true
			}
		} else {
			result.Fast.StandardTotals.Add(totals)
		}
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	if result.Fast.Totals.Requests > 0 && !result.Fast.UnknownMultiplier {
		result.Fast.EquivalentTokens = &equivalent
		minimum := strconv.FormatFloat(minimumMultiplier, 'f', -1, 64)
		result.Fast.MultiplierText = minimum + "×"
		if minimumMultiplier != maximumMultiplier {
			maximum := strconv.FormatFloat(maximumMultiplier, 'f', -1, 64)
			result.Fast.MultiplierText = minimum + "–" + maximum + "×"
		}
	}
	result.Models = sortedGroups(models)
	for _, entry := range result.Models {
		if service := services[fmt.Sprint(*entry.App)]; service != nil {
			service.Models = append(service.Models, entry)
		}
		if provider := providers[entry.Provider]; provider != nil {
			provider.Models = append(provider.Models, entry)
		}
	}
	result.Services = sortedGroups(services)
	result.Providers = sortedGroups(providers)
	for at := chartFrom; at.Before(to); at = nextBucket(at, grain, 1) {
		bucket := buckets[at.Unix()]
		bucket.Services = sortedGroups(bucketGroups[at.Unix()])
		result.Buckets = append(result.Buckets, *bucket)
	}
	return result, nil
}

// conversationCTE 先汇总请求再读取每个对话的元数据
func conversationCTE(condition string) string {
	return `WITH usage_totals AS (SELECT u.conversation AS id,MIN(u.app) AS app,MIN(u.time) AS first,MAX(u.time) AS last,
 SUM(u.input) AS input,SUM(u.output) AS output,SUM(u.cache_read) AS cache_read,SUM(u.cache_write) AS cache_write,
 SUM(u.cache_write_1h) AS cache_write_1h,COUNT(*) AS requests,COALESCE(SUM(u.cost),0) AS cost,
 GROUP_CONCAT(DISTINCT u.model) AS models,SUM(CASE WHEN u.speed='fast' THEN 1 ELSE 0 END) AS fast
 FROM usage u WHERE u.time>=? AND u.time<?` + condition + ` GROUP BY u.conversation),
 summaries AS (SELECT totals.*,COALESCE(c.title,'') AS title,COALESCE(c.project,c.cwd,'') AS project,
 COALESCE(c.project_status,'') AS project_status,COALESCE(c.cwd,'') AS cwd,COALESCE(c.branch,'') AS branch,
 COALESCE(c.subtasks,0) AS subtasks,COALESCE(c.cache_write_avail,0) AS cache_write_avail
 FROM usage_totals totals LEFT JOIN conversation c ON c.id=totals.id) `
}

const summaryColumns = `id,title,project,project_status,cwd,branch,subtasks,cache_write_avail,app,first,last,input,output,cache_read,cache_write,cache_write_1h,requests,cost,models,fast`

// scanSummary 将数据库聚合映射为完整对话摘要
func scanSummary(scanner sqlScanner) (model.ConversationSummary, error) {
	var result model.ConversationSummary
	var first, last, fast int64
	var models string
	err := scanner.Scan(&result.ID, &result.Title, &result.Project, &result.ProjectStatus, &result.Cwd, &result.Branch, &result.Subtasks, &result.CacheWriteAvail, &result.App, &first, &last, &result.Totals.Input, &result.Totals.Output, &result.Totals.CacheRead, &result.Totals.CacheWrite, &result.Totals.CacheWrite1h, &result.Totals.Requests, &result.Totals.Cost, &models, &fast)
	result.StartedAt = time.UnixMilli(first)
	result.EndedAt = time.UnixMilli(last)
	result.Models = strings.Split(models, ",")
	result.Totals.Add(model.UsageTotals{})
	result.Speed = "standard"
	if fast == result.Totals.Requests && fast > 0 {
		result.Speed = "fast"
	} else if fast > 0 {
		result.Speed = "mixed"
	}
	return result, err
}

// Conversations 一次聚合后筛选项目并返回稳定分页
func (store *Store) Conversations(ctx context.Context, query model.ConversationQuery) (model.ConversationPage, error) {
	result := model.ConversationPage{Items: []model.ConversationSummary{}, Projects: []model.ProjectSummary{}}
	from, to, err := bounds(query.From, query.To)
	if err != nil {
		return result, err
	}
	cte := conversationCTE(sourceFilter("u.", query.App, query.AllowedApps))
	rows, err := store.db.QueryContext(ctx, cte+"SELECT "+summaryColumns+" FROM summaries", from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return result, err
	}
	defer rows.Close()
	search := strings.ToLower(strings.TrimSpace(query.Search))
	projects := map[string]*model.ProjectSummary{}
	items := []model.ConversationSummary{}
	for rows.Next() {
		item, itemErr := scanSummary(rows)
		if itemErr != nil {
			return result, itemErr
		}
		if !conversationMatches(item, search) {
			continue
		}
		project := projects[item.Project]
		if project == nil {
			name := filepath.Base(strings.TrimRight(item.Project, "/\\"))
			if item.Project == "" {
				name = "无明确项目"
			} else if item.ProjectStatus == "system" {
				name = "CCBar"
			}
			project = &model.ProjectSummary{ID: item.Project, Name: name, Status: item.ProjectStatus, LastAt: item.EndedAt}
			projects[item.Project] = project
		}
		project.Count++
		if item.EndedAt.After(project.LastAt) {
			project.LastAt = item.EndedAt
		}
		if query.Project == nil || item.Project == *query.Project {
			items = append(items, item)
		}
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	for _, project := range projects {
		result.Projects = append(result.Projects, *project)
	}
	sort.Slice(result.Projects, func(i, j int) bool {
		left, right := result.Projects[i], result.Projects[j]
		if !left.LastAt.Equal(right.LastAt) {
			return left.LastAt.After(right.LastAt)
		}
		return left.ID < right.ID
	})
	sort.SliceStable(items, func(i, j int) bool { return conversationLess(items[i], items[j], query.Sort) })
	result.Total = int64(len(items))
	limit := query.Limit
	if limit <= 0 {
		limit = 25
	}
	offset := min(max(query.Offset, 0), len(items))
	result.Items = append(result.Items, items[offset:min(offset+limit, len(items))]...)
	return result, nil
}

// conversationMatches 匹配标题项目模型和对话标识
func conversationMatches(item model.ConversationSummary, search string) bool {
	if search == "" {
		return true
	}
	for _, value := range []string{item.Title, item.Project, item.Cwd, strings.Join(item.Models, ","), item.ID} {
		if strings.Contains(strings.ToLower(value), search) {
			return true
		}
	}
	return false
}

// conversationLess 保持不同排序方式的稳定次序
func conversationLess(left, right model.ConversationSummary, order string) bool {
	switch order {
	case "tokens":
		if left.Totals.Tokens != right.Totals.Tokens {
			return left.Totals.Tokens > right.Totals.Tokens
		}
	case "cost":
		if left.Totals.Cost != right.Totals.Cost {
			return left.Totals.Cost > right.Totals.Cost
		}
	case "title":
		leftTitle, rightTitle := strings.ToLower(left.Title), strings.ToLower(right.Title)
		if leftTitle != rightTitle {
			return leftTitle < rightTitle
		}
		return left.ID < right.ID
	case "oldest":
		if !left.StartedAt.Equal(right.StartedAt) {
			return left.StartedAt.Before(right.StartedAt)
		}
		return left.ID < right.ID
	}
	if !left.EndedAt.Equal(right.EndedAt) {
		return left.EndedAt.After(right.EndedAt)
	}
	return left.ID < right.ID
}

// Detail 返回整个对话的原始请求及精确价格拆分
func (store *Store) Detail(ctx context.Context, id string) (model.ConversationDetail, error) {
	result := model.ConversationDetail{Events: []model.UsageRow{}, Models: []model.UsageGroup{}}
	row := store.db.QueryRowContext(ctx, conversationCTE(" AND u.conversation=?")+"SELECT "+summaryColumns+" FROM summaries", int64(0), int64(1<<62), id)
	var err error
	result.Conversation, err = scanSummary(row)
	if err != nil {
		return result, err
	}
	rows, err := store.db.QueryContext(ctx, "SELECT "+eventColumns+" FROM usage WHERE conversation=? ORDER BY time DESC,event_key", id)
	if err != nil {
		return result, err
	}
	result.Events, err = readRows(rows)
	if err != nil {
		return result, err
	}
	parts := map[string]*model.CostBreakdown{}
	priceRows, err := store.db.QueryContext(ctx, `SELECT u.event_key,b.value FROM usage u
 JOIN scan_blob b ON b.name='usage-cost:'||u.event_key WHERE u.conversation=?`, id)
	if err != nil {
		return result, err
	}
	for priceRows.Next() {
		var key, value string
		if err = priceRows.Scan(&key, &value); err != nil {
			priceRows.Close()
			return result, err
		}
		var part model.CostBreakdown
		if err = json.Unmarshal([]byte(value), &part); err != nil {
			priceRows.Close()
			return result, err
		}
		parts[key] = &part
	}
	err = priceRows.Err()
	priceRows.Close()
	if err != nil {
		return result, err
	}
	groups := map[string]*model.UsageGroup{}
	for index := range result.Events {
		event := &result.Events[index]
		event.ReportedCosts = parts[event.ID]
		key := fmt.Sprintf("%d:%s:%s", event.App, event.Model, event.Speed)
		group(groups, key, event.Model, &event.App, event.Model, Provider(event.App, event.Model), event.Speed).Totals.Add(event.Totals())
		if costs := store.pricing.CostBreakdown(*event); costs != nil {
			result.Costs.Input += costs.Input
			result.Costs.Output += costs.Output
			result.Costs.CacheRead += costs.CacheRead
			result.Costs.CacheWrite += costs.CacheWrite
		}
	}
	result.Models = sortedGroups(groups)
	return result, nil
}
