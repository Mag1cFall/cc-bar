package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
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

// TestGetCyclesFutureNotCached 验证未开始的未来周期不写入已完成周期缓存
func TestGetCyclesFutureNotCached(t *testing.T) {
	dataDir := t.TempDir()
	now := time.Now().UTC()
	payload := history.CyclePayload{
		Version:           4,
		TrackingStartedAt: now.Add(-24 * time.Hour),
		Records: []history.CycleRecord{
			{
				ID:             "finished-cycle",
				AccountKey:     "codex:primary:acct",
				App:            model.UsageCodex,
				LimitID:        "primary",
				LimitKind:      model.FiveHour,
				StartAt:        now.Add(-10 * time.Hour),
				EndAt:          now.Add(-5 * time.Hour),
				ScheduledEndAt: now.Add(-5 * time.Hour),
			},
			{
				ID:             "future-cycle",
				AccountKey:     "codex:primary:acct",
				App:            model.UsageCodex,
				LimitID:        "primary",
				LimitKind:      model.FiveHour,
				StartAt:        now.Add(time.Hour),
				EndAt:          now.Add(6 * time.Hour),
				ScheduledEndAt: now.Add(6 * time.Hour),
			},
		},
		AccountSegments: []history.AccountSegment{
			{ID: "seg-1", AccountKey: "codex:primary:acct", App: model.UsageCodex, StartAt: now.Add(-24 * time.Hour)},
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "quota-cycle-history.json"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	service, err := New(dataDir, t.TempDir(), "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Stop()
	views, err := service.GetCycles(context.Background())
	if err != nil || len(views.Records) != 2 {
		t.Fatalf("GetCycles 失败: err=%v count=%d", err, len(views.Records))
	}
	service.cyclesCacheMu.Lock()
	cacheLen := len(service.cyclesCache)
	finishedHit := false
	futureHit := false
	for key := range service.cyclesCache {
		if strings.HasPrefix(key, "finished-cycle|") {
			finishedHit = true
		}
		if strings.HasPrefix(key, "future-cycle|") {
			futureHit = true
		}
	}
	service.cyclesCacheMu.Unlock()
	if cacheLen != 1 || !finishedHit || futureHit {
		t.Fatalf("仅应缓存已结束周期而非未来周期: len=%d finished=%t future=%t", cacheLen, finishedHit, futureHit)
	}
}
