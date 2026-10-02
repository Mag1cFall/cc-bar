package history

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Mag1cFall/cc-bar/internal/model"
)

// Sample 表示一条额度采样
type Sample struct {
	AccountKey       string          `json:"accountKey"`
	App              model.QuotaApp  `json:"app"`
	Kind             string          `json:"kind"`
	SampledAt        time.Time       `json:"sampledAt"`
	LimitID          string          `json:"limitId"`
	LimitKind        model.LimitKind `json:"limitKind"`
	RemainingPercent int             `json:"remainingPercent"`
	ResetsAt         *time.Time      `json:"resetsAt,omitempty"`
}

// Change 保留额度变化的前后值
type Change struct {
	ID                     string          `json:"id"`
	AccountKey             string          `json:"accountKey"`
	App                    model.QuotaApp  `json:"app"`
	Kind                   string          `json:"kind"`
	SampledAt              time.Time       `json:"sampledAt"`
	LimitID                string          `json:"limitId"`
	LimitKind              model.LimitKind `json:"limitKind"`
	BeforeRemainingPercent int             `json:"beforeRemainingPercent"`
	AfterRemainingPercent  int             `json:"afterRemainingPercent"`
	DeltaPercent           int             `json:"deltaPercent"`
	ResetsAt               *time.Time      `json:"resetsAt,omitempty"`
}

// Entry 表示时间线上的变化或最新采样
type Entry struct {
	ID               string     `json:"id"`
	IsChange         bool       `json:"isChange"`
	SampledAt        time.Time  `json:"sampledAt"`
	RemainingPercent int        `json:"remainingPercent"`
	DeltaPercent     *int       `json:"deltaPercent,omitempty"`
	ResetsAt         *time.Time `json:"resetsAt,omitempty"`
	WindowIndex      int        `json:"windowIndex"`
}

// Period 表示当天或真实额度周期
type Period struct {
	ID         string    `json:"id"`
	Kind       int       `json:"kind"`
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	Entries    []Entry   `json:"entries"`
	TotalDelta int       `json:"totalDelta"`
}

type historyPayload struct {
	Version     int               `json:"version"`
	DayStart    time.Time         `json:"dayStart"`
	LastSamples map[string]Sample `json:"lastSamples"`
	Events      []Change          `json:"events"`
}

// Store 持久化额度时间线及周期边界
type Store struct {
	mu        sync.Mutex
	directory string
	history   historyPayload
	cycles    CyclePayload
}

// Open 读取现存历史文件并保留原有账号分区
func Open(directory string) (*Store, error) {
	store := &Store{directory: directory, history: historyPayload{Version: 3, LastSamples: map[string]Sample{}, Events: []Change{}},
		cycles: CyclePayload{Version: 4, TrackingStartedAt: time.Now(), Records: []CycleRecord{}, AccountSegments: []AccountSegment{}}}
	for _, file := range []struct {
		name  string
		value any
	}{{"quota-history.json", &store.history}, {"quota-cycle-history.json", &store.cycles}} {
		contents, err := os.ReadFile(filepath.Join(directory, file.name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if err = json.Unmarshal(contents, file.value); err != nil {
			return nil, fmt.Errorf("读取%s: %w", file.name, err)
		}
	}
	if store.history.LastSamples == nil {
		store.history.LastSamples = map[string]Sample{}
	}
	store.normalizeCycles()
	return store, nil
}

// KeyFor 沿用旧版账号标识以连续展示历史数据
func KeyFor(app model.QuotaApp, credential *model.Credential) string {
	if credential == nil {
		return ""
	}
	switch app {
	case model.Codex:
		if credential.AccountID != "" {
			return "codex:primary:" + strings.TrimSpace(credential.AccountID)
		}
		return "codex:primary"
	case model.Claude:
		if credential.Email != "" {
			return "claude:primary:" + identityKey(credential.Email)
		}
		if credential.AccountUUID != "" {
			return "claude:primary:uuid:" + identityKey(credential.AccountUUID)
		}
		return "claude:primary"
	case model.Antigravity:
		if credential.Email != "" {
			return "antigravity:primary:" + identityKey(credential.Email)
		}
		return "antigravity:primary"
	}
	return ""
}

func identityKey(value string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(value))))
	return hex.EncodeToString(sum[:])
}
func kindName(kind model.LimitKind) string {
	if kind == model.FiveHour {
		return "FiveHour"
	}
	if kind == model.Weekly {
		return "Weekly"
	}
	return "Unknown"
}
func seriesKey(key string, kind model.LimitKind) string { return key + "|" + kindName(kind) }
func sameWindow(a, b *time.Time) bool {
	return a == nil || b == nil || math.Abs(a.Sub(*b).Seconds()) <= 1800
}
func accountKind(key string) string {
	if strings.HasPrefix(key, "claude") {
		return "claudePrimary"
	}
	if strings.HasPrefix(key, "antigravity") {
		return "antigravityPrimary"
	}
	if strings.HasPrefix(key, "codex:imported") {
		return "codexImported"
	}
	return "codexPrimary"
}

