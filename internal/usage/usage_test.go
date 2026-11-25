package usage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mag1cFall/cc-bar/internal/model"
	"github.com/klauspost/compress/zstd"
)

// writeFixture 创建当前测试所需的日志文件
func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

// TestUsageMigration 核验全部来源增量扫描及既有SQLite聚合格式
func TestUsageMigration(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	dataDir := t.TempDir()
	store, err := Open(dataDir, home)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	connections := []*sql.Conn{}
	for range 4 {
		connection, connectionErr := store.db.Conn(ctx)
		if connectionErr != nil {
			t.Fatal(connectionErr)
		}
		connections = append(connections, connection)
		var timeout int
		if connectionErr = connection.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&timeout); connectionErr != nil || timeout != 10000 {
			t.Fatalf("连接等待设置不一致: %d %v", timeout, connectionErr)
		}
	}
	for _, connection := range connections {
		connection.Close()
	}
	at := time.Date(2026, 9, 20, 13, 0, 0, 0, time.Local)
	stamp := at.Format(time.RFC3339)
	milliseconds := at.UnixMilli()
	codex := filepath.Join(home, ".codex", "sessions", "session.jsonl")
	writeFixture(t, codex, fmt.Sprintf(`{"type":"session_meta","payload":{"id":"codex-session","cwd":"%s"}}
{"type":"turn_context","payload":{"model":"openai/gpt-5.5"}}
{"type":"event_msg","payload":{"type":"user_message","message":"修复页面 布局"}}
{"timestamp":"%s","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":110,"cached_input_tokens":20,"cache_write_tokens":10,"output_tokens":30},"total_token_usage":{"input_tokens":110,"output_tokens":30}}}}
{"timestamp":"%s","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":110,"cached_input_tokens":20,"cache_write_tokens":10,"output_tokens":30},"total_token_usage":{"input_tokens":110,"output_tokens":30}}}}
`, strings.ReplaceAll(home, "\\", "\\\\"), stamp, stamp))
	claude := filepath.Join(home, ".claude", "projects", "project", "thread.jsonl")
	writeFixture(t, claude, fmt.Sprintf(`{"type":"user","sessionId":"claude-session","message":{"content":[{"type":"text","text":"优化统计页面"}]}}
{"type":"assistant","sessionId":"claude-session","cwd":"%s","gitBranch":"main","timestamp":"%s","message":{"id":"claude-message","model":"claude-sonnet-4-6-20260101","stop_reason":"end_turn","usage":{"input_tokens":100,"output_tokens":40,"cache_read_input_tokens":20,"cache_creation_input_tokens":30,"cache_creation":{"ephemeral_5m_input_tokens":10,"ephemeral_1h_input_tokens":20},"speed":"standard"}}}
`, strings.ReplaceAll(home, "\\", "\\\\"), stamp))
	pi := filepath.Join(home, ".pi", "agent", "sessions", "thread.jsonl")
	writeFixture(t, pi, fmt.Sprintf(`{"type":"session","id":"pi-session","cwd":"%s"}
{"type":"message","id":"pi-user","timestamp":"%s","message":{"role":"user","content":"审查实现"}}
{"type":"message","id":"pi-message","timestamp":"%s","message":{"role":"assistant","provider":"openai","model":"gpt-5.5","usage":{"input":11,"output":12,"cacheRead":13,"cacheWrite":14,"cost":{"total":0.25}}}}
`, strings.ReplaceAll(home, "\\", "\\\\"), stamp, stamp))
	omp := filepath.Join(home, ".omp", "agent", "sessions", "project", "root.jsonl")
	writeFixture(t, omp, fmt.Sprintf(`{"type":"title","title":"OMP会话"}
{"type":"session","id":"omp-session","cwd":"%s"}
{"type":"service_tier_change","id":"tier","timestamp":"%s","serviceTier":{"openai":"priority"}}
{"type":"message","id":"omp-message","timestamp":"%s","message":{"role":"assistant","provider":"openai-codex","model":"gpt-5.5","usage":{"input":10,"output":20,"cacheRead":5,"cacheWrite":0,"cost":{"input":0.1,"output":0.2,"cacheRead":0.03,"cacheWrite":0,"total":0.33}}}}
{"type":"model_usage","id":"omp-background","timestamp":"%s","provider":"anthropic","model":"claude-sonnet-4-6","usage":{"input":2,"output":3,"cacheRead":0,"cacheWrite":0,"cost":{"input":0.01,"output":0.02,"cacheRead":0,"cacheWrite":0,"total":0.03}}}
`, strings.ReplaceAll(home, "\\", "\\\\"), stamp, stamp, stamp))
	writeFixture(t, filepath.Join(home, ".omp", "agent", "sessions", "project", "root", "advisor.jsonl"), fmt.Sprintf(`{"type":"session","id":"omp-advisor"}
{"type":"message","id":"omp-advisor-message","timestamp":"%s","message":{"role":"assistant","provider":"anthropic","model":"claude-sonnet-4-6","usage":{"input":4,"output":6,"cacheRead":0,"cacheWrite":0,"cost":{"input":0.04,"output":0.06,"cacheRead":0,"cacheWrite":0,"total":0.1}}}}
`, stamp))
	dsh := filepath.Join(home, ".dsh", "sessions", "root", "session.v2.jsonl.zstd")
	dshText := fmt.Sprintf(`{"type":"session","id":"dsh-root","cwd":"%s"}
{"type":"session/title","data":{"title":"DSH主会话"}}
{"type":"request/context","data":{"provider":"deepseek","model":"deepseek-flash"}}
{"type":"assistant/attempt","time":%d,"data":{"turn":1,"step":1,"usage":{"inputTokens":20,"outputTokens":10}}}
{"type":"assistant/message","time":%d,"data":{"turn":1,"step":1,"usage":{"inputTokens":25,"outputTokens":15}}}
`, strings.ReplaceAll(home, "\\", "\\\\"), milliseconds, milliseconds)
	if err = os.MkdirAll(filepath.Dir(dsh), 0700); err != nil {
		t.Fatal(err)
	}
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	compressed := encoder.EncodeAll([]byte(dshText), nil)
	encoder.Close()
	compressed = append(compressed, 0x28, 0xb5, 0x2f)
	if err = os.WriteFile(dsh, compressed, 0600); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(home, ".dsh", "sessions", "child", "session.jsonl"), fmt.Sprintf(`{"type":"session","id":"dsh-child","parentSession":"dsh-root","seedLength":2}
{"type":"assistant/message","seq":1,"time":%d,"data":{"usage":{"inputTokens":999}}}
{"type":"request/context","seq":2,"data":{"provider":"deepseek","model":"deepseek-flash"}}
{"type":"assistant/message","seq":3,"time":%d,"data":{"turn":1,"step":1,"usage":{"inputTokens":5,"outputTokens":5}}}
`, milliseconds, milliseconds))
	openCodePath := filepath.Join(home, ".local", "share", "opencode", "opencode.db")
	if err = os.MkdirAll(filepath.Dir(openCodePath), 0700); err != nil {
		t.Fatal(err)
	}
	openCode, err := sql.Open("sqlite", openCodePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = openCode.Exec(`CREATE TABLE workspace(id TEXT,branch TEXT);CREATE TABLE session(id TEXT,title TEXT,directory TEXT,workspace_id TEXT);CREATE TABLE message(id TEXT,session_id TEXT,time_created INTEGER,time_updated INTEGER NOT NULL DEFAULT 0,data TEXT);CREATE TABLE part(id TEXT,session_id TEXT,time_created INTEGER,data TEXT);INSERT INTO workspace VALUES('workspace','feat');INSERT INTO session VALUES('open-session','OpenCode会话','','workspace');`); err != nil {
		t.Fatal(err)
	}
	if _, err = openCode.Exec("INSERT INTO message(id,session_id,time_created,data) VALUES(?,?,?,?)", "open-message", "open-session", milliseconds, `{"role":"assistant","providerID":"anthropic","modelID":"claude-sonnet-4-6","cost":0.5,"tokens":{"input":10,"output":20,"reasoning":5,"cache":{"read":30,"write":40}}}`); err != nil {
		t.Fatal(err)
	}
	openCode.Close()
	writeFixture(t, filepath.Join(home, ".codex", "session_index.jsonl"), "{\"id\":\"codex-session\",\"thread_name\":\"对齐布局索引\"}\n")
	if err = store.Scan(ctx, false); err != nil {
		t.Fatal(err)
	}
	from, to := at.Add(-time.Hour), at.Add(time.Hour)
	totals, err := store.Totals(ctx, nil, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if totals.Requests != 9 || totals.Tokens != 585 || totals.CacheWrite1h != 20 {
		t.Fatalf("来源统计不一致: %+v", totals)
	}
	intervalTotals, err := store.TotalsInIntervals(ctx, model.UsageOmp, []Interval{{From: at, To: at.Add(time.Millisecond)}})
	if err != nil || intervalTotals.Requests != 3 || intervalTotals.Tokens != 50 {
		t.Fatalf("额度归属区间聚合错误: %+v %v", intervalTotals, err)
	}
	if earliest, firstErr := store.FirstEventAt(ctx, model.UsageOmp); firstErr != nil || earliest == nil || !earliest.Equal(at) {
		t.Fatalf("首条计量时间错误: %v %v", earliest, firstErr)
	}
	overview, err := store.Overview(ctx, model.UsageQuery{From: from.Format(time.RFC3339), To: to.Format(time.RFC3339), Grain: "day", Compare: true})
	if err != nil {
		t.Fatal(err)
	}
	if overview.Totals.Requests != 9 || overview.Totals.Tokens != totals.Tokens || overview.PreviousTotals.Requests != 0 || len(overview.Services) != 6 || len(overview.Buckets) != 14 {
		t.Fatalf("时分范围或聚合不一致: %+v", overview)
	}
	visible, err := store.Overview(ctx, model.UsageQuery{AllowedApps: map[model.UsageApp]bool{model.UsageOmp: true}, From: from.Format(time.RFC3339), To: to.Format(time.RFC3339)})
	if err != nil || visible.Totals.Requests != 3 || len(visible.Services) != 1 {
		t.Fatalf("总览来源过滤错误: %+v %v", visible, err)
	}
	visiblePage, err := store.Conversations(ctx, model.ConversationQuery{AllowedApps: map[model.UsageApp]bool{model.UsageOmp: true}})
	if err != nil || visiblePage.Total != 1 || len(visiblePage.Items) != 1 || visiblePage.Items[0].App != model.UsageOmp {
		t.Fatalf("对话来源过滤错误: %+v %v", visiblePage, err)
	}
	emptyPage, err := store.Conversations(ctx, model.ConversationQuery{AllowedApps: map[model.UsageApp]bool{}})
	if err != nil || emptyPage.Total != 0 || len(emptyPage.Projects) != 0 {
		t.Fatalf("空来源过滤错误: %+v %v", emptyPage, err)
	}
	allOverview, err := store.Overview(ctx, model.UsageQuery{AllowedApps: map[model.UsageApp]bool{model.UsageOmp: true}, From: time.UnixMilli(0).Format(time.RFC3339), To: to.Format(time.RFC3339)})
	if err != nil || len(allOverview.Buckets) != 14 || allOverview.Totals.Requests != 3 {
		t.Fatalf("全部时间图表范围错误: %d桶 %v", len(allOverview.Buckets), err)
	}
	page, err := store.Conversations(ctx, model.ConversationQuery{Search: "对齐", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.Items[0].Title != "对齐布局索引" {
		t.Fatalf("全局搜索失败: %+v", page)
	}
	detail, err := store.Detail(ctx, "dsh:dsh-root")
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Events) != 2 || !detail.Conversation.Subtasks || detail.Conversation.Totals.Tokens != 50 {
		t.Fatalf("DSH去重或子会话归并错误: %+v", detail)
	}
	ompDetail, err := store.Detail(ctx, "omp:omp-session")
	if err != nil {
		t.Fatal(err)
	}
	if ompDetail.Conversation.Totals.Requests != 3 || !ompDetail.Conversation.Subtasks || ompDetail.Conversation.Speed != "mixed" || math.Abs(ompDetail.Costs.Input-.15) > 1e-9 {
		t.Fatalf("OMP子代理后台用量或上报价格错误: %+v", ompDetail)
	}
	if err = store.Scan(ctx, false); err != nil {
		t.Fatal(err)
	}
	same, err := store.Totals(ctx, nil, from, to)
	if err != nil || same != totals {
		t.Fatalf("重复扫描发生重复统计: %+v %v", same, err)
	}
	appendFile, err := os.OpenFile(codex, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = appendFile.WriteString(fmt.Sprintf("{\"timestamp\":\"%s\",\"type\":\"event_msg\",\"payload\":{\"type\":\"token_count\",\"info\":{\"last_token_usage\":{\"input_tokens\":12,\"output_tokens\":8},\"total_token_usage\":{\"input_tokens\":122,\"output_tokens\":38}}}}\r\n{\"type\":", stamp))
	appendFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Scan(ctx, false); err != nil {
		t.Fatal(err)
	}
	after, err := store.Totals(ctx, nil, from, to)
	if err != nil || after.Requests != 10 || after.Tokens != 605 {
		t.Fatalf("CRLF增量错误: %+v %v", after, err)
	}
	var offset int64
	if err = store.db.QueryRow("SELECT offset FROM scan_file WHERE path=?", codex).Scan(&offset); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(codex)
	if offset != info.Size()-int64(len("{\"type\":")) {
		t.Fatalf("不完整行偏移错误 %d %d", offset, info.Size())
	}
	insertRow := func(row model.UsageRow, source string) {
		t.Helper()
		batch, bErr := store.beginWrite(ctx)
		if bErr != nil {
			t.Fatal(bErr)
		}
		if bErr = batch.saveUsage(ctx, row, source); bErr != nil {
			batch.rollback()
			t.Fatal(bErr)
		}
		if bErr = batch.commit(ctx); bErr != nil {
			t.Fatal(bErr)
		}
	}
	legacyCost := 0.75
	insertRow(model.UsageRow{ID: "legacy", App: model.UsageCursor, Conversation: "cursor:daily", Model: "legacy-model", Speed: "standard", Time: at, Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4, Cost: &legacyCost, Requests: 1}, "cursor:legacy")
	if err = store.Scan(ctx, false); err != nil {
		t.Fatal(err)
	}
	cursor := model.UsageCursor
	legacy, err := store.Totals(ctx, &cursor, from, to)
	if err != nil || legacy.Requests != 1 || legacy.Cost != .75 {
		t.Fatalf("既有SQLite行错误: %+v %v", legacy, err)
	}
	openCode, err = sql.Open("sqlite", openCodePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = openCode.Exec("UPDATE message SET time_updated=?,data=? WHERE id='open-message'", milliseconds+1000, `{"role":"assistant","providerID":"anthropic","modelID":"claude-sonnet-4-6","cost":0.6,"tokens":{"input":10,"output":30,"reasoning":5,"cache":{"read":30,"write":40}}}`); err != nil {
		t.Fatal(err)
	}
	openCode.Close()
	if _, err = store.db.Exec("UPDATE scan_meta SET value=? WHERE name IN('opencode','opencode-updated')", milliseconds+100); err != nil {
		t.Fatal(err)
	}
	if _, err = store.scanOpenCode(ctx, false); err != nil {
		t.Fatal(err)
	}
	openApp := model.UsageOpencode
	updated, err := store.Totals(ctx, &openApp, from, to)
	if err != nil || updated.Requests != 1 || updated.Tokens != 115 || updated.Cost != .6 {
		t.Fatalf("OpenCode已创建消息更新错误: %+v %v", updated, err)
	}
	query := model.UsageQuery{From: from.Format(time.RFC3339), To: to.Format(time.RFC3339)}
	fast, err := store.Overview(ctx, query)
	if err != nil || fast.Fast.MultiplierText != "2.5×" {
		t.Fatalf("单一Fast倍率错误: %+v %v", fast.Fast, err)
	}
	fastCost := 0.1
	insertRow(model.UsageRow{ID: "fast-claude", App: model.UsageClaude, Conversation: "test-fast", Model: "claude-opus-4-6", Speed: "fast", Time: at, Input: 10, Output: 20, Cost: &fastCost, Requests: 1}, "test")
	fast, err = store.Overview(ctx, query)
	if err != nil || fast.Fast.MultiplierText != "2.5–6×" {
		t.Fatalf("混合Fast倍率错误: %+v %v", fast.Fast, err)
	}
	insertRow(model.UsageRow{ID: "fast-unknown", App: model.UsageClaude, Conversation: "test-fast", Model: "unknown", Speed: "fast", Time: at, Input: 10, Output: 20, Requests: 1}, "test")
	fast, err = store.Overview(ctx, query)
	if err != nil || fast.Fast.MultiplierText != "—" || !fast.Fast.UnknownMultiplier {
		t.Fatalf("未知Fast倍率错误: %+v %v", fast.Fast, err)
	}
}

// TestSchemaV1ToV2Migration 验证旧版V1数据库无损幂等升级到V2及预聚合查询一致性
func TestSchemaV1ToV2Migration(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	home := t.TempDir()
	dbPath := filepath.Join(dataDir, "usage.db")

	legacy, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	legacySchema := `
CREATE TABLE usage (
 event_key TEXT PRIMARY KEY,source TEXT NOT NULL,app INTEGER NOT NULL,
 conversation TEXT NOT NULL,model TEXT NOT NULL,speed TEXT NOT NULL,time INTEGER NOT NULL,
 input INTEGER NOT NULL,output INTEGER NOT NULL,cache_read INTEGER NOT NULL,
 cache_write INTEGER NOT NULL,cost REAL,cache_write_1h INTEGER NOT NULL DEFAULT 0);
CREATE INDEX usage_time ON usage(time,app);
CREATE INDEX usage_app_time ON usage(app,time);
CREATE INDEX usage_conversation ON usage(conversation);
CREATE TABLE scan_file (
 path TEXT PRIMARY KEY,length INTEGER NOT NULL,modified INTEGER NOT NULL,
 offset INTEGER NOT NULL,model TEXT,speed TEXT,signature TEXT,
 session TEXT,emitting TEXT,title TEXT,cwd TEXT,branch TEXT,
 subtasks INTEGER NOT NULL DEFAULT 0,sidechain_all INTEGER);
CREATE TABLE conversation (
 id TEXT PRIMARY KEY,title TEXT,cwd TEXT,source TEXT,branch TEXT,
 subtasks INTEGER NOT NULL DEFAULT 0,project TEXT,project_status TEXT,
 cache_write_avail INTEGER NOT NULL DEFAULT 0);
CREATE TABLE scan_meta(name TEXT PRIMARY KEY,value INTEGER NOT NULL);
CREATE TABLE scan_blob(name TEXT PRIMARY KEY,value TEXT NOT NULL);
CREATE TABLE scan_candidate(path TEXT NOT NULL,session TEXT NOT NULL,cwd TEXT NOT NULL,
 sidechain INTEGER NOT NULL,PRIMARY KEY(path,session,cwd,sidechain));`
	if _, err = legacy.Exec(legacySchema); err != nil {
		t.Fatal(err)
	}
	t1 := time.Date(2026, 10, 1, 9, 7, 12, 0, time.Local)
	t2 := time.Date(2026, 10, 1, 10, 20, 0, 0, time.Local)
	t3 := time.Date(2026, 10, 2, 11, 35, 0, 0, time.Local)
	if _, err = legacy.Exec(`
INSERT INTO usage(event_key,source,app,conversation,model,speed,time,input,output,cache_read,cache_write,cost,cache_write_1h) VALUES
 ('codex:s1#1','/tmp/codex1.jsonl',0,'codex:s1','gpt-5.4','standard',?,1000,200,500,0,0.05,0),
 ('claude:m1','/tmp/claude1.jsonl',1,'claude:c1','claude-opus-4-6','fast',?,2000,300,100,50,NULL,20),
 ('claude:m2','/tmp/claude1.jsonl',1,'claude:c1','claude-sonnet-4-6','standard',?,500,100,0,0,0.02,0),
 ('omp:e1@ts','/tmp/omp1.jsonl',6,'omp:o1','wjkde/gpt-5.6-sol','standard',?,400,50,100,0,0.12,0),
 ('dsh:root:1','/tmp/dsh1.zstd',5,'dsh:root','deepseek/deepseek-flash','standard',?,300,40,0,0,0.01,0);
INSERT INTO scan_blob(name,value) VALUES
 ('usage-cost:omp:e1@ts','{"input":0.08,"output":0.03,"cacheRead":0.01,"cacheWrite":0}'),
 ('dsh_roots','{"root":"root"}');
INSERT INTO conversation(id,title,cwd,source,branch,subtasks,project,project_status,cache_write_avail) VALUES
 ('codex:s1','Codex标题','/proj/a','/tmp/codex1.jsonl','main',0,'/proj/a','available',0),
 ('claude:c1','Claude跨日对话','/proj/b','/tmp/claude1.jsonl','dev',1,'/proj/b','available',1),
 ('omp:o1','OMP会话','/proj/c','/tmp/omp1.jsonl','',0,'/proj/c','available',1);
INSERT INTO scan_meta(name,value) VALUES('opencode',12345);`,
		t1.UnixMilli(), t2.UnixMilli(), t3.UnixMilli(), t2.UnixMilli(), t3.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err = legacy.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(dataDir, home)
	if err != nil {
		t.Fatalf("V1到V2迁移失败: %v", err)
	}
	var ver int
	if err = store.db.QueryRow("PRAGMA user_version").Scan(&ver); err != nil || ver != currentSchemaVersion {
		t.Fatalf("schema版本错误: %d %v", ver, err)
	}
	var leftBlobs int
	if err = store.db.QueryRow("SELECT COUNT(*) FROM scan_blob WHERE name LIKE 'usage-cost:%'").Scan(&leftBlobs); err != nil || leftBlobs != 0 {
		t.Fatalf("旧usage-cost未迁出scan_blob: %d %v", leftBlobs, err)
	}
	var dshBlob string
	if err = store.db.QueryRow("SELECT value FROM scan_blob WHERE name='dsh_roots'").Scan(&dshBlob); err != nil || dshBlob != `{"root":"root"}` {
		t.Fatalf("非usage-cost的scan_blob丢失: %q %v", dshBlob, err)
	}

	allTotals, err := store.Totals(ctx, nil, time.UnixMilli(0), t3.Add(time.Hour))
	if err != nil || allTotals.Requests != 5 || allTotals.Input != 4200 || allTotals.Output != 690 || allTotals.CacheRead != 700 || allTotals.CacheWrite != 50 || allTotals.CacheWrite1h != 20 || math.Abs(allTotals.Cost-0.20) > 1e-9 {
		t.Fatalf("迁移后总计不一致: %+v %v", allTotals, err)
	}
	// 非整15分钟边界查询必须与精确事件完全一致
	edgeTotals, err := store.Totals(ctx, nil, t1.Add(-time.Second), t1.Add(time.Second))
	if err != nil || edgeTotals.Requests != 1 || edgeTotals.Input != 1000 {
		t.Fatalf("非对齐毫秒边界Totals不一致: %+v %v", edgeTotals, err)
	}
	// 跨日边界裁剪查询 Conversations
	day1From := time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local)
	day1To := day1From.AddDate(0, 0, 1)
	day1Convs, err := store.Conversations(ctx, model.ConversationQuery{From: day1From.Format(time.RFC3339), To: day1To.Format(time.RFC3339)})
	if err != nil || day1Convs.Total != 3 {
		t.Fatalf("边界裁剪对话数错误: %+v %v", day1Convs, err)
	}
	for _, item := range day1Convs.Items {
		if item.ID == "claude:c1" && (item.Totals.Requests != 1 || item.Totals.Input != 2000 || item.Speed != "fast") {
			t.Fatalf("跨日对话在单日内裁剪聚合错误: %+v", item)
		}
	}
	// 验证 OMP 上报费用拆分已无损迁移至 usage_cost
	ompDetail, err := store.Detail(ctx, "omp:o1")
	if err != nil || math.Abs(ompDetail.Costs.Input-0.08) > 1e-9 || math.Abs(ompDetail.Costs.Output-0.03) > 1e-9 || math.Abs(ompDetail.Costs.CacheRead-0.01) > 1e-9 {
		t.Fatalf("OMP上报费用迁移错误: %+v %v", ompDetail.Costs, err)
	}
	// 验证 PopoverCosts 单次预聚合查询
	todayCosts, weekCosts, err := store.PopoverCosts(ctx, nil, day1To, day1From, day1To.AddDate(0, 0, 1))
	if err != nil || math.Abs(todayCosts[model.UsageClaude]-0.02) > 1e-9 || math.Abs(weekCosts[model.UsageOmp]-0.12) > 1e-9 {
		t.Fatalf("PopoverCosts汇总错误: today=%+v week=%+v err=%v", todayCosts, weekCosts, err)
	}
	// 验证 repriceMissing 通过部分索引更新 cost 并同步 usage_rollup 与 conv_summary
	updated, err := store.repriceMissing(ctx)
	if err != nil || !updated {
		t.Fatalf("repriceMissing未补齐可定价记录: updated=%v err=%v", updated, err)
	}
	afterReprice, err := store.Totals(ctx, nil, time.UnixMilli(0), t3.Add(time.Hour))
	if err != nil || afterReprice.Cost <= 0.20 {
		t.Fatalf("repriceMissing后usage_rollup未同步: %+v %v", afterReprice, err)
	}
	claudeDetail, err := store.Detail(ctx, "claude:c1")
	if err != nil || claudeDetail.Conversation.Totals.Cost <= 0.02 {
		t.Fatalf("repriceMissing后conv_summary未同步: %+v %v", claudeDetail.Conversation, err)
	}
	// 验证再次写入相同 event_key 时 100% 幂等去重
	batch, err := store.beginWrite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	dupCost := 9.99
	if err = batch.saveUsage(ctx, model.UsageRow{ID: "codex:s1#1", App: model.UsageCodex, Conversation: "codex:s1", Model: "gpt-5.4", Speed: "standard", Time: t1, Input: 9999, Cost: &dupCost, Requests: 1}, "/tmp/codex1.jsonl"); err != nil {
		batch.rollback()
		t.Fatal(err)
	}
	if err = batch.commit(ctx); err != nil {
		t.Fatal(err)
	}
	if batch.mutated {
		t.Fatal("重复event_key不应产生写入变更")
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	// 验证再次 Open 幂等
	reopened, err := Open(dataDir, home)
	if err != nil {
		t.Fatalf("迁移后重复Open失败: %v", err)
	}
	defer reopened.Close()
	checkTotals, err := reopened.Totals(ctx, nil, time.UnixMilli(0), t3.Add(time.Hour))
	if err != nil || checkTotals != afterReprice {
		t.Fatalf("重复Open后数据不一致: %+v vs %+v (%v)", checkTotals, afterReprice, err)
	}
	// 验证删除对话唯一来源后 conv_summary 清除不因 NULL 聚合报错
	delBatch, err := reopened.beginWrite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = delBatch.deleteBySource(ctx, "/tmp/codex1.jsonl"); err != nil {
		delBatch.rollback()
		t.Fatal(err)
	}
	if err = delBatch.commit(ctx); err != nil {
		t.Fatalf("清空对话提交失败: %v", err)
	}
	var codexSummary int
	if err = reopened.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM conv_summary cs JOIN usage_conv c ON c.id=cs.conv_id WHERE c.name='codex:s1'").Scan(&codexSummary); err != nil || codexSummary != 0 {
		t.Fatalf("空对话汇总未清除: count=%d err=%v", codexSummary, err)
	}
}

// TestPricing 核验阶段价边界、无GPT长上下文加价、非GPT阶梯、峰谷规则、1h缓存与重算逻辑
func TestPricing(t *testing.T) {
	pricing := NewPricing(t.TempDir())
	pricing.active.LiteLLM.StandardRates["gpt-5.6"] = Price{9, 45, .9, 11.25}
	pricing.active.LiteLLM.StandardRates["gemini-4-carbon"] = Price{7, 35, .35, 0}
	pricing.active.LiteLLM.StandardRates["gemini-3.1-pro-preview"] = Price{2, 12, .2, 0}

	beforePromo := utc(2026, 8, 20, 12)
	inPromo := utc(2026, 9, 20, 12)
	afterPromo := utc(2026, 12, 1, 0)
	dsPreGA := utc(2026, 8, 16, 15)
	dsPeak := utc(2026, 9, 21, 2)
	dsOffPeak := utc(2026, 9, 20, 0)

	cases := []struct {
		name    string
		row     model.UsageRow
		want    float64
		missing bool
	}{
		{"阶段前历史价不被在线覆盖", model.UsageRow{App: model.UsageCodex, Model: "gpt-5.6", Speed: "standard", Time: beforePromo, Input: 1_000_000, Output: 1_000_000}, 35, false},
		{"促销期内且GPT超272K不加价", model.UsageRow{App: model.UsageCodex, Model: "gpt-5.6", Speed: "standard", Time: inPromo, Input: 300_000, Output: 100_000}, 3.2, false},
		{"促销到期后无死覆盖自动走在线目录", model.UsageRow{App: model.UsageCodex, Model: "gpt-5.6", Speed: "standard", Time: afterPromo, Input: 1_000_000, Output: 1_000_000}, 54, false},
		{"变体后缀促销期内锁定阶段价", model.UsageRow{App: model.UsageAntigravity, Model: "gemini-4-carbon-high", Speed: "standard", Time: inPromo, Input: 1_000_000, Output: 1_000_000}, 12, false},
		{"变体后缀到期后走在线目录", model.UsageRow{App: model.UsageAntigravity, Model: "gemini-4-carbon-high", Speed: "standard", Time: afterPromo, Input: 1_000_000, Output: 1_000_000}, 42, false},
		{"CodexFast超272K不返回nil", model.UsageRow{App: model.UsageCodex, Model: "gpt-5.6", Speed: "fast", Time: inPromo, Input: 300_000, Output: 100_000}, 6.4, false},
		{"Haiku5.5短上下文", model.UsageRow{App: model.UsageClaude, Model: "claude-haiku-5-5", Speed: "standard", Time: inPromo, Input: 100_000, Output: 100_000}, 0.06, false},
		{"Haiku5.5超100K阶梯", model.UsageRow{App: model.UsageClaude, Model: "claude-haiku-5-5", Speed: "standard", Time: inPromo, Input: 1, CacheRead: 100_000, Output: 100_000}, 0.2550005, false},
		{"GeminiPro预览后缀超200K不被在线短价截胡", model.UsageRow{App: model.UsageAntigravity, Model: "gemini-3.1-pro-preview", Speed: "standard", Time: inPromo, Input: 200_001, Output: 100_000}, 2.600004, false},
		{"Grok4.7满200K触发阶梯", model.UsageRow{App: model.UsageOmp, Model: "xai/grok-4-7", Speed: "standard", Time: inPromo, Input: 200_000, Output: 100_000}, 2, false},
		{"Claude1h缓存写入2倍", model.UsageRow{App: model.UsageClaude, Model: "anthropic/claude-sonnet-4-6-20260101", Speed: "standard", Time: inPromo, Input: 100, Output: 40, CacheRead: 20, CacheWrite: 30, CacheWrite1h: 20}, .0010635, false},
		{"Claude历史Fast固定价", model.UsageRow{App: model.UsageClaude, Model: "claude-opus-4-6", Speed: "fast", Time: inPromo, Input: 1_000_000, Output: 1_000_000}, 180, false},
		{"DeepSeek发布前平价", model.UsageRow{App: model.UsageDsh, Model: "deepseek/deepseek-flash", Speed: "standard", Time: dsPreGA, Input: 1_000_000, Output: 1_000_000}, 0.42, false},
		{"DeepSeek高峰价", model.UsageRow{App: model.UsageDsh, Model: "deepseek/deepseek-flash", Speed: "standard", Time: dsPeak, Input: 1_000_000, Output: 1_000_000, CacheRead: 1_000_000}, 1.506, false},
		{"DeepSeek低谷半价", model.UsageRow{App: model.UsageDsh, Model: "deepseek/deepseek-flash", Speed: "standard", Time: dsOffPeak, Input: 1_000_000, Output: 1_000_000, CacheRead: 1_000_000}, 0.753, false},
		{"未知速度返回nil", model.UsageRow{App: model.UsageClaude, Model: "claude-opus-4-6", Speed: "unknown", Time: inPromo, Input: 100}, 0, true},
	}
	for _, entry := range cases {
		cost := pricing.Cost(entry.row)
		if entry.missing {
			if cost != nil {
				t.Errorf("%s应缺少价格: %v", entry.name, *cost)
			}
		} else if cost == nil || math.Abs(*cost-entry.want) > 1e-9 {
			t.Errorf("%s费用 %v，期望 %f", entry.name, cost, entry.want)
		}
	}

	root, err := parse([]byte(`{"openai":{"models":{"gpt-5.4":{"cost":{"input":2.5,"output":15},"experimental":{"modes":{"fast":{"cost":{"input":5,"output":30}}}}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if decoded := decodeCatalog(root, false); decoded.CodexFastRates["gpt-5.4"].Input != 5 {
		t.Fatal("Models.dev快速价格解析错误")
	}
	liteRoot, err := parse([]byte(`{"gemini/gemini-2.5-pro":{"litellm_provider":"gemini","input_cost_per_token":0.00000125,"output_cost_per_token":0.00001}}`))
	if err != nil {
		t.Fatal(err)
	}
	if decoded := decodeCatalog(liteRoot, true); math.Abs(decoded.StandardRates["gemini-2.5-pro"].Input-1.25) > 1e-9 {
		t.Fatal("LiteLLM gemini价格解析错误")
	}

	// 验证 repriceAll 仅更新价格发生变化的非上报行并保留 usage_cost 上报行
	ctx := context.Background()
	store, err := Open(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	batch, err := store.beginWrite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	staleCost, reportedCost := 99.0, 1.23
	if err = batch.saveUsage(ctx, model.UsageRow{ID: "rp:calc", App: model.UsageCodex, Conversation: "codex:rp", Model: "gpt-5.6", Speed: "standard", Time: inPromo, Input: 1_000_000, Cost: &staleCost, Requests: 1}, "rp-src"); err != nil {
		batch.rollback()
		t.Fatal(err)
	}
	if err = batch.saveUsage(ctx, model.UsageRow{ID: "rp:rep", App: model.UsageOmp, Conversation: "omp:rp", Model: "gpt-5.6", Speed: "standard", Time: inPromo, Input: 1_000_000, Cost: &reportedCost, ReportedCosts: &model.CostBreakdown{Input: 1.23}, Requests: 1}, "rp-src"); err != nil {
		batch.rollback()
		t.Fatal(err)
	}
	if err = batch.commit(ctx); err != nil {
		t.Fatal(err)
	}
	if updated, err := store.repriceAll(ctx); err != nil || !updated {
		t.Fatalf("repriceAll 未更新过期计算行: updated=%v err=%v", updated, err)
	}
	totals, err := store.Totals(ctx, nil, inPromo.Add(-time.Hour), inPromo.Add(time.Hour))
	if err != nil || math.Abs(totals.Cost-5.23) > 1e-9 {
		t.Fatalf("repriceAll 后 usage_rollup 汇总错误: %+v %v", totals, err)
	}
	detail, err := store.Detail(ctx, "codex:rp")
	if err != nil || math.Abs(detail.Conversation.Totals.Cost-4) > 1e-9 {
		t.Fatalf("repriceAll 后 conv_summary 未同步: %+v %v", detail.Conversation, err)
	}
}

// TestCursorPagination 核验分页重复边界与远端上报费用
func TestCursorPagination(t *testing.T) {
	at := time.Now().Add(-time.Hour)
	event := func(index int) map[string]any {
		return map[string]any{"timestamp": at.Add(time.Duration(index) * time.Millisecond).UnixMilli(), "model": "gpt-5.5", "chargedCents": "12.5", "tokenUsage": map[string]any{"inputTokens": index + 1, "outputTokens": 2}}
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Cookie") != "WorkosCursorSessionToken=account%3A%3Atoken" {
			t.Error("Cursor Cookie格式错误")
		}
		var payload map[string]any
		_ = json.NewDecoder(request.Body).Decode(&payload)
		page := int(payload["page"].(float64))
		events := []map[string]any{}
		start, end := 0, 1000
		if page == 2 {
			start, end = 999, 1001
		}
		for index := start; index < end; index++ {
			events = append(events, event(index))
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"totalUsageEventsCount": "1001", "usageEventsDisplay": events})
	}))
	defer server.Close()
	rows, err := fetchCursor(context.Background(), server.Client(), server.URL, model.Credential{AccountID: "account", AccessToken: "token"}, at.Add(-time.Minute), at.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1001 || rows[1000].Input != 1001 || rows[0].Cost == nil || *rows[0].Cost != .125 {
		t.Fatalf("Cursor分页/费用错误 %d", len(rows))
	}
}

// TestParseEdgeCases 验证Claude正文防误判Harness字符串message与引号毫秒时间戳
func TestParseEdgeCases(t *testing.T) {
	store := &Store{pricing: NewPricing(t.TempDir())}

	state := scanState{Model: "unknown", Speed: "standard", Titles: map[string]string{}, Candidates: map[string][]projectCandidate{}}
	userLine := []byte(`{"type":"user","sessionId":"s1","toolUseResult":{"isSidechain":true},"message":{"content":"排查解析逻辑"}}`)
	if row := store.parseClaudeLine(userLine, &state); row != nil {
		t.Fatalf("用户消息不应产出用量行: %+v", row)
	}
	if state.Subtasks {
		t.Fatal("嵌套字段含 isSidechain:true 不应误判为子任务")
	}
	userLine2 := []byte(`{"type":"user","sessionId":"s2","slug":"tool_result","message":{"content":"排查工具结果标题"}}`)
	if row := store.parseClaudeLine(userLine2, &state); row != nil || state.Titles["s2"] != "排查工具结果标题" {
		t.Fatalf("普通用户标题含 tool_result 字样被误跳过: %q", state.Titles["s2"])
	}

	assistantLine := []byte(`{"type":"assistant","sessionId":"s1","cwd":"/proj","timestamp":"1760000000000","meta":{"isSidechain":true},"message":{"id":"m1","model":"claude-sonnet-4-6","stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5,"speed":"standard"}}}`)
	row := store.parseClaudeLine(assistantLine, &state)
	if row == nil || state.Subtasks || row.Time.UnixMilli() != 1760000000000 {
		t.Fatalf("Claude助手行或引号毫秒时间戳解析错误: row=%+v subtasks=%v", row, state.Subtasks)
	}

	sidechainLine := []byte(`{"type":"assistant","isSidechain":true,"sessionId":"s1","cwd":"/proj","timestamp":"1760000000000","message":{"id":"m2","model":"claude-sonnet-4-6","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":2,"speed":"standard"}}}`)
	if store.parseClaudeLine(sidechainLine, &state) == nil || !state.Subtasks {
		t.Fatal("真实顶层 isSidechain:true 未识别为子任务")
	}

	hState := scanState{Session: "h1", Model: "gpt-5.5", Speed: "standard"}
	compactionLine := []byte(`{"type":"compaction","id":"c1","timestamp":"2026-09-20T13:00:00Z","provider":"openai","model":"gpt-5.5","message":"compacted summary","usage":{"input":100,"output":20,"cost":{"total":0.01}}}`)
	cRow := store.parseHarnessLine(compactionLine, &hState, model.UsageOmp)
	if cRow == nil || cRow.Input != 100 || cRow.Output != 20 {
		t.Fatalf("compaction带字符串message被丢弃: %+v", cRow)
	}

	msgLine := []byte(`{"type":"message","id":"m2","timestamp":"2026-09-20T13:00:00Z","message":{"role":"assistant","provider":"openai","model":"gpt-5.5","timestamp":"1760000000000","usage":{"input":30,"output":10,"cost":{"total":0.005}}}}`)
	mRow := store.parseHarnessLine(msgLine, &hState, model.UsagePi)
	if mRow == nil || mRow.Time.UnixMilli() != 1760000000000 {
		t.Fatalf("Harness引号毫秒时间戳解析错误: %+v", mRow)
	}
}

func testPBVarint(v uint64) []byte {
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

func testPBFieldVarint(fn uint32, v uint64) []byte {
	return append(testPBVarint(uint64(fn)<<3), testPBVarint(v)...)
}

func testPBFieldBytes(fn uint32, payload []byte) []byte {
	out := append(testPBVarint(uint64(fn)<<3|2), testPBVarint(uint64(len(payload)))...)
	return append(out, payload...)
}

func testPBTimestamp(at time.Time) []byte {
	out := testPBFieldVarint(1, uint64(at.Unix()))
	if at.Nanosecond() > 0 {
		out = append(out, testPBFieldVarint(2, uint64(at.Nanosecond()))...)
	}
	return out
}

func buildTestAgGenMeta(stepIdxs []int64, respModel, displayName string, in, out, cr, cw, think, respOut int64) []byte {
	var usage []byte
	usage = append(usage, testPBFieldVarint(1, 1535)...)
	if in > 0 {
		usage = append(usage, testPBFieldVarint(2, uint64(in))...)
	}
	if out > 0 {
		usage = append(usage, testPBFieldVarint(3, uint64(out))...)
	}
	if cw > 0 {
		usage = append(usage, testPBFieldVarint(4, uint64(cw))...)
	}
	if cr > 0 {
		usage = append(usage, testPBFieldVarint(5, uint64(cr))...)
	}
	if think > 0 {
		usage = append(usage, testPBFieldVarint(9, uint64(think))...)
	}
	if respOut > 0 {
		usage = append(usage, testPBFieldVarint(10, uint64(respOut))...)
	}
	var chat []byte
	chat = append(chat, testPBFieldVarint(3, 1535)...)
	chat = append(chat, testPBFieldBytes(4, usage)...)
	if respModel != "" {
		chat = append(chat, testPBFieldBytes(19, []byte(respModel))...)
	}
	if displayName != "" {
		chat = append(chat, testPBFieldBytes(21, []byte(displayName))...)
	}
	var packed []byte
	for _, si := range stepIdxs {
		packed = append(packed, testPBVarint(uint64(si))...)
	}
	var top []byte
	top = append(top, testPBFieldBytes(1, chat)...)
	if len(packed) > 0 {
		top = append(top, testPBFieldBytes(2, packed)...)
	}
	return top
}

// TestAntigravityProtobufAndScan 验证 Antigravity protobuf 解析、定价与端到端增量扫描
func TestAntigravityProtobufAndScan(t *testing.T) {
	ctx := context.Background()

	// 验证 protobuf 解析与缺省模型名/输出回退
	fallbackBlob := buildTestAgGenMeta([]int64{1, 2}, "", "Gemini 4 Neon (XHigh)", 1000, 0, 5000, 200, 120, 80)
	parsed := parseAgGenMetadata(fallbackBlob)
	if !parsed.HasUsage || parsed.Input != 1000 || parsed.Output != 200 || parsed.CacheRead != 5000 || parsed.CacheWrite != 200 {
		t.Fatalf("Antigravity usage解析错误: %+v", parsed)
	}
	if len(parsed.StepIndices) != 2 || parsed.StepIndices[0] != 1 || parsed.StepIndices[1] != 2 {
		t.Fatalf("Antigravity step_indices解析错误: %+v", parsed.StepIndices)
	}
	if got := antigravityModelName(parsed); got != "gemini-4-neon" {
		t.Fatalf("Antigravity 展示名回退错误: got=%q", got)
	}

	// 构造桌面端(主对话+子任务)与 CLI 端(WAL)完整本地目录
	home := t.TempDir()
	dataDir := t.TempDir()
	agConvDir := filepath.Join(home, ".gemini", "antigravity", "conversations")
	cliConvDir := filepath.Join(home, ".gemini", "antigravity-cli", "conversations")
	if err := os.MkdirAll(agConvDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cliConvDir, 0700); err != nil {
		t.Fatal(err)
	}

	createAgDB := func(path string, wal bool, rootID, parentID, wsURI, branch string, createdAt time.Time, gms []struct {
		idx   int64
		blob  []byte
		sIdx  int64
		sTime time.Time
	}) *sql.DB {
		t.Helper()
		db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
		if err != nil {
			t.Fatal(err)
		}
		if wal {
			if _, err = db.Exec("PRAGMA journal_mode=WAL"); err != nil {
				t.Fatal(err)
			}
		}
		schema := `CREATE TABLE gen_metadata (idx INTEGER PRIMARY KEY, data BLOB, size INTEGER NOT NULL DEFAULT 0);
CREATE TABLE steps (idx INTEGER PRIMARY KEY, step_type INTEGER NOT NULL DEFAULT 0, metadata BLOB);
CREATE TABLE trajectory_metadata_blob (id TEXT PRIMARY KEY DEFAULT 'main', data BLOB);`
		if _, err = db.Exec(schema); err != nil {
			t.Fatal(err)
		}
		var traj []byte
		if wsURI != "" || branch != "" {
			var ws []byte
			if wsURI != "" {
				ws = append(ws, testPBFieldBytes(1, []byte(wsURI))...)
			}
			if branch != "" {
				ws = append(ws, testPBFieldBytes(4, []byte(branch))...)
			}
			traj = append(traj, testPBFieldBytes(1, ws)...)
		}
		traj = append(traj, testPBFieldBytes(2, testPBTimestamp(createdAt))...)
		if parentID != "" {
			traj = append(traj, testPBFieldBytes(5, []byte(parentID))...)
		}
		if rootID != "" {
			traj = append(traj, testPBFieldBytes(6, []byte(rootID))...)
		}
		if _, err = db.Exec("INSERT INTO trajectory_metadata_blob(id,data) VALUES('main',?)", traj); err != nil {
			t.Fatal(err)
		}
		for _, gm := range gms {
			if _, err = db.Exec("INSERT INTO gen_metadata(idx,data,size) VALUES(?,?,?)", gm.idx, gm.blob, len(gm.blob)); err != nil {
				t.Fatal(err)
			}
			if gm.sIdx >= 0 {
				stepMeta := testPBFieldBytes(8, testPBTimestamp(gm.sTime))
				if _, err = db.Exec("INSERT INTO steps(idx,step_type,metadata) VALUES(?,15,?)", gm.sIdx, stepMeta); err != nil {
					t.Fatal(err)
				}
			}
		}
		return db
	}

	writeSummaryDB := func(path string, rows [][5]string) {
		t.Helper()
		db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err = db.Exec(`CREATE TABLE conversation_summaries (conversation_id TEXT, title TEXT, preview TEXT, workspace_uris TEXT, parent_conversation_id TEXT)`); err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if _, err = db.Exec("INSERT INTO conversation_summaries VALUES(?,?,?,?,?)", r[0], r[1], r[2], r[3], r[4]); err != nil {
				t.Fatal(err)
			}
		}
	}

	rootID := "11111111-1111-1111-1111-111111111111"
	subID := "22222222-2222-2222-2222-222222222222"
	cliID := "33333333-3333-3333-3333-333333333333"
	t1 := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Minute)
	t3 := t1.Add(2 * time.Minute)
	t4 := t1.Add(3 * time.Minute)

	rootDB := createAgDB(filepath.Join(agConvDir, rootID+".db"), false, rootID, "", "file:///e%3A/ACE_Collect", "main", t1, []struct {
		idx   int64
		blob  []byte
		sIdx  int64
		sTime time.Time
	}{{0, buildTestAgGenMeta([]int64{1}, "gemini-4-neon", "Gemini 4 Neon (XHigh)", 1_000_000, 100_000, 2_000_000, 0, 40_000, 60_000), 1, t1}})
	rootDB.Close()

	subDB := createAgDB(filepath.Join(agConvDir, subID+".db"), false, rootID, rootID, "file:///e%3A/ACE_Collect", "", t2, []struct {
		idx   int64
		blob  []byte
		sIdx  int64
		sTime time.Time
	}{{0, buildTestAgGenMeta([]int64{1}, "gemini-4-hydrogen-tiered-xhigh", "Gemini 4 Hydrogen (XHigh)", 1_000_000, 100_000, 0, 0, 0, 0), 1, t2}})
	subDB.Close()

	cliDB := createAgDB(filepath.Join(cliConvDir, cliID+".db"), true, cliID, "", "file:///D:/Trade", "", t3, []struct {
		idx   int64
		blob  []byte
		sIdx  int64
		sTime time.Time
	}{{0, buildTestAgGenMeta([]int64{1}, "gemini-4-carbon", "Gemini 4 Carbon (Medium)", 500_000, 100_000, 1_000_000, 0, 0, 0), 1, t3}})
	defer cliDB.Close()

	writeSummaryDB(filepath.Join(home, ".gemini", "antigravity", "conversation_summaries.db"), [][5]string{
		{rootID, "主对话标题", "主对话预览", `["file:///e%3A/ACE_Collect"]`, ""},
		{subID, "", "", `["file:///e%3A/ACE_Collect"]`, rootID},
	})
	writeSummaryDB(filepath.Join(home, ".gemini", "antigravity-cli", "conversation_summaries.db"), [][5]string{
		{cliID, "", "CLI 预览标题", `["file:///D:/Trade"]`, ""},
	})

	store, err := Open(dataDir, home)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if err = store.Scan(ctx, false); err != nil {
		t.Fatalf("首次扫描失败: %v", err)
	}

	allowed := map[model.UsageApp]bool{model.UsageAntigravity: true}
	ov, err := store.Overview(ctx, model.UsageQuery{
		AllowedApps: allowed,
		From:        t1.Add(-time.Hour).Format(time.RFC3339),
		To:          t4.Add(time.Hour).Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	wantTokens := int64(3_100_000 + 1_100_000 + 1_600_000)
	if ov.Totals.Requests != 3 || ov.Totals.Tokens != wantTokens || ov.Totals.Cost <= 0 {
		t.Fatalf("Antigravity 概览汇总错误: %+v", ov.Totals)
	}
	if len(ov.Services) != 1 || ov.Services[0].Name != "Antigravity" || ov.Services[0].App == nil || *ov.Services[0].App != model.UsageAntigravity {
		t.Fatalf("Antigravity 服务分组错误: %+v", ov.Services)
	}

	page, err := store.Conversations(ctx, model.ConversationQuery{AllowedApps: allowed})
	if err != nil || page.Total != 2 || len(page.Items) != 2 {
		t.Fatalf("Antigravity 对话归并错误: err=%v page=%+v", err, page)
	}
	byID := map[string]model.ConversationSummary{}
	for _, item := range page.Items {
		byID[item.ID] = item
	}
	rootConv := byID["antigravity:"+rootID]
	if rootConv.Title != "主对话标题" || !rootConv.Subtasks || rootConv.Branch != "main" || rootConv.Totals.Requests != 2 {
		t.Fatalf("主对话归并元数据错误: %+v", rootConv)
	}
	cliConv := byID["antigravity:"+cliID]
	if cliConv.Title != "CLI 预览标题" || cliConv.Subtasks || cliConv.Totals.Requests != 1 {
		t.Fatalf("CLI 对话元数据错误: %+v", cliConv)
	}

	detail, err := store.Detail(ctx, "antigravity:"+rootID)
	if err != nil || len(detail.Events) != 2 || detail.Conversation.Totals.Cost <= 0 {
		t.Fatalf("Antigravity 对话详情错误: err=%v detail=%+v", err, detail)
	}

	todayCosts, weekCosts, err := store.PopoverCosts(ctx, allowed, t1.Add(-time.Minute), t1.Add(-time.Hour), t4.Add(time.Hour))
	if err != nil || math.Abs(todayCosts[model.UsageAntigravity]-ov.Totals.Cost) > 1e-6 || math.Abs(weekCosts[model.UsageAntigravity]-ov.Totals.Cost) > 1e-6 {
		t.Fatalf("托盘费用汇总错误: err=%v today=%v week=%v", err, todayCosts, weekCosts)
	}

	// 再次扫描无变更时不应提升 revision
	revBefore := store.Status().Revision
	if err = store.Scan(ctx, false); err != nil {
		t.Fatal(err)
	}
	if revAfter := store.Status().Revision; revAfter != revBefore {
		t.Fatalf("无变更重复扫描不应触发写入: before=%d after=%d", revBefore, revAfter)
	}

	// 向 WAL 活跃数据库追加一条新事件，验证增量捕获且无重复双计
	newBlob := buildTestAgGenMeta([]int64{2}, "gpt-oss-120b-medium", "GPT-OSS 120B (Medium)", 1_000_000, 1_000_000, 0, 0, 0, 0)
	if _, err = cliDB.Exec("INSERT INTO gen_metadata(idx,data,size) VALUES(1,?,?)", newBlob, len(newBlob)); err != nil {
		t.Fatal(err)
	}
	if _, err = cliDB.Exec("INSERT INTO steps(idx,step_type,metadata) VALUES(2,15,?)", testPBFieldBytes(8, testPBTimestamp(t4))); err != nil {
		t.Fatal(err)
	}
	if err = store.Scan(ctx, false); err != nil {
		t.Fatal(err)
	}
	ov2, err := store.Overview(ctx, model.UsageQuery{
		AllowedApps: allowed,
		From:        t1.Add(-time.Hour).Format(time.RFC3339),
		To:          t4.Add(time.Hour).Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	if ov2.Totals.Requests != 4 || ov2.Totals.Cost <= ov.Totals.Cost {
		t.Fatalf("WAL 增量扫描结果错误: %+v", ov2.Totals)
	}
}

// TestAntigravityCorruptCursor 验证损坏DB隔离与WAL尾部进行中游标回填
func TestAntigravityCorruptCursor(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	dataDir := t.TempDir()
	agDir := filepath.Join(home, ".gemini", "antigravity", "conversations")
	if err := os.MkdirAll(agDir, 0700); err != nil {
		t.Fatal(err)
	}

	t1 := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	makeDB := func(name string, wal bool, rows []struct {
		idx  int64
		blob []byte
	}) *sql.DB {
		t.Helper()
		path := filepath.Join(agDir, name)
		db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
		if err != nil {
			t.Fatal(err)
		}
		if wal {
			if _, err = db.Exec("PRAGMA journal_mode=WAL"); err != nil {
				t.Fatal(err)
			}
		}
		if _, err = db.Exec(`CREATE TABLE gen_metadata (idx INTEGER PRIMARY KEY, data BLOB, size INTEGER NOT NULL DEFAULT 0);
CREATE TABLE steps (idx INTEGER PRIMARY KEY, step_type INTEGER NOT NULL DEFAULT 0, metadata BLOB);
CREATE TABLE trajectory_metadata_blob (id TEXT PRIMARY KEY DEFAULT 'main', data BLOB);`); err != nil {
			t.Fatal(err)
		}
		traj := testPBFieldBytes(2, testPBTimestamp(t1))
		if _, err = db.Exec("INSERT INTO trajectory_metadata_blob(id,data) VALUES('main',?)", traj); err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if _, err = db.Exec("INSERT INTO gen_metadata(idx,data,size) VALUES(?,?,?)", r.idx, r.blob, len(r.blob)); err != nil {
				t.Fatal(err)
			}
		}
		return db
	}

	// 构造前后两个有效DB与中间一个损坏DB，外加一个尾部存在无usage进行中行的WAL DB
	dbA := makeDB("aaa.db", false, []struct {
		idx  int64
		blob []byte
	}{{0, buildTestAgGenMeta([]int64{1}, "gemini-4-neon", "", 1_000_000, 0, 0, 0, 0, 0)}})
	dbA.Close()

	corruptPath := filepath.Join(agDir, "bbb-corrupt.db")
	if err := os.WriteFile(corruptPath, []byte("not-a-valid-sqlite-database"), 0600); err != nil {
		t.Fatal(err)
	}

	dbC := makeDB("ccc.db", false, []struct {
		idx  int64
		blob []byte
	}{{0, buildTestAgGenMeta([]int64{1}, "gemini-4-carbon", "", 1_000_000, 0, 0, 0, 0, 0)}})
	dbC.Close()

	// 尾部 idx=1 尚无 usage（空 blob），验证 offset 不跳过 idx=1 且回填后可捕获
	walDB := makeDB("ddd-wal.db", true, []struct {
		idx  int64
		blob []byte
	}{
		{0, buildTestAgGenMeta([]int64{1}, "gemini-4-hydrogen", "", 1_000_000, 0, 0, 0, 0, 0)},
		{1, []byte{}},
	})
	defer walDB.Close()

	store, err := Open(dataDir, home)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	scanErr := store.Scan(ctx, true)
	if scanErr == nil || !strings.Contains(scanErr.Error(), "bbb-corrupt.db") {
		t.Fatalf("应上报损坏DB错误: %v", scanErr)
	}
	agApp := model.UsageAntigravity
	totals, err := store.Totals(ctx, &agApp, t1.Add(-time.Hour), t1.Add(time.Hour))
	if err != nil || totals.Requests != 3 {
		t.Fatalf("损坏DB不应阻断前后有效DB入库: totals=%+v err=%v", totals, err)
	}

	var walOffset int64
	if err = store.db.QueryRowContext(ctx, "SELECT offset FROM scan_file WHERE path=?", filepath.Join(agDir, "ddd-wal.db")).Scan(&walOffset); err != nil {
		t.Fatal(err)
	}
	if walOffset != 1 {
		t.Fatalf("尾部无usage行不应推进游标: got=%d want=1", walOffset)
	}

	// 客户端回填 idx=1 的 usage 后再次扫描应捕获该行
	backfill := buildTestAgGenMeta([]int64{2}, "gemini-4-neon", "", 1_000_000, 0, 0, 0, 0, 0)
	if _, err = walDB.Exec("UPDATE gen_metadata SET data=?,size=? WHERE idx=1", backfill, len(backfill)); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(corruptPath)
	if err = store.Scan(ctx, false); err != nil {
		t.Fatal(err)
	}
	totals2, err := store.Totals(ctx, &agApp, t1.Add(-time.Hour), t1.Add(time.Hour))
	if err != nil || totals2.Requests != 4 {
		t.Fatalf("回填尾部行未被增量捕获: totals=%+v err=%v", totals2, err)
	}
	if err = store.db.QueryRowContext(ctx, "SELECT offset FROM scan_file WHERE path=?", filepath.Join(agDir, "ddd-wal.db")).Scan(&walOffset); err != nil {
		t.Fatal(err)
	}
	if walOffset != 2 {
		t.Fatalf("回填后游标未推进: got=%d want=2", walOffset)
	}
}

// TestApplyTitlesDelayedConversation 验证索引文件先于会话响应落盘时新会话仍可补齐标题
func TestApplyTitlesDelayedConversation(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	dataDir := t.TempDir()
	store, err := Open(dataDir, home)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// 首次扫描前仅存在索引文件，对应会话日志尚未生成
	writeFixture(t, filepath.Join(home, ".claude", "history.jsonl"), fmt.Sprintf(`{"sessionId":"delayed-claude","display":"延迟Claude标题","project":"%s"}`+"\n", strings.ReplaceAll(home, "\\", "\\\\")))
	writeFixture(t, filepath.Join(home, ".codex", "session_index.jsonl"), `{"id":"delayed-codex","thread_name":"延迟Codex标题"}`+"\n")
	if err = store.Scan(ctx, false); err != nil {
		t.Fatal(err)
	}

	// 随后首条响应落盘，索引文件本身未再改动
	at := time.Date(2026, 11, 25, 11, 0, 0, 0, time.UTC).Format(time.RFC3339)
	writeFixture(t, filepath.Join(home, ".claude", "projects", "proj", "delayed-claude.jsonl"), fmt.Sprintf(`{"type":"assistant","sessionId":"delayed-claude","cwd":"%s","timestamp":"%s","message":{"id":"m1","model":"claude-sonnet-4-6","stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5,"speed":"standard"}}}`+"\n", strings.ReplaceAll(home, "\\", "\\\\"), at))
	writeFixture(t, filepath.Join(home, ".codex", "sessions", "delayed-codex.jsonl"), fmt.Sprintf(`{"type":"session_meta","payload":{"id":"delayed-codex","cwd":"%s"}}
{"type":"turn_context","payload":{"model":"gpt-5.5"}}
{"timestamp":"%s","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":10,"output_tokens":5},"total_token_usage":{"input_tokens":10,"output_tokens":5}}}}
`, strings.ReplaceAll(home, "\\", "\\\\"), at))

	if err = store.Scan(ctx, false); err != nil {
		t.Fatal(err)
	}
	page, err := store.Conversations(ctx, model.ConversationQuery{})
	if err != nil {
		t.Fatal(err)
	}
	titles := map[string]string{}
	for _, item := range page.Items {
		titles[item.ID] = item.Title
	}
	if titles["claude:delayed-claude"] != "延迟Claude标题" || titles["codex:delayed-codex"] != "延迟Codex标题" {
		t.Fatalf("延迟到达会话标题未补齐: %+v", titles)
	}
}

// TestTotalsInIntervalsForFamily 验证 Antigravity 区间聚合按模型族隔离且排除 argon 混入 gemini
func TestTotalsInIntervalsForFamily(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// 跨越 15 分钟 rollup 桶边界以同时覆盖 usage_rollup 与 usage 边缘查询
	base := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	rows := []model.UsageRow{
		{ID: "ag:1", App: model.UsageAntigravity, Conversation: "antigravity:c1", Model: "gemini-4-neon", Speed: "standard", Time: base.Add(2 * time.Minute), Input: 100, Requests: 1},
		{ID: "ag:2", App: model.UsageAntigravity, Conversation: "antigravity:c1", Model: "google/gemini-2.5-pro", Speed: "standard", Time: base.Add(20 * time.Minute), Input: 200, Requests: 1},
		{ID: "ag:3", App: model.UsageAntigravity, Conversation: "antigravity:c1", Model: "gemini-4-argon", Speed: "standard", Time: base.Add(22 * time.Minute), Input: 300, Requests: 1},
		{ID: "ag:4", App: model.UsageAntigravity, Conversation: "antigravity:c1", Model: "claude-sonnet-4-5", Speed: "standard", Time: base.Add(25 * time.Minute), Input: 400, Requests: 1},
		{ID: "ag:5", App: model.UsageAntigravity, Conversation: "antigravity:c1", Model: "gpt-oss-120b-medium", Speed: "standard", Time: base.Add(35 * time.Minute), Input: 500, Requests: 1},
	}
	batch, err := store.beginWrite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		c := float64(row.Input) / 100
		row.Cost = &c
		if err = batch.saveUsage(ctx, row, "ag-test"); err != nil {
			batch.rollback()
			t.Fatal(err)
		}
	}
	if err = batch.commit(ctx); err != nil {
		t.Fatal(err)
	}

	intervals := []Interval{{From: base.Add(time.Minute), To: base.Add(40 * time.Minute)}}
	gem, err := store.TotalsInIntervalsForFamily(ctx, model.UsageAntigravity, "gemini", intervals)
	if err != nil || gem.Requests != 2 || gem.Input != 300 || math.Abs(gem.Cost-3) > 1e-9 {
		t.Fatalf("gemini 模型族过滤错误: totals=%+v err=%v", gem, err)
	}
	argon, err := store.TotalsInIntervalsForFamily(ctx, model.UsageAntigravity, "argon", intervals)
	if err != nil || argon.Requests != 1 || argon.Input != 300 || math.Abs(argon.Cost-3) > 1e-9 {
		t.Fatalf("argon 模型族过滤错误: totals=%+v err=%v", argon, err)
	}
	claude, err := store.TotalsInIntervalsForFamily(ctx, model.UsageAntigravity, "claude", intervals)
	if err != nil || claude.Requests != 2 || claude.Input != 900 || math.Abs(claude.Cost-9) > 1e-9 {
		t.Fatalf("claude 模型族过滤错误: totals=%+v err=%v", claude, err)
	}
	all, err := store.TotalsInIntervals(ctx, model.UsageAntigravity, intervals)
	if err != nil || all.Requests != 5 || all.Input != 1500 {
		t.Fatalf("全量模型区间汇总错误: totals=%+v err=%v", all, err)
	}
}

// TestResolveDictNoPoolDeadlock 验证连接池仅剩单条连接时字典回填复用当前只读事务不死锁
func TestResolveDictNoPoolDeadlock(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	at := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	cost := 1.25
	batch, err := store.beginWrite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = batch.saveUsage(ctx, model.UsageRow{
		ID: "pool:1", App: model.UsageClaude, Conversation: "claude:pool", Model: "claude-sonnet-4-6",
		Speed: "standard", Time: at, Input: 100, Output: 50, Cost: &cost, Requests: 1,
	}, "pool-src"); err != nil {
		batch.rollback()
		t.Fatal(err)
	}
	if err = batch.commit(ctx); err != nil {
		t.Fatal(err)
	}

	// 占满 3/4 连接并清空内存字典缓存，强迫 Overview 与 Detail 在单连接上回填字典
	held := make([]*sql.Conn, 0, 3)
	for range 3 {
		conn, connErr := store.db.Conn(ctx)
		if connErr != nil {
			t.Fatal(connErr)
		}
		held = append(held, conn)
	}
	defer func() {
		for _, conn := range held {
			conn.Close()
		}
	}()

	clearDict := func() {
		store.dictMu.Lock()
		store.modelNames = map[int64]string{}
		store.speedNames = map[int64]string{}
		store.dictMu.Unlock()
	}

	qCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	clearDict()
	ov, err := store.Overview(qCtx, model.UsageQuery{From: at.Add(-time.Hour).Format(time.RFC3339), To: at.Add(time.Hour).Format(time.RFC3339)})
	if err != nil || ov.Totals.Requests != 1 || len(ov.Models) != 1 || ov.Models[0].Model != "claude-sonnet-4-6" {
		t.Fatalf("单连接下 Overview 字典回填失败或死锁: ov=%+v err=%v", ov, err)
	}

	clearDict()
	detail, err := store.Detail(qCtx, "claude:pool")
	if err != nil || len(detail.Events) != 1 || detail.Events[0].Model != "claude-sonnet-4-6" {
		t.Fatalf("单连接下 Detail 字典回填失败或死锁: detail=%+v err=%v", detail, err)
	}
}
