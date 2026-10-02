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
	if _, err = store.db.Exec("INSERT INTO usage(event_key,source,app,conversation,model,speed,time,input,output,cache_read,cache_write,cost) VALUES('legacy','cursor:legacy',2,'cursor:daily','legacy-model','standard',?,1,2,3,4,0.75)", milliseconds); err != nil {
		t.Fatal(err)
	}
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
	if err = store.scanOpenCode(ctx); err != nil {
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
	if _, err = store.db.Exec(`INSERT INTO usage(event_key,source,app,conversation,model,speed,time,input,output,cache_read,cache_write,cost)
 VALUES('fast-claude','test',1,'test-fast','claude-opus-4-6','fast',?,10,20,0,0,0.1)`, milliseconds); err != nil {
		t.Fatal(err)
	}
	fast, err = store.Overview(ctx, query)
	if err != nil || fast.Fast.MultiplierText != "2.5–6×" {
		t.Fatalf("混合Fast倍率错误: %+v %v", fast.Fast, err)
	}
	if _, err = store.db.Exec(`INSERT INTO usage(event_key,source,app,conversation,model,speed,time,input,output,cache_read,cache_write,cost)
 VALUES('fast-unknown','test',1,'test-fast','unknown','fast',?,10,20,0,0,NULL)`, milliseconds); err != nil {
		t.Fatal(err)
	}
	fast, err = store.Overview(ctx, query)
	if err != nil || fast.Fast.MultiplierText != "—" || !fast.Fast.UnknownMultiplier {
		t.Fatalf("未知Fast倍率错误: %+v %v", fast.Fast, err)
	}
}

// TestExistingHarnessLogs 使用临时数据库核验本机Pi和OMP日志
func TestExistingHarnessLogs(t *testing.T) {
	if os.Getenv("CCBAR_VERIFY_EXISTING") != "1" {
		t.Skip("仅在本机迁移验收时读取真实日志")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	store, err := Open(t.TempDir(), home)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	for _, app := range []model.UsageApp{model.UsagePi, model.UsageOmp} {
		roots := SessionRoots(home, app)
		files := []scanFile{}
		headers := map[string]string{}
		for _, root := range roots {
			err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
				if os.IsNotExist(err) {
					return nil
				}
				if err != nil {
					return err
				}
				if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl") {
					file := scanFile{app: app, path: path}
					if app == model.UsageOmp {
						file.parent = ompParent(path, roots, headers)
					}
					files = append(files, file)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		for _, file := range files {
			if file.parent == "" {
				if err = store.scanJSONL(ctx, file); err != nil {
					t.Fatal(err)
				}
			}
		}
		for _, file := range files {
			if file.parent != "" {
				if err = store.scanJSONL(ctx, file); err != nil {
					t.Fatal(err)
				}
			}
		}
		totals, err := store.Totals(ctx, &app, time.UnixMilli(0), time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("真实%s日志: %d文件，%d请求，%d Tokens，$%.6f", model.UsageName(app), len(files), totals.Requests, totals.Tokens, totals.Cost)
		if len(files) > 0 && totals.Requests == 0 {
			t.Fatalf("%s日志没有解析到请求", model.UsageName(app))
		}
	}
	source := filepath.Join(home, ".local", "share", "opencode", "opencode.db")
	if _, err = os.Stat(source); err == nil {
		read, openErr := sql.Open("sqlite", "file:"+filepath.ToSlash(source)+"?mode=ro")
		if openErr != nil {
			t.Fatal(openErr)
		}
		defer read.Close()
		for _, table := range []string{"message", "session", "workspace"} {
			rows, queryErr := read.Query("PRAGMA table_info(" + table + ")")
			if queryErr != nil {
				t.Fatal(queryErr)
			}
			columns := []string{}
			for rows.Next() {
				var cid, notnull, pk int
				var name, kind string
				var defaults any
				if queryErr = rows.Scan(&cid, &name, &kind, &notnull, &defaults, &pk); queryErr != nil {
					rows.Close()
					t.Fatal(queryErr)
				}
				columns = append(columns, name)
			}
			rows.Close()
			t.Logf("真实OpenCode %s字段: %s", table, strings.Join(columns, ","))
		}
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

// TestExistingDatabaseReadOnly 以只读连接核验本机已有数据库的统计结果
func TestExistingDatabaseReadOnly(t *testing.T) {
	if os.Getenv("CCBAR_VERIFY_EXISTING") != "1" {
		t.Skip("仅在本机迁移验收时读取真实数据")
	}
	path := filepath.Join(os.Getenv("LOCALAPPDATA"), "CCBar", "usage.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := &Store{db: db, pricing: NewPricing(filepath.Dir(path))}
	ctx := context.Background()
	from, to := time.UnixMilli(0), time.Now().Add(time.Hour)
	started := time.Now()
	totals, err := store.Totals(ctx, nil, from, to)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("全量汇总: %s", time.Since(started))
	started = time.Now()
	overview, err := store.Overview(ctx, model.UsageQuery{Grain: "month", From: from.Format(time.RFC3339), To: to.Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	if overview.Totals.Tokens != totals.Tokens || overview.Totals.Requests != totals.Requests || math.Abs(overview.Totals.Cost-totals.Cost) > 1e-6 {
		t.Fatalf("真实库聚合不一致: %+v / %+v", totals, overview.Totals)
	}
	t.Logf("全历史总览: %s", time.Since(started))
	started = time.Now()
	page, err := store.Conversations(ctx, model.ConversationQuery{Limit: 10, Sort: "cost"})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("全历史对话分页: %s", time.Since(started))
	if len(page.Items) > 0 {
		started = time.Now()
		detail, err := store.Detail(ctx, page.Items[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		if detail.Conversation.Totals.Tokens != page.Items[0].Totals.Tokens {
			t.Fatal("真实对话详情与列表不一致")
		}
		t.Logf("对话详情: %s", time.Since(started))
	}
	info, _ := os.Stat(path)
	t.Logf("只读现存数据库: %s, %.1f MB, %d请求, %d Tokens, $%.6f, %d对话, %d项目", path, float64(info.Size())/(1024*1024), totals.Requests, totals.Tokens, totals.Cost, page.Total, len(page.Projects))
}
