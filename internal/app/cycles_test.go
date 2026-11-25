package app

import (
	"context"
	"database/sql"
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

func testCyclePBVarint(v uint64) []byte {
	var out []byte
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			b |= 0x80
		}
		out = append(out, b)
		if v == 0 {
			return out
		}
	}
}

func testCyclePBBytes(fn uint32, payload []byte) []byte {
	out := append(testCyclePBVarint(uint64(fn)<<3|2), testCyclePBVarint(uint64(len(payload)))...)
	return append(out, payload...)
}

func testCycleAgBlob(modelName string, input int64) []byte {
	usage := append(testCyclePBVarint(2<<3), testCyclePBVarint(uint64(input))...)
	chat := append(testCyclePBBytes(4, usage), testCyclePBBytes(19, []byte(modelName))...)
	return testCyclePBBytes(1, chat)
}

// TestGetCyclesAntigravityModelFamilyScope 验证 Antigravity 周期仅汇总对应模型族本地消费
func TestGetCyclesAntigravityModelFamilyScope(t *testing.T) {
	dataDir := t.TempDir()
	home := t.TempDir()
	start := time.Now().UTC().Truncate(time.Hour).Add(-10 * time.Hour)
	end := start.Add(5 * time.Hour)
	sampleAt := start.Add(time.Hour)

	agDir := filepath.Join(home, ".gemini", "antigravity", "conversations")
	if err := os.MkdirAll(agDir, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(agDir, "conv-family.db")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE gen_metadata (idx INTEGER PRIMARY KEY, data BLOB, size INTEGER NOT NULL DEFAULT 0);
CREATE TABLE steps (idx INTEGER PRIMARY KEY, step_type INTEGER NOT NULL DEFAULT 0, metadata BLOB);
CREATE TABLE trajectory_metadata_blob (id TEXT PRIMARY KEY DEFAULT 'main', data BLOB);`); err != nil {
		t.Fatal(err)
	}
	ts := append(testCyclePBVarint(1<<3), testCyclePBVarint(uint64(sampleAt.Unix()))...)
	if _, err = db.Exec("INSERT INTO trajectory_metadata_blob(id,data) VALUES('main',?)", testCyclePBBytes(2, ts)); err != nil {
		t.Fatal(err)
	}
	gemBlob := testCycleAgBlob("gemini-4-neon", 1_000_000)
	claBlob := testCycleAgBlob("claude-sonnet-4-5", 1_000_000)
	if _, err = db.Exec("INSERT INTO gen_metadata(idx,data,size) VALUES(0,?,?),(1,?,?)", gemBlob, len(gemBlob), claBlob, len(claBlob)); err != nil {
		t.Fatal(err)
	}
	db.Close()

	payload := history.CyclePayload{
		Version:           4,
		TrackingStartedAt: start.Add(-time.Hour),
		Records: []history.CycleRecord{
			{
				ID: "ag|gemini-5h|1", AccountKey: "antigravity:primary:acct", App: model.UsageAntigravity,
				LimitID: "gemini-5h", LimitKind: model.FiveHour, StartAt: start, EndAt: end, ScheduledEndAt: end,
				FirstSampleAt: &sampleAt, LastSampleAt: &sampleAt, LatestUsedPercent: 50,
				AllowanceSegments: []history.AllowanceSegment{{ID: "s1", StartAt: start, BaselineUsedPercent: 0, LatestUsedPercent: 50, MaximumUsedPercent: 50, FirstSampleAt: sampleAt, LastSampleAt: sampleAt}},
			},
			{
				ID: "ag|claude-5h|1", AccountKey: "antigravity:primary:acct", App: model.UsageAntigravity,
				LimitID: "claude-5h", LimitKind: model.FiveHour, StartAt: start, EndAt: end, ScheduledEndAt: end,
				FirstSampleAt: &sampleAt, LastSampleAt: &sampleAt, LatestUsedPercent: 50,
				AllowanceSegments: []history.AllowanceSegment{{ID: "s2", StartAt: start, BaselineUsedPercent: 0, LatestUsedPercent: 50, MaximumUsedPercent: 50, FirstSampleAt: sampleAt, LastSampleAt: sampleAt}},
			},
		},
		AccountSegments: []history.AccountSegment{
			{ID: "seg-ag", AccountKey: "antigravity:primary:acct", App: model.UsageAntigravity, StartAt: start.Add(-time.Hour)},
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dataDir, "quota-cycle-history.json"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	service, err := New(dataDir, home, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Stop()
	if err = service.usage.Scan(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	views, err := service.GetCycles(context.Background())
	if err != nil || len(views.Records) != 2 {
		t.Fatalf("GetCycles 失败: err=%v count=%d", err, len(views.Records))
	}
	byLimit := map[string]CycleView{}
	for _, rec := range views.Records {
		byLimit[rec.LimitID] = rec
	}
	gem := byLimit["gemini-5h"]
	cla := byLimit["claude-5h"]
	if gem.LocalTotals.Requests != 1 || math.Abs(gem.LocalTotals.Cost-3) > 1e-6 || gem.EstimatedTotalCost == nil || math.Abs(*gem.EstimatedTotalCost-6) > 1e-6 {
		t.Fatalf("gemini-5h 周期混入了非 Gemini 消费: %+v", gem)
	}
	if cla.LocalTotals.Requests != 1 || math.Abs(cla.LocalTotals.Cost-3) > 1e-6 || cla.EstimatedTotalCost == nil || math.Abs(*cla.EstimatedTotalCost-6) > 1e-6 {
		t.Fatalf("claude-5h 周期本地归属错误: %+v", cla)
	}
}
