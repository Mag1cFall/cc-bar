package usage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Mag1cFall/cc-bar/internal/model"
	_ "modernc.org/sqlite"
)

// Store 复用并维护V2压缩结构的用量数据库并序列化扫描写入
type Store struct {
	db            *sql.DB
	dataDir       string
	home          string
	pricing       *Pricing
	gate          sync.Mutex
	statusMu      sync.RWMutex
	status        model.UsageStatus
	dictMu        sync.RWMutex
	sources       map[string]int64
	convs         map[string]int64
	models        map[string]int64
	speeds        map[string]int64
	convNames     map[int64]string
	modelNames    map[int64]string
	speedNames    map[int64]string
	cursorBackoff time.Time
	OnChanged     func()
}

// Open 打开或无损升级用量数据库并初始化连接参数与内存字典
func Open(dataDir, home string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(dataDir, "usage.db")
	if err := migrateIfNeeded(dbPath); err != nil {
		return nil, err
	}
	fresh := false
	if info, err := os.Stat(dbPath); os.IsNotExist(err) || (err == nil && info.Size() == 0) {
		fresh = true
	}
	pragmas := []string{
		"busy_timeout(10000)",
		"journal_mode(WAL)",
		"synchronous(NORMAL)",
		"cache_size(-32768)",
		"temp_store(MEMORY)",
		"mmap_size(268435456)",
		"wal_autocheckpoint(1000)",
		"journal_size_limit(16777216)",
	}
	if fresh {
		pragmas = append([]string{"page_size(8192)"}, pragmas...)
	}
	options := url.Values{"_pragma": pragmas}
	databaseURL := "file:" + filepath.ToSlash(dbPath) + "?" + options.Encode()
	db, err := sql.Open("sqlite", databaseURL)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	if _, err = db.Exec(schemaV2Tables); err != nil {
		db.Close()
		return nil, err
	}
	if _, err = db.Exec(schemaV2IndexesAndTriggers); err != nil {
		db.Close()
		return nil, err
	}
	if _, err = db.Exec(fmt.Sprintf("PRAGMA user_version = %d", currentSchemaVersion)); err != nil {
		db.Close()
		return nil, err
	}
	store := &Store{
		db:         db,
		dataDir:    dataDir,
		home:       home,
		pricing:    NewPricing(dataDir),
		sources:    map[string]int64{},
		convs:      map[string]int64{},
		models:     map[string]int64{},
		speeds:     map[string]int64{},
		convNames:  map[int64]string{},
		modelNames: map[int64]string{},
		speedNames: map[int64]string{},
	}
	if err = store.loadDicts(); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

// loadDicts 预加载全部小字典表到内存以消除查询连接开销
func (store *Store) loadDicts() error {
	store.dictMu.Lock()
	defer store.dictMu.Unlock()
	store.sources = map[string]int64{}
	store.convs = map[string]int64{}
	store.models = map[string]int64{}
	store.speeds = map[string]int64{}
	store.convNames = map[int64]string{}
	store.modelNames = map[int64]string{}
	store.speedNames = map[int64]string{}
	load := func(query string, forward map[string]int64, reverse map[int64]string) error {
		rows, err := store.db.Query(query)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var name string
			if err = rows.Scan(&id, &name); err != nil {
				return err
			}
			forward[name] = id
			if reverse != nil {
				reverse[id] = name
			}
		}
		return rows.Err()
	}
	if err := load("SELECT id,path FROM usage_source", store.sources, nil); err != nil {
		return err
	}
	if err := load("SELECT id,name FROM usage_conv", store.convs, store.convNames); err != nil {
		return err
	}
	if err := load("SELECT id,name FROM usage_model", store.models, store.modelNames); err != nil {
		return err
	}
	return load("SELECT id,name FROM usage_speed", store.speeds, store.speedNames)
}

