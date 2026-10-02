package app

import (
	"math"
	"testing"
	"time"

	"github.com/Mag1cFall/cc-bar/internal/history"
	"github.com/Mag1cFall/cc-bar/internal/model"
)

// TestCycleForecast 验证账号切换区间与额外重置后的同区间预测
func TestCycleForecast(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(5 * time.Hour)
	record := history.CycleRecord{AccountKey: "account-one", App: model.UsageCodex, StartAt: start, EndAt: end}
	switchAt := start.Add(time.Hour)
	segments := []history.AccountSegment{
		{AccountKey: "account-one", App: model.UsageCodex, StartAt: start, EndAt: &switchAt},
		{AccountKey: "account-two", App: model.UsageCodex, StartAt: switchAt},
		{AccountKey: "account-one", App: model.UsageClaude, StartAt: start},
	}
	intervals := accountIntervals(start, end, record, segments)
	if len(intervals) != 1 || !intervals[0].From.Equal(start) || !intervals[0].To.Equal(switchAt) {
		t.Fatalf("账号切换后的归属错误: %+v", intervals)
	}
	view := CycleView{CycleRecord: history.CycleRecord{LatestUsedPercent: 60}, LocalTotals: model.UsageTotals{Tokens: 250, Cost: 25}}
	observed := model.UsageTotals{Tokens: 100, Cost: 10, Requests: 2}
	allowance := history.AllowanceSegment{BaselineUsedPercent: 10, MaximumUsedPercent: 60}
	setCycleForecast(&view, observed, model.UsageTotals{Tokens: 150, Cost: 15}, allowance)
	if view.EstimatedTotalTokens == nil || *view.EstimatedTotalTokens != 300 || view.RemainingLocalTokens == nil || *view.RemainingLocalTokens != 80 || view.EstimatedTotalCost == nil || math.Abs(*view.EstimatedTotalCost-30) > 1e-9 || view.ForecastConfidence != "reference" {
		t.Fatalf("额外重置与基准消耗推算错误: %+v", view)
	}
}
