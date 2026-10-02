package usage

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Mag1cFall/cc-bar/internal/model"
	"github.com/klauspost/compress/zstd"
)

const dshOutputLimit = 256 * 1024 * 1024

// completeFrames 返回完整的Zstandard帧并容忍末尾正在写入的帧
func completeFrames(data []byte) ([][2]int, error) {
	frames := [][2]int{}
	offset := 0
	for offset < len(data) {
		start := offset
		if len(data)-offset < 4 {
			return frames, nil
		}
		if binary.LittleEndian.Uint32(data[offset:]) != 0xfd2fb528 {
			return nil, fmt.Errorf("DSH Zstandard帧损坏")
		}
		offset += 4
		if offset == len(data) {
			return frames, nil
		}
		descriptor := data[offset]
		offset++
		if descriptor&24 != 0 {
			return nil, fmt.Errorf("DSH Zstandard帧头损坏")
		}
		contentSizeFlag := int(descriptor >> 6)
		single := descriptor&32 != 0
		dictionary := int(descriptor & 3)
		if dictionary == 3 {
			dictionary = 4
		}
		contentSize := 0
		if contentSizeFlag == 0 {
			if single {
				contentSize = 1
			}
		} else {
			contentSize = 1 << contentSizeFlag
		}
		header := dictionary + contentSize
		if !single {
			header++
		}
		if len(data)-offset < header {
			return frames, nil
		}
		offset += header
		for {
			if len(data)-offset < 3 {
				return frames, nil
			}
			block := int(data[offset]) | int(data[offset+1])<<8 | int(data[offset+2])<<16
			offset += 3
			kind := (block >> 1) & 3
			if kind == 3 {
				return nil, fmt.Errorf("DSH Zstandard区块损坏")
			}
			payload := block >> 3
			if kind == 1 {
				payload = 1
			}
			if len(data)-offset < payload {
				return frames, nil
			}
			offset += payload
			if block&1 != 0 {
				break
			}
		}
		if descriptor&4 != 0 {
			if len(data)-offset < 4 {
				return frames, nil
			}
			offset += 4
		}
		frames = append(frames, [2]int{start, offset})
	}
	return frames, nil
}

func dshLines(path string) ([][]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(path, ".zstd") {
		return bytes.Split(data, []byte{'\n'}), nil
	}
	frames, err := completeFrames(data)
	if err != nil {
		return nil, err
	}
	decoder, err := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(dshOutputLimit), zstd.WithDecoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	defer decoder.Close()
	decoded := []byte{}
	for _, frame := range frames {
		part, err := decoder.DecodeAll(data[frame[0]:frame[1]], nil)
		if err != nil {
			return nil, err
		}
		if len(part)+len(decoded) > dshOutputLimit {
			return nil, fmt.Errorf("DSH解压内容超过大小限制")
		}
		decoded = append(decoded, part...)
	}
	return bytes.Split(decoded, []byte{'\n'}), nil
}

var dshVersion = regexp.MustCompile(`^session(?:\.v([1-9][0-9]*))?\.jsonl(\.zstd)?$`)

func dshRoot(id string, parents map[string]string) string {
	visited := map[string]int{id: 0}
	path := []string{id}
	current := id
	for depth := 0; depth < 64; depth++ {
		parent := parents[current]
		if parent == "" {
			break
		}
		if _, exists := parents[parent]; !exists {
			break
		}
		if index, exists := visited[parent]; exists {
			loop := append([]string{}, path[index:]...)
			sort.Strings(loop)
			return loop[0]
		}
		visited[parent] = len(path)
		path = append(path, parent)
		current = parent
	}
	return current
}