// Record 将服务采样写入独立账号时间线
func (store *Store) Record(key string, app model.QuotaApp, snapshot model.QuotaSnapshot, source string, at time.Time) error {
	if key == "" {
		return nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	start := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, at.Location())
	store.history.DayStart = start
	cutoff := start.AddDate(0, 0, -14)
	kept := store.history.Events[:0]
	for _, event := range store.history.Events {
		if !event.SampledAt.Before(cutoff) {
			kept = append(kept, event)
		}
	}
	store.history.Events = kept
	for series, sample := range store.history.LastSamples {
		if sample.SampledAt.Before(cutoff) {
			delete(store.history.LastSamples, series)
		}
	}
	for _, limit := range []*model.QuotaLimit{snapshot.PrimaryLimit, snapshot.SecondaryLimit} {
		if limit == nil || (limit.Kind != model.FiveHour && limit.Kind != model.Weekly) {
			continue
		}
		series := seriesKey(key, limit.Kind)
		previous, found := store.history.LastSamples[series]
		remaining := int(math.Round(math.Max(0, math.Min(100, 100-limit.Window.UsedPercent))))
		if found && (previous.LimitID != limit.ID || previous.LimitKind != limit.Kind) {
			filtered := store.history.Events[:0]
			for _, event := range store.history.Events {
				if event.AccountKey != key || event.LimitKind != limit.Kind {
					filtered = append(filtered, event)
				}
			}
			store.history.Events = filtered
			found = false
		}
		store.history.LastSamples[series] = Sample{key, app, accountKind(key), at, limit.ID, limit.Kind, remaining, limit.Window.ResetsAt}
		if found {
			for index := len(store.history.Events) - 1; index >= 0; index-- {
				tail := store.history.Events[index]
				if tail.AccountKey == key && tail.LimitKind == limit.Kind {
					if sameWindow(previous.ResetsAt, tail.ResetsAt) && !previous.SampledAt.Before(tail.SampledAt) && previous.RemainingPercent != tail.AfterRemainingPercent {
						previous.RemainingPercent = tail.AfterRemainingPercent
						previous.ResetsAt = tail.ResetsAt
					}
					break
				}
			}
		}
		if found && previous.RemainingPercent != remaining && sameWindow(previous.ResetsAt, limit.Window.ResetsAt) {
			store.history.Events = append(store.history.Events, Change{fmt.Sprintf("%s|%s|%d|%d|%d", key, limit.ID, at.Unix(), previous.RemainingPercent, remaining), key, app, accountKind(key), at, limit.ID, limit.Kind, previous.RemainingPercent, remaining, remaining - previous.RemainingPercent, limit.Window.ResetsAt})
		}
	}
	if (app == model.Codex || app == model.Claude) && !strings.HasPrefix(key, "codex:imported:") {
		usageApp := model.UsageCodex
		if app == model.Claude {
			usageApp = model.UsageClaude
		}
		store.recordCycles(key, usageApp, snapshot, source, at)
	}
	return store.save()
}

