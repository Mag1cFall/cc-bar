package usage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Mag1cFall/cc-bar/internal/model"
	"modernc.org/sqlite"
)

// init 为聚合注册本机日界线计算
func init() {
	sqlite.MustRegisterDeterministicScalarFunction("ccbar_day", 1, func(_ *sqlite.FunctionContext, values []driver.Value) (driver.Value, error) {
		at := time.UnixMilli(values[0].(int64))
		return bucketStart(at, "day").UnixMilli(), nil
	})
}

// Store 复用现有毫秒时间的用量数据库并序列化扫描写入
type Store struct {
	db            *sql.DB
	dataDir       string
	home          string
	pricing       *Pricing
	gate          sync.Mutex
	statusMu      sync.RWMutex
	status        model.UsageStatus
	cursorBackoff time.Time
	OnChanged     func()
}

const schema = `
CREATE TABLE IF NOT EXISTS usage (
 event_key TEXT PRIMARY KEY,source TEXT NOT NULL,app INTEGER NOT NULL,
 conversation TEXT NOT NULL,model TEXT NOT NULL,speed TEXT NOT NULL,time INTEGER NOT NULL,
 input INTEGER NOT NULL,output INTEGER NOT NULL,cache_read INTEGER NOT NULL,
 cache_write INTEGER NOT NULL,cost REAL,cache_write_1h INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS usage_time ON usage(time,app);
CREATE INDEX IF NOT EXISTS usage_app_time ON usage(app,time);
CREATE INDEX IF NOT EXISTS usage_conversation ON usage(conversation);
CREATE TABLE IF NOT EXISTS scan_file (
 path TEXT PRIMARY KEY,length INTEGER NOT NULL,modified INTEGER NOT NULL,
 offset INTEGER NOT NULL,model TEXT,speed TEXT,signature TEXT,
 session TEXT,emitting TEXT,title TEXT,cwd TEXT,branch TEXT,
 subtasks INTEGER NOT NULL DEFAULT 0,sidechain_all INTEGER);
CREATE TABLE IF NOT EXISTS conversation (
 id TEXT PRIMARY KEY,title TEXT,cwd TEXT,source TEXT,branch TEXT,
 subtasks INTEGER NOT NULL DEFAULT 0,project TEXT,project_status TEXT,
 cache_write_avail INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS scan_meta(name TEXT PRIMARY KEY,value INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS scan_blob(name TEXT PRIMARY KEY,value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS scan_candidate(path TEXT NOT NULL,session TEXT NOT NULL,cwd TEXT NOT NULL,
 sidechain INTEGER NOT NULL,PRIMARY KEY(path,session,cwd,sidechain));`

// Open 打开现有数据库并确认实际存在的列
func Open(dataDir, home string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, err
	}
	options := url.Values{"_pragma": {"busy_timeout(10000)", "journal_mode(WAL)", "synchronous(NORMAL)"}}
	databaseURL := "file:" + filepath.ToSlash(filepath.Join(dataDir, "usage.db")) + "?" + options.Encode()
	db, err := sql.Open("sqlite", databaseURL)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	if _, err = db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	for table, columns := range map[string]map[string]string{
		"usage":        {"cache_write_1h": "INTEGER NOT NULL DEFAULT 0"},
		"conversation": {"branch": "TEXT", "subtasks": "INTEGER NOT NULL DEFAULT 0", "project": "TEXT", "project_status": "TEXT", "cache_write_avail": "INTEGER NOT NULL DEFAULT 0"},
		"scan_file":    {"branch": "TEXT", "subtasks": "INTEGER NOT NULL DEFAULT 0", "sidechain_all": "INTEGER"},
	} {
		rows, queryErr := db.Query("PRAGMA table_info(" + table + ")")
		if queryErr != nil {
			db.Close()
			return nil, queryErr
		}
		present := map[string]bool{}
		for rows.Next() {
			var cid, notnull, pk int
			var name, kind string
			var defaults any
			if err = rows.Scan(&cid, &name, &kind, &notnull, &defaults, &pk); err != nil {
				rows.Close()
				db.Close()
				return nil, err
			}
			present[name] = true
		}
		rows.Close()
		for name, definition := range columns {
			if !present[name] {
				if _, err = db.Exec("ALTER TABLE " + table + " ADD COLUMN " + name + " " + definition); err != nil {
					db.Close()
					return nil, err
				}
			}
		}
	}
	return &Store{db: db, dataDir: dataDir, home: home, pricing: NewPricing(dataDir)}, nil
}

