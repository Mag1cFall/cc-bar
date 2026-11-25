package history

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mag1cFall/cc-bar/internal/model"
	"github.com/Mag1cFall/cc-bar/internal/providers"
)

// TestClaudeWeeklyCycle 保留未起约束作用的已使用周额度周期
func TestClaudeWeeklyCycle(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root, err := providers.DecodeObject([]byte(`{"seven_day":{"utilization":88,"resets_at":"2030-10-06T08:59:59.868657+08:00"},"limits":[{"kind":"weekly_all","percent":88,"resets_at":"2030-10-06T08:59:59.868657+08:00","is_active":false}]}`))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := providers.ParseClaude(root)
	reset, _ := time.Parse(time.RFC3339Nano, "2030-10-06T08:59:59.868657+08:00")
	if snapshot.PrimaryLimit == nil || snapshot.PrimaryLimit.Window.ResetsAt == nil || !snapshot.PrimaryLimit.Window.ResetsAt.Equal(reset) {
		t.Fatal("周额度重置时间解析丢失")
	}
	if err = store.Record("claude:primary:weekly", model.Claude, *snapshot, "api", reset.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	cycles := store.Cycles().Records
	if len(cycles) != 1 || cycles[0].LimitKind != model.Weekly || cycles[0].LatestUsedPercent != 88 || !cycles[0].EndAt.Equal(reset) {
		t.Fatalf("周额度周期丢失: %+v", cycles)
	}
}

// TestAccountCycles 验证额外重置与导入账号的时间线分区
func TestAccountCycles(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Truncate(time.Second)
	reset := at.Add(2 * time.Hour)
	seconds := 18000
	snapshot := model.QuotaSnapshot{App: model.Codex, PrimaryLimit: &model.QuotaLimit{
		ID: "five-hour", Kind: model.FiveHour, Window: model.QuotaWindow{UsedPercent: 80, ResetsAt: &reset, WindowSeconds: &seconds},
	}}
	if err = store.Record("codex:primary:one", model.Codex, snapshot, "api", at); err != nil {
		t.Fatal(err)
	}
	snapshot.PrimaryLimit.Window.UsedPercent = 10
	if err = store.Record("codex:primary:one", model.Codex, snapshot, "api", at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	snapshot.PrimaryLimit.Window.UsedPercent = 20
	if err = store.Record("codex:primary:one", model.Codex, snapshot, "api", at.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err = store.Record("codex:imported:two", model.Codex, snapshot, "api", at.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	payload := store.Cycles()
	if _, err = Open(store.directory); err != nil {
		t.Fatalf("历史文件重新读取失败: %v", err)
	}
	if len(payload.AccountSegments) != 1 || payload.AccountSegments[0].AccountKey != "codex:primary:one" {
		t.Fatalf("导入账号改变了本地归属: %+v", payload.AccountSegments)
	}
	record := payload.Records[0]
	if record.ExtraResets() != 1 || record.ReportedUsed() != 100 {
		t.Fatalf("额外重置统计错误: %+v", record)
	}
	periods := store.Periods("codex:primary:one", model.FiveHour, at)
	if len(periods) != 1 || len(periods[0].Entries) != 2 {
		t.Fatalf("时间线变化丢失: %+v", periods)
	}
	if periods[0].TotalDelta != 10 {
		t.Fatalf("重置混入了实际消耗: %d", periods[0].TotalDelta)
	}
	later := store.Periods("codex:primary:one", model.FiveHour, at.AddDate(0, 0, 2))
	if len(later) != 2 || len(later[0].Entries) != 0 || len(later[1].Entries) != 2 {
		t.Fatalf("历史日期时间线丢失: %+v", later)
	}
	if os.Getenv("CCBAR_VERIFY_EXISTING") == "1" {
		existing, openErr := Open(filepath.Join(os.Getenv("LOCALAPPDATA"), "CCBar"))
		if openErr != nil {
			t.Fatal(openErr)
		}
		shortEntries, weeklyEntries, shortDates := 0, 0, 0
		for _, key := range existing.Keys() {
			for _, period := range existing.Periods(key, model.FiveHour, time.Now()) {
				shortEntries += len(period.Entries)
				if len(period.Entries) > 0 {
					shortDates++
				}
			}
			for _, period := range existing.Periods(key, model.Weekly, time.Now()) {
				weeklyEntries += len(period.Entries)
			}
		}
		t.Logf("只读真实历史: %d账号，%d周期，%d短窗日期/%d采样，%d周额度采样", len(existing.Keys()), len(existing.Cycles().Records), shortDates, shortEntries, weeklyEntries)
	}
}

// TestRecordBatchAndFlush 验证批量记录与延迟采样在Flush后完整落盘
func TestRecordBatchAndFlush(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Truncate(time.Second)
	reset := at.Add(3 * time.Hour)
	snap := model.QuotaSnapshot{App: model.Codex, PrimaryLimit: &model.QuotaLimit{
		ID: "five-hour", Kind: model.FiveHour, Window: model.QuotaWindow{UsedPercent: 30, ResetsAt: &reset},
	}}
	if err = store.RecordBatch([]RecordInput{
		{Key: "codex:imported:a", App: model.Codex, Snapshot: snap, Source: "api", At: at},
		{Key: "codex:imported:b", App: model.Codex, Snapshot: snap, Source: "api", At: at},
	}); err != nil {
		t.Fatal(err)
	}
	if err = store.Record("codex:imported:a", model.Codex, snap, "api", at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err = store.Flush(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Keys()) != 2 {
		t.Fatalf("批量记录重载数量错误: %+v", reloaded.Keys())
	}
}

// TestFlushSkipsCleanAndMarksCleanupDirty 验证无脏不重写与跨日清理正确标脏落盘
func TestFlushSkipsCleanAndMarksCleanupDirty(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Flush(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"quota-history.json", "quota-cycle-history.json"} {
		if _, statErr := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(statErr) {
			t.Fatalf("空状态Flush不应创建文件 %s: %v", name, statErr)
		}
	}

	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.Local)
	reset := at.Add(3 * time.Hour)
	snap := model.QuotaSnapshot{App: model.Codex, PrimaryLimit: &model.QuotaLimit{
		ID: "five-hour", Kind: model.FiveHour, Window: model.QuotaWindow{UsedPercent: 25, ResetsAt: &reset},
	}}
	if err = store.Record("codex:primary:clean", model.Codex, snap, "api", at); err != nil {
		t.Fatal(err)
	}
	if err = store.Flush(); err != nil {
		t.Fatal(err)
	}

	past := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	for _, name := range []string{"quota-history.json", "quota-cycle-history.json"} {
		p := filepath.Join(dir, name)
		if err = os.Chtimes(p, past, past); err != nil {
			t.Fatal(err)
		}
	}
	if err = store.Flush(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"quota-history.json", "quota-cycle-history.json"} {
		info, statErr := os.Stat(filepath.Join(dir, name))
		if statErr != nil {
			t.Fatal(statErr)
		}
		if !info.ModTime().Equal(past) {
			t.Fatalf("无脏Flush不应重写文件 %s: got=%v want=%v", name, info.ModTime(), past)
		}
	}

	// 无limit的跨日采样也须将过期样本清理标脏并落盘
	future := at.AddDate(0, 0, 20)
	if err = store.Record("antigravity:primary:empty", model.Antigravity, model.QuotaSnapshot{App: model.Antigravity}, "api", future); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Keys()) != 0 {
		t.Fatalf("跨日清理过期采样未标脏落盘: %+v", reloaded.Keys())
	}
}

// TestAntigravityCycles 验证 Antigravity 额度采样按 UsageAntigravity 记录周期
func TestAntigravityCycles(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	reset := at.Add(3 * time.Hour)
	seconds := 18000
	snap := model.QuotaSnapshot{
		App: model.Antigravity,
		PrimaryLimit: &model.QuotaLimit{
			ID: "gemini-5h", Kind: model.FiveHour, Window: model.QuotaWindow{UsedPercent: 35, ResetsAt: &reset, WindowSeconds: &seconds},
		},
	}
	if err = store.Record("antigravity:primary:test", model.Antigravity, snap, "api", at); err != nil {
		t.Fatal(err)
	}
	if err = store.Flush(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	cycles := reloaded.Cycles()
	if len(cycles.Records) != 1 || cycles.Records[0].App != model.UsageAntigravity || cycles.Records[0].LatestUsedPercent != 35 {
		t.Fatalf("Antigravity 周期记录错误: %+v", cycles.Records)
	}
}

// TestAntigravityLimitIDIsolation 验证不同 LimitID 回退采样不会互相合并或触发误判重置
func TestAntigravityLimitIDIsolation(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	resetGemini := at.Add(3 * time.Hour)
	resetClaude := resetGemini.Add(15 * time.Second)
	seconds := 18000

	geminiSnap := model.QuotaSnapshot{
		App: model.Antigravity,
		PrimaryLimit: &model.QuotaLimit{
			ID: "gemini-5h", Kind: model.FiveHour, Window: model.QuotaWindow{UsedPercent: 80, ResetsAt: &resetGemini, WindowSeconds: &seconds},
		},
	}
	if err = store.Record("antigravity:primary:iso", model.Antigravity, geminiSnap, "api", at); err != nil {
		t.Fatal(err)
	}

	// 紧随其后发生主额度回退到 claude-5h，重置时间仅差 15 秒且利用率骤降
	claudeSnap := model.QuotaSnapshot{
		App: model.Antigravity,
		PrimaryLimit: &model.QuotaLimit{
			ID: "claude-5h", Kind: model.FiveHour, Window: model.QuotaWindow{UsedPercent: 15, ResetsAt: &resetClaude, WindowSeconds: &seconds},
		},
	}
	if err = store.Record("antigravity:primary:iso", model.Antigravity, claudeSnap, "api", at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	records := store.Cycles().Records
	if len(records) != 2 {
		t.Fatalf("不同 LimitID 不应合并为同一周期: %+v", records)
	}
	byLimit := map[string]CycleRecord{}
	for _, rec := range records {
		byLimit[rec.LimitID] = rec
	}
	gem, okGem := byLimit["gemini-5h"]
	cla, okCla := byLimit["claude-5h"]
	if !okGem || !okCla {
		t.Fatalf("缺少独立周期记录: %+v", records)
	}
	if gem.LatestUsedPercent != 80 || gem.ExtraResets() != 0 {
		t.Fatalf("gemini-5h 周期被污染或误判重置: %+v", gem)
	}
	if cla.LatestUsedPercent != 15 || cla.ExtraResets() != 0 {
		t.Fatalf("claude-5h 周期状态错误: %+v", cla)
	}
	if !gem.EndAt.Equal(resetGemini) {
		t.Fatalf("不同 LimitID 新建周期不应截断旧周期结束时间: got=%v want=%v", gem.EndAt, resetGemini)
	}
}
