package usage

import (
	"bufio"
	"context"
	"database/sql"
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
}

// fileTicks 沿用已有CSharp扫描游标的百纳秒时间格式
func fileTicks(info fs.FileInfo) int64 {
	return info.ModTime().UnixNano()/100 + 621355968000000000
}

// Scan 增量扫描全部本地来源且保留旧数据库的远端数据
func (store *Store) Scan(ctx context.Context, rebuild bool) (scanErr error) {
	store.gate.Lock()
	defer store.gate.Unlock()
	store.statusMu.Lock()
	store.status.IsScanning = true
	store.status.Error = ""
	store.status.FilesCompleted = 0
	store.statusMu.Unlock()
	store.changed()
	defer func() {
		now := time.Now()
		store.statusMu.Lock()
		store.status.IsScanning = false
		store.status.LastScanAt = &now
		if scanErr != nil {
			store.status.Error = scanErr.Error()
		}
		store.statusMu.Unlock()
		store.changed()
	}()
	if rebuild {
		if _, err := store.db.ExecContext(ctx, `DELETE FROM usage WHERE app<>2;DELETE FROM scan_file;DELETE FROM scan_meta WHERE name NOT LIKE 'cursor:%' AND name NOT LIKE 'cursor-coverage:%';DELETE FROM conversation;DELETE FROM scan_candidate;DELETE FROM scan_blob;`); err != nil {
			return err
		}
	}
	files := []scanFile{}
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
		err := filepath.WalkDir(root.path, func(path string, entry fs.DirEntry, err error) error {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			if !entry.IsDir() && strings.HasSuffix(strings.ToLower(entry.Name()), ".jsonl") {
				key := strings.ToLower(path)
				if !seen[key] {
					seen[key] = true
					files = append(files, scanFile{app: root.app, path: path})
				}
			}
			return ctx.Err()
		})
		if err != nil {
			problems = append(problems, err)
		}
	}
	ompHeaders := map[string]string{}
	ompRoots := SessionRoots(store.home, model.UsageOmp)
	for index := range files {
		if files[index].app == model.UsageOmp {
			files[index].parent = ompParent(files[index].path, ompRoots, ompHeaders)
		}
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].app != files[j].app {
			return files[i].app < files[j].app
		}
		if (files[i].parent == "") != (files[j].parent == "") {
			return files[i].parent == ""
		}
		left := sessionUUID.FindString(strings.TrimSuffix(filepath.Base(files[i].path), ".jsonl"))
		right := sessionUUID.FindString(strings.TrimSuffix(filepath.Base(files[j].path), ".jsonl"))
		if left != right {
			return strings.ToLower(left) < strings.ToLower(right)
		}
		return files[i].path < files[j].path
	})
	store.statusMu.Lock()
	store.status.FilesTotal = len(files)
	store.statusMu.Unlock()
	store.changed()
	for index, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := store.scanJSONL(ctx, file); err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", file.path, err))
		}
		store.statusMu.Lock()
		store.status.FilesCompleted = index + 1
		store.statusMu.Unlock()
		if (index+1)%30 == 0 {
			store.changed()
		}
	}
	for _, scan := range []func(context.Context) error{store.scanOpenCode, store.scanDsh, store.mergeImported, store.applyTitles, store.resolveProjects, store.repriceMissing} {
		if err := scan(ctx); err != nil {
			problems = append(problems, err)
		}
	}
	return errors.Join(problems...)
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
func (store *Store) scanJSONL(ctx context.Context, file scanFile) error {
	info, err := os.Stat(file.path)
	if err != nil {
		return err
	}
	state, err := store.loadState(ctx, file.path)
	if err != nil {
		return err
	}
	if state.Length == info.Size() && state.Modified == fileTicks(info) {
		return nil
	}
	if file.app == model.UsageOmp {
		_, title, _ := harnessHeader(file.path)
		state.Title = coalesce(title, state.Title)
	}
	input, err := os.Open(file.path)
	if err != nil {
		return err
	}
	defer input.Close()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if state.Offset > info.Size() || state.Length == info.Size() {
		if _, err = tx.ExecContext(ctx, `DELETE FROM scan_blob WHERE name IN
 (SELECT 'usage-cost:'||event_key FROM usage WHERE source=?)`, file.path); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM usage WHERE source=?", file.path); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM scan_candidate WHERE path=?", file.path); err != nil {
			return err
		}
		state = scanState{Length: -1, Model: "unknown", Speed: "standard", Titles: map[string]string{}, Candidates: map[string][]projectCandidate{}, Session: sessionUUID.FindString(strings.TrimSuffix(filepath.Base(file.path), ".jsonl"))}
		state.Emitting = state.Session
	}
	if _, err = input.Seek(state.Offset, io.SeekStart); err != nil {
		return err
	}
	reader := bufio.NewReaderSize(input, 65536)
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		line, readErr := reader.ReadBytes('\n')
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
		if state.Offset+int64(len(line)) > info.Size() {
			break
		}
		if root, parseErr := parse(line); parseErr == nil {
			var row *model.UsageRow
			switch file.app {
			case model.UsageCodex:
				row = store.parseCodex(root, file.path, state.Offset, &state)
			case model.UsageClaude:
				row = store.parseClaude(root, file.path, &state)
			case model.UsagePi, model.UsageOmp:
				row = store.parseHarness(root, &state, file.app)
			}
			if row != nil {
				if file.parent != "" {
					row.Conversation = "omp:" + file.parent
				}
				if err = saveUsage(ctx, tx, *row, file.path); err != nil {
					return err
				}
			}
		}
		state.Offset += int64(len(line))
	}
	if err = saveState(ctx, tx, file.path, state, info); err != nil {
		return err
	}
	if file.app == model.UsageClaude {
		for session, candidates := range state.Candidates {
			sidechain := false
			for _, candidate := range candidates {
				sidechain = sidechain || candidate.Sidechain
				if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO scan_candidate(path,session,cwd,sidechain) VALUES(?,?,?,?)", file.path, session, candidate.Path, candidate.Sidechain); err != nil {
					return err
				}
			}
			if err = saveConversation(ctx, tx, "claude:"+session, state.Titles[session], state.Cwd, file.path, state.Branch, state.Subtasks || sidechain, true); err != nil {
				return err
			}
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
		if err = saveConversation(ctx, tx, conversation, title, cwd, source, state.Branch, state.Subtasks, file.app == model.UsagePi || file.app == model.UsageOmp || state.CacheSeen); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// repriceMissing 使用更新后的目录补齐已知档位的缺失费用
func (store *Store) repriceMissing(ctx context.Context) error {
	rows, err := store.db.QueryContext(ctx, "SELECT "+eventColumns+" FROM usage WHERE cost IS NULL AND app<>2")
	if err != nil {
		return err
	}
	pending, err := readRows(rows)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, row := range pending {
		if cost := store.pricing.Cost(row); cost != nil {
			if _, err = tx.ExecContext(ctx, "UPDATE usage SET cost=? WHERE event_key=?", *cost, row.ID); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// modelLabel 保留OpenCode的提供商与模型组合
func modelLabel(root node) string {
	provider, name := str(root, "providerID"), str(root, "modelID")
	if provider != "" && name != "" {
		return provider + "/" + name
	}
	return ""
}

// scanOpenCode 只读扫描OpenCode数据库并保存推理与缓存用量
func (store *Store) scanOpenCode(ctx context.Context) error {
	source := filepath.Join(store.home, ".local", "share", "opencode", "opencode.db")
	if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	read, err := sql.Open("sqlite", "file:"+filepath.ToSlash(source)+"?mode=ro")
	if err != nil {
		return err
	}
	defer read.Close()
	read.SetMaxOpenConns(1)
	var last int64
	err = store.db.QueryRowContext(ctx, "SELECT value FROM scan_meta WHERE name='opencode'").Scan(&last)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	lastUpdated := last
	err = store.db.QueryRowContext(ctx, "SELECT value FROM scan_meta WHERE name='opencode-updated'").Scan(&lastUpdated)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	query := `SELECT m.id,m.session_id,m.time_created,m.time_updated,m.data,s.title,s.directory,w.branch
 FROM message m JOIN session s ON s.id=m.session_id LEFT JOIN workspace w ON w.id=s.workspace_id
 WHERE m.time_created>=? OR m.time_updated>=? ORDER BY m.time_created,m.id`
	rows, err := read.QueryContext(ctx, query, last, lastUpdated)
	if err != nil {
		return err
	}
	defer rows.Close()
	type item struct {
		id, session, data, title, directory, branch string
		created, updated                            int64
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	lastModel := map[string]string{}
	metadata := map[string]item{}
	for rows.Next() {
		var entry item
		var title, directory, branch sql.NullString
		if err = rows.Scan(&entry.id, &entry.session, &entry.created, &entry.updated, &entry.data, &title, &directory, &branch); err != nil {
			return err
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
		if err = saveUsage(ctx, tx, row, source); err != nil {
			return err
		}
		if _, exists := metadata[entry.session]; !exists {
			entry.data = ""
			metadata[entry.session] = entry
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, entry := range metadata {
		if strings.TrimSpace(entry.title) == "" {
			var text string
			if read.QueryRowContext(ctx, "SELECT data FROM part WHERE session_id=? AND json_extract(data,'$.type')='text' ORDER BY time_created,id LIMIT 1", entry.session).Scan(&text) == nil {
				if part, parseErr := parse([]byte(text)); parseErr == nil {
					entry.title = cleanTitle(str(part, "text"))
				}
			}
		}
		if err = saveConversation(ctx, tx, "opencode:"+entry.session, entry.title, entry.directory, source, entry.branch, false, true); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO scan_meta(name,value) VALUES('opencode',?) ON CONFLICT(name) DO UPDATE SET value=excluded.value", last); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO scan_meta(name,value) VALUES('opencode-updated',?) ON CONFLICT(name) DO UPDATE SET value=excluded.value", lastUpdated); err != nil {
		return err
	}
	return tx.Commit()
}

// mergeImported 只在缺少本地Claude日志的日期补入历史统计
func (store *Store) mergeImported(ctx context.Context) error {
	path := filepath.Join(store.dataDir, "imported-usage-claude-2026H1-gapfill.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var entries []node
	decoder := jsonDecoder(data)
	if err = decoder.Decode(&entries); err != nil {
		return err
	}
	rows, err := store.db.QueryContext(ctx, "SELECT DISTINCT strftime('%Y-%m-%d',time/1000,'unixepoch','localtime') FROM usage WHERE app=1 AND source<>'imported-backfill'")
	if err != nil {
		return err
	}
	covered := map[string]bool{}
	for rows.Next() {
		var day string
		if err = rows.Scan(&day); err != nil {
			rows.Close()
			return err
		}
		covered[day] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "DELETE FROM usage WHERE source='imported-backfill'"); err != nil {
		return err
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
			if err = saveUsage(ctx, tx, row, "imported-backfill"); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
