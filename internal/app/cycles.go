package app

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/Mag1cFall/cc-bar/internal/history"
	"github.com/Mag1cFall/cc-bar/internal/model"
	"github.com/Mag1cFall/cc-bar/internal/usage"
)

// CycleView 合并真实额度周期与同一账号的本地用量
type CycleView struct {
	history.CycleRecord
	ExtraResetCount          int               `json:"extraResetCount"`
	TotalObservedUsedPercent float64           `json:"totalObservedUsedPercent"`
	ReportedUsedPercent      float64           `json:"reportedUsedPercent"`
	LocalTotals              model.UsageTotals `json:"localTotals"`
	EstimatedTotalCost       *float64          `json:"estimatedTotalCost,omitempty"`
	RemainingLocalCost       *float64          `json:"remainingLocalCost,omitempty"`
	EstimatedTotalTokens     *float64          `json:"estimatedTotalTokens,omitempty"`
	RemainingLocalTokens     *float64          `json:"remainingLocalTokens,omitempty"`
	ForecastConfidence       string            `json:"forecastConfidence,omitempty"`
	IsCurrent                bool              `json:"isCurrent"`
	IsActiveAccount          bool              `json:"isActiveAccount"`
}

// CyclesView 返回周期列表及开始观测时间
type CyclesView struct {
	TrackingStartedAt time.Time   `json:"trackingStartedAt"`
	Records           []CycleView `json:"records"`
}

// GetCycles 将用量按实际登录区间归属到额度周期
func (service *Service) GetCycles(ctx context.Context) (result CyclesView, err error) {
	defer func() {
		if errors.Is(err, context.Canceled) {
			result, err = CyclesView{}, nil
		}
	}()
	payload := service.history.Cycles()
	result = CyclesView{TrackingStartedAt: payload.TrackingStartedAt, Records: []CycleView{}}
	now := time.Now()
	activeAccounts := map[string]bool{}
	service.mu.RLock()
	for _, state := range service.states {
		if state.Account != nil {
			activeAccounts[history.KeyFor(state.App, state.Account)] = true
		}
	}
	service.mu.RUnlock()
	for _, record := range payload.Records {
		view := CycleView{
			CycleRecord:              record,
			ExtraResetCount:          record.ExtraResets(),
			TotalObservedUsedPercent: record.ObservedUsed(),
			ReportedUsedPercent:      record.ReportedUsed(),
			IsCurrent:                !now.Before(record.StartAt) && now.Before(record.EndAt),
			IsActiveAccount:          activeAccounts[record.AccountKey],
		}
		if !strings.HasPrefix(record.AccountKey, "codex:imported:") {
			end := record.EndAt
			if now.Before(end) {
				end = now.Add(time.Millisecond)
			}
			intervals := accountIntervals(record.StartAt, end, record, payload.AccountSegments)
			var err error
			view.LocalTotals, err = service.usage.TotalsInIntervals(ctx, record.App, intervals)
			if err != nil {
				return result, err
			}
			if len(record.AllowanceSegments) > 0 {
				allowance := record.AllowanceSegments[len(record.AllowanceSegments)-1]
				start := allowance.StartAt
				if allowance.BaselineUsedPercent > 0 {
					start = allowance.FirstSampleAt
				}
				observedIntervals := accountIntervals(start, allowance.LastSampleAt.Add(time.Millisecond), record, payload.AccountSegments)
				observed, readErr := service.usage.TotalsInIntervals(ctx, record.App, observedIntervals)
				if readErr != nil {
					return result, readErr
				}
				allowanceIntervals := accountIntervals(allowance.StartAt, end, record, payload.AccountSegments)
				currentAllowance, readErr := service.usage.TotalsInIntervals(ctx, record.App, allowanceIntervals)
				if readErr != nil {
					return result, readErr
				}
				setCycleForecast(&view, observed, currentAllowance, allowance)
			}
		}
		result.Records = append(result.Records, view)
	}
	sort.SliceStable(result.Records, func(i, j int) bool { return result.Records[i].StartAt.After(result.Records[j].StartAt) })
	return result, nil
}

