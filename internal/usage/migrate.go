package usage

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	currentSchemaVersion = 2
	rollupBucketMs       = 15 * 60 * 1000
)

const schemaV2Tables = `
CREATE TABLE IF NOT EXISTS usage_source (id INTEGER PRIMARY KEY, path TEXT NOT NULL UNIQUE);
CREATE TABLE IF NOT EXISTS usage_conv (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE);
CREATE TABLE IF NOT EXISTS usage_model (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE);
CREATE TABLE IF NOT EXISTS usage_speed (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE);
CREATE TABLE IF NOT EXISTS usage (
 id INTEGER PRIMARY KEY,
 key_hash BLOB NOT NULL,
 source_id INTEGER NOT NULL,
 app INTEGER NOT NULL,
 conv_id INTEGER NOT NULL,
 model_id INTEGER NOT NULL,
 speed_id INTEGER NOT NULL,
 time INTEGER NOT NULL,
 input INTEGER NOT NULL,
 output INTEGER NOT NULL,
 cache_read INTEGER NOT NULL,
 cache_write INTEGER NOT NULL,
 cost REAL,
 cache_write_1h INTEGER NOT NULL DEFAULT 0,
 dsh_key TEXT
);
CREATE TABLE IF NOT EXISTS usage_cost (
 usage_id INTEGER PRIMARY KEY,
 input REAL NOT NULL,
 output REAL NOT NULL,
 cache_read REAL NOT NULL,
 cache_write REAL NOT NULL
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS usage_rollup (
 bucket INTEGER NOT NULL,
 app INTEGER NOT NULL,
 model_id INTEGER NOT NULL,
 speed_id INTEGER NOT NULL,
 input INTEGER NOT NULL,
 output INTEGER NOT NULL,
 cache_read INTEGER NOT NULL,
 cache_write INTEGER NOT NULL,
 cache_write_1h INTEGER NOT NULL,
 requests INTEGER NOT NULL,
 cost REAL NOT NULL,
 PRIMARY KEY(bucket,app,model_id,speed_id)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS conv_summary (
 conv_id INTEGER PRIMARY KEY,
 app INTEGER NOT NULL,
 first_time INTEGER NOT NULL,
 last_time INTEGER NOT NULL,
 input INTEGER NOT NULL,
 output INTEGER NOT NULL,
 cache_read INTEGER NOT NULL,
 cache_write INTEGER NOT NULL,
 cache_write_1h INTEGER NOT NULL,
 requests INTEGER NOT NULL,
 cost REAL NOT NULL,
 models TEXT NOT NULL,
 fast_requests INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS scan_file (
 path TEXT PRIMARY KEY,length INTEGER NOT NULL,modified INTEGER NOT NULL,
 offset INTEGER NOT NULL,model TEXT,speed TEXT,signature TEXT,
 session TEXT,emitting TEXT,title TEXT,cwd TEXT,branch TEXT,
 subtasks INTEGER NOT NULL DEFAULT 0,sidechain_all INTEGER
);
CREATE TABLE IF NOT EXISTS conversation (
 id TEXT PRIMARY KEY,title TEXT,cwd TEXT,source TEXT,branch TEXT,
 subtasks INTEGER NOT NULL DEFAULT 0,project TEXT,project_status TEXT,
 cache_write_avail INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS scan_meta(name TEXT PRIMARY KEY,value INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS scan_blob(name TEXT PRIMARY KEY,value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS scan_candidate(
 path TEXT NOT NULL,session TEXT NOT NULL,cwd TEXT NOT NULL,
 sidechain INTEGER NOT NULL,PRIMARY KEY(path,session,cwd,sidechain)
);`

