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

// applyTitles 从官方历史索引补齐会话标题和项目位置
func (store *Store) applyTitles(ctx context.Context) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE conversation SET title=NULL WHERE title LIKE '<%'"); err != nil {
		return err
	}
	historyProjects := map[string]string{}
	for _, entry := range []struct{ path, prefix, key, title, project string }{{filepath.Join(store.home, ".codex", "session_index.jsonl"), "codex:", "id", "thread_name", ""}, {filepath.Join(store.home, ".claude", "history.jsonl"), "claude:", "sessionId", "display", "project"}} {
		input, openErr := os.Open(entry.path)
		if errors.Is(openErr, os.ErrNotExist) {
			continue
		}
		if openErr != nil {
			return openErr
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
							if _, err = tx.ExecContext(ctx, "UPDATE conversation SET title=COALESCE(?,title),cwd=COALESCE(?,cwd) WHERE id=?", nullable(label), nullable(project), entry.prefix+session); err != nil {
								input.Close()
								return err
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
				return readErr
			}
		}
		input.Close()
	}
	if len(historyProjects) > 0 {
		data, _ := json.Marshal(historyProjects)
		if _, err = tx.ExecContext(ctx, "INSERT INTO scan_blob(name,value) VALUES('claude_history_projects',?) ON CONFLICT(name) DO UPDATE SET value=excluded.value", string(data)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type resolvedProject struct{ Path, Status string }

func within(directory, target string) bool {
	relative, err := filepath.Rel(directory, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

// resolveProject 归并项目目录并识别工具内部的系统会话
func (store *Store) resolveProject(raw string) *resolvedProject {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if strings.HasPrefix(raw, "~") {
		raw = store.home + raw[1:]
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
	home := strings.TrimRight(store.home, "/\\")
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
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		for current := path; within(home, current); current = filepath.Dir(current) {
			if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
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
func (store *Store) resolveClaude(historyPath string, candidates []projectCandidate) *resolvedProject {
	if history := store.resolveProject(historyPath); history != nil {
		return history
	}
	type item = struct {
		candidate projectCandidate
		project   *resolvedProject
	}
	normal := []item{}
	for _, candidate := range candidates {
		if project := store.resolveProject(candidate.Path); project != nil {
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

// resolveProjects 持久化各来源的项目归并与存在状态
func (store *Store) resolveProjects(ctx context.Context) error {
	history := map[string]string{}
	var stored string
	if store.db.QueryRowContext(ctx, "SELECT value FROM scan_blob WHERE name='claude_history_projects'").Scan(&stored) == nil {
		_ = json.Unmarshal([]byte(stored), &history)
	}
	candidates := map[string][]projectCandidate{}
	rows, err := store.db.QueryContext(ctx, "SELECT session,cwd,sidechain,path FROM scan_candidate")
	if err != nil {
		return err
	}
	claudeRoot := filepath.Join(store.home, ".claude", "projects")
	for rows.Next() {
		var session, cwd, path string
		var sidechain bool
		if err = rows.Scan(&session, &cwd, &sidechain, &path); err != nil {
			rows.Close()
			return err
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
		return err
	}
	type conversation struct{ id, cwd string }
	all := []conversation{}
	rows, err = store.db.QueryContext(ctx, "SELECT id,cwd FROM conversation")
	if err != nil {
		return err
	}
	for rows.Next() {
		var entry conversation
		var cwd sql.NullString
		if err = rows.Scan(&entry.id, &cwd); err != nil {
			rows.Close()
			return err
		}
		entry.cwd = cwd.String
		all = append(all, entry)
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
	for _, entry := range all {
		project := store.resolveProject(entry.cwd)
		if strings.HasPrefix(entry.id, "claude:") {
			project = store.resolveClaude(history[strings.TrimPrefix(entry.id, "claude:")], candidates[entry.id])
		}
		var path, status any
		if project != nil {
			path, status = project.Path, project.Status
		}
		if _, err = tx.ExecContext(ctx, "UPDATE conversation SET project=?,project_status=? WHERE id=?", path, status, entry.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
