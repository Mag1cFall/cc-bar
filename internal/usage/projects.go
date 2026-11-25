package usage

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// applyTitles 按源文件变更或新会话到达增量补齐会话标题和项目位置
func (store *Store) applyTitles(ctx context.Context, known map[string]fileStamp, force, codexDirty, claudeDirty bool) (bool, error) {
	sources := []struct {
		path, prefix, key, title, project string
		dirty                             bool
	}{
		{filepath.Join(store.home, ".codex", "session_index.jsonl"), "codex:", "id", "thread_name", "", codexDirty},
		{filepath.Join(store.home, ".claude", "history.jsonl"), "claude:", "sessionId", "display", "project", claudeDirty},
	}
	type pendingSource struct {
		path, prefix, key, title, project string
		info                              os.FileInfo
	}
	toRead := []pendingSource{}
	for _, entry := range sources {
		info, err := os.Stat(entry.path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		if !force && !entry.dirty {
			if stamp, ok := known[entry.path]; ok && stamp.length == info.Size() && stamp.modified == fileTicks(info) {
				continue
			}
		}
		toRead = append(toRead, pendingSource{entry.path, entry.prefix, entry.key, entry.title, entry.project, info})
	}
	if len(toRead) == 0 && !force {
		return false, nil
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	changed := false
	if force {
		if res, execErr := tx.ExecContext(ctx, "UPDATE conversation SET title=NULL WHERE title LIKE '<%'"); execErr != nil {
			return false, execErr
		} else if n, _ := res.RowsAffected(); n > 0 {
			changed = true
		}
	}
	stmt, err := tx.PrepareContext(ctx, `UPDATE conversation
 SET title=COALESCE(?,title),cwd=COALESCE(?,cwd)
 WHERE id=? AND ((? IS NOT NULL AND title IS NOT ?) OR (? IS NOT NULL AND cwd IS NOT ?))`)
	if err != nil {
		return false, err
	}
	defer stmt.Close()

	historyProjects := map[string]string{}
	for _, entry := range toRead {
		input, openErr := os.Open(entry.path)
		if errors.Is(openErr, os.ErrNotExist) {
			continue
		}
		if openErr != nil {
			return false, openErr
		}
		reader := bufio.NewReaderSize(input, 65536)
		titled := map[string]bool{}
		for {
			line, readErr := reader.ReadBytes('\n')
			if len(line) > 0 {
				if root, parseErr := parse(line); parseErr == nil {
					session := str(root, entry.key)
					label := cleanTitle(str(root, entry.title))
					project := str(root, entry.project)
					if session != "" {
						if project != "" && entry.prefix == "claude:" {
							historyProjects[session] = project
						}
						if label != "" {
							if titled[session] {
								label = ""
							} else {
								titled[session] = true
							}
						}
						if label != "" || project != "" {
							nLabel, nProject := nullable(label), nullable(project)
							res, execErr := stmt.ExecContext(ctx, nLabel, nProject, entry.prefix+session, nLabel, nLabel, nProject, nProject)
							if execErr != nil {
								input.Close()
								return false, execErr
							}
							if n, _ := res.RowsAffected(); n > 0 {
								changed = true
							}
						}
					}
				}
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				input.Close()
				return false, readErr
			}
		}
		input.Close()
		if err = saveState(ctx, tx, entry.path, scanState{Model: "index", Speed: "standard", Offset: entry.info.Size()}, entry.info); err != nil {
			return false, err
		}
	}
	if len(historyProjects) > 0 {
		data, _ := json.Marshal(historyProjects)
		encoded := string(data)
		var existing string
		err = tx.QueryRowContext(ctx, "SELECT value FROM scan_blob WHERE name='claude_history_projects'").Scan(&existing)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && existing != encoded) {
			if _, err = tx.ExecContext(ctx, "INSERT INTO scan_blob(name,value) VALUES('claude_history_projects',?) ON CONFLICT(name) DO UPDATE SET value=excluded.value", encoded); err != nil {
				return false, err
			}
			changed = true
		} else if err != nil {
			return false, err
		}
	}
	return changed, tx.Commit()
}

type resolvedProject struct{ Path, Status string }

func within(directory, target string) bool {
	relative, err := filepath.Rel(directory, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

type projectResolver struct {
	home     string
	cache    map[string]*resolvedProject
	gitCache map[string]bool
	dirCache map[string]bool
}

func newProjectResolver(home string) *projectResolver {
	return &projectResolver{
		home:     home,
		cache:    map[string]*resolvedProject{},
		gitCache: map[string]bool{},
		dirCache: map[string]bool{},
	}
}

func (resolver *projectResolver) isDir(path string) bool {
	if ok, cached := resolver.dirCache[path]; cached {
		return ok
	}
	info, err := os.Stat(path)
	ok := err == nil && info.IsDir()
	resolver.dirCache[path] = ok
	return ok
}

func (resolver *projectResolver) hasGit(dir string) bool {
	if ok, cached := resolver.gitCache[dir]; cached {
		return ok
	}
	_, err := os.Stat(filepath.Join(dir, ".git"))
	ok := err == nil
	resolver.gitCache[dir] = ok
	return ok
}

// resolve 归并项目目录并缓存文件系统探测以消除重复os.Stat
func (resolver *projectResolver) resolve(raw string) *resolvedProject {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if cached, ok := resolver.cache[raw]; ok {
		return cached
	}
	result := resolver.resolveUncached(raw)
	resolver.cache[raw] = result
	return result
}

func (resolver *projectResolver) resolveUncached(raw string) *resolvedProject {
	if strings.HasPrefix(raw, "~") {
		raw = resolver.home + raw[1:]
	}
	path, err := filepath.Abs(raw)
	if err != nil {
		return nil
	}
	path = strings.TrimRight(path, "/\\")
	lower := strings.ToLower(strings.ReplaceAll(path, "\\", "/"))
	if strings.Contains(lower, "/ccbar-codex-wakeup/") || strings.Contains(lower, "/application support/ccbar/claudeprobe") || strings.Contains(lower, "/application support/codexbar/claudeprobe") || strings.Contains(lower, "/application support/claudebar/probe") {
		return &resolvedProject{path, "system"}
	}
	home := strings.TrimRight(resolver.home, "/\\")
	if strings.EqualFold(path, home) || strings.EqualFold(path, strings.TrimRight(filepath.VolumeName(path)+string(filepath.Separator), "/\\")) {
		return nil
	}
	canCheck := false
	if relative, err := filepath.Rel(home, path); err == nil && within(home, path) {
		top := strings.ToLower(strings.Split(relative, string(filepath.Separator))[0])
		canCheck = true
		for _, protected := range []string{"desktop", "documents", "downloads", "pictures", "music", "movies", "library"} {
			if top == protected {
				canCheck = false
				break
			}
		}
	}
	if !canCheck {
		return &resolvedProject{path, "unverified"}
	}
	if resolver.isDir(path) {
		for current := path; within(home, current); current = filepath.Dir(current) {
			if resolver.hasGit(current) {
				return &resolvedProject{current, "available"}
			}
			if parent := filepath.Dir(current); parent == current {
				break
			}
		}
		return &resolvedProject{path, "available"}
	}
	return &resolvedProject{path, "unavailable"}
}

func sameProject(candidates []struct {
	candidate projectCandidate
	project   *resolvedProject
}) *resolvedProject {
	if len(candidates) == 0 {
		return nil
	}
	first := candidates[0].project
	for _, candidate := range candidates[1:] {
		if candidate.project.Path != first.Path {
			return nil
		}
	}
	return first
}

// resolveClaude 优先使用历史项目再比较主任务和子任务路径
func (resolver *projectResolver) resolveClaude(historyPath string, candidates []projectCandidate) *resolvedProject {
	if history := resolver.resolve(historyPath); history != nil {
		return history
	}
	type item = struct {
		candidate projectCandidate
		project   *resolvedProject
	}
	normal := []item{}
	for _, candidate := range candidates {
		if project := resolver.resolve(candidate.Path); project != nil {
			normal = append(normal, item{candidate, project})
		}
	}
	if project := sameProject(normal); project != nil {
		return project
	}
	main := []item{}
	containers := []item{}
	for _, entry := range normal {
		if !entry.candidate.Sidechain {
			main = append(main, entry)
		}
		if strings.ReplaceAll(strings.ReplaceAll(entry.candidate.Path, "\\", "/"), "/", "-") == entry.candidate.Container {
			containers = append(containers, entry)
		}
	}
	if project := sameProject(main); project != nil {
		return project
	}
	if project := sameProject(containers); project != nil {
		return project
	}
	if len(normal) == 0 {
		return nil
	}
	sort.Slice(normal, func(i, j int) bool { return len(normal[i].project.Path) < len(normal[j].project.Path) })
	root := normal[0].project
	allNested := true
	for _, entry := range normal {
		allNested = allNested && within(root.Path, entry.project.Path)
	}
	if allNested {
		return root
	}
	for _, entry := range normal {
		if entry.project.Status == "system" {
			return entry.project
		}
	}
	return nil
}

// resolveProjects 缓存目录探测并仅对发生变化的对话行执行更新
func (store *Store) resolveProjects(ctx context.Context) (bool, error) {
	history := map[string]string{}
	var stored string
	if store.db.QueryRowContext(ctx, "SELECT value FROM scan_blob WHERE name='claude_history_projects'").Scan(&stored) == nil {
		_ = json.Unmarshal([]byte(stored), &history)
	}
	candidates := map[string][]projectCandidate{}
	rows, err := store.db.QueryContext(ctx, "SELECT session,cwd,sidechain,path FROM scan_candidate")
	if err != nil {
		return false, err
	}
	claudeRoot := filepath.Join(store.home, ".claude", "projects")
	for rows.Next() {
		var session, cwd, path string
		var sidechain bool
		if err = rows.Scan(&session, &cwd, &sidechain, &path); err != nil {
			rows.Close()
			return false, err
		}
		relative, relativeErr := filepath.Rel(claudeRoot, path)
		if relativeErr != nil || !within(claudeRoot, path) {
			continue
		}
		container := strings.Split(relative, string(filepath.Separator))[0]
		key := "claude:" + session
		candidates[key] = append(candidates[key], projectCandidate{cwd, container, sidechain})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}

	type convUpdate struct {
		id     string
		path   any
		status any
	}
	resolver := newProjectResolver(store.home)
	updates := []convUpdate{}
	rows, err = store.db.QueryContext(ctx, "SELECT id,cwd,project,project_status FROM conversation")
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var id string
		var cwd, curProj, curStatus sql.NullString
		if err = rows.Scan(&id, &cwd, &curProj, &curStatus); err != nil {
			rows.Close()
			return false, err
		}
		project := resolver.resolve(cwd.String)
		if strings.HasPrefix(id, "claude:") {
			project = resolver.resolveClaude(history[strings.TrimPrefix(id, "claude:")], candidates[id])
		}
		wantPath, wantStatus := "", ""
		var pathArg, statusArg any
		if project != nil {
			wantPath, wantStatus = project.Path, project.Status
			pathArg, statusArg = project.Path, project.Status
		}
		if curProj.Valid == (project != nil) && curProj.String == wantPath && curStatus.Valid == (project != nil) && curStatus.String == wantStatus {
			continue
		}
		updates = append(updates, convUpdate{id: id, path: pathArg, status: statusArg})
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(updates) == 0 {
		return false, err
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, "UPDATE conversation SET project=?,project_status=? WHERE id=?")
	if err != nil {
		return false, err
	}
	for _, u := range updates {
		if _, err = stmt.ExecContext(ctx, u.path, u.status, u.id); err != nil {
			stmt.Close()
			return false, err
		}
	}
	stmt.Close()
	return true, tx.Commit()
}