const schemaV2IndexesAndTriggers = `
CREATE UNIQUE INDEX IF NOT EXISTS usage_key_hash ON usage(key_hash);
CREATE INDEX IF NOT EXISTS usage_app_time ON usage(app,time);
CREATE INDEX IF NOT EXISTS usage_conv_time ON usage(conv_id,time);
CREATE INDEX IF NOT EXISTS usage_source_idx ON usage(source_id);
CREATE INDEX IF NOT EXISTS usage_missing_cost ON usage(app,speed_id,model_id) WHERE cost IS NULL AND app<>2;
CREATE INDEX IF NOT EXISTS conv_summary_time ON conv_summary(last_time,first_time);
CREATE TRIGGER IF NOT EXISTS usage_ai AFTER INSERT ON usage BEGIN
 INSERT INTO usage_rollup(bucket,app,model_id,speed_id,input,output,cache_read,cache_write,cache_write_1h,requests,cost)
 VALUES(NEW.time/900000,NEW.app,NEW.model_id,NEW.speed_id,NEW.input,NEW.output,NEW.cache_read,NEW.cache_write,NEW.cache_write_1h,1,COALESCE(NEW.cost,0))
 ON CONFLICT(bucket,app,model_id,speed_id) DO UPDATE SET
  input=input+excluded.input,
  output=output+excluded.output,
  cache_read=cache_read+excluded.cache_read,
  cache_write=cache_write+excluded.cache_write,
  cache_write_1h=cache_write_1h+excluded.cache_write_1h,
  requests=requests+1,
  cost=cost+excluded.cost;
END;
CREATE TRIGGER IF NOT EXISTS usage_ad AFTER DELETE ON usage BEGIN
 UPDATE usage_rollup SET
  input=input-OLD.input,
  output=output-OLD.output,
  cache_read=cache_read-OLD.cache_read,
  cache_write=cache_write-OLD.cache_write,
  cache_write_1h=cache_write_1h-OLD.cache_write_1h,
  requests=requests-1,
  cost=cost-COALESCE(OLD.cost,0)
 WHERE bucket=OLD.time/900000 AND app=OLD.app AND model_id=OLD.model_id AND speed_id=OLD.speed_id;
 DELETE FROM usage_rollup
 WHERE bucket=OLD.time/900000 AND app=OLD.app AND model_id=OLD.model_id AND speed_id=OLD.speed_id AND requests<=0;
 DELETE FROM usage_cost WHERE usage_id=OLD.id;
END;
CREATE TRIGGER IF NOT EXISTS usage_au AFTER UPDATE ON usage
WHEN OLD.time<>NEW.time OR OLD.app<>NEW.app OR OLD.model_id<>NEW.model_id OR OLD.speed_id<>NEW.speed_id
 OR OLD.input<>NEW.input OR OLD.output<>NEW.output OR OLD.cache_read<>NEW.cache_read OR OLD.cache_write<>NEW.cache_write
 OR OLD.cache_write_1h<>NEW.cache_write_1h OR OLD.cost IS NOT NEW.cost
BEGIN
 UPDATE usage_rollup SET
  input=input-OLD.input,
  output=output-OLD.output,
  cache_read=cache_read-OLD.cache_read,
  cache_write=cache_write-OLD.cache_write,
  cache_write_1h=cache_write_1h-OLD.cache_write_1h,
  requests=requests-1,
  cost=cost-COALESCE(OLD.cost,0)
 WHERE bucket=OLD.time/900000 AND app=OLD.app AND model_id=OLD.model_id AND speed_id=OLD.speed_id;
 DELETE FROM usage_rollup
 WHERE bucket=OLD.time/900000 AND app=OLD.app AND model_id=OLD.model_id AND speed_id=OLD.speed_id AND requests<=0;
 INSERT INTO usage_rollup(bucket,app,model_id,speed_id,input,output,cache_read,cache_write,cache_write_1h,requests,cost)
 VALUES(NEW.time/900000,NEW.app,NEW.model_id,NEW.speed_id,NEW.input,NEW.output,NEW.cache_read,NEW.cache_write,NEW.cache_write_1h,1,COALESCE(NEW.cost,0))
 ON CONFLICT(bucket,app,model_id,speed_id) DO UPDATE SET
  input=input+excluded.input,
  output=output+excluded.output,
  cache_read=cache_read+excluded.cache_read,
  cache_write=cache_write+excluded.cache_write,
  cache_write_1h=cache_write_1h+excluded.cache_write_1h,
  requests=requests+1,
  cost=cost+excluded.cost;
END;`

// hashEventKey 计算去重主键的128位确定性摘要
func hashEventKey(key string) [16]byte {
	sum := sha256.Sum256([]byte(key))
	var out [16]byte
	copy(out[:], sum[:16])
	return out
}

