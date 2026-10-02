package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/Mag1cFall/cc-bar/internal/model"
)

const cursorUsageURL = "https://cursor.com/api/dashboard/get-filtered-usage-events"
const cursorPageSize = 1000

// CursorHTTPError 保留远端响应状态供调度器识别登录和限流
type CursorHTTPError struct{ Status int }

func (err CursorHTTPError) Error() string { return fmt.Sprintf("Cursor用量HTTP %d", err.Status) }

type timeRange struct{ from, to time.Time }

func mergeRanges(ranges []timeRange) []timeRange {
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].from.Before(ranges[j].from) })
	result := []timeRange{}
	for _, entry := range ranges {
		if !entry.from.Before(entry.to) {
			continue
		}
		if len(result) > 0 && !entry.from.After(result[len(result)-1].to) {
			if entry.to.After(result[len(result)-1].to) {
				result[len(result)-1].to = entry.to
			}
		} else {
			result = append(result, entry)
		}
	}
	return result
}

func missingRanges(covered []timeRange, from, to time.Time) []timeRange {
	covered = mergeRanges(covered)
	result := []timeRange{}
	cursor := from
	for _, entry := range covered {
		if !entry.to.After(cursor) || !entry.from.Before(to) {
			continue
		}
		if entry.from.After(cursor) {
			result = append(result, timeRange{cursor, entry.from})
		}
		if entry.to.After(cursor) {
			cursor = entry.to
		}
	}
	if cursor.Before(to) {
		result = append(result, timeRange{cursor, to})
	}
	return result
}

func cursorEventEqual(left, right model.UsageRow) bool {
	return left.Time.Equal(right.Time) && left.Model == right.Model && left.Input == right.Input && left.Output == right.Output && left.CacheRead == right.CacheRead && left.CacheWrite == right.CacheWrite && ((left.Cost == nil && right.Cost == nil) || (left.Cost != nil && right.Cost != nil && *left.Cost == *right.Cost))
}

func cursorRequest(ctx context.Context, client *http.Client, url string, account model.Credential, payload any) (node, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Cookie", "WorkosCursorSessionToken="+account.AccountID+"%3A%3A"+account.AccessToken)
	request.Header.Set("Origin", "https://cursor.com")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, CursorHTTPError{response.StatusCode}
	}
	var root node
	decoder := json.NewDecoder(response.Body)
	decoder.UseNumber()
	err = decoder.Decode(&root)
	return root, err
}

func proveCursorEmpty(ctx context.Context, client *http.Client, url string, account model.Credential, from, to time.Time) (bool, error) {
	root, err := cursorRequest(ctx, client, url, account, map[string]any{"page": 1, "pageSize": cursorPageSize})
	if err != nil {
		return false, err
	}
	events, valid := root["usageEventsDisplay"].([]any)
	if !valid || !has(root, "totalUsageEventsCount") || integer(root, "totalUsageEventsCount") != int64(len(events)) {
		return false, nil
	}
	for _, raw := range events {
		timestamp := integer(object(raw), "timestamp")
		if timestamp <= 0 || (timestamp >= from.UnixMilli() && timestamp < to.UnixMilli()) {
			return false, nil
		}
	}
	return true, nil
}

var errCursorPageLimit = fmt.Errorf("Cursor用量超过分页范围")

