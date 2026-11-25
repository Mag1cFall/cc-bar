package usage

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Mag1cFall/cc-bar/internal/model"
)

var sessionUUID = regexp.MustCompile(`[0-9a-fA-F]{8}(?:-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}$`)

type scanFile struct {
	app    model.UsageApp
	path   string
	parent string
	uuid   string
	info   fs.FileInfo
}

type fileStamp struct {
	length   int64
	modified int64
}

// fileTicks 沿用已有CSharp扫描游标的百纳秒时间格式
func fileTicks(info fs.FileInfo) int64 {
	return info.ModTime().UnixNano()/100 + 621355968000000000
}

// loadFileStamps 一次性读取scan_file全部游标以便内存中跳过未变更文件
func (store *Store) loadFileStamps(ctx context.Context) (map[string]fileStamp, error) {
	rows, err := store.db.QueryContext(ctx, "SELECT path,length,modified FROM scan_file")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	stamps := map[string]fileStamp{}
	for rows.Next() {
		var path string
		var stamp fileStamp
		if err = rows.Scan(&path, &stamp.length, &stamp.modified); err != nil {
			return nil, err
		}
		stamps[path] = stamp
	}
	return stamps, rows.Err()
}

// Scan 增量扫描全部本地来源且仅在有实际变更时更新视图版本
func (store *Store) Scan(ctx context.Context, rebuild bool) (scanErr error) {
	store.gate.Lock()
	defer store.gate.Unlock()

	mutated := rebuild
	convsMutated := rebuild
	claudeMutated := false
	announced := false
	announceStart := func(total int) {
		if announced {
			return
		}
		announced = true
		store.statusMu.Lock()
		store.status.IsScanning = true
		store.status.Error = ""
		store.status.FilesCompleted = 0
		store.status.FilesTotal = total
		store.statusMu.Unlock()
		store.notify(false)
	}
	defer func() {
		now := time.Now()
		store.statusMu.Lock()
		store.status.IsScanning = false
		store.status.LastScanAt = &now
		if scanErr != nil {
			store.status.Error = scanErr.Error()
		} else {
			store.status.Error = ""
		}
		store.statusMu.Unlock()
		if mutated || announced || scanErr != nil {
			store.notify(mutated)
		}
		if mutated {
			_, _ = store.db.Exec("PRAGMA wal_checkpoint(PASSIVE)")
		}
	}()

	if rebuild {
		announceStart(0)
		if err := store.resetLocalData(ctx); err != nil {
			return err
		}
	}

	known, err := store.loadFileStamps(ctx)
	if err != nil {
		return err
	}

	dirty := []scanFile{}
	var problems []error
	roots := []scanFile{
		{app: model.UsageCodex, path: filepath.Join(store.home, ".codex", "sessions")},
		{app: model.UsageCodex, path: filepath.Join(store.home, ".codex", "archived_sessions")},
		{app: model.UsageClaude, path: filepath.Join(store.home, ".claude", "projects")},
	}
	for _, app := range []model.UsageApp{model.UsagePi, model.UsageOmp} {
		for _, path := range SessionRoots(store.home, app) {
			roots = append(roots, scanFile{app: app, path: path})
		}
	}
	seen := map[string]bool{}
	for _, root := range roots {
		err := filepath.WalkDir(root.path, func(path string, entry fs.DirEntry, walkErr error) error {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			if walkErr != nil {
				return walkErr
			}
			if !entry.IsDir() && strings.HasSuffix(strings.ToLower(entry.Name()), ".jsonl") {
				key := strings.ToLower(path)
				if !seen[key] {
					seen[key] = true
					info, infoErr := entry.Info()
					if infoErr != nil {
						return nil
					}
					if stamp, ok := known[path]; ok && stamp.length == info.Size() && stamp.modified == fileTicks(info) {
						return ctx.Err()
					}
					dirty = append(dirty, scanFile{app: root.app, path: path, info: info})
				}
			}
			return ctx.Err()
		})
		if err != nil {
			problems = append(problems, err)
		}
	}

	if len(dirty) > 0 {
		ompHeaders := map[string]string{}
		var ompRoots []string
		for index := range dirty {
			dirty[index].uuid = strings.ToLower(sessionUUID.FindString(strings.TrimSuffix(filepath.Base(dirty[index].path), ".jsonl")))
			if dirty[index].app == model.UsageOmp {
				if ompRoots == nil {
					ompRoots = SessionRoots(store.home, model.UsageOmp)
				}
				dirty[index].parent = ompParent(dirty[index].path, ompRoots, ompHeaders)
			}
		}
		sort.Slice(dirty, func(i, j int) bool {
			if dirty[i].app != dirty[j].app {
				return dirty[i].app < dirty[j].app
			}
			if (dirty[i].parent == "") != (dirty[j].parent == "") {
				return dirty[i].parent == ""
			}
			if dirty[i].uuid != dirty[j].uuid {
				return dirty[i].uuid < dirty[j].uuid
			}
			return dirty[i].path < dirty[j].path
		})
		announceStart(len(dirty))
		for index, file := range dirty {
			if err := ctx.Err(); err != nil {
				return err
			}
			changed, fileErr := store.scanJSONL(ctx, file)
			if fileErr != nil {
				problems = append(problems, fmt.Errorf("%s: %w", file.path, fileErr))
			}
			if changed {
				mutated = true
				convsMutated = true
				if file.app == model.UsageClaude {
					claudeMutated = true
				}
			}
			store.statusMu.Lock()
			store.status.FilesCompleted = index + 1
			store.statusMu.Unlock()
			if (index+1)%30 == 0 {
				store.notify(false)
			}
		}
	}

	if changed, err := store.scanOpenCode(ctx, rebuild); err != nil {
		problems = append(problems, err)
	} else if changed {
		mutated = true
		convsMutated = true
	}
	if changed, err := store.scanDsh(ctx, known, rebuild); err != nil {
		problems = append(problems, err)
	} else if changed {
		mutated = true
		convsMutated = true
	}
	if changed, err := store.mergeImported(ctx, known, rebuild || claudeMutated); err != nil {
		problems = append(problems, err)
	} else if changed {
		mutated = true
	}
	titlesChanged, titleErr := store.applyTitles(ctx, known, rebuild)
	if titleErr != nil {
		problems = append(problems, titleErr)
	} else if titlesChanged {
		mutated = true
	}
	if rebuild || convsMutated || titlesChanged {
		if changed, err := store.resolveProjects(ctx); err != nil {
			problems = append(problems, err)
		} else if changed {
			mutated = true
		}
	}
	return errors.Join(problems...)
}

