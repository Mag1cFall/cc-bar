package usage

import (
	"context"
	"database/sql"
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
	if strings.HasPrefix(name, "google/") || strings.HasPrefix(name, "google-vertex/") || strings.HasPrefix(name, "gemini/") || strings.HasPrefix(name, "gemini-") {
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
	for candidate := model.UsageCodex; candidate <= model.UsageAntigravity; candidate++ {
		if allowed[candidate] {
			apps = append(apps, fmt.Sprint(candidate))
		}
	}
	if len(apps) == 0 {
		return " AND 0=1"
	}
	return " AND " + prefix + "app IN (" + strings.Join(apps, ",") + ")"
}

// rawAppFilter 确保原始usage表时间范围查询始终命中usage_app_time复合索引
func rawAppFilter(prefix string, app *model.UsageApp, allowed map[model.UsageApp]bool) string {
	if filter := sourceFilter(prefix, app, allowed); filter != "" {
		return filter
	}
	return " AND " + prefix + "app IN (0,1,2,3,4,5,6,7)"
}

// PopoverCosts 单次扫描预聚合桶返回各服务今日与本周费用
func (store *Store) PopoverCosts(ctx context.Context, allowed map[model.UsageApp]bool, todayFrom, weekFrom, to time.Time) (map[model.UsageApp]float64, map[model.UsageApp]float64, error) {
	todayCosts := map[model.UsageApp]float64{}
	weekCosts := map[model.UsageApp]float64{}
	start := weekFrom
	if todayFrom.Before(start) {
		start = todayFrom
	}
	if !start.Before(to) {
		return todayCosts, weekCosts, nil
	}
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	todayMs := todayFrom.UnixMilli()
	weekMs := weekFrom.UnixMilli()
	addRange := func(fromMs, toMs int64, inToday, inWeek bool) error {
		if fromMs >= toMs || (!inToday && !inWeek) {
			return nil
		}
		left, buckets, right := splitRollupRange(fromMs, toMs)
		if buckets[0] < buckets[1] {
			rows, err := tx.QueryContext(ctx, "SELECT app,COALESCE(SUM(cost),0) FROM usage_rollup WHERE bucket>=? AND bucket<?"+sourceFilter("", nil, allowed)+" GROUP BY app", buckets[0], buckets[1])
			if err != nil {
				return err
			}
			for rows.Next() {
				var app model.UsageApp
				var cost float64
				if err = rows.Scan(&app, &cost); err != nil {
					rows.Close()
					return err
				}
				if inToday {
					todayCosts[app] += cost
				}
				if inWeek {
					weekCosts[app] += cost
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
		}
		rawFilter := rawAppFilter("", nil, allowed)
		for _, edge := range [2][2]int64{left, right} {
			if edge[0] >= edge[1] {
				continue
			}
			rows, err := tx.QueryContext(ctx, "SELECT app,COALESCE(SUM(cost),0) FROM usage WHERE time>=? AND time<?"+rawFilter+" GROUP BY app", edge[0], edge[1])
			if err != nil {
				return err
			}
			for rows.Next() {
				var app model.UsageApp
				var cost float64
				if err = rows.Scan(&app, &cost); err != nil {
					rows.Close()
					return err
				}
				if inToday {
					todayCosts[app] += cost
				}
				if inWeek {
					weekCosts[app] += cost
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
		}
		return nil
	}
	toMs := to.UnixMilli()
	if weekMs < todayMs {
		if err := addRange(weekMs, min(todayMs, toMs), false, true); err != nil {
			return nil, nil, err
		}
		if err := addRange(todayMs, toMs, true, true); err != nil {
			return nil, nil, err
		}
	} else {
		if err := addRange(todayMs, min(weekMs, toMs), true, false); err != nil {
			return nil, nil, err
		}
		if err := addRange(weekMs, toMs, true, true); err != nil {
			return nil, nil, err
		}
	}
	return todayCosts, weekCosts, nil
}

// Overview 通过15分钟预聚合桶与索引边缘生成环比与日周月图表
func (store *Store) Overview(ctx context.Context, query model.UsageQuery) (model.UsageOverview, error) {
	result := model.UsageOverview{Services: []model.UsageGroup{}, Models: []model.UsageGroup{}, Providers: []model.UsageGroup{}, Buckets: []model.UsageBucket{}}
	result.Fast.MultiplierText = "—"
	from, to, err := bounds(query.From, query.To)
	if err != nil {
		return result, err
	}
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
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
		minimumSQL := "SELECT MIN(time) FROM usage WHERE time<?" + rawAppFilter("", query.Service, query.AllowedApps)
		var first sql.NullInt64
		if err = tx.QueryRowContext(ctx, minimumSQL, to.UnixMilli()).Scan(&first); err != nil {
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

	equivalent := 0.0
	minimumMultiplier, maximumMultiplier := 0.0, 0.0
	bucketDayCache := map[int64]time.Time{}
	dayForBucket := func(b int64) time.Time {
		if d, ok := bucketDayCache[b]; ok {
			return d
		}
		d := bucketStart(time.UnixMilli(b*rollupBucketMs), "day")
		bucketDayCache[b] = d
		return d
	}

	applyRow := func(app model.UsageApp, name, speed string, at time.Time, period int, totals model.UsageTotals) {
		totals.Add(model.UsageTotals{})
		serviceKey := strconv.Itoa(int(app))
		provider := Provider(app, name)
		modelKey := serviceKey + ":" + name + ":" + speed
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
			return
		}
		if period != 1 {
			return
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

	type segment struct {
		fromMs, toMs int64
		period       int
	}
	segments := make([]segment, 0, 3)
	readMs, prevMs, fromMs, toMs := readFrom.UnixMilli(), previousFrom.UnixMilli(), from.UnixMilli(), to.UnixMilli()
	if query.Compare && prevMs < fromMs {
		if readMs < prevMs {
			segments = append(segments, segment{readMs, prevMs, 0})
		}
		segments = append(segments, segment{prevMs, fromMs, 2})
	} else if readMs < fromMs {
		segments = append(segments, segment{readMs, fromMs, 0})
	}
	segments = append(segments, segment{fromMs, toMs, 1})

	rollupFilter := sourceFilter("", query.Service, query.AllowedApps)
	rawFilter := rawAppFilter("", query.Service, query.AllowedApps)
	modelNames, speedNames := store.dictSnapshot()

	for _, seg := range segments {
		if seg.fromMs >= seg.toMs {
			continue
		}
		left, full, right := splitRollupRange(seg.fromMs, seg.toMs)
		if full[0] < full[1] {
			rows, queryErr := tx.QueryContext(ctx, `SELECT app,model_id,speed_id,bucket,input,output,cache_read,cache_write,cache_write_1h,requests,cost
 FROM usage_rollup WHERE bucket>=? AND bucket<?`+rollupFilter, full[0], full[1])
			if queryErr != nil {
				return result, queryErr
			}
			for rows.Next() {
				var app model.UsageApp
				var modelID, speedID, bucket int64
				var totals model.UsageTotals
				if err = rows.Scan(&app, &modelID, &speedID, &bucket, &totals.Input, &totals.Output, &totals.CacheRead, &totals.CacheWrite, &totals.CacheWrite1h, &totals.Requests, &totals.Cost); err != nil {
					rows.Close()
					return result, err
				}
				modelName, speedName := store.resolveDict(ctx, tx, modelID, speedID, modelNames, speedNames)
				applyRow(app, modelName, speedName, dayForBucket(bucket), seg.period, totals)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return result, err
			}
		}
		for _, edge := range [2][2]int64{left, right} {
			if edge[0] >= edge[1] {
				continue
			}
			rows, queryErr := tx.QueryContext(ctx, `SELECT app,model_id,speed_id,time,input,output,cache_read,cache_write,cache_write_1h,COALESCE(cost,0)
 FROM usage WHERE time>=? AND time<?`+rawFilter, edge[0], edge[1])
			if queryErr != nil {
				return result, queryErr
			}
			for rows.Next() {
				var app model.UsageApp
				var modelID, speedID, atMs int64
				var totals model.UsageTotals
				totals.Requests = 1
				if err = rows.Scan(&app, &modelID, &speedID, &atMs, &totals.Input, &totals.Output, &totals.CacheRead, &totals.CacheWrite, &totals.CacheWrite1h, &totals.Cost); err != nil {
					rows.Close()
					return result, err
				}
				modelName, speedName := store.resolveDict(ctx, tx, modelID, speedID, modelNames, speedNames)
				applyRow(app, modelName, speedName, bucketStart(time.UnixMilli(atMs), "day"), seg.period, totals)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return result, err
			}
		}
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
		if service := services[strconv.Itoa(int(*entry.App))]; service != nil {
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

type convMeta struct {
	id              string
	title           string
	project         string
	projectStatus   string
	cwd             string
	branch          string
	subtasks        bool
	cacheWriteAvail bool
}

func buildConversationSummary(meta convMeta, app model.UsageApp, first, last int64, totals model.UsageTotals, models string, fast int64) model.ConversationSummary {
	item := model.ConversationSummary{
		ID:              meta.id,
		Title:           meta.title,
		App:             app,
		Project:         meta.project,
		ProjectStatus:   meta.projectStatus,
		Cwd:             meta.cwd,
		Branch:          meta.branch,
		Subtasks:        meta.subtasks,
		CacheWriteAvail: meta.cacheWriteAvail,
		StartedAt:       time.UnixMilli(first),
		EndedAt:         time.UnixMilli(last),
		Totals:          totals,
		Speed:           "standard",
	}
	if models != "" {
		item.Models = strings.Split(models, ",")
	} else {
		item.Models = []string{}
	}
	item.Totals.Add(model.UsageTotals{})
	if fast == item.Totals.Requests && fast > 0 {
		item.Speed = "fast"
	} else if fast > 0 {
		item.Speed = "mixed"
	}
	return item
}

// Conversations 借助conv_summary几何边界裁剪仅对跨界对话精确聚合并返回稳定分页
func (store *Store) Conversations(ctx context.Context, query model.ConversationQuery) (model.ConversationPage, error) {
	result := model.ConversationPage{Items: []model.ConversationSummary{}, Projects: []model.ProjectSummary{}}
	from, to, err := bounds(query.From, query.To)
	if err != nil {
		return result, err
	}
	fromMs, toMs := from.UnixMilli(), to.UnixMilli()
	filter := sourceFilter("cs.", query.App, query.AllowedApps)
	rows, err := store.db.QueryContext(ctx, `SELECT cs.conv_id,cv.name,
 COALESCE(c.title,''),COALESCE(c.project,c.cwd,''),COALESCE(c.project_status,''),
 COALESCE(c.cwd,''),COALESCE(c.branch,''),COALESCE(c.subtasks,0),COALESCE(c.cache_write_avail,0),
 cs.app,cs.first_time,cs.last_time,cs.input,cs.output,cs.cache_read,cs.cache_write,cs.cache_write_1h,
 cs.requests,cs.cost,cs.models,cs.fast_requests
 FROM conv_summary cs
 JOIN usage_conv cv ON cv.id=cs.conv_id
 LEFT JOIN conversation c ON c.id=cv.name
 WHERE cs.last_time>=? AND cs.first_time<?`+filter, fromMs, toMs)
	if err != nil {
		return result, err
	}
	search := strings.ToLower(strings.TrimSpace(query.Search))
	projects := map[string]*model.ProjectSummary{}
	items := []model.ConversationSummary{}
	collect := func(item model.ConversationSummary) {
		if !conversationMatches(item, search) {
			return
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

	boundaryIDs := []int64{}
	boundaryMeta := map[int64]convMeta{}
	for rows.Next() {
		var convID, first, last, fast int64
		var app model.UsageApp
		var meta convMeta
		var totals model.UsageTotals
		var models string
		if err = rows.Scan(&convID, &meta.id, &meta.title, &meta.project, &meta.projectStatus, &meta.cwd, &meta.branch, &meta.subtasks, &meta.cacheWriteAvail, &app, &first, &last, &totals.Input, &totals.Output, &totals.CacheRead, &totals.CacheWrite, &totals.CacheWrite1h, &totals.Requests, &totals.Cost, &models, &fast); err != nil {
			rows.Close()
			return result, err
		}
		if first >= fromMs && last < toMs {
			collect(buildConversationSummary(meta, app, first, last, totals, models, fast))
			continue
		}
		boundaryIDs = append(boundaryIDs, convID)
		boundaryMeta[convID] = meta
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}

	if len(boundaryIDs) > 0 {
		rawFilter := sourceFilter("u.", query.App, query.AllowedApps)
		const chunkSize = 200
		for start := 0; start < len(boundaryIDs); start += chunkSize {
			end := min(start+chunkSize, len(boundaryIDs))
			chunk := boundaryIDs[start:end]
			placeholders := strings.Repeat("?,", len(chunk))
			placeholders = placeholders[:len(placeholders)-1]
			args := make([]any, 0, len(chunk)+2)
			for _, id := range chunk {
				args = append(args, id)
			}
			args = append(args, fromMs, toMs)
			bRows, bErr := store.db.QueryContext(ctx, `SELECT u.conv_id,MIN(u.app),MIN(u.time),MAX(u.time),
 SUM(u.input),SUM(u.output),SUM(u.cache_read),SUM(u.cache_write),SUM(u.cache_write_1h),COUNT(*),
 COALESCE(SUM(u.cost),0),COALESCE(GROUP_CONCAT(DISTINCT m.name),''),SUM(CASE WHEN s.name='fast' THEN 1 ELSE 0 END)
 FROM usage u
 JOIN usage_model m ON m.id=u.model_id
 JOIN usage_speed s ON s.id=u.speed_id
 WHERE u.conv_id IN (`+placeholders+`) AND u.time>=? AND u.time<?`+rawFilter+`
 GROUP BY u.conv_id`, args...)
			if bErr != nil {
				return result, bErr
			}
			for bRows.Next() {
				var convID, first, last, fast int64
				var app model.UsageApp
				var totals model.UsageTotals
				var models string
				if err = bRows.Scan(&convID, &app, &first, &last, &totals.Input, &totals.Output, &totals.CacheRead, &totals.CacheWrite, &totals.CacheWrite1h, &totals.Requests, &totals.Cost, &models, &fast); err != nil {
					bRows.Close()
					return result, err
				}
				if models != "" {
					parts := strings.Split(models, ",")
					sort.Strings(parts)
					models = strings.Join(parts, ",")
				}
				collect(buildConversationSummary(boundaryMeta[convID], app, first, last, totals, models, fast))
			}
			err = bRows.Err()
			bRows.Close()
			if err != nil {
				return result, err
			}
		}
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
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	store.dictMu.RLock()
	convID, ok := store.convs[id]
	store.dictMu.RUnlock()
	if !ok {
		if err := tx.QueryRowContext(ctx, "SELECT id FROM usage_conv WHERE name=?", id).Scan(&convID); err != nil {
			return result, err
		}
	}
	modelNames, speedNames := store.dictSnapshot()
	var meta convMeta
	meta.id = id
	var app model.UsageApp
	var first, last, fast int64
	var totals model.UsageTotals
	var models string
	err = tx.QueryRowContext(ctx, `SELECT
 COALESCE(c.title,''),COALESCE(c.project,c.cwd,''),COALESCE(c.project_status,''),
 COALESCE(c.cwd,''),COALESCE(c.branch,''),COALESCE(c.subtasks,0),COALESCE(c.cache_write_avail,0),
 cs.app,cs.first_time,cs.last_time,cs.input,cs.output,cs.cache_read,cs.cache_write,cs.cache_write_1h,
 cs.requests,cs.cost,cs.models,cs.fast_requests
 FROM conv_summary cs
 LEFT JOIN conversation c ON c.id=?
 WHERE cs.conv_id=?`, id, convID).Scan(&meta.title, &meta.project, &meta.projectStatus, &meta.cwd, &meta.branch, &meta.subtasks, &meta.cacheWriteAvail, &app, &first, &last, &totals.Input, &totals.Output, &totals.CacheRead, &totals.CacheWrite, &totals.CacheWrite1h, &totals.Requests, &totals.Cost, &models, &fast)
	if err != nil {
		return result, err
	}
	result.Conversation = buildConversationSummary(meta, app, first, last, totals, models, fast)

	rows, err := tx.QueryContext(ctx, `SELECT COALESCE(u.dsh_key,hex(u.key_hash)),u.app,u.model_id,u.speed_id,
 u.time,u.input,u.output,u.cache_read,u.cache_write,u.cost,u.cache_write_1h,
 uc.input,uc.output,uc.cache_read,uc.cache_write
 FROM usage u
 LEFT JOIN usage_cost uc ON uc.usage_id=u.id
 WHERE u.conv_id=?
 ORDER BY u.time DESC,u.id`, convID)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	groups := map[string]*model.UsageGroup{}
	for rows.Next() {
		var row model.UsageRow
		var modelID, speedID, milliseconds int64
		var cIn, cOut, cRead, cWrite sql.NullFloat64
		if err = rows.Scan(&row.ID, &row.App, &modelID, &speedID, &milliseconds, &row.Input, &row.Output, &row.CacheRead, &row.CacheWrite, &row.Cost, &row.CacheWrite1h, &cIn, &cOut, &cRead, &cWrite); err != nil {
			return result, err
		}
		row.Conversation = id
		row.Model, row.Speed = store.resolveDict(ctx, tx, modelID, speedID, modelNames, speedNames)
		row.Time = time.UnixMilli(milliseconds)
		row.Requests = 1
		if cIn.Valid {
			row.ReportedCosts = &model.CostBreakdown{Input: cIn.Float64, Output: cOut.Float64, CacheRead: cRead.Float64, CacheWrite: cWrite.Float64}
		}
		result.Events = append(result.Events, row)
		key := strconv.Itoa(int(row.App)) + ":" + row.Model + ":" + row.Speed
		group(groups, key, row.Model, &row.App, row.Model, Provider(row.App, row.Model), row.Speed).Totals.Add(row.Totals())
		if costs := store.pricing.CostBreakdown(row); costs != nil {
			result.Costs.Input += costs.Input
			result.Costs.Output += costs.Output
			result.Costs.CacheRead += costs.CacheRead
			result.Costs.CacheWrite += costs.CacheWrite
		}
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	result.Models = sortedGroups(groups)
	return result, nil
}