func (store *Store) scanDsh(ctx context.Context) error {
	type selected struct {
		path       string
		version    int
		compressed bool
	}
	selectedFiles := map[string]selected{}
	root := filepath.Join(store.home, ".dsh", "sessions")
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		matched := dshVersion.FindStringSubmatch(entry.Name())
		if matched == nil {
			return nil
		}
		version, _ := strconv.Atoi(matched[1])
		candidate := selected{path, version, matched[2] != ""}
		directory := filepath.Dir(path)
		old, exists := selectedFiles[directory]
		if !exists || version > old.version || (version == old.version && candidate.compressed && !old.compressed) {
			selectedFiles[directory] = candidate
		}
		return ctx.Err()
	})
	if err != nil {
		return err
	}
	parents := map[string]string{}
	files := []string{}
	for _, entry := range selectedFiles {
		files = append(files, entry.path)
	}
	sort.Strings(files)
	for _, path := range files {
		lines, readErr := dshLines(path)
		if readErr != nil {
			return readErr
		}
		for index, line := range lines {
			if index >= 20 {
				break
			}
			if header, parseErr := parse(line); parseErr == nil && str(header, "type") == "session" {
				if id := str(header, "id"); id != "" {
					if _, exists := parents[id]; !exists {
						parents[id] = str(header, "parentSession")
					}
				}
				break
			}
		}
	}
	roots := map[string]string{}
	for id := range parents {
		roots[id] = dshRoot(id, parents)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for id, resolved := range roots {
		if _, err = tx.ExecContext(ctx, "UPDATE usage SET conversation=? WHERE app=? AND event_key LIKE ?", "dsh:"+resolved, model.UsageDsh, "dsh:"+id+":%"); err != nil {
			tx.Rollback()
			return err
		}
	}
	data, _ := json.Marshal(roots)
	if _, err = tx.ExecContext(ctx, "INSERT INTO scan_blob(name,value) VALUES('dsh_roots',?) ON CONFLICT(name) DO UPDATE SET value=excluded.value", string(data)); err != nil {
		tx.Rollback()
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	type meta struct{ title, cwd, path string }
	metadata := map[string]meta{}
	for _, path := range files {
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		state, err := store.loadState(ctx, path)
		if err != nil {
			return err
		}
		if state.Length != info.Size() || state.Modified != fileTicks(info) {
			state, err = store.scanDshFile(ctx, path, info, roots)
			if err != nil {
				return err
			}
		}
		if state.Session != "" {
			metadata[state.Session] = meta{state.Title, state.Cwd, path}
		}
	}
	rows, err := store.db.QueryContext(ctx, "SELECT conversation,COUNT(DISTINCT substr(event_key,5,instr(substr(event_key,5),':')-1)) FROM usage WHERE app=? GROUP BY conversation", model.UsageDsh)
	if err != nil {
		return err
	}
	counts := map[string]int{}
	for rows.Next() {
		var id string
		var count int
		if err = rows.Scan(&id, &count); err != nil {
			rows.Close()
			return err
		}
		counts[id] = count
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	tx, err = store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for id, count := range counts {
		rootID := strings.TrimPrefix(id, "dsh:")
		info := metadata[rootID]
		for memberID, member := range metadata {
			if coalesce(roots[memberID], memberID) == rootID {
				info.title = coalesce(info.title, member.title)
				info.cwd = coalesce(info.cwd, member.cwd)
				info.path = coalesce(info.path, member.path)
			}
		}
		if err = saveConversation(ctx, tx, id, info.title, info.cwd, info.path, "", count > 1, true); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (store *Store) scanDshFile(ctx context.Context, path string, info fs.FileInfo, roots map[string]string) (scanState, error) {
	state := scanState{Model: "unknown", Speed: "standard", Offset: info.Size()}
	lines, err := dshLines(path)
	if err != nil {
		return state, err
	}
	entries := map[string]model.UsageRow{}
	routeProvider, routeModel := "", ""
	seedCut := int64(-1)
	seeded := false
	retry, sequence := 0, 0
	currentSlot := ""
	slotOpen := false
	for _, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		root, parseErr := parse(line)
		if parseErr != nil {
			return state, parseErr
		}
		kind := str(root, "type")
		body := obj(root, "data")
		if kind == "session" {
			state.Session = str(root, "id")
			state.Cwd = str(root, "cwd")
			if has(root, "seedLength") {
				seeded = true
				seedCut = integer(root, "seedLength")
			}
			continue
		}
		if kind == "session/end-seed" && flag(body, "inherited") {
			entries = map[string]model.UsageRow{}
			seeded = false
			slotOpen = false
			continue
		}
		if seeded && integer(root, "seq") < seedCut {
			continue
		}
		switch kind {
		case "session/title":
			state.Title = coalesce(str(body, "title"), state.Title)
			continue
		case "request/context":
			routeProvider = str(body, "provider")
			routeModel = str(body, "model")
			continue
		case "request/header":
			config := obj(obj(body, "header"), "config")
			routeProvider = coalesce(str(config, "provider"), routeProvider)
			routeModel = coalesce(str(config, "model"), routeModel)
			continue
		case "llm/retry-started":
			retry++
			slotOpen = false
			continue
		}
		if (kind != "assistant/message" && kind != "assistant/attempt") || state.Session == "" {
			continue
		}
		usage := obj(body, "usage")
		if usage == nil {
			if stream, ok := body["stream"].([]any); ok {
				for index := len(stream) - 1; index >= 0; index-- {
					chunk := object(stream[index])
					usage = obj(obj(chunk, "chunk"), "usage")
					if usage == nil {
						usage = obj(chunk, "usage")
					}
					if usage != nil {
						break
					}
				}
			}
		}
		if usage == nil || integer(root, "time") <= 0 {
			continue
		}
		slot := fmt.Sprintf("%d:%d:%d", integer(body, "turn"), integer(body, "step"), retry)
		if !slotOpen || currentSlot != slot {
			sequence++
		}
		currentSlot = slot
		slotOpen = has(body, "turn") && has(body, "step")
		source := obj(obj(body, "message"), "source")
		name := coalesce(str(source, "model"), routeModel, "unknown")
		provider := coalesce(str(source, "provider"), routeProvider)
		if provider != "" {
			name = provider + "/" + name
		}
		key := fmt.Sprintf("dsh:%s:%d", state.Session, sequence)
		row := model.UsageRow{ID: key, App: model.UsageDsh, Conversation: "dsh:" + coalesce(roots[state.Session], state.Session), Model: name, Speed: "standard", Time: time.UnixMilli(integer(root, "time")), Input: nonnegative(usage, "inputTokens"), Output: nonnegative(usage, "outputTokens"), CacheRead: nonnegative(usage, "cacheReadTokens"), CacheWrite: nonnegative(usage, "cacheWriteTokens"), Requests: 1}
		row.Cost = store.pricing.Cost(row)
		entries[key] = row
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return state, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "DELETE FROM usage WHERE source=?", path); err != nil {
		return state, err
	}
	for _, row := range entries {
		if err = saveUsage(ctx, tx, row, path); err != nil {
			return state, err
		}
	}
	if err = saveState(ctx, tx, path, state, info); err != nil {
		return state, err
	}
	return state, tx.Commit()
}
