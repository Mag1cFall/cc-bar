package history

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/Mag1cFall/cc-bar/internal/model"
)

// AllowanceSegment 表示一次初始额度或额外重置
type AllowanceSegment struct {
	ID                  string     `json:"id"`
	StartAt             time.Time  `json:"startAt"`
	EndAt               *time.Time `json:"endAt,omitempty"`
	BaselineUsedPercent float64    `json:"baselineUsedPercent"`
	LatestUsedPercent   float64    `json:"latestUsedPercent"`
	MaximumUsedPercent  float64    `json:"maximumUsedPercent"`
	FirstSampleAt       time.Time  `json:"firstSampleAt"`
	LastSampleAt        time.Time  `json:"lastSampleAt"`
	StartReason         int        `json:"startReason"`
}

// CycleRecord 描述一个真实额度周期
type CycleRecord struct {
	ID                string             `json:"id"`
	AccountKey        string             `json:"accountKey"`
	App               model.UsageApp     `json:"app"`
	LimitID           string             `json:"limitId"`
	LimitKind         model.LimitKind    `json:"limitKind"`
	StartAt           time.Time          `json:"startAt"`
	EndAt             time.Time          `json:"endAt"`
	ScheduledEndAt    time.Time          `json:"scheduledEndAt"`
	FirstSampleAt     *time.Time         `json:"firstSampleAt,omitempty"`
	LastSampleAt      *time.Time         `json:"lastSampleAt,omitempty"`
	LatestUsedPercent float64            `json:"latestUsedPercent"`
	AllowanceSegments []AllowanceSegment `json:"allowanceSegments"`
	Source            string             `json:"source"`
	BoundaryQuality   int                `json:"boundaryQuality"`
}

// AccountSegment 记录某账号实际使用的时间区间
type AccountSegment struct {
	ID         string         `json:"id"`
	AccountKey string         `json:"accountKey"`
	App        model.UsageApp `json:"app"`
	StartAt    time.Time      `json:"startAt"`
	EndAt      *time.Time     `json:"endAt,omitempty"`
}

// CyclePayload 保持已有周期文件结构
type CyclePayload struct {
	Version           int              `json:"version"`
	TrackingStartedAt time.Time        `json:"trackingStartedAt"`
	Records           []CycleRecord    `json:"records"`
	AccountSegments   []AccountSegment `json:"accountSegments"`
}

// Cycles 返回隔离后的周期记录快照
func (store *Store) Cycles() CyclePayload {
	store.mu.Lock()
	defer store.mu.Unlock()
	payload := store.cycles
	payload.Records = append([]CycleRecord{}, payload.Records...)
	payload.AccountSegments = append([]AccountSegment{}, payload.AccountSegments...)
	for i := range payload.Records {
		payload.Records[i].AllowanceSegments = append([]AllowanceSegment{}, payload.Records[i].AllowanceSegments...)
	}
	return payload
}

// ObservedUsed 返回采样开始后实际观测到的用量
func (record CycleRecord) ObservedUsed() float64 {
	total := 0.0
	for _, segment := range record.AllowanceSegments {
		total += math.Max(0, segment.MaximumUsedPercent-segment.BaselineUsedPercent)
	}
	return total
}

// ReportedUsed 返回周期全部额度与额外重置的已用比例
func (record CycleRecord) ReportedUsed() float64 {
	initial := 0.0
	hasInitial := false
	extra := 0.0
	for _, segment := range record.AllowanceSegments {
		if segment.StartReason == 0 {
			hasInitial = true
			initial = math.Max(initial, math.Min(100, segment.MaximumUsedPercent))
		} else {
			extra += math.Max(0, math.Min(100, segment.MaximumUsedPercent))
		}
	}
	if !hasInitial {
		initial = math.Max(0, record.LatestUsedPercent)
	}
	return initial + extra
}

// ExtraResets 返回周期内额外重置次数
func (record CycleRecord) ExtraResets() int {
	count := 0
	for _, segment := range record.AllowanceSegments {
		if segment.StartReason == 1 {
			count++
		}
	}
	return count
}