// recoverInterruptedMigration 恢复意外中断留下的临时数据库状态
func recoverInterruptedMigration(dbPath string) error {
	bakPath := dbPath + ".v1bak"
	tmpPath := dbPath + ".v2tmp"
	_ = os.Remove(tmpPath)
	_ = os.Remove(tmpPath + "-wal")
	_ = os.Remove(tmpPath + "-shm")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		if _, bakErr := os.Stat(bakPath); bakErr == nil {
			for _, suffix := range []string{"-wal", "-shm", ""} {
				if _, statErr := os.Stat(bakPath + suffix); statErr == nil {
					if err = os.Rename(bakPath+suffix, dbPath+suffix); err != nil {
						return fmt.Errorf("恢复旧版数据库: %w", err)
					}
				}
			}
		}
	} else {
		_ = os.Remove(bakPath)
		_ = os.Remove(bakPath + "-wal")
		_ = os.Remove(bakPath + "-shm")
	}
	return nil
}

// tableColumns 查询表中当前实际存在的列名集合
func tableColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, kind string
		var defaults any
		if err = rows.Scan(&cid, &name, &kind, &notnull, &defaults, &pk); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, rows.Err()
}

// migrateIfNeeded 检测旧版V1数据库并通过全新紧凑文件流式无损升级到V2
func migrateIfNeeded(dbPath string) error {
	if err := recoverInterruptedMigration(dbPath); err != nil {
		return err
	}
	info, err := os.Stat(dbPath)
	if os.IsNotExist(err) || (err == nil && info.Size() == 0) {
		return nil
	}
	if err != nil {
		return err
	}
	probeOpts := url.Values{"_pragma": {"busy_timeout(10000)"}}
	probe, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?"+probeOpts.Encode())
	if err != nil {
		return err
	}
	probe.SetMaxOpenConns(1)
	var version int
	if err = probe.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		probe.Close()
		return err
	}
	if version >= currentSchemaVersion {
		probe.Close()
		return nil
	}
	cols, err := tableColumns(probe, "usage")
	if err != nil {
		probe.Close()
		return err
	}
	if len(cols) == 0 || !cols["event_key"] {
		probe.Close()
		return nil
	}
	if err = migrateV1ToV2(probe, dbPath, cols); err != nil {
		probe.Close()
		return err
	}
	return nil
}

type rollupKey struct {
	bucket  int64
	app     int
	modelID int64
	speedID int64
}

type rollupAcc struct {
	input, output, cacheRead, cacheWrite, cacheWrite1h, requests int64
	cost                                                         float64
}

type convAcc struct {
	app                                                          int
	first, last                                                  int64
	input, output, cacheRead, cacheWrite, cacheWrite1h, requests int64
	fast                                                         int64
	cost                                                         float64
	models                                                       map[string]struct{}
}

type costParts struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