// resetLocalData 保留远端Cursor记录并快速清空本地日志衍生数据
func (store *Store) resetLocalData(ctx context.Context) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmts := []string{
		"DROP TRIGGER IF EXISTS usage_ad",
		"DELETE FROM usage_cost WHERE usage_id IN (SELECT id FROM usage WHERE app<>2)",
		"DELETE FROM usage WHERE app<>2",
		"DELETE FROM usage_rollup WHERE app<>2",
		"DELETE FROM conv_summary WHERE app<>2",
		"DELETE FROM scan_file",
		"DELETE FROM scan_meta WHERE name NOT LIKE 'cursor:%' AND name NOT LIKE 'cursor-coverage:%'",
		"DELETE FROM conversation",
		"DELETE FROM scan_candidate",
		"DELETE FROM scan_blob",
		schemaV2IndexesAndTriggers,
	}
	for _, stmt := range stmts {
		if _, err = tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return store.loadDicts()
}

// loadState 读取增量解析所需的模型身份标题和累计签名
func (store *Store) loadState(ctx context.Context, path string) (scanState, error) {
	state := scanState{Length: -1, Model: "unknown", Speed: "standard", Titles: map[string]string{}, Candidates: map[string][]projectCandidate{}}
	state.Session = sessionUUID.FindString(strings.TrimSuffix(filepath.Base(path), ".jsonl"))
	state.Emitting = state.Session
	var modelName, speed, signature, session, emitting, title, cwd, branch sql.NullString
	err := store.db.QueryRowContext(ctx, `SELECT length,modified,offset,model,speed,signature,session,emitting,title,cwd,branch,subtasks,sidechain_all FROM scan_file WHERE path=?`, path).Scan(&state.Length, &state.Modified, &state.Offset, &modelName, &speed, &signature, &session, &emitting, &title, &cwd, &branch, &state.Subtasks, &state.AllChain)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	state.Model = coalesce(modelName.String, state.Model)
	state.Speed = coalesce(speed.String, state.Speed)
	state.Signature = signature.String
	state.Session = coalesce(session.String, state.Session)
	state.Emitting = coalesce(emitting.String, state.Emitting)
	state.Title = title.String
	state.Cwd = cwd.String
	state.Branch = branch.String
	if state.Session != "" && state.Title != "" {
		state.Titles[state.Session] = state.Title
	}
	return state, nil
}