// Periods 返回当天或本周期与上一周期的时间线
func (store *Store) Periods(key string, kind model.LimitKind, now time.Time) []Period {
	store.mu.Lock()
	defer store.mu.Unlock()
	events := []Change{}
	for _, event := range store.history.Events {
		if event.AccountKey == key && event.LimitKind == kind {
			events = append(events, event)
		}
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].SampledAt.Before(events[j].SampledAt) })
	sample, hasSample := store.history.LastSamples[seriesKey(key, kind)]
	if kind == model.FiveHour {
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		periods := []Period{}
		for day := 0; day < 14; day++ {
			from := start.AddDate(0, 0, -day)
			period := makePeriod(key, 0, from, from.AddDate(0, 0, 1), events, &sample, hasSample, false)
			if day == 0 || len(period.Entries) > 0 {
				periods = append(periods, period)
			}
		}
		return periods
	}
	if kind != model.Weekly {
		return []Period{}
	}
	var end *time.Time
	if hasSample {
		end = sample.ResetsAt
	}
	if end == nil && len(events) > 0 {
		end = events[len(events)-1].ResetsAt
	}
	if end == nil {
		return []Period{}
	}
	start := end.Add(-7 * 24 * time.Hour)
	var previousEnd time.Time
	for _, event := range events {
		if event.ResetsAt != nil && event.ResetsAt.Before(*end) && !sameWindow(event.ResetsAt, end) {
			if previousEnd.IsZero() || event.ResetsAt.After(previousEnd) {
				previousEnd = *event.ResetsAt
			}
		}
	}
	if previousEnd.IsZero() || sameWindow(&previousEnd, &start) {
		previousEnd = start
	}
	return []Period{makePeriod(key, 1, start, *end, events, &sample, hasSample, true), makePeriod(key, 2, previousEnd.Add(-7*24*time.Hour), previousEnd, events, nil, false, true)}
}

func makePeriod(key string, kind int, start, end time.Time, changes []Change, sample *Sample, hasSample, weekly bool) Period {
	period := Period{ID: fmt.Sprintf("%s|%d|%d", key, kind, end.Unix()), Kind: kind, Start: start, End: end, Entries: []Entry{}}
	belongs := func(at time.Time, reset *time.Time) bool {
		if at.Before(start) || at.After(end) {
			return false
		}
		if weekly && reset != nil {
			return sameWindow(reset, &end)
		}
		return at.Before(end)
	}
	for _, event := range changes {
		if !belongs(event.SampledAt, event.ResetsAt) {
			continue
		}
		delta := event.DeltaPercent
		period.Entries = append(period.Entries, Entry{"event|" + event.ID, true, event.SampledAt, event.AfterRemainingPercent, &delta, event.ResetsAt, 0})
		if delta < 0 {
			period.TotalDelta -= delta
		}
	}
	if hasSample && sample != nil && belongs(sample.SampledAt, sample.ResetsAt) {
		duplicate := false
		for _, entry := range period.Entries {
			if entry.SampledAt.Equal(sample.SampledAt) && entry.RemainingPercent == sample.RemainingPercent {
				duplicate = true
				break
			}
		}
		if !duplicate {
			period.Entries = append(period.Entries, Entry{fmt.Sprintf("sample|%s|%d", key, sample.SampledAt.Unix()), false, sample.SampledAt, sample.RemainingPercent, nil, sample.ResetsAt, 0})
		}
	}
	sort.SliceStable(period.Entries, func(i, j int) bool { return period.Entries[i].SampledAt.Before(period.Entries[j].SampledAt) })
	index := 0
	var last *time.Time
	for entry := range period.Entries {
		current := period.Entries[entry].ResetsAt
		if current != nil {
			if last != nil && !sameWindow(last, current) {
				index++
			}
			last = current
		}
		period.Entries[entry].WindowIndex = index
	}
	return period
}

// Keys 返回已有额度分区
func (store *Store) Keys() []string {
	store.mu.Lock()
	defer store.mu.Unlock()
	keys := map[string]bool{}
	for _, sample := range store.history.LastSamples {
		keys[sample.AccountKey] = true
	}
	for _, event := range store.history.Events {
		keys[event.AccountKey] = true
	}
	result := []string{}
	for key := range keys {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

func (store *Store) save() error {
	if err := os.MkdirAll(store.directory, 0700); err != nil {
		return err
	}
	for _, file := range []struct {
		name  string
		value any
	}{{"quota-history.json", store.history}, {"quota-cycle-history.json", store.cycles}} {
		encoded, err := json.Marshal(file.value)
		if err != nil {
			return err
		}
		path := filepath.Join(store.directory, file.name)
		temporary := path + ".tmp"
		if err = os.WriteFile(temporary, encoded, 0600); err != nil {
			return err
		}
		if err = os.Rename(temporary, path); err != nil {
			return err
		}
	}
	return nil
}

// Flush 写入当前的历史采样
func (store *Store) Flush() error { store.mu.Lock(); defer store.mu.Unlock(); return store.save() }