func fetchCursorPages(ctx context.Context, client *http.Client, url string, account model.Credential, from, to time.Time) ([]model.UsageRow, error) {
	pages := [][]model.UsageRow{}
	total := int64(-1)
	complete := false
	for page := 1; page <= 200; page++ {
		root, err := cursorRequest(ctx, client, url, account, map[string]any{"page": page, "pageSize": cursorPageSize, "startDate": strconv.FormatInt(from.UnixMilli(), 10), "endDate": strconv.FormatInt(to.UnixMilli(), 10)})
		if err != nil {
			return nil, err
		}
		events, valid := root["usageEventsDisplay"].([]any)
		if !valid {
			if page == 1 && len(root) == 0 {
				empty, err := proveCursorEmpty(ctx, client, url, account, from, to)
				if err != nil {
					return nil, err
				}
				if empty {
					return []model.UsageRow{}, nil
				}
			}
			return nil, fmt.Errorf("Cursor用量响应缺少事件")
		}
		if !has(root, "totalUsageEventsCount") || integer(root, "totalUsageEventsCount") < 0 {
			return nil, fmt.Errorf("Cursor用量响应缺少总数")
		}
		reported := integer(root, "totalUsageEventsCount")
		if total >= 0 && total != reported {
			return nil, fmt.Errorf("Cursor用量分页总数变化")
		}
		total = reported
		parsed := []model.UsageRow{}
		for _, raw := range events {
			entry := object(raw)
			timestamp := integer(entry, "timestamp")
			if timestamp <= 0 {
				return nil, fmt.Errorf("Cursor用量事件缺少时间")
			}
			usage := obj(entry, "tokenUsage")
			row := model.UsageRow{App: model.UsageCursor, Conversation: "cursor:daily", Model: coalesce(str(entry, "model"), "unknown"), Speed: "standard", Time: time.UnixMilli(timestamp), Input: nonnegative(usage, "inputTokens"), Output: nonnegative(usage, "outputTokens"), CacheRead: nonnegative(usage, "cacheReadTokens"), CacheWrite: nonnegative(usage, "cacheWriteTokens"), Requests: 1}
			if has(entry, "chargedCents") && number(entry, "chargedCents") >= 0 {
				cost := number(entry, "chargedCents") / 100
				row.Cost = &cost
			}
			parsed = append(parsed, row)
		}
		pages = append(pages, parsed)
		if len(parsed) < cursorPageSize {
			complete = true
			break
		}
	}
	if !complete {
		return nil, errCursorPageLimit
	}
	count := int64(0)
	for _, page := range pages {
		count += int64(len(page))
	}
	excess := count - total
	if excess < 0 {
		return nil, fmt.Errorf("Cursor用量事件总数不足")
	}
	result := []model.UsageRow{}
	for _, page := range pages {
		overlap := min(int(excess), min(len(page), len(result)))
		for overlap > 0 {
			equal := true
			for index := 0; index < overlap; index++ {
				if !cursorEventEqual(result[len(result)-overlap+index], page[index]) {
					equal = false
					break
				}
			}
			if equal {
				break
			}
			overlap--
		}
		result = append(result, page[overlap:]...)
		excess -= int64(overlap)
	}
	if excess != 0 || int64(len(result)) != total {
		return nil, fmt.Errorf("Cursor用量分页边界不一致")
	}
	return result, nil
}

func fetchCursor(ctx context.Context, client *http.Client, url string, account model.Credential, from, to time.Time) ([]model.UsageRow, error) {
	rows, err := fetchCursorPages(ctx, client, url, account, from, to)
	if err != errCursorPageLimit {
		return rows, err
	}
	days := int(to.Sub(from).Hours() / 24)
	midpoint := bucketStart(from, "day").AddDate(0, 0, days/2)
	if days <= 1 || !midpoint.After(from) || !midpoint.Before(to) {
		return nil, fmt.Errorf("Cursor单日用量超过分页范围")
	}
	left, err := fetchCursor(ctx, client, url, account, from, midpoint)
	if err != nil {
		return nil, err
	}
	right, err := fetchCursor(ctx, client, url, account, midpoint, to)
	if err != nil {
		return nil, err
	}
	return append(left, right...), nil
}