// accountIntervals 合并同一账号与额度周期重叠的实际使用区间
func accountIntervals(from, to time.Time, record history.CycleRecord, segments []history.AccountSegment) []usage.Interval {
	intervals := []usage.Interval{}
	for _, segment := range segments {
		if segment.App != record.App || segment.AccountKey != record.AccountKey {
			continue
		}
		start, end := from, to
		if segment.StartAt.After(start) {
			start = segment.StartAt
		}
		if segment.EndAt != nil && segment.EndAt.Before(end) {
			end = *segment.EndAt
		}
		if record.StartAt.After(start) {
			start = record.StartAt
		}
		if record.EndAt.Before(end) {
			end = record.EndAt
		}
		if start.Before(end) {
			intervals = append(intervals, usage.Interval{From: start, To: end})
		}
	}
	sort.Slice(intervals, func(i, j int) bool { return intervals[i].From.Before(intervals[j].From) })
	merged := []usage.Interval{}
	for _, interval := range intervals {
		if len(merged) > 0 && !interval.From.After(merged[len(merged)-1].To) {
			last := &merged[len(merged)-1]
			if interval.To.After(last.To) {
				last.To = interval.To
			}
			continue
		}
		merged = append(merged, interval)
	}
	return merged
}

// setCycleForecast 使用当前额度段的同区间观测推算周期容量
func setCycleForecast(view *CycleView, observed, currentAllowance model.UsageTotals, allowance history.AllowanceSegment) {
	used := math.Max(0, allowance.MaximumUsedPercent-allowance.BaselineUsedPercent)
	if used == 0 || observed.Requests == 0 {
		return
	}
	remaining := math.Max(0, 100-view.LatestUsedPercent) / 100
	if observed.Cost > 0 {
		capacity := observed.Cost / used * 100
		total := math.Max(0, view.LocalTotals.Cost-currentAllowance.Cost) + capacity
		left := capacity * remaining
		view.EstimatedTotalCost, view.RemainingLocalCost = &total, &left
	}
	if observed.Tokens > 0 {
		capacity := float64(observed.Tokens) / used * 100
		total := math.Max(0, float64(view.LocalTotals.Tokens-currentAllowance.Tokens)) + capacity
		left := capacity * remaining
		view.EstimatedTotalTokens, view.RemainingLocalTokens = &total, &left
	}
	switch {
	case used >= 80:
		view.ForecastConfidence = "reliable"
	case used >= 30:
		view.ForecastConfidence = "reference"
	case used >= 10:
		view.ForecastConfidence = "rough"
	default:
		view.ForecastConfidence = "early"
	}
}

// TimelineQuery 选择账号及额度窗口
type TimelineQuery struct {
	App        *model.QuotaApp `json:"app"`
	AccountKey string          `json:"accountKey"`
	LimitKind  model.LimitKind `json:"limitKind"`
}

// TimelineAccount 表示时间线中的一个账号
type TimelineAccount struct {
	Key     string           `json:"key"`
	Name    string           `json:"name"`
	App     model.QuotaApp   `json:"app"`
	Periods []history.Period `json:"periods"`
}

// TimelineView 返回按账号分区的额度时间线
type TimelineView struct {
	Accounts []TimelineAccount `json:"accounts"`
}

// GetTimeline 查询当天或本周期与上一周期额度变化
func (service *Service) GetTimeline(query TimelineQuery) TimelineView {
	result := TimelineView{Accounts: []TimelineAccount{}}
	snapshot := service.GetSnapshot()
	for _, key := range service.history.Keys() {
		app := model.Codex
		if strings.HasPrefix(key, "claude:") {
			app = model.Claude
		}
		if strings.HasPrefix(key, "antigravity:") {
			app = model.Antigravity
		}
		if query.App != nil && *query.App != app || query.AccountKey != "" && query.AccountKey != key {
			continue
		}
		result.Accounts = append(result.Accounts, TimelineAccount{
			Key: key, Name: readableAccount(key, snapshot.Providers, snapshot.ClaudeAccounts, snapshot.ImportedCodexAccounts), App: app,
			Periods: service.history.Periods(key, query.LimitKind, time.Now()),
		})
	}
	return result
}