// Close 关闭数据库资源
func (store *Store) Close() error { return store.db.Close() }

// Status 返回当前扫描进度
func (store *Store) Status() model.UsageStatus {
	store.statusMu.RLock()
	defer store.statusMu.RUnlock()
	return store.status
}

func (store *Store) changed() {
	store.statusMu.Lock()
	if !store.status.IsScanning {
		store.status.Revision++
	}
	store.statusMu.Unlock()
	if store.OnChanged != nil {
		store.OnChanged()
	}
}

func nullable(text string) any {
	if text == "" {
		return nil
	}
	return text
}

func saveUsage(ctx context.Context, tx *sql.Tx, row model.UsageRow, source string) error {
	conflict := " ON CONFLICT(event_key) DO NOTHING"
	if row.App == model.UsageOpencode {
		conflict = ` ON CONFLICT(event_key) DO UPDATE SET model=excluded.model,speed=excluded.speed,
 input=excluded.input,output=excluded.output,cache_read=excluded.cache_read,
 cache_write=excluded.cache_write,cache_write_1h=excluded.cache_write_1h,cost=excluded.cost`
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO usage
 (event_key,source,app,conversation,model,speed,time,input,output,cache_read,cache_write,cost,cache_write_1h)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`+conflict, row.ID, source, row.App, row.Conversation, row.Model, row.Speed, row.Time.UnixMilli(), row.Input, row.Output, row.CacheRead, row.CacheWrite, row.Cost, row.CacheWrite1h)
	if err != nil {
		return err
	}
	if row.ReportedCosts != nil {
		data, err := json.Marshal(row.ReportedCosts)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO scan_blob(name,value) VALUES(?,?)", "usage-cost:"+row.ID, string(data))
		return err
	}
	return nil
}

func saveConversation(ctx context.Context, tx *sql.Tx, id, title, cwd, source, branch string, subtasks, cacheAvailable bool) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO conversation(id,title,cwd,source,branch,subtasks,cache_write_avail)
 VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET
 title=COALESCE(excluded.title,conversation.title),cwd=COALESCE(excluded.cwd,conversation.cwd),
 source=COALESCE(NULLIF(excluded.source,''),conversation.source),branch=COALESCE(excluded.branch,conversation.branch),
 subtasks=MAX(conversation.subtasks,excluded.subtasks),cache_write_avail=MAX(conversation.cache_write_avail,excluded.cache_write_avail)`, id, nullable(title), nullable(cwd), source, nullable(branch), subtasks, cacheAvailable)
	return err
}

func bounds(from, to string) (time.Time, time.Time, error) {
	start := time.UnixMilli(0)
	end := time.Now().Add(time.Minute)
	var err error
	if from != "" {
		start, err = time.Parse(time.RFC3339Nano, from)
		if err != nil {
			return start, end, fmt.Errorf("开始时间: %w", err)
		}
	}
	if to != "" {
		end, err = time.Parse(time.RFC3339Nano, to)
		if err != nil {
			return start, end, fmt.Errorf("结束时间: %w", err)
		}
	}
	if !start.Before(end) {
		return start, end, fmt.Errorf("开始时间须早于结束时间")
	}
	return start, end, nil
}

const totalsSQL = `COALESCE(SUM(input),0),COALESCE(SUM(output),0),COALESCE(SUM(cache_read),0),COALESCE(SUM(cache_write),0),COALESCE(SUM(cache_write_1h),0),COUNT(*),COALESCE(SUM(cost),0)`

type sqlScanner interface{ Scan(...any) error }

func scanTotals(scanner sqlScanner) (model.UsageTotals, error) {
	var result model.UsageTotals
	err := scanner.Scan(&result.Input, &result.Output, &result.CacheRead, &result.CacheWrite, &result.CacheWrite1h, &result.Requests, &result.Cost)
	result.Add(model.UsageTotals{})
	return result, err
}

// Totals 在数据库内汇总服务的半开时间区间
func (store *Store) Totals(ctx context.Context, app *model.UsageApp, from, to time.Time) (model.UsageTotals, error) {
	return scanTotals(store.db.QueryRowContext(ctx, "SELECT "+totalsSQL+" FROM usage WHERE time>=? AND time<?"+sourceFilter("", app, nil), from.UnixMilli(), to.UnixMilli()))
}

// Interval 表示用于额度归属的半开时间区间
type Interval struct {
	From time.Time
	To   time.Time
}