func (store *Store) cursorCoverage(ctx context.Context, accountID string) ([]timeRange, error) {
	prefix := "cursor-coverage:" + accountID + ":"
	rows, err := store.db.QueryContext(ctx, "SELECT name,value FROM scan_meta WHERE substr(name,1,length(?))=? ORDER BY name", prefix, prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []timeRange{}
	for rows.Next() {
		var name string
		var end int64
		if err = rows.Scan(&name, &end); err != nil {
			return nil, err
		}
		start, err := strconv.ParseInt(name[len(prefix):], 10, 64)
		if err != nil {
			return nil, err
		}
		result = append(result, timeRange{time.UnixMilli(start), time.UnixMilli(end)})
	}
	return result, rows.Err()
}

// CursorCoverageCount 返回已记录的远端区间数量
func (store *Store) CursorCoverageCount(ctx context.Context, accountID string) int {
	ranges, err := store.cursorCoverage(ctx, accountID)
	if err != nil {
		return 0
	}
	return len(ranges)
}

// RefreshCursor 获取并原子替换指定账号的用量区间
func (store *Store) RefreshCursor(ctx context.Context, account model.Credential, from, to time.Time) (err error) {
	if account.AccountID == "" || account.AccessToken == "" {
		return fmt.Errorf("Cursor账号凭据缺失")
	}
	if !from.Before(to) {
		return fmt.Errorf("Cursor用量时间范围无效")
	}
	store.gate.Lock()
	defer store.gate.Unlock()
	if time.Now().Before(store.cursorBackoff) {
		return fmt.Errorf("Cursor用量限流，请稍后重试")
	}
	defer func() {
		store.statusMu.Lock()
		if err != nil {
			store.status.CursorError = err.Error()
		} else {
			store.status.CursorError = ""
		}
		store.statusMu.Unlock()
		store.changed()
	}()
	rows, err := fetchCursor(ctx, &http.Client{Timeout: 30 * time.Second}, cursorUsageURL, account, from, to)
	if err != nil {
		if status, ok := err.(CursorHTTPError); ok && status.Status == 429 {
			store.cursorBackoff = time.Now().Add(10 * time.Minute)
		}
		return err
	}
	source := "cursor:" + account.AccountID
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "DELETE FROM usage WHERE app=? AND source=? AND time>=? AND time<?", model.UsageCursor, source, from.UnixMilli(), to.UnixMilli()); err != nil {
		return err
	}
	duplicates := map[string]int{}
	for _, row := range rows {
		if row.Time.Before(from) || !row.Time.Before(to) {
			continue
		}
		charge := "unknown"
		if row.Cost != nil {
			charge = strconv.FormatFloat(*row.Cost, 'g', -1, 64)
		}
		identity := fmt.Sprintf("%s:%d:%q:%d:%d:%d:%d:%s", source, row.Time.UnixMilli(), row.Model, row.Input, row.Output, row.CacheRead, row.CacheWrite, charge)
		row.ID = fmt.Sprintf("%s:%d", identity, duplicates[identity])
		duplicates[identity]++
		if err = saveUsage(ctx, tx, row, source); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO scan_meta(name,value) VALUES(?,?) ON CONFLICT(name) DO UPDATE SET value=excluded.value", "cursor-coverage:"+account.AccountID+":"+strconv.FormatInt(from.UnixMilli(), 10), to.UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

// RefreshCursorRecent 补足本周与计费周期并重新查询最近三天
func (store *Store) RefreshCursorRecent(ctx context.Context, account model.Credential, snapshot *model.QuotaSnapshot) error {
	now := time.Now()
	today := bucketStart(now, "day")
	recent := today.AddDate(0, 0, -2)
	week := bucketStart(now, "week")
	coverage, err := store.cursorCoverage(ctx, account.AccountID)
	if err != nil {
		return err
	}
	ranges := missingRanges(coverage, week, now)
	if len(coverage) == 0 {
		start := week
		if recent.Before(start) {
			start = recent
		}
		if snapshot != nil {
			for _, limit := range snapshot.AllLimits() {
				if limit.Window.ResetsAt != nil && limit.Window.WindowSeconds != nil && *limit.Window.WindowSeconds > 0 {
					billing := bucketStart(limit.Window.ResetsAt.Add(-time.Duration(*limit.Window.WindowSeconds)*time.Second), "day")
					if billing.Before(start) {
						start = billing
					}
					break
				}
			}
		}
		ranges = []timeRange{{start, now}}
	} else {
		ranges = append(ranges, timeRange{recent, now})
	}
	for _, entry := range mergeRanges(ranges) {
		if err = store.RefreshCursor(ctx, account, entry.from, entry.to); err != nil {
			return err
		}
	}
	return nil
}

// RefreshCursorHistory 按月份补全尚未覆盖的历史区间
func (store *Store) RefreshCursorHistory(ctx context.Context, account model.Credential, from, to time.Time) error {
	if to.After(time.Now()) {
		to = time.Now()
	}
	from = bucketStart(from, "day")
	if !from.Before(to) {
		return nil
	}
	coverage, err := store.cursorCoverage(ctx, account.AccountID)
	if err != nil {
		return err
	}
	for _, entry := range missingRanges(coverage, from, to) {
		for start := entry.from; start.Before(entry.to); {
			end := nextBucket(bucketStart(start, "month"), "month", 1)
			if end.After(entry.to) {
				end = entry.to
			}
			if err = store.RefreshCursor(ctx, account, start, end); err != nil {
				return err
			}
			start = end
		}
	}
	return nil
}