// migrateV1ToV2 从只读旧库流式构建紧凑8KB页V2新库并原子替换
func migrateV1ToV2(oldDB *sql.DB, dbPath string, usageCols map[string]bool) error {
	tmpPath := dbPath + ".v2tmp"
	bakPath := dbPath + ".v1bak"
	_ = os.Remove(tmpPath)
	_ = os.Remove(tmpPath + "-wal")
	_ = os.Remove(tmpPath + "-shm")

	newDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(tmpPath))
	if err != nil {
		return err
	}
	newDB.SetMaxOpenConns(1)
	cleanup := func() {
		newDB.Close()
		_ = os.Remove(tmpPath)
		_ = os.Remove(tmpPath + "-wal")
		_ = os.Remove(tmpPath + "-shm")
	}

	if _, err = newDB.Exec(`
PRAGMA page_size = 8192;
PRAGMA journal_mode = OFF;
PRAGMA synchronous = OFF;
PRAGMA locking_mode = EXCLUSIVE;
PRAGMA temp_store = MEMORY;
PRAGMA cache_size = -65536;`); err != nil {
		cleanup()
		return err
	}
	if _, err = newDB.Exec(schemaV2Tables); err != nil {
		cleanup()
		return err
	}

	var expectedCount int64
	if err = oldDB.QueryRow("SELECT COUNT(*) FROM usage").Scan(&expectedCount); err != nil {
		cleanup()
		return err
	}

	tx, err := newDB.Begin()
	if err != nil {
		cleanup()
		return err
	}

	if err = copySmallTables(oldDB, tx); err != nil {
		_ = tx.Rollback()
		cleanup()
		return err
	}

	costMap, err := loadLegacyCostBlobs(oldDB)
	if err != nil {
		_ = tx.Rollback()
		cleanup()
		return err
	}

	copied, err := streamUsageToV2(oldDB, tx, usageCols, costMap)
	if err != nil {
		_ = tx.Rollback()
		cleanup()
		return err
	}
	if copied != expectedCount {
		_ = tx.Rollback()
		cleanup()
		return fmt.Errorf("迁移行数校验不一致: 源=%d 目标=%d", expectedCount, copied)
	}

	if _, err = tx.Exec(schemaV2IndexesAndTriggers); err != nil {
		_ = tx.Rollback()
		cleanup()
		return err
	}
	if _, err = tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", currentSchemaVersion)); err != nil {
		_ = tx.Rollback()
		cleanup()
		return err
	}
	if err = tx.Commit(); err != nil {
		cleanup()
		return err
	}

	if _, err = newDB.Exec(`
ANALYZE;
PRAGMA locking_mode = NORMAL;
PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;
PRAGMA journal_size_limit = 16777216;
PRAGMA wal_checkpoint(TRUNCATE);`); err != nil {
		cleanup()
		return err
	}
	if err = newDB.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	var busy, logFrames, checkpointed int
	if err = oldDB.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if busy != 0 || logFrames != checkpointed {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("旧版数据库WAL未完成截断: busy=%d log=%d checkpointed=%d", busy, logFrames, checkpointed)
	}
	if err = oldDB.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}

	_ = os.Remove(bakPath)
	_ = os.Remove(bakPath + "-wal")
	_ = os.Remove(bakPath + "-shm")
	if err = os.Rename(dbPath, bakPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	movedWAL, movedSHM := false, false
	restore := func() {
		if movedSHM {
			_ = os.Rename(bakPath+"-shm", dbPath+"-shm")
		}
		if movedWAL {
			_ = os.Rename(bakPath+"-wal", dbPath+"-wal")
		}
		_ = os.Rename(bakPath, dbPath)
	}
	if _, statErr := os.Stat(dbPath + "-wal"); statErr == nil {
		if err = os.Rename(dbPath+"-wal", bakPath+"-wal"); err != nil {
			restore()
			_ = os.Remove(tmpPath)
			return err
		}
		movedWAL = true
	}
	if _, statErr := os.Stat(dbPath + "-shm"); statErr == nil {
		if err = os.Rename(dbPath+"-shm", bakPath+"-shm"); err != nil {
			restore()
			_ = os.Remove(tmpPath)
			return err
		}
		movedSHM = true
	}
	if err = os.Rename(tmpPath, dbPath); err != nil {
		restore()
		_ = os.Remove(tmpPath)
		return err
	}
	_ = os.Remove(bakPath)
	_ = os.Remove(bakPath + "-wal")
	_ = os.Remove(bakPath + "-shm")
	return nil
}

