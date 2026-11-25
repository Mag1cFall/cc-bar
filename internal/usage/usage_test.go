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
}

// TestPricing 核验缓存TTL促销长上下文与快速档位
func TestPricing(t *testing.T) {
	pricing := NewPricing(t.TempDir())
	at := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		row     model.UsageRow
		want    float64
		missing bool
	}{
		{"Claude缓存TTL", model.UsageRow{App: model.UsageClaude, Model: "anthropic/claude-sonnet-4-6-20260101", Speed: "standard", Time: at, Input: 100, Output: 40, CacheRead: 20, CacheWrite: 30, CacheWrite1h: 20}, .0010635, false},
		{"Codex促销", model.UsageRow{App: model.UsageCodex, Model: "gpt-5.6", Speed: "standard", Time: at, Input: 1000000, Output: 1000000}, 38, false},
		{"Codex快速长上下文", model.UsageRow{App: model.UsageCodex, Model: "gpt-5.6", Speed: "fast", Time: at, Input: 272001}, 0, true},
		{"Claude快速", model.UsageRow{App: model.UsageClaude, Model: "claude-opus-4-6", Speed: "fast", Time: at, Input: 1000000, Output: 1000000}, 180, false},
		{"DeepSeek时间价格", model.UsageRow{App: model.UsageDsh, Model: "deepseek/deepseek-flash", Speed: "standard", Time: at, Input: 1000000, Output: 1000000, CacheRead: 1000000}, 1.506, false},
		{"未知速度", model.UsageRow{App: model.UsageClaude, Model: "claude-opus-4-6", Speed: "unknown", Time: at, Input: 100}, 0, true},
	}
	for _, entry := range cases {
		cost := pricing.Cost(entry.row)
		if entry.missing {
			if cost != nil {
				t.Errorf("%s应缺少价格", entry.name)
			}
		} else if cost == nil || math.Abs(*cost-entry.want) > 1e-9 {
			t.Errorf("%s费用 %v，期望 %f", entry.name, cost, entry.want)
		}
	}
	root, err := parse([]byte(`{"openai":{"models":{"gpt-5.4":{"cost":{"input":2.5,"output":15},"experimental":{"modes":{"fast":{"cost":{"input":5,"output":30}}}}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	decoded := decodeCatalog(root, false)
	if decoded.CodexFastRates["gpt-5.4"].Input != 5 {
		t.Fatal("在线快速价格解析错误")
	}
	if os.Getenv("CCBAR_VERIFY_EXISTING") == "1" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err = pricing.Refresh(ctx, true); err != nil {
			t.Fatal(err)
		}
		t.Logf("真实在线价格目录: LiteLLM %d模型，Models.dev %d模型，Codex Fast %d模型，Claude Fast %d模型",
			len(pricing.active.LiteLLM.StandardRates), len(pricing.active.ModelsDev.StandardRates),
			len(pricing.active.LiteLLM.CodexFastRates)+len(pricing.active.ModelsDev.CodexFastRates),
			len(pricing.active.LiteLLM.ClaudeFastRates)+len(pricing.active.ModelsDev.ClaudeFastRates))
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

// TestEmptyConvSummaryAndIndexPlans 验证空对话汇总删除索引命中与字典并发兜底
func TestEmptyConvSummaryAndIndexPlans(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	at := time.Date(2026, 10, 5, 12, 0, 0, 0, time.Local)
	batch, err := store.beginWrite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = batch.saveUsage(ctx, model.UsageRow{
		ID: "claude:null-check", App: model.UsageClaude, Conversation: "claude:empty-conv",
		Model: "unpriced-model-x", Speed: "standard", Time: at, Input: 100, Output: 50, Requests: 1,
	}, "/tmp/empty-check.jsonl"); err != nil {
		batch.rollback()
		t.Fatal(err)
	}
	if err = batch.commit(ctx); err != nil {
		t.Fatal(err)
	}

	var modelID, speedID, sourceID int64
	if err = store.db.QueryRowContext(ctx, "SELECT model_id,speed_id,source_id FROM usage LIMIT 1").Scan(&modelID, &speedID, &sourceID); err != nil {
		t.Fatal(err)
	}

	queryPlan := func(sqlText string, args ...any) string {
		t.Helper()
		rows, qErr := store.db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+sqlText, args...)
		if qErr != nil {
			t.Fatal(qErr)
		}
		defer rows.Close()
		var parts []string
		for rows.Next() {
			var id, parent, notused int
			var detail string
			if sErr := rows.Scan(&id, &parent, &notused, &detail); sErr != nil {
				t.Fatal(sErr)
			}
			parts = append(parts, detail)
		}
		if rErr := rows.Err(); rErr != nil {
			t.Fatal(rErr)
		}
		return strings.Join(parts, "; ")
	}

	if plan := queryPlan("SELECT id,conv_id,time,input,output,cache_read,cache_write,cache_write_1h FROM usage WHERE cost IS NULL AND app<>2 AND app=? AND speed_id=? AND model_id=?", model.UsageClaude, speedID, modelID); !strings.Contains(plan, "usage_missing_cost") {
		t.Fatalf("补价查询未命中部分索引 usage_missing_cost: %s", plan)
	}
	if plan := queryPlan("SELECT DISTINCT conv_id FROM usage WHERE source_id=?", sourceID); !strings.Contains(plan, "usage_source_idx") {
		t.Fatalf("按来源查对话未命中 usage_source_idx: %s", plan)
	}
	if plan := queryPlan("DELETE FROM usage WHERE source_id=?", sourceID); !strings.Contains(plan, "usage_source_idx") {
		t.Fatalf("按来源删除未命中 usage_source_idx: %s", plan)
	}

	// 清空本地快照与内存字典以验证 resolveDict 从数据库回填
	store.dictMu.Lock()
	delete(store.modelNames, modelID)
	delete(store.speedNames, speedID)
	store.dictMu.Unlock()
	localModels, localSpeeds := map[int64]string{}, map[int64]string{}
	gotModel, gotSpeed := store.resolveDict(ctx, modelID, speedID, localModels, localSpeeds)
	if gotModel != "unpriced-model-x" || gotSpeed != "standard" || localModels[modelID] != "unpriced-model-x" || localSpeeds[speedID] != "standard" {
		t.Fatalf("resolveDict回填失败: model=%q speed=%q", gotModel, gotSpeed)
	}

	// 清空对话后 refreshConvSummaries 不得因空聚合 NULL 报错回滚
	delBatch, err := store.beginWrite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = delBatch.deleteBySource(ctx, "/tmp/empty-check.jsonl"); err != nil {
		delBatch.rollback()
		t.Fatal(err)
	}
	if err = delBatch.commit(ctx); err != nil {
		t.Fatalf("清空对话提交失败: %v", err)
	}
	var summaryCount int
	if err = store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM conv_summary").Scan(&summaryCount); err != nil || summaryCount != 0 {
		t.Fatalf("空对话汇总未清除: count=%d err=%v", summaryCount, err)
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
	if got := rawInt64(json.RawMessage(`"1760000000000"`)); got != 1760000000000 {
		t.Fatalf("rawInt64引号数字解析错误: %d", got)
	}
	if got := parseRawTimestamp(json.RawMessage(`"1760000000000"`)).UnixMilli(); got != 1760000000000 {
		t.Fatalf("parseRawTimestamp引号数字解析错误: %d", got)
	}
}