// dictSnapshot 复制小型模型与档位字典快照以避免并发读写竞争
func (store *Store) dictSnapshot() (map[int64]string, map[int64]string) {
	store.dictMu.RLock()
	defer store.dictMu.RUnlock()
	return maps.Clone(store.modelNames), maps.Clone(store.speedNames)
}

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// resolveDict 在指定查询器上解析模型与档位名称并在内存快照缺失时回填
func (store *Store) resolveDict(ctx context.Context, q rowQuerier, modelID, speedID int64, modelNames, speedNames map[int64]string) (string, string) {
	modelName, okModel := modelNames[modelID]
	speedName, okSpeed := speedNames[speedID]
	if okModel && okSpeed {
		return modelName, speedName
	}
	store.dictMu.RLock()
	if !okModel {
		if name, ok := store.modelNames[modelID]; ok {
			modelName, okModel = name, true
			modelNames[modelID] = name
		}
	}
	if !okSpeed {
		if name, ok := store.speedNames[speedID]; ok {
			speedName, okSpeed = name, true
			speedNames[speedID] = name
		}
	}
	store.dictMu.RUnlock()
	if okModel && okSpeed {
		return modelName, speedName
	}
	var loadedModel, loadedSpeed bool
	if !okModel && q.QueryRowContext(ctx, "SELECT name FROM usage_model WHERE id=?", modelID).Scan(&modelName) == nil {
		loadedModel = true
		modelNames[modelID] = modelName
	}
	if !okSpeed && q.QueryRowContext(ctx, "SELECT name FROM usage_speed WHERE id=?", speedID).Scan(&speedName) == nil {
		loadedSpeed = true
		speedNames[speedID] = speedName
	}
	if loadedModel || loadedSpeed {
		store.dictMu.Lock()
		if loadedModel {
			store.models[modelName] = modelID
			store.modelNames[modelID] = modelName
		}
		if loadedSpeed {
			store.speeds[speedName] = speedID
			store.speedNames[speedID] = speedName
		}
		store.dictMu.Unlock()
	}
	return modelName, speedName
}

// Close 尝试轻量截断WAL并关闭数据库资源
func (store *Store) Close() error {
	_, _ = store.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return store.db.Close()
}

// Status 返回当前扫描进度
func (store *Store) Status() model.UsageStatus {
	store.statusMu.RLock()
	defer store.statusMu.RUnlock()
	return store.status
}

func (store *Store) notify(bumpRevision bool) {
	if bumpRevision {
		store.statusMu.Lock()
		if !store.status.IsScanning {
			store.status.Revision++
		}
		store.statusMu.Unlock()
	}
	if store.OnChanged != nil {
		store.OnChanged()
	}
}

func (store *Store) changed() { store.notify(true) }

func nullable(text string) any {
	if text == "" {
		return nil
	}
	return text
}

type writeBatch struct {
	store         *Store
	tx            *sql.Tx
	insertStd     *sql.Stmt
	insertOpen    *sql.Stmt
	insertCost    *sql.Stmt
	newSources    map[string]int64
	newConvs      map[string]int64
	newModels     map[string]int64
	newSpeeds     map[string]int64
	touchedConvs  map[int64]struct{}
	mutated       bool
}

// beginWrite 启动包含预编译语句与事务级字典缓存的写入批次
func (store *Store) beginWrite(ctx context.Context) (*writeBatch, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	insertStd, err := tx.PrepareContext(ctx, `INSERT INTO usage
 (key_hash,source_id,app,conv_id,model_id,speed_id,time,input,output,cache_read,cache_write,cost,cache_write_1h,dsh_key)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(key_hash) DO NOTHING RETURNING id`)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	insertOpen, err := tx.PrepareContext(ctx, `INSERT INTO usage
 (key_hash,source_id,app,conv_id,model_id,speed_id,time,input,output,cache_read,cache_write,cost,cache_write_1h,dsh_key)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(key_hash) DO UPDATE SET
 model_id=excluded.model_id,speed_id=excluded.speed_id,input=excluded.input,output=excluded.output,
 cache_read=excluded.cache_read,cache_write=excluded.cache_write,cache_write_1h=excluded.cache_write_1h,cost=excluded.cost
 WHERE usage.model_id<>excluded.model_id OR usage.speed_id<>excluded.speed_id OR usage.input<>excluded.input
 OR usage.output<>excluded.output OR usage.cache_read<>excluded.cache_read OR usage.cache_write<>excluded.cache_write
 OR usage.cache_write_1h<>excluded.cache_write_1h OR usage.cost IS NOT excluded.cost
 RETURNING id`)
	if err != nil {
		insertStd.Close()
		_ = tx.Rollback()
		return nil, err
	}
	insertCost, err := tx.PrepareContext(ctx, "INSERT OR REPLACE INTO usage_cost(usage_id,input,output,cache_read,cache_write) VALUES(?,?,?,?,?)")
	if err != nil {
		insertStd.Close()
		insertOpen.Close()
		_ = tx.Rollback()
		return nil, err
	}
	return &writeBatch{
		store:        store,
		tx:           tx,
		insertStd:    insertStd,
		insertOpen:   insertOpen,
		insertCost:   insertCost,
		newSources:   map[string]int64{},
		newConvs:     map[string]int64{},
		newModels:    map[string]int64{},
		newSpeeds:    map[string]int64{},
		touchedConvs: map[int64]struct{}{},
	}, nil
}

