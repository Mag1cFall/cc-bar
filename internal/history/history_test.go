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