// copySmallTables 完整复制旧库中的扫描游标对话与元数据表
func copySmallTables(oldDB *sql.DB, tx *sql.Tx) error {
	// scan_file
	if cols, err := tableColumns(oldDB, "scan_file"); err == nil && len(cols) > 0 {
		branchExpr := "NULL"
		if cols["branch"] {
			branchExpr = "branch"
		}
		subtasksExpr := "0"
		if cols["subtasks"] {
			subtasksExpr = "subtasks"
		}
		sidechainExpr := "NULL"
		if cols["sidechain_all"] {
			sidechainExpr = "sidechain_all"
		}
		rows, err := oldDB.Query("SELECT path,length,modified,offset,model,speed,signature,session,emitting,title,cwd," + branchExpr + "," + subtasksExpr + "," + sidechainExpr + " FROM scan_file")
		if err != nil {
			return err
		}
		stmt, err := tx.Prepare("INSERT OR REPLACE INTO scan_file(path,length,modified,offset,model,speed,signature,session,emitting,title,cwd,branch,subtasks,sidechain_all) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)")
		if err != nil {
			rows.Close()
			return err
		}
		for rows.Next() {
			var path string
			var length, modified, offset int64
			var modelName, speed, sig, session, emitting, title, cwd, branch sql.NullString
			var subtasks int64
			var sidechain sql.NullInt64
			if err = rows.Scan(&path, &length, &modified, &offset, &modelName, &speed, &sig, &session, &emitting, &title, &cwd, &branch, &subtasks, &sidechain); err != nil {
				stmt.Close()
				rows.Close()
				return err
			}
			if _, err = stmt.Exec(path, length, modified, offset, modelName, speed, sig, session, emitting, title, cwd, branch, subtasks, sidechain); err != nil {
				stmt.Close()
				rows.Close()
				return err
			}
		}
		stmt.Close()
		if err = rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}

	// conversation
	if cols, err := tableColumns(oldDB, "conversation"); err == nil && len(cols) > 0 {
		optCol := func(name, def string) string {
			if cols[name] {
				return name
			}
			return def
		}
		rows, err := oldDB.Query("SELECT id,title,cwd,source," + optCol("branch", "NULL") + "," + optCol("subtasks", "0") + "," + optCol("project", "NULL") + "," + optCol("project_status", "NULL") + "," + optCol("cache_write_avail", "0") + " FROM conversation")
		if err != nil {
			return err
		}
		stmt, err := tx.Prepare("INSERT OR REPLACE INTO conversation(id,title,cwd,source,branch,subtasks,project,project_status,cache_write_avail) VALUES(?,?,?,?,?,?,?,?,?)")
		if err != nil {
			rows.Close()
			return err
		}
		for rows.Next() {
			var id string
			var title, cwd, source, branch, project, status sql.NullString
			var subtasks, cacheAvail int64
			if err = rows.Scan(&id, &title, &cwd, &source, &branch, &subtasks, &project, &status, &cacheAvail); err != nil {
				stmt.Close()
				rows.Close()
				return err
			}
			if _, err = stmt.Exec(id, title, cwd, source, branch, subtasks, project, status, cacheAvail); err != nil {
				stmt.Close()
				rows.Close()
				return err
			}
		}
		stmt.Close()
		if err = rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}

	// scan_meta
	if cols, err := tableColumns(oldDB, "scan_meta"); err == nil && len(cols) > 0 {
		rows, err := oldDB.Query("SELECT name,value FROM scan_meta")
		if err != nil {
			return err
		}
		stmt, err := tx.Prepare("INSERT OR REPLACE INTO scan_meta(name,value) VALUES(?,?)")
		if err != nil {
			rows.Close()
			return err
		}
		for rows.Next() {
			var name string
			var value int64
			if err = rows.Scan(&name, &value); err != nil {
				stmt.Close()
				rows.Close()
				return err
			}
			if _, err = stmt.Exec(name, value); err != nil {
				stmt.Close()
				rows.Close()
				return err
			}
		}
		stmt.Close()
		if err = rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}

	// scan_candidate
	if cols, err := tableColumns(oldDB, "scan_candidate"); err == nil && len(cols) > 0 {
		rows, err := oldDB.Query("SELECT path,session,cwd,sidechain FROM scan_candidate")
		if err != nil {
			return err
		}
		stmt, err := tx.Prepare("INSERT OR IGNORE INTO scan_candidate(path,session,cwd,sidechain) VALUES(?,?,?,?)")
		if err != nil {
			rows.Close()
			return err
		}
		for rows.Next() {
			var path, session, cwd string
			var sidechain int64
			if err = rows.Scan(&path, &session, &cwd, &sidechain); err != nil {
				stmt.Close()
				rows.Close()
				return err
			}
			if _, err = stmt.Exec(path, session, cwd, sidechain); err != nil {
				stmt.Close()
				rows.Close()
				return err
			}
		}
		stmt.Close()
		if err = rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}

	// scan_blob (跳过已迁入 usage_cost 的 usage-cost: 项)
	if cols, err := tableColumns(oldDB, "scan_blob"); err == nil && len(cols) > 0 {
		rows, err := oldDB.Query("SELECT name,value FROM scan_blob WHERE name NOT LIKE 'usage-cost:%'")
		if err != nil {
			return err
		}
		stmt, err := tx.Prepare("INSERT OR REPLACE INTO scan_blob(name,value) VALUES(?,?)")
		if err != nil {
			rows.Close()
			return err
		}
		for rows.Next() {
			var name, value string
			if err = rows.Scan(&name, &value); err != nil {
				stmt.Close()
				rows.Close()
				return err
			}
			if _, err = stmt.Exec(name, value); err != nil {
				stmt.Close()
				rows.Close()
				return err
			}
		}
		stmt.Close()
		if err = rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}
	return nil
}