// TotalsInIntervals 在SQL内汇总同一账号的实际使用区间
func (store *Store) TotalsInIntervals(ctx context.Context, app model.UsageApp, intervals []Interval) (model.UsageTotals, error) {
	clauses := []string{}
	arguments := []any{app}
	for _, interval := range intervals {
		if !interval.From.Before(interval.To) {
			continue
		}
		clauses = append(clauses, "(time>=? AND time<?)")
		arguments = append(arguments, interval.From.UnixMilli(), interval.To.UnixMilli())
	}
	if len(clauses) == 0 {
		return model.UsageTotals{}, nil
	}
	return scanTotals(store.db.QueryRowContext(ctx, "SELECT "+totalsSQL+" FROM usage WHERE app=? AND ("+strings.Join(clauses, " OR ")+")", arguments...))
}

// FirstEventAt 返回所选服务的首条本地计量时间
func (store *Store) FirstEventAt(ctx context.Context, app model.UsageApp) (*time.Time, error) {
	var milliseconds sql.NullInt64
	if err := store.db.QueryRowContext(ctx, "SELECT MIN(time) FROM usage WHERE app=?", app).Scan(&milliseconds); err != nil {
		return nil, err
	}
	if !milliseconds.Valid {
		return nil, nil
	}
	at := time.UnixMilli(milliseconds.Int64)
	return &at, nil
}

const eventColumns = `event_key,app,conversation,model,speed,time,input,output,cache_read,cache_write,cost,cache_write_1h`

func readRows(rows *sql.Rows) ([]model.UsageRow, error) {
	defer rows.Close()
	result := []model.UsageRow{}
	for rows.Next() {
		var row model.UsageRow
		var milliseconds int64
		if err := rows.Scan(&row.ID, &row.App, &row.Conversation, &row.Model, &row.Speed, &milliseconds, &row.Input, &row.Output, &row.CacheRead, &row.CacheWrite, &row.Cost, &row.CacheWrite1h); err != nil {
			return nil, err
		}
		row.Time = time.UnixMilli(milliseconds)
		row.Requests = 1
		result = append(result, row)
	}
	return result, rows.Err()
}

// HasData 检查服务是否已有统计记录
func (store *Store) HasData(ctx context.Context, app model.UsageApp) bool {
	var present int
	return store.db.QueryRowContext(ctx, "SELECT 1 FROM usage WHERE app=? LIMIT 1", app).Scan(&present) == nil
}

// RefreshPricing 刷新目录并重新计算已缺失的请求价格
func (store *Store) RefreshPricing(ctx context.Context) error {
	refreshErr := store.pricing.Refresh(ctx, true)
	store.gate.Lock()
	defer func() {
		store.gate.Unlock()
		store.changed()
	}()
	if err := store.repriceMissing(ctx); err != nil {
		return err
	}
	return refreshErr
}

// RefreshPricingIfNeeded 每日刷新目录并对新出现的缺价模型定时重查
func (store *Store) RefreshPricingIfNeeded(ctx context.Context) error {
	rows, err := store.db.QueryContext(ctx, `SELECT DISTINCT app,speed,model FROM usage
 WHERE cost IS NULL AND app<>2 AND speed<>'unknown' AND model<>'codex-auto-review'`)
	if err != nil {
		return err
	}
	missing := []string{}
	for rows.Next() {
		var app model.UsageApp
		var speed, name string
		if err = rows.Scan(&app, &speed, &name); err != nil {
			rows.Close()
			return err
		}
		missing = append(missing, fmt.Sprintf("%d|%s|%s", app, speed, name))
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	now := time.Now()
	force := false
	store.pricing.mu.Lock()
	for key, at := range store.pricing.active.MissingAttempts {
		if now.Sub(at) > 7*24*time.Hour {
			delete(store.pricing.active.MissingAttempts, key)
		}
	}
	for _, key := range missing {
		if at, recorded := store.pricing.active.MissingAttempts[key]; !recorded || now.Sub(at) >= 30*time.Minute {
			store.pricing.active.MissingAttempts[key] = now
			force = true
		}
	}
	store.pricing.mu.Unlock()
	refreshErr := store.pricing.Refresh(ctx, force)
	store.gate.Lock()
	defer func() {
		store.gate.Unlock()
		store.changed()
	}()
	if err = store.repriceMissing(ctx); err != nil {
		return err
	}
	return refreshErr
}