func (batch *writeBatch) closeStmts() {
	if batch.insertStd != nil {
		batch.insertStd.Close()
		batch.insertStd = nil
	}
	if batch.insertOpen != nil {
		batch.insertOpen.Close()
		batch.insertOpen = nil
	}
	if batch.insertCost != nil {
		batch.insertCost.Close()
		batch.insertCost = nil
	}
}

func (batch *writeBatch) rollback() {
	batch.closeStmts()
	_ = batch.tx.Rollback()
}

func (batch *writeBatch) intern(ctx context.Context, shared map[string]int64, local map[string]int64, insertSQL, selectSQL, key string) (int64, error) {
	if id, ok := local[key]; ok {
		return id, nil
	}
	batch.store.dictMu.RLock()
	id, ok := shared[key]
	batch.store.dictMu.RUnlock()
	if ok {
		return id, nil
	}
	if _, err := batch.tx.ExecContext(ctx, insertSQL, key); err != nil {
		return 0, err
	}
	if err := batch.tx.QueryRowContext(ctx, selectSQL, key).Scan(&id); err != nil {
		return 0, err
	}
	local[key] = id
	return id, nil
}

func (batch *writeBatch) internSource(ctx context.Context, path string) (int64, error) {
	return batch.intern(ctx, batch.store.sources, batch.newSources,
		"INSERT OR IGNORE INTO usage_source(path) VALUES(?)",
		"SELECT id FROM usage_source WHERE path=?", path)
}

func (batch *writeBatch) internConv(ctx context.Context, name string) (int64, error) {
	return batch.intern(ctx, batch.store.convs, batch.newConvs,
		"INSERT OR IGNORE INTO usage_conv(name) VALUES(?)",
		"SELECT id FROM usage_conv WHERE name=?", name)
}

func (batch *writeBatch) internModel(ctx context.Context, name string) (int64, error) {
	return batch.intern(ctx, batch.store.models, batch.newModels,
		"INSERT OR IGNORE INTO usage_model(name) VALUES(?)",
		"SELECT id FROM usage_model WHERE name=?", name)
}

func (batch *writeBatch) internSpeed(ctx context.Context, name string) (int64, error) {
	return batch.intern(ctx, batch.store.speeds, batch.newSpeeds,
		"INSERT OR IGNORE INTO usage_speed(name) VALUES(?)",
		"SELECT id FROM usage_speed WHERE name=?", name)
}

// saveUsage 写入单条用量及可选的上报费用拆分
func (batch *writeBatch) saveUsage(ctx context.Context, row model.UsageRow, source string) error {
	srcID, err := batch.internSource(ctx, source)
	if err != nil {
		return err
	}
	convID, err := batch.internConv(ctx, row.Conversation)
	if err != nil {
		return err
	}
	modelID, err := batch.internModel(ctx, row.Model)
	if err != nil {
		return err
	}
	speedID, err := batch.internSpeed(ctx, row.Speed)
	if err != nil {
		return err
	}
	keyHash := hashEventKey(row.ID)
	var dshKey any
	if row.App == model.UsageDsh {
		dshKey = row.ID
	}
	stmt := batch.insertStd
	if row.App == model.UsageOpencode {
		stmt = batch.insertOpen
	}
	var usageID int64
	err = stmt.QueryRowContext(ctx, keyHash[:], srcID, row.App, convID, modelID, speedID, row.Time.UnixMilli(), row.Input, row.Output, row.CacheRead, row.CacheWrite, row.Cost, row.CacheWrite1h, dshKey).Scan(&usageID)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	batch.mutated = true
	batch.touchedConvs[convID] = struct{}{}
	if row.ReportedCosts != nil {
		if _, err = batch.insertCost.ExecContext(ctx, usageID, row.ReportedCosts.Input, row.ReportedCosts.Output, row.ReportedCosts.CacheRead, row.ReportedCosts.CacheWrite); err != nil {
			return err
		}
	}
	return nil
}