// saveState 与用量写入共同提交当前日志解析位置
func saveState(ctx context.Context, tx *sql.Tx, path string, state scanState, info fs.FileInfo) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO scan_file(path,length,modified,offset,model,speed,signature,session,emitting,title,cwd,branch,subtasks,sidechain_all)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(path) DO UPDATE SET length=excluded.length,modified=excluded.modified,
 offset=excluded.offset,model=excluded.model,speed=excluded.speed,signature=excluded.signature,session=excluded.session,
 emitting=excluded.emitting,title=excluded.title,cwd=excluded.cwd,branch=excluded.branch,subtasks=excluded.subtasks,sidechain_all=excluded.sidechain_all`, path, info.Size(), fileTicks(info), state.Offset, state.Model, state.Speed, nullable(state.Signature), nullable(state.Session), nullable(state.Emitting), nullable(state.Title), nullable(state.Cwd), nullable(state.Branch), state.Subtasks, state.AllChain)
	return err
}

// scanJSONL 读取新增完整行并在文件重写时重新解析该来源
func (store *Store) scanJSONL(ctx context.Context, file scanFile) (bool, error) {
	info := file.info
	if info == nil {
		var err error
		info, err = os.Stat(file.path)
		if err != nil {
			return false, err
		}
	}
	state, err := store.loadState(ctx, file.path)
	if err != nil {
		return false, err
	}
	if state.Length == info.Size() && state.Modified == fileTicks(info) {
		return false, nil
	}
	if file.app == model.UsageClaude {
		state.IsSubagentPath = strings.Contains(strings.ReplaceAll(file.path, "\\", "/"), "/subagents/")
	}
	if file.app == model.UsageOmp {
		_, title, _ := harnessHeader(file.path)
		state.Title = coalesce(title, state.Title)
	}
	input, err := os.Open(file.path)
	if err != nil {
		return false, err
	}
	defer input.Close()
	batch, err := store.beginWrite(ctx)
	if err != nil {
		return false, err
	}
	committed := false
	defer func() {
		if !committed {
			batch.rollback()
		}
	}()
	if state.Offset > info.Size() || state.Length == info.Size() {
		if err = batch.deleteBySource(ctx, file.path); err != nil {
			return false, err
		}
		if _, err = batch.tx.ExecContext(ctx, "DELETE FROM scan_candidate WHERE path=?", file.path); err != nil {
			return false, err
		}
		isSub := state.IsSubagentPath
		state = scanState{Length: -1, Model: "unknown", Speed: "standard", IsSubagentPath: isSub, Titles: map[string]string{}, Candidates: map[string][]projectCandidate{}, Session: sessionUUID.FindString(strings.TrimSuffix(filepath.Base(file.path), ".jsonl"))}
		state.Emitting = state.Session
	}
	if _, err = input.Seek(state.Offset, io.SeekStart); err != nil {
		return false, err
	}
	reader := bufio.NewReaderSize(input, 131072)
	for {
		if err = ctx.Err(); err != nil {
			return false, err
		}
		line, readErr := reader.ReadBytes('\n')
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return false, readErr
		}
		if state.Offset+int64(len(line)) > info.Size() {
			break
		}
		var row *model.UsageRow
		switch file.app {
		case model.UsageCodex:
			row = store.parseCodexLine(line, file.path, state.Offset, &state)
		case model.UsageClaude:
			row = store.parseClaudeLine(line, &state)
		case model.UsagePi, model.UsageOmp:
			row = store.parseHarnessLine(line, &state, file.app)
		}
		if row != nil {
			if file.parent != "" {
				row.Conversation = "omp:" + file.parent
			}
			if err = batch.saveUsage(ctx, *row, file.path); err != nil {
				return false, err
			}
		}
		state.Offset += int64(len(line))
	}
	if err = saveState(ctx, batch.tx, file.path, state, info); err != nil {
		return false, err
	}
	metaChanged := false
	if file.app == model.UsageClaude {
		for session, candidates := range state.Candidates {
			sidechain := false
			for _, candidate := range candidates {
				sidechain = sidechain || candidate.Sidechain
				res, execErr := batch.tx.ExecContext(ctx, "INSERT OR IGNORE INTO scan_candidate(path,session,cwd,sidechain) VALUES(?,?,?,?)", file.path, session, candidate.Path, candidate.Sidechain)
				if execErr != nil {
					return false, execErr
				}
				if n, _ := res.RowsAffected(); n > 0 {
					metaChanged = true
				}
			}
			convChanged, convErr := saveConversation(ctx, batch.tx, "claude:"+session, state.Titles[session], state.Cwd, file.path, state.Branch, state.Subtasks || sidechain, true)
			if convErr != nil {
				return false, convErr
			}
			metaChanged = metaChanged || convChanged
		}
	} else if state.Session != "" {
		prefix := "codex:"
		if file.app == model.UsagePi {
			prefix = "pi:"
		}
		if file.app == model.UsageOmp {
			prefix = "omp:"
		}
		conversation, title := prefix+state.Session, state.Title
		cwd, source := state.Cwd, file.path
		if file.parent != "" {
			conversation = "omp:" + file.parent
			title = ""
			cwd, source = "", ""
			state.Subtasks = true
		}
		convChanged, convErr := saveConversation(ctx, batch.tx, conversation, title, cwd, source, state.Branch, state.Subtasks, file.app == model.UsagePi || file.app == model.UsageOmp || state.CacheSeen)
		if convErr != nil {
			return false, convErr
		}
		metaChanged = metaChanged || convChanged
	}
	if err = batch.commit(ctx); err != nil {
		return false, err
	}
	committed = true
	return batch.mutated || metaChanged, nil
}

// repriceMissing 借助部分索引仅筛查当前价格目录可定价的缺失记录
func (store *Store) repriceMissing(ctx context.Context) (bool, error) {
	rows, err := store.db.QueryContext(ctx, "SELECT DISTINCT app,speed_id,model_id FROM usage WHERE cost IS NULL AND app<>2")
	if err != nil {
		return false, err
	}
	modelNames, speedNames := store.dictSnapshot()
	type combo struct {
		app     model.UsageApp
		speedID int64
		modelID int64
		speed   string
		model   string
	}
	eligible := []combo{}
	now := time.Now()
	for rows.Next() {
		var c combo
		if err = rows.Scan(&c.app, &c.speedID, &c.modelID); err != nil {
			rows.Close()
			return false, err
		}
		c.model, c.speed = store.resolveDict(ctx, c.modelID, c.speedID, modelNames, speedNames)
		if c.speed == "unknown" {
			continue
		}
		probeShort := model.UsageRow{App: c.app, Model: c.model, Speed: c.speed, Time: now, Input: 1}
		probeLong := model.UsageRow{App: c.app, Model: c.model, Speed: c.speed, Time: now, Input: 300000}
		if store.pricing.Cost(probeShort) != nil || store.pricing.Cost(probeLong) != nil {
			eligible = append(eligible, c)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(eligible) == 0 {
		return false, err
	}

	type pendingRow struct {
		id     int64
		convID int64
		row    model.UsageRow
	}
	pending := []pendingRow{}
	for _, c := range eligible {
		qRows, qErr := store.db.QueryContext(ctx, `SELECT id,conv_id,time,input,output,cache_read,cache_write,cache_write_1h
 FROM usage WHERE cost IS NULL AND app<>2 AND app=? AND speed_id=? AND model_id=?`, c.app, c.speedID, c.modelID)
		if qErr != nil {
			return false, qErr
		}
		for qRows.Next() {
			var p pendingRow
			var ms int64
			if err = qRows.Scan(&p.id, &p.convID, &ms, &p.row.Input, &p.row.Output, &p.row.CacheRead, &p.row.CacheWrite, &p.row.CacheWrite1h); err != nil {
				qRows.Close()
				return false, err
			}
			p.row.App = c.app
			p.row.Model = c.model
			p.row.Speed = c.speed
			p.row.Time = time.UnixMilli(ms)
			if cost := store.pricing.Cost(p.row); cost != nil {
				p.row.Cost = cost
				pending = append(pending, p)
			}
		}
		err = qRows.Err()
		qRows.Close()
		if err != nil {
			return false, err
		}
	}
	if len(pending) == 0 {
		return false, nil
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, "UPDATE usage SET cost=? WHERE id=?")
	if err != nil {
		return false, err
	}
	touchedConvs := map[int64]struct{}{}
	for _, p := range pending {
		if _, err = stmt.ExecContext(ctx, *p.row.Cost, p.id); err != nil {
			stmt.Close()
			return false, err
		}
		touchedConvs[p.convID] = struct{}{}
	}
	stmt.Close()
	if err = refreshConvSummaries(ctx, tx, touchedConvs); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// modelLabel 保留OpenCode的提供商与模型组合
func modelLabel(root node) string {
	provider, name := str(root, "providerID"), str(root, "modelID")
	if provider != "" && name != "" {
		return provider + "/" + name
	}
	return ""
}

// sqliteFileSignature 组合文件大小修改时间与SQLite页头变更计数以零开销精准判重
func sqliteFileSignature(path string, info fs.FileInfo) (int64, int64) {
	sigSize := info.Size()
	sigMod := fileTicks(info)
	if f, err := os.Open(path); err == nil {
		var hdr [32]byte
		if n, _ := f.ReadAt(hdr[:], 0); n >= 28 {
			changeCounter := int64(binary.BigEndian.Uint32(hdr[24:28]))
			sigSize ^= changeCounter << 20
		}
		f.Close()
	}
	if walInfo, walErr := os.Stat(path + "-wal"); walErr == nil {
		sigSize ^= walInfo.Size() << 1
		if walTicks := fileTicks(walInfo); walTicks > sigMod {
			sigMod = walTicks
		}
	}
	return sigSize, sigMod
}

// scanOpenCode 只读扫描OpenCode数据库并先比对主库与WAL文件状态以跳过空扫描
func (store *Store) scanOpenCode(ctx context.Context, rebuild bool) (bool, error) {
	source := filepath.Join(store.home, ".local", "share", "opencode", "opencode.db")
	dbInfo, err := os.Stat(source)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	walInfo, walErr := os.Stat(source + "-wal")
	hasActiveWAL := walErr == nil && walInfo.Size() > 0
	sigSize, sigMod := sqliteFileSignature(source, dbInfo)
	if !rebuild && !hasActiveWAL {
		var savedSize, savedMod int64
		err1 := store.db.QueryRowContext(ctx, "SELECT value FROM scan_meta WHERE name='opencode-sig-size'").Scan(&savedSize)
		err2 := store.db.QueryRowContext(ctx, "SELECT value FROM scan_meta WHERE name='opencode-sig-mod'").Scan(&savedMod)
		if err1 == nil && err2 == nil && savedSize == sigSize && savedMod == sigMod {
			return false, nil
		}
	}

	read, err := sql.Open("sqlite", "file:"+filepath.ToSlash(source)+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return false, err
	}
	defer read.Close()
	read.SetMaxOpenConns(1)
	var last int64
	err = store.db.QueryRowContext(ctx, "SELECT value FROM scan_meta WHERE name='opencode'").Scan(&last)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	lastUpdated := last
	err = store.db.QueryRowContext(ctx, "SELECT value FROM scan_meta WHERE name='opencode-updated'").Scan(&lastUpdated)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	query := `SELECT m.id,m.session_id,m.time_created,m.time_updated,m.data,s.title,s.directory,w.branch
 FROM message m JOIN session s ON s.id=m.session_id LEFT JOIN workspace w ON w.id=s.workspace_id
 WHERE m.time_created>=? OR m.time_updated>=? ORDER BY m.time_created,m.id`
	rows, err := read.QueryContext(ctx, query, last, lastUpdated)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	type item struct {
		id, session, data, title, directory, branch string
		created, updated                            int64
	}
	batch, err := store.beginWrite(ctx)
	if err != nil {
		return false, err
	}
	committed := false
	defer func() {
		if !committed {
			batch.rollback()
		}
	}()
	lastModel := map[string]string{}
	metadata := map[string]item{}
	for rows.Next() {
		var entry item
		var title, directory, branch sql.NullString
		if err = rows.Scan(&entry.id, &entry.session, &entry.created, &entry.updated, &entry.data, &title, &directory, &branch); err != nil {
			return false, err
		}
		entry.title, entry.directory, entry.branch = title.String, directory.String, branch.String
		last = max(last, entry.created)
		lastUpdated = max(lastUpdated, entry.updated)
		root, parseErr := parse([]byte(entry.data))
		if parseErr != nil {
			continue
		}
		if str(root, "role") == "user" {
			if inherited := modelLabel(obj(root, "model")); inherited != "" {
				lastModel[entry.session] = inherited
			}
			continue
		}
		if str(root, "role") != "assistant" {
			continue
		}
		tokens := obj(root, "tokens")
		if tokens == nil {
			continue
		}
		cache := obj(tokens, "cache")
		row := model.UsageRow{ID: "opencode:" + entry.id, App: model.UsageOpencode, Conversation: "opencode:" + entry.session, Model: coalesce(modelLabel(root), lastModel[entry.session], "unknown/unknown"), Speed: "standard", Time: time.UnixMilli(entry.created), Input: nonnegative(tokens, "input"), Output: nonnegative(tokens, "output") + nonnegative(tokens, "reasoning"), CacheRead: nonnegative(cache, "read"), CacheWrite: nonnegative(cache, "write"), Requests: 1}
		cost := number(root, "cost")
		if row.Totals().Tokens == 0 && integer(tokens, "total") == 0 && cost <= 0 {
			continue
		}
		if cost > 0 {
			row.Cost = &cost
		} else {
			row.Cost = store.pricing.Cost(row)
		}
		if err = batch.saveUsage(ctx, row, source); err != nil {
			return false, err
		}
		if _, exists := metadata[entry.session]; !exists {
			entry.data = ""
			metadata[entry.session] = entry
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	metaChanged := false
	for _, entry := range metadata {
		if strings.TrimSpace(entry.title) == "" {
			var text string
			if read.QueryRowContext(ctx, "SELECT data FROM part WHERE session_id=? AND json_extract(data,'$.type')='text' ORDER BY time_created,id LIMIT 1", entry.session).Scan(&text) == nil {
				if part, parseErr := parse([]byte(text)); parseErr == nil {
					entry.title = cleanTitle(str(part, "text"))
				}
			}
		}
		convChanged, convErr := saveConversation(ctx, batch.tx, "opencode:"+entry.session, entry.title, entry.directory, source, entry.branch, false, true)
		if convErr != nil {
			return false, convErr
		}
		metaChanged = metaChanged || convChanged
	}
	for name, val := range map[string]int64{
		"opencode":          last,
		"opencode-updated":  lastUpdated,
		"opencode-sig-size": sigSize,
		"opencode-sig-mod":  sigMod,
	} {
		if _, err = batch.tx.ExecContext(ctx, "INSERT INTO scan_meta(name,value) VALUES(?,?) ON CONFLICT(name) DO UPDATE SET value=excluded.value", name, val); err != nil {
			return false, err
		}
	}
	if err = batch.commit(ctx); err != nil {
		return false, err
	}
	committed = true
	return batch.mutated || metaChanged, nil
}

// mergeImported 只在缺少本地Claude日志的日期补入历史统计
func (store *Store) mergeImported(ctx context.Context, known map[string]fileStamp, rebuild bool) (bool, error) {
	path := filepath.Join(store.dataDir, "imported-usage-claude-2026H1-gapfill.json")
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !rebuild {
		if stamp, ok := known[path]; ok && stamp.length == info.Size() && stamp.modified == fileTicks(info) {
			return false, nil
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	var entries []node
	decoder := jsonDecoder(data)
	if err = decoder.Decode(&entries); err != nil {
		return false, err
	}
	rows, err := store.db.QueryContext(ctx, `SELECT DISTINCT strftime('%Y-%m-%d',u.time/1000,'unixepoch','localtime')
 FROM usage u JOIN usage_source s ON s.id=u.source_id
 WHERE u.app=1 AND s.path<>'imported-backfill'`)
	if err != nil {
		return false, err
	}
	covered := map[string]bool{}
	for rows.Next() {
		var day string
		if err = rows.Scan(&day); err != nil {
			rows.Close()
			return false, err
		}
		covered[day] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	batch, err := store.beginWrite(ctx)
	if err != nil {
		return false, err
	}
	committed := false
	defer func() {
		if !committed {
			batch.rollback()
		}
	}()
	if err = batch.deleteBySource(ctx, "imported-backfill"); err != nil {
		return false, err
	}
	reference := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, entry := range entries {
		if str(entry, "app") != "claude" || !has(entry, "day") {
			continue
		}
		at := reference.Add(time.Duration(number(entry, "day") * float64(time.Second)))
		date := at.In(time.Local).Format("2006-01-02")
		if covered[date] {
			continue
		}
		name := coalesce(str(entry, "model"), "unknown")
		cost := number(entry, "costUSD")
		requests := max(integer(entry, "requestCount"), 1)
		for index := int64(0); index < requests; index++ {
			row := model.UsageRow{ID: fmt.Sprintf("imported:%s:%s:%d", name, date, index), App: model.UsageClaude, Conversation: "imported:cc-switch:" + name, Model: name, Speed: coalesce(str(entry, "speed"), "unknown"), Time: at, Requests: 1}
			if index == 0 {
				row.Input = integer(entry, "inputTokens")
				row.Output = integer(entry, "outputTokens")
				row.CacheRead = integer(entry, "cacheReadTokens")
				row.CacheWrite = integer(entry, "cacheCreationTokens")
				if has(entry, "costUSD") {
					row.Cost = &cost
				}
			} else {
				zero := 0.0
				row.Cost = &zero
			}
			if err = batch.saveUsage(ctx, row, "imported-backfill"); err != nil {
				return false, err
			}
		}
	}
	if err = saveState(ctx, batch.tx, path, scanState{Model: "imported", Speed: "standard", Offset: info.Size()}, info); err != nil {
		return false, err
	}
	if err = batch.commit(ctx); err != nil {
		return false, err
	}
	committed = true
	return batch.mutated, nil
}