// loadLegacyCostBlobs 读取旧库scan_blob中的上报费用JSON并按事件摘要索引
func loadLegacyCostBlobs(oldDB *sql.DB) (map[[16]byte]costParts, error) {
	result := map[[16]byte]costParts{}
	cols, err := tableColumns(oldDB, "scan_blob")
	if err != nil || len(cols) == 0 {
		return result, nil
	}
	rows, err := oldDB.Query("SELECT substr(name,12),value FROM scan_blob WHERE name LIKE 'usage-cost:%'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, raw string
		if err = rows.Scan(&key, &raw); err != nil {
			return nil, err
		}
		var parts costParts
		if json.Unmarshal([]byte(raw), &parts) == nil {
			result[hashEventKey(key)] = parts
		}
	}
	return result, rows.Err()
}

// streamUsageToV2 流式转换旧usage表并同步在内存构造字典与双层预聚合
func streamUsageToV2(oldDB *sql.DB, tx *sql.Tx, usageCols map[string]bool, costMap map[[16]byte]costParts) (int64, error) {
	cw1hExpr := "0"
	if usageCols["cache_write_1h"] {
		cw1hExpr = "cache_write_1h"
	}
	rows, err := oldDB.Query("SELECT event_key,source,app,conversation,model,speed,time,input,output,cache_read,cache_write,cost," + cw1hExpr + " FROM usage")
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	srcStmt, err := tx.Prepare("INSERT INTO usage_source(id,path) VALUES(?,?)")
	if err != nil {
		return 0, err
	}
	defer srcStmt.Close()
	convStmt, err := tx.Prepare("INSERT INTO usage_conv(id,name) VALUES(?,?)")
	if err != nil {
		return 0, err
	}
	defer convStmt.Close()
	modelStmt, err := tx.Prepare("INSERT INTO usage_model(id,name) VALUES(?,?)")
	if err != nil {
		return 0, err
	}
	defer modelStmt.Close()
	speedStmt, err := tx.Prepare("INSERT INTO usage_speed(id,name) VALUES(?,?)")
	if err != nil {
		return 0, err
	}
	defer speedStmt.Close()
	usageStmt, err := tx.Prepare("INSERT INTO usage(id,key_hash,source_id,app,conv_id,model_id,speed_id,time,input,output,cache_read,cache_write,cost,cache_write_1h,dsh_key) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)")
	if err != nil {
		return 0, err
	}
	defer usageStmt.Close()
	costStmt, err := tx.Prepare("INSERT INTO usage_cost(usage_id,input,output,cache_read,cache_write) VALUES(?,?,?,?,?)")
	if err != nil {
		return 0, err
	}
	defer costStmt.Close()

	sources := make(map[string]int64, 4096)
	convs := make(map[string]int64, 4096)
	models := make(map[string]int64, 256)
	speeds := make(map[string]int64, 8)
	intern := func(m map[string]int64, stmt *sql.Stmt, key string) (int64, error) {
		if id, ok := m[key]; ok {
			return id, nil
		}
		id := int64(len(m) + 1)
		if _, execErr := stmt.Exec(id, key); execErr != nil {
			return 0, execErr
		}
		m[key] = id
		return id, nil
	}

	rollups := make(map[rollupKey]*rollupAcc, 16384)
	convAccs := make(map[int64]*convAcc, 16384)

	var nextID int64
	for rows.Next() {
		var eventKey, source, conv, modelName, speed string
		var app int
		var at, input, output, cacheRead, cacheWrite, cacheWrite1h int64
		var cost sql.NullFloat64
		if err = rows.Scan(&eventKey, &source, &app, &conv, &modelName, &speed, &at, &input, &output, &cacheRead, &cacheWrite, &cost, &cacheWrite1h); err != nil {
			return 0, err
		}
		srcID, err := intern(sources, srcStmt, source)
		if err != nil {
			return 0, err
		}
		convID, err := intern(convs, convStmt, conv)
		if err != nil {
			return 0, err
		}
		modelID, err := intern(models, modelStmt, modelName)
		if err != nil {
			return 0, err
		}
		speedID, err := intern(speeds, speedStmt, speed)
		if err != nil {
			return 0, err
		}
		keyHash := hashEventKey(eventKey)
		var dshKey any
		if app == 5 {
			dshKey = eventKey
		}
		var costVal any
		costNum := 0.0
		if cost.Valid {
			costVal = cost.Float64
			costNum = cost.Float64
		}
		nextID++
		if _, err = usageStmt.Exec(nextID, keyHash[:], srcID, app, convID, modelID, speedID, at, input, output, cacheRead, cacheWrite, costVal, cacheWrite1h, dshKey); err != nil {
			return 0, err
		}
		if parts, ok := costMap[keyHash]; ok {
			if _, err = costStmt.Exec(nextID, parts.Input, parts.Output, parts.CacheRead, parts.CacheWrite); err != nil {
				return 0, err
			}
		}

		rk := rollupKey{bucket: at / rollupBucketMs, app: app, modelID: modelID, speedID: speedID}
		rAcc := rollups[rk]
		if rAcc == nil {
			rAcc = &rollupAcc{}
			rollups[rk] = rAcc
		}
		rAcc.input += input
		rAcc.output += output
		rAcc.cacheRead += cacheRead
		rAcc.cacheWrite += cacheWrite
		rAcc.cacheWrite1h += cacheWrite1h
		rAcc.requests++
		rAcc.cost += costNum

		cAcc := convAccs[convID]
		if cAcc == nil {
			cAcc = &convAcc{app: app, first: at, last: at, models: map[string]struct{}{modelName: {}}}
			convAccs[convID] = cAcc
		} else {
			if app < cAcc.app {
				cAcc.app = app
			}
			if at < cAcc.first {
				cAcc.first = at
			}
			if at > cAcc.last {
				cAcc.last = at
			}
			cAcc.models[modelName] = struct{}{}
		}
		cAcc.input += input
		cAcc.output += output
		cAcc.cacheRead += cacheRead
		cAcc.cacheWrite += cacheWrite
		cAcc.cacheWrite1h += cacheWrite1h
		cAcc.requests++
		cAcc.cost += costNum
		if speed == "fast" {
			cAcc.fast++
		}
	}
	if err = rows.Err(); err != nil {
		return 0, err
	}

	// 按主键序批量写入 usage_rollup
	rKeys := make([]rollupKey, 0, len(rollups))
	for k := range rollups {
		rKeys = append(rKeys, k)
	}
	sort.Slice(rKeys, func(i, j int) bool {
		if rKeys[i].bucket != rKeys[j].bucket {
			return rKeys[i].bucket < rKeys[j].bucket
		}
		if rKeys[i].app != rKeys[j].app {
			return rKeys[i].app < rKeys[j].app
		}
		if rKeys[i].modelID != rKeys[j].modelID {
			return rKeys[i].modelID < rKeys[j].modelID
		}
		return rKeys[i].speedID < rKeys[j].speedID
	})
	rollupStmt, err := tx.Prepare("INSERT INTO usage_rollup(bucket,app,model_id,speed_id,input,output,cache_read,cache_write,cache_write_1h,requests,cost) VALUES(?,?,?,?,?,?,?,?,?,?,?)")
	if err != nil {
		return 0, err
	}
	defer rollupStmt.Close()
	for _, k := range rKeys {
		a := rollups[k]
		if _, err = rollupStmt.Exec(k.bucket, k.app, k.modelID, k.speedID, a.input, a.output, a.cacheRead, a.cacheWrite, a.cacheWrite1h, a.requests, a.cost); err != nil {
			return 0, err
		}
	}

	// 按主键序批量写入 conv_summary
	cIDs := make([]int64, 0, len(convAccs))
	for id := range convAccs {
		cIDs = append(cIDs, id)
	}
	sort.Slice(cIDs, func(i, j int) bool { return cIDs[i] < cIDs[j] })
	summaryStmt, err := tx.Prepare("INSERT INTO conv_summary(conv_id,app,first_time,last_time,input,output,cache_read,cache_write,cache_write_1h,requests,cost,models,fast_requests) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)")
	if err != nil {
		return 0, err
	}
	defer summaryStmt.Close()
	for _, id := range cIDs {
		a := convAccs[id]
		names := make([]string, 0, len(a.models))
		for m := range a.models {
			names = append(names, m)
		}
		sort.Strings(names)
		if _, err = summaryStmt.Exec(id, a.app, a.first, a.last, a.input, a.output, a.cacheRead, a.cacheWrite, a.cacheWrite1h, a.requests, a.cost, strings.Join(names, ","), a.fast); err != nil {
			return 0, err
		}
	}
	return nextID, nil
}