// deleteBySource 删除指定来源的全部用量并记录受影响的对话以便重建摘要
func (batch *writeBatch) deleteBySource(ctx context.Context, source string) error {
	batch.store.dictMu.RLock()
	srcID, ok := batch.store.sources[source]
	batch.store.dictMu.RUnlock()
	if !ok {
		if id, found := batch.newSources[source]; found {
			srcID, ok = id, true
		}
	}
	if !ok {
		return nil
	}
	rows, err := batch.tx.QueryContext(ctx, "SELECT DISTINCT conv_id FROM usage WHERE source_id=?", srcID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var convID int64
		if err = rows.Scan(&convID); err != nil {
			rows.Close()
			return err
		}
		batch.touchedConvs[convID] = struct{}{}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	res, err := batch.tx.ExecContext(ctx, "DELETE FROM usage WHERE source_id=?", srcID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		batch.mutated = true
	}
	return nil
}

// commit 同步受影响对话摘要提交事务并合并新增字典到内存缓存
func (batch *writeBatch) commit(ctx context.Context) error {
	if len(batch.touchedConvs) > 0 {
		if err := refreshConvSummaries(ctx, batch.tx, batch.touchedConvs); err != nil {
			batch.rollback()
			return err
		}
	}
	batch.closeStmts()
	if err := batch.tx.Commit(); err != nil {
		return err
	}
	if len(batch.newSources) > 0 || len(batch.newConvs) > 0 || len(batch.newModels) > 0 || len(batch.newSpeeds) > 0 {
		batch.store.dictMu.Lock()
		for k, id := range batch.newSources {
			batch.store.sources[k] = id
		}
		for k, id := range batch.newConvs {
			batch.store.convs[k] = id
			batch.store.convNames[id] = k
		}
		for k, id := range batch.newModels {
			batch.store.models[k] = id
			batch.store.modelNames[id] = k
		}
		for k, id := range batch.newSpeeds {
			batch.store.speeds[k] = id
			batch.store.speedNames[id] = k
		}
		batch.store.dictMu.Unlock()
	}
	return nil
}

// refreshConvSummaries 从usage真源重算指定对话的汇总行以保证零漂移
func refreshConvSummaries(ctx context.Context, tx *sql.Tx, convIDs map[int64]struct{}) error {
	queryStmt, err := tx.PrepareContext(ctx, `SELECT MIN(u.app),MIN(u.time),MAX(u.time),
 COALESCE(SUM(u.input),0),COALESCE(SUM(u.output),0),COALESCE(SUM(u.cache_read),0),
 COALESCE(SUM(u.cache_write),0),COALESCE(SUM(u.cache_write_1h),0),COUNT(*),COALESCE(SUM(u.cost),0),
 COALESCE(GROUP_CONCAT(DISTINCT m.name),''),COALESCE(SUM(CASE WHEN s.name='fast' THEN 1 ELSE 0 END),0)
 FROM usage u
 JOIN usage_model m ON m.id=u.model_id
 JOIN usage_speed s ON s.id=u.speed_id
 WHERE u.conv_id=?`)
	if err != nil {
		return err
	}
	defer queryStmt.Close()
	upsertStmt, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO conv_summary
 (conv_id,app,first_time,last_time,input,output,cache_read,cache_write,cache_write_1h,requests,cost,models,fast_requests)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer upsertStmt.Close()
	delStmt, err := tx.PrepareContext(ctx, "DELETE FROM conv_summary WHERE conv_id=?")
	if err != nil {
		return err
	}
	defer delStmt.Close()

	for convID := range convIDs {
		var app, first, last sql.NullInt64
		var input, output, cacheRead, cacheWrite, cacheWrite1h, requests, fast int64
		var cost float64
		var models string
		if err = queryStmt.QueryRowContext(ctx, convID).Scan(&app, &first, &last, &input, &output, &cacheRead, &cacheWrite, &cacheWrite1h, &requests, &cost, &models, &fast); err != nil {
			return err
		}
		if requests == 0 || !app.Valid {
			if _, err = delStmt.ExecContext(ctx, convID); err != nil {
				return err
			}
			continue
		}
		parts := strings.Split(models, ",")
		sort.Strings(parts)
		if _, err = upsertStmt.ExecContext(ctx, convID, app.Int64, first.Int64, last.Int64, input, output, cacheRead, cacheWrite, cacheWrite1h, requests, cost, strings.Join(parts, ","), fast); err != nil {
			return err
		}
	}
	return nil
}

func saveConversation(ctx context.Context, tx *sql.Tx, id, title, cwd, source, branch string, subtasks, cacheAvailable bool) (bool, error) {
	res, err := tx.ExecContext(ctx, `INSERT INTO conversation(id,title,cwd,source,branch,subtasks,cache_write_avail)
 VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET
 title=COALESCE(excluded.title,conversation.title),cwd=COALESCE(excluded.cwd,conversation.cwd),
 source=COALESCE(NULLIF(excluded.source,''),conversation.source),branch=COALESCE(excluded.branch,conversation.branch),
 subtasks=MAX(conversation.subtasks,excluded.subtasks),cache_write_avail=MAX(conversation.cache_write_avail,excluded.cache_write_avail)
 WHERE COALESCE(excluded.title,conversation.title) IS NOT conversation.title
  OR COALESCE(excluded.cwd,conversation.cwd) IS NOT conversation.cwd
  OR COALESCE(NULLIF(excluded.source,''),conversation.source) IS NOT conversation.source
  OR COALESCE(excluded.branch,conversation.branch) IS NOT conversation.branch
  OR MAX(conversation.subtasks,excluded.subtasks)<>conversation.subtasks
  OR MAX(conversation.cache_write_avail,excluded.cache_write_avail)<>conversation.cache_write_avail`, id, nullable(title), nullable(cwd), source, nullable(branch), subtasks, cacheAvailable)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
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
const rollupTotalsSQL = `COALESCE(SUM(input),0),COALESCE(SUM(output),0),COALESCE(SUM(cache_read),0),COALESCE(SUM(cache_write),0),COALESCE(SUM(cache_write_1h),0),COALESCE(SUM(requests),0),COALESCE(SUM(cost),0)`

type sqlScanner interface{ Scan(...any) error }

func scanTotals(scanner sqlScanner) (model.UsageTotals, error) {
	var result model.UsageTotals
	err := scanner.Scan(&result.Input, &result.Output, &result.CacheRead, &result.CacheWrite, &result.CacheWrite1h, &result.Requests, &result.Cost)
	result.Add(model.UsageTotals{})
	return result, err
}

// splitRollupRange 将半开毫秒区间切分为15分钟完整桶与首尾零头边界
func splitRollupRange(startMs, endMs int64) (left [2]int64, buckets [2]int64, right [2]int64) {
	if startMs >= endMs {
		return
	}
	if startMs < 0 {
		if endMs <= 0 {
			return [2]int64{startMs, endMs}, [2]int64{}, [2]int64{}
		}
		first := int64(0)
		last := endMs / rollupBucketMs
		if last <= first {
			return [2]int64{startMs, endMs}, [2]int64{}, [2]int64{}
		}
		return [2]int64{startMs, 0}, [2]int64{first, last}, [2]int64{last * rollupBucketMs, endMs}
	}
	first := (startMs + rollupBucketMs - 1) / rollupBucketMs
	last := endMs / rollupBucketMs
	if first >= last {
		return [2]int64{startMs, endMs}, [2]int64{}, [2]int64{}
	}
	return [2]int64{startMs, first * rollupBucketMs}, [2]int64{first, last}, [2]int64{last * rollupBucketMs, endMs}
}

// Totals 在单一只读快照内汇总半开时间区间的预聚合桶与索引边缘
func (store *Store) Totals(ctx context.Context, app *model.UsageApp, from, to time.Time) (model.UsageTotals, error) {
	var total model.UsageTotals
	left, buckets, right := splitRollupRange(from.UnixMilli(), to.UnixMilli())
	if buckets[0] >= buckets[1] && left[0] >= left[1] && right[0] >= right[1] {
		return total, nil
	}
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return total, err
	}
	defer tx.Rollback()
	rawFilter := rawAppFilter("", app, nil)
	if buckets[0] < buckets[1] {
		part, err := scanTotals(tx.QueryRowContext(ctx, "SELECT "+rollupTotalsSQL+" FROM usage_rollup WHERE bucket>=? AND bucket<?"+sourceFilter("", app, nil), buckets[0], buckets[1]))
		if err != nil {
			return total, err
		}
		total.Add(part)
	}
	for _, edge := range [2][2]int64{left, right} {
		if edge[0] < edge[1] {
			part, err := scanTotals(tx.QueryRowContext(ctx, "SELECT "+totalsSQL+" FROM usage WHERE time>=? AND time<?"+rawFilter, edge[0], edge[1]))
			if err != nil {
				return total, err
			}
			total.Add(part)
		}
	}
	return total, nil
}

// Interval 表示用于额度归属的半开时间区间
type Interval struct {
	From time.Time
	To   time.Time
}

// matchModelFamily 判断模型名是否归属于指定的 Antigravity 额度模型族
func matchModelFamily(name, family string) bool {
	if family == "" {
		return true
	}
	norm := NormalizeModel(name)
	isArgon := strings.Contains(norm, "argon")
	isGemini := !isArgon && strings.HasPrefix(norm, "gemini")
	switch family {
	case "argon":
		return isArgon
	case "gemini":
		return isGemini
	case "claude":
		return !isArgon && !isGemini
	default:
		return true
	}
}

// familyModelIDs 在指定只读事务快照中收集属于目标模型族的模型编号
func familyModelIDs(ctx context.Context, tx *sql.Tx, family string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id,name FROM usage_model")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id int64
		var name string
		if err = rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		if matchModelFamily(name, family) {
			ids = append(ids, strconv.FormatInt(id, 10))
		}
	}
	return ids, rows.Err()
}