func extraReset(previous, current float64) bool {
	drop := previous - current
	return drop >= 10 || (current <= 2 && drop >= 3)
}
func windowDuration(kind model.LimitKind) time.Duration {
	if kind == model.FiveHour {
		return 5 * time.Hour
	}
	return 7 * 24 * time.Hour
}
func minimum(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func maximum(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
func sourcePreference(a, b string) string {
	if a == "api" || b == "api" {
		return "api"
	}
	return b
}

func (store *Store) recordCycles(key string, app model.UsageApp, snapshot model.QuotaSnapshot, source string, at time.Time) {
	active := -1
	for index := len(store.cycles.AccountSegments) - 1; index >= 0; index-- {
		segment := store.cycles.AccountSegments[index]
		if segment.App == app && segment.EndAt == nil {
			active = index
			break
		}
	}
	if active < 0 || store.cycles.AccountSegments[active].AccountKey != key {
		if active >= 0 {
			store.cycles.AccountSegments[active].EndAt = &at
		}
		start := at
		first := true
		for _, segment := range store.cycles.AccountSegments {
			if segment.App == app {
				first = false
				break
			}
		}
		if first {
			start = store.cycles.TrackingStartedAt
		}
		store.cycles.AccountSegments = append(store.cycles.AccountSegments, AccountSegment{fmt.Sprintf("%d|%s|%d", app, key, at.Unix()), key, app, start, nil})
	}
	for _, limit := range []*model.QuotaLimit{snapshot.PrimaryLimit, snapshot.SecondaryLimit} {
		if limit == nil || limit.Window.ResetsAt == nil || (limit.Kind != model.FiveHour && limit.Kind != model.Weekly) {
			continue
		}
		end := *limit.Window.ResetsAt
		duration := windowDuration(limit.Kind)
		quality := 1
		if limit.Window.WindowSeconds != nil && *limit.Window.WindowSeconds > 0 {
			duration = time.Duration(*limit.Window.WindowSeconds) * time.Second
			quality = 0
		}
		start := end.Add(-duration)
		used := math.Max(0, math.Min(100, limit.Window.UsedPercent))
		id := fmt.Sprintf("%s|%s|%d", key, limit.ID, end.Unix())
		index := -1
		for i, record := range store.cycles.Records {
			if record.AccountKey != key || record.App != app || record.LimitKind != limit.Kind {
				continue
			}
			if record.ID == id || math.Abs(record.EndAt.Sub(end).Seconds()) < 60 || (record.EndAt.After(at) && math.Abs(record.ScheduledEndAt.Sub(end).Seconds()) <= 1800 && !extraReset(record.LatestUsedPercent, used)) {
				index = i
				break
			}
		}
		if index >= 0 {
			record := &store.cycles.Records[index]
			previous := record.LatestUsedPercent
			wasActive := record.EndAt.After(at)
			record.StartAt = minimum(record.StartAt, start)
			if math.Abs(record.ScheduledEndAt.Sub(end).Seconds()) >= 5 {
				record.ScheduledEndAt = end
			}
			if wasActive {
				record.EndAt = record.ScheduledEndAt
			} else {
				record.EndAt = minimum(record.EndAt, record.ScheduledEndAt)
			}
			if record.FirstSampleAt == nil || at.Before(*record.FirstSampleAt) {
				record.FirstSampleAt = &at
			}
			if record.LastSampleAt == nil || at.After(*record.LastSampleAt) {
				record.LastSampleAt = &at
			}
			record.LatestUsedPercent = used
			record.Source = sourcePreference(record.Source, source)
			if quality == 0 {
				record.BoundaryQuality = 0
			}
			if len(record.AllowanceSegments) == 0 {
				record.AllowanceSegments = append(record.AllowanceSegments, store.initialAllowance(*record, used, at))
			} else {
				last := &record.AllowanceSegments[len(record.AllowanceSegments)-1]
				if extraReset(previous, used) {
					last.EndAt = &at
					record.AllowanceSegments = append(record.AllowanceSegments, AllowanceSegment{fmt.Sprintf("%s|allowance|%d", record.ID, at.Unix()), at, nil, used, used, used, at, at, 1})
				} else {
					last.LatestUsedPercent = used
					last.MaximumUsedPercent = math.Max(last.MaximumUsedPercent, used)
					last.LastSampleAt = maximum(last.LastSampleAt, at)
				}
			}
		} else {
			for i := range store.cycles.Records {
				record := &store.cycles.Records[i]
				if record.AccountKey == key && record.App == app && record.LimitKind == limit.Kind && record.StartAt.Before(start) && record.EndAt.After(start) && math.Abs(record.StartAt.Sub(start).Seconds()) >= 60 && record.EndAt.Sub(start) > 60*time.Second {
					record.EndAt = start
					if len(record.AllowanceSegments) > 0 {
						last := &record.AllowanceSegments[len(record.AllowanceSegments)-1]
						if last.EndAt == nil {
							last.EndAt = &start
						}
					}
				}
			}
			record := CycleRecord{ID: id, AccountKey: key, App: app, LimitID: limit.ID, LimitKind: limit.Kind, StartAt: start, EndAt: end, ScheduledEndAt: end, FirstSampleAt: &at, LastSampleAt: &at, LatestUsedPercent: used, Source: source, BoundaryQuality: quality}
			record.AllowanceSegments = []AllowanceSegment{store.initialAllowance(record, used, at)}
			store.cycles.Records = append(store.cycles.Records, record)
		}
	}
	store.normalizeCycles()
}

func (store *Store) initialAllowance(record CycleRecord, used float64, at time.Time) AllowanceSegment {
	start := at
	baseline := used
	if !store.cycles.TrackingStartedAt.After(record.StartAt) {
		start = record.StartAt
		baseline = 0
	}
	return AllowanceSegment{fmt.Sprintf("%s|allowance|%d", record.ID, start.Unix()), start, nil, baseline, used, used, at, at, 0}
}

func (store *Store) normalizeCycles() {
	records := []CycleRecord{}
	for _, record := range store.cycles.Records {
		if record.EndAt.Sub(record.StartAt) < time.Minute {
			continue
		}
		duration := windowDuration(record.LimitKind)
		if record.EndAt.Sub(record.StartAt) > time.Duration(float64(duration)*1.15) {
			record.StartAt = record.ScheduledEndAt.Add(-duration)
			if record.FirstSampleAt != nil && record.FirstSampleAt.Before(record.StartAt) {
				record.FirstSampleAt = &record.StartAt
			}
		}
		records = append(records, record)
	}
	sort.SliceStable(records, func(i, j int) bool { return records[i].EndAt.After(records[j].EndAt) })
	merged := []CycleRecord{}
	consumed := map[int]bool{}
	for i, record := range records {
		if consumed[i] {
			continue
		}
		for j := i + 1; j < len(records); j++ {
			other := records[j]
			if consumed[j] || record.AccountKey != other.AccountKey || record.App != other.App || record.LimitKind != other.LimitKind {
				continue
			}
			overlap := minimum(record.EndAt, other.EndAt).Sub(maximum(record.StartAt, other.StartAt))
			if overlap <= time.Minute || math.Abs(record.ScheduledEndAt.Sub(other.ScheduledEndAt).Seconds()) > 1800 {
				continue
			}
			consumed[j] = true
			if other.LastSampleAt != nil && (record.LastSampleAt == nil || other.LastSampleAt.After(*record.LastSampleAt)) {
				record.LatestUsedPercent = other.LatestUsedPercent
				record.LastSampleAt = other.LastSampleAt
			}
			if other.FirstSampleAt != nil && (record.FirstSampleAt == nil || other.FirstSampleAt.Before(*record.FirstSampleAt)) {
				record.FirstSampleAt = other.FirstSampleAt
			}
			record.ScheduledEndAt = minimum(record.ScheduledEndAt, other.ScheduledEndAt)
			record.StartAt = record.ScheduledEndAt.Add(-windowDuration(record.LimitKind))
			record.AllowanceSegments = mergeAllowance(record.AllowanceSegments, other.AllowanceSegments)
			record.Source = sourcePreference(record.Source, other.Source)
			if other.BoundaryQuality == 0 {
				record.BoundaryQuality = 0
			}
		}
		merged = append(merged, record)
	}
	sort.SliceStable(merged, func(i, j int) bool { return merged[i].StartAt.Before(merged[j].StartAt) })
	result := []CycleRecord{}
	for _, next := range merged {
		joined := false
		for i := len(result) - 1; i >= 0; i-- {
			earlier := &result[i]
			if earlier.AccountKey != next.AccountKey || earlier.App != next.App || earlier.LimitKind != next.LimitKind || earlier.LimitID != next.LimitID {
				continue
			}
			if math.Abs(earlier.EndAt.Sub(next.StartAt).Seconds()) <= 1 && earlier.ScheduledEndAt.Sub(earlier.EndAt) > time.Minute && !extraReset(earlier.LatestUsedPercent, next.LatestUsedPercent) && math.Abs(earlier.ScheduledEndAt.Sub(next.ScheduledEndAt).Seconds()) <= 1800 {
				next.ScheduledEndAt = minimum(earlier.ScheduledEndAt, next.ScheduledEndAt)
				next.StartAt = next.ScheduledEndAt.Add(-windowDuration(next.LimitKind))
				next.FirstSampleAt = earlier.FirstSampleAt
				next.AllowanceSegments = mergeAllowance(earlier.AllowanceSegments, next.AllowanceSegments)
				next.Source = sourcePreference(earlier.Source, next.Source)
				if earlier.BoundaryQuality == 0 {
					next.BoundaryQuality = 0
				}
				result[i] = next
				joined = true
			}
			break
		}
		if !joined {
			result = append(result, next)
		}
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].EndAt.After(result[j].EndAt) })
	store.cycles.Records = result
}

func mergeAllowance(a, b []AllowanceSegment) []AllowanceSegment {
	all := append(append([]AllowanceSegment{}, a...), b...)
	sort.SliceStable(all, func(i, j int) bool { return all[i].StartAt.Before(all[j].StartAt) })
	result := []AllowanceSegment{}
	for _, segment := range all {
		if len(result) > 0 {
			last := &result[len(result)-1]
			if math.Abs(last.StartAt.Sub(segment.StartAt).Seconds()) < 1 {
				if !segment.LastSampleAt.Before(last.LastSampleAt) {
					*last = segment
				}
				continue
			}
			if last.EndAt == nil {
				last.EndAt = &segment.StartAt
			}
		}
		result = append(result, segment)
	}
	return result
}