// TotalsInIntervals 在单一只读快照内汇总同一账号的实际使用区间
func (store *Store) TotalsInIntervals(ctx context.Context, app model.UsageApp, intervals []Interval) (model.UsageTotals, error) {
	return store.TotalsInIntervalsForFamily(ctx, app, "", intervals)
}

// TotalsInIntervalsForFamily 在单一只读快照内按可选模型族汇总使用区间
func (store *Store) TotalsInIntervalsForFamily(ctx context.Context, app model.UsageApp, family string, intervals []Interval) (model.UsageTotals, error) {
	var total model.UsageTotals
	bucketClauses := []string{}
	bucketArgs := []any{app}
	rawClauses := []string{}
	rawArgs := []any{app}
	for _, interval := range intervals {
		if !interval.From.Before(interval.To) {
			continue
		}
		left, buckets, right := splitRollupRange(interval.From.UnixMilli(), interval.To.UnixMilli())
		if buckets[0] < buckets[1] {
			bucketClauses = append(bucketClauses, "(bucket>=? AND bucket<?)")
			bucketArgs = append(bucketArgs, buckets[0], buckets[1])
		}
		if left[0] < left[1] {
			rawClauses = append(rawClauses, "(time>=? AND time<?)")
			rawArgs = append(rawArgs, left[0], left[1])
		}
		if right[0] < right[1] {
			rawClauses = append(rawClauses, "(time>=? AND time<?)")
			rawArgs = append(rawArgs, right[0], right[1])
		}
	}
	if len(bucketClauses) == 0 && len(rawClauses) == 0 {
		return total, nil
	}
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return total, err
	}
	defer tx.Rollback()
	modelFilter := ""
	if family != "" {
		ids, idErr := familyModelIDs(ctx, tx, family)
		if idErr != nil {
			return total, idErr
		}
		if len(ids) == 0 {
			return total, nil
		}
		modelFilter = " AND model_id IN (" + strings.Join(ids, ",") + ")"
	}
	if len(bucketClauses) > 0 {
		part, err := scanTotals(tx.QueryRowContext(ctx, "SELECT "+rollupTotalsSQL+" FROM usage_rollup WHERE app=?"+modelFilter+" AND ("+strings.Join(bucketClauses, " OR ")+")", bucketArgs...))
		if err != nil {
			return total, err
		}
		total.Add(part)
	}
	if len(rawClauses) > 0 {
		part, err := scanTotals(tx.QueryRowContext(ctx, "SELECT "+totalsSQL+" FROM usage WHERE app=?"+modelFilter+" AND ("+strings.Join(rawClauses, " OR ")+")", rawArgs...))
		if err != nil {
			return total, err
		}
		total.Add(part)
	}
	return total, nil
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

// HasData 检查服务是否已有统计记录
func (store *Store) HasData(ctx context.Context, app model.UsageApp) bool {
	var present int
	return store.db.QueryRowContext(ctx, "SELECT 1 FROM usage WHERE app=? LIMIT 1", app).Scan(&present) == nil
}

// RefreshPricing 刷新目录并重算非上报记录中价格发生变化的条目
func (store *Store) RefreshPricing(ctx context.Context) error {
	refreshErr := store.pricing.Refresh(ctx, true)
	store.gate.Lock()
	defer func() {
		store.gate.Unlock()
		store.changed()
	}()
	if _, err := store.repriceAll(ctx); err != nil {
		return err
	}
	return refreshErr
}

// RefreshPricingIfNeeded 每日刷新目录并对首次出现的缺价模型立即重查且对已知无价模型退避24小时
func (store *Store) RefreshPricingIfNeeded(ctx context.Context) error {
	rows, err := store.db.QueryContext(ctx, `SELECT DISTINCT app,speed_id,model_id FROM usage WHERE cost IS NULL AND app<>2`)
	if err != nil {
		return err
	}
	type rawCombo struct {
		app     model.UsageApp
		speedID int64
		modelID int64
	}
	combos := []rawCombo{}
	for rows.Next() {
		var c rawCombo
		if err = rows.Scan(&c.app, &c.speedID, &c.modelID); err != nil {
			rows.Close()
			return err
		}
		combos = append(combos, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	modelNames, speedNames := store.dictSnapshot()
	now := time.Now()
	missing := []string{}
	canReprice := false
	for _, c := range combos {
		name, speed := store.resolveDict(ctx, store.db, c.modelID, c.speedID, modelNames, speedNames)
		if speed == "unknown" || name == "codex-auto-review" {
			continue
		}
		if store.pricing.canPrice(c.app, name, speed, now) {
			canReprice = true
			continue
		}
		missing = append(missing, fmt.Sprintf("%d|%s|%s", c.app, speed, name))
	}
	force := false
	toRecord := []string{}
	store.pricing.mu.Lock()
	for key, at := range store.pricing.active.MissingAttempts {
		if now.Sub(at) > 7*24*time.Hour {
			delete(store.pricing.active.MissingAttempts, key)
		}
	}
	for _, key := range missing {
		if at, recorded := store.pricing.active.MissingAttempts[key]; !recorded || now.Sub(at) >= 24*time.Hour {
			toRecord = append(toRecord, key)
			force = true
		}
	}
	store.pricing.mu.Unlock()
	beforeVersion := store.pricing.Version()
	refreshErr := store.pricing.Refresh(ctx, force)
	if refreshErr == nil && len(toRecord) > 0 {
		store.pricing.refresh.Lock()
		store.pricing.mu.Lock()
		for _, key := range toRecord {
			store.pricing.active.MissingAttempts[key] = now
		}
		data, marshalErr := json.Marshal(store.pricing.active)
		store.pricing.mu.Unlock()
		if marshalErr == nil {
			_ = os.WriteFile(store.pricing.path, data, 0600)
		}
		store.pricing.refresh.Unlock()
	}
	afterVersion := store.pricing.Version()
	if !force && !canReprice && afterVersion == beforeVersion {
		return refreshErr
	}
	store.gate.Lock()
	updated, repriceErr := store.repriceMissing(ctx)
	store.gate.Unlock()
	if updated {
		store.changed()
	}
	if repriceErr != nil {
		return repriceErr
	}
	return refreshErr
}
