package usage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Mag1cFall/cc-bar/internal/model"
)

// AntigravityRoots 返回桌面端与 CLI 版的 Antigravity 对话目录列表
func AntigravityRoots(home string) []string {
	if home == "" {
		return nil
	}
	return []string{
		filepath.Join(home, ".gemini", "antigravity", "conversations"),
		filepath.Join(home, ".gemini", "antigravity-cli", "conversations"),
	}
}

type agSummary struct {
	Title     string
	Preview   string
	Workspace string
	Parent    string
}

type agGenMeta struct {
	StepIndices       []int64
	ModelEnum         uint64
	ResponseModel     string
	ResponseModelFull string
	DisplayName       string
	PlannerModel      string
	LabelModelEnum    string
	Input             int64
	Output            int64
	CacheRead         int64
	CacheWrite        int64
	HasUsage          bool
}

type agTrajectoryMeta struct {
	CreatedAt time.Time
	ParentID  string
	RootID    string
	Workspace string
	Branch    string
}

// pbVarint 读取标准 protobuf varint 编码的无符号整数
func pbVarint(data []byte) (uint64, int, bool) {
	var value uint64
	for i := 0; i < len(data) && i < 10; i++ {
		b := data[i]
		value |= uint64(b&0x7f) << (7 * i)
		if b < 0x80 {
			return value, i + 1, true
		}
	}
	return 0, 0, false
}

// pbNextField 按 protobuf wire 格式读取下一个字段并返回剩余切片
func pbNextField(data []byte) (uint32, byte, uint64, []byte, []byte, bool) {
	tag, n, ok := pbVarint(data)
	if !ok || tag < 8 {
		return 0, 0, 0, nil, nil, false
	}
	fieldNum := uint32(tag >> 3)
	wireType := byte(tag & 7)
	rest := data[n:]
	switch wireType {
	case 0:
		v, m, ok := pbVarint(rest)
		if !ok {
			return 0, 0, 0, nil, nil, false
		}
		return fieldNum, wireType, v, nil, rest[m:], true
	case 1:
		if len(rest) < 8 {
			return 0, 0, 0, nil, nil, false
		}
		return fieldNum, wireType, 0, rest[:8], rest[8:], true
	case 2:
		size, m, ok := pbVarint(rest)
		if !ok {
			return 0, 0, 0, nil, nil, false
		}
		rest = rest[m:]
		if size > uint64(len(rest)) {
			return 0, 0, 0, nil, nil, false
		}
		length := int(size)
		return fieldNum, wireType, 0, rest[:length], rest[length:], true
	case 5:
		if len(rest) < 4 {
			return 0, 0, 0, nil, nil, false
		}
		return fieldNum, wireType, 0, rest[:4], rest[4:], true
	default:
		return 0, 0, 0, nil, nil, false
	}
}

// parseAgTimestamp 解析 google.protobuf.Timestamp 消息
func parseAgTimestamp(data []byte) time.Time {
	var seconds, nanos int64
	for len(data) > 0 {
		fn, wt, val, _, rest, ok := pbNextField(data)
		if !ok {
			break
		}
		data = rest
		if wt != 0 {
			continue
		}
		if fn == 1 {
			seconds = int64(val)
		} else if fn == 2 {
			nanos = int64(val)
		}
	}
	if seconds <= 0 {
		return time.Time{}
	}
	return time.Unix(seconds, nanos)
}

// parseAgStepTime 从 CortexStepMetadata 提取完成时间并回退到创建时间
func parseAgStepTime(data []byte) time.Time {
	var created, finished, completed time.Time
	for len(data) > 0 {
		fn, wt, _, raw, rest, ok := pbNextField(data)
		if !ok {
			break
		}
		data = rest
		if wt != 2 {
			continue
		}
		switch fn {
		case 1:
			created = parseAgTimestamp(raw)
		case 7:
			finished = parseAgTimestamp(raw)
		case 8:
			completed = parseAgTimestamp(raw)
		}
	}
	if !completed.IsZero() {
		return completed
	}
	if !finished.IsZero() {
		return finished
	}
	return created
}

// parseAgTrajectoryMeta 从 CortexTrajectoryMetadata 提取时间、父子关系与工作区
func parseAgTrajectoryMeta(data []byte) agTrajectoryMeta {
	var meta agTrajectoryMeta
	for len(data) > 0 {
		fn, wt, _, raw, rest, ok := pbNextField(data)
		if !ok {
			break
		}
		data = rest
		if wt != 2 {
			continue
		}
		switch fn {
		case 1:
			uri, gitRoot, branch := parseAgWorkspaceMeta(raw)
			if meta.Workspace == "" {
				meta.Workspace = coalesce(fileURIToPath(uri), fileURIToPath(gitRoot))
			}
			if meta.Branch == "" {
				meta.Branch = strings.TrimSpace(branch)
			}
		case 2:
			meta.CreatedAt = parseAgTimestamp(raw)
		case 5:
			meta.ParentID = strings.TrimSpace(string(raw))
		case 6:
			meta.RootID = strings.TrimSpace(string(raw))
		case 7:
			if meta.Workspace == "" {
				meta.Workspace = fileURIToPath(string(raw))
			}
		}
	}
	return meta
}

func parseAgWorkspaceMeta(data []byte) (uri, gitRoot, branch string) {
	for len(data) > 0 {
		fn, wt, _, raw, rest, ok := pbNextField(data)
		if !ok {
			break
		}
		data = rest
		if wt != 2 {
			continue
		}
		switch fn {
		case 1:
			uri = string(raw)
		case 2:
			gitRoot = string(raw)
		case 4:
			branch = string(raw)
		}
	}
	return uri, gitRoot, branch
}

// parseAgGenMetadata 从 CortexStepGeneratorMetadata 提取步骤索引、模型与令牌统计
func parseAgGenMetadata(data []byte) agGenMeta {
	var meta agGenMeta
	for len(data) > 0 {
		fn, wt, val, raw, rest, ok := pbNextField(data)
		if !ok {
			break
		}
		data = rest
		switch fn {
		case 1:
			if wt == 2 {
				parseAgChatModel(raw, &meta)
			}
		case 2:
			if wt == 2 {
				for len(raw) > 0 {
					v, m, ok := pbVarint(raw)
					if !ok {
						break
					}
					meta.StepIndices = append(meta.StepIndices, int64(v))
					raw = raw[m:]
				}
			} else if wt == 0 {
				meta.StepIndices = append(meta.StepIndices, int64(val))
			}
		case 3:
			if wt == 2 {
				for len(raw) > 0 {
					sfn, swt, _, sraw, srest, ok := pbNextField(raw)
					if !ok {
						break
					}
					raw = srest
					if sfn == 28 && swt == 2 {
						meta.PlannerModel = strings.TrimSpace(string(sraw))
					}
				}
			}
		}
	}
	return meta
}

func parseAgChatModel(data []byte, meta *agGenMeta) {
	for len(data) > 0 {
		fn, wt, val, raw, rest, ok := pbNextField(data)
		if !ok {
			break
		}
		data = rest
		switch fn {
		case 3:
			if wt == 0 && meta.ModelEnum == 0 {
				meta.ModelEnum = val
			}
		case 4:
			if wt == 2 {
				parseAgModelUsage(raw, meta)
			}
		case 19:
			if wt == 2 {
				meta.ResponseModel = strings.TrimSpace(string(raw))
			}
		case 20:
			if wt == 2 {
				k, v := parseAgMapEntry(raw)
				if k == "model_enum" && v != "" {
					meta.LabelModelEnum = v
				}
			}
		case 21:
			if wt == 2 {
				meta.DisplayName = strings.TrimSpace(string(raw))
			}
		case 22:
			if wt == 2 {
				meta.ResponseModelFull = strings.TrimSpace(string(raw))
			}
		}
	}
}

func parseAgModelUsage(data []byte, meta *agGenMeta) {
	var thinking, responseOut int64
	meta.HasUsage = true
	for len(data) > 0 {
		fn, wt, val, _, rest, ok := pbNextField(data)
		if !ok {
			break
		}
		data = rest
		if wt != 0 {
			continue
		}
		switch fn {
		case 1:
			meta.ModelEnum = val
		case 2:
			meta.Input = max(int64(val), 0)
		case 3:
			meta.Output = max(int64(val), 0)
		case 4:
			meta.CacheWrite = max(int64(val), 0)
		case 5:
			meta.CacheRead = max(int64(val), 0)
		case 9:
			thinking = max(int64(val), 0)
		case 10:
			responseOut = max(int64(val), 0)
		}
	}
	if meta.Output == 0 && (thinking > 0 || responseOut > 0) {
		meta.Output = thinking + responseOut
	}
}

func parseAgMapEntry(data []byte) (key, value string) {
	for len(data) > 0 {
		fn, wt, _, raw, rest, ok := pbNextField(data)
		if !ok {
			break
		}
		data = rest
		if wt != 2 {
			continue
		}
		if fn == 1 {
			key = string(raw)
		} else if fn == 2 {
			value = string(raw)
		}
	}
	return key, value
}

// normalizeAgDisplayName 将展示名称转为小写连字符模型标识
func normalizeAgDisplayName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if idx := strings.IndexByte(name, '('); idx >= 0 {
		name = strings.TrimSpace(name[:idx])
	}
	fields := strings.Fields(strings.ToLower(name))
	return strings.Join(fields, "-")
}

// antigravityModelName 按响应模型、展示名称、规划器配置与枚举回退确定模型名
func antigravityModelName(meta agGenMeta) string {
	if meta.ResponseModel != "" {
		return meta.ResponseModel
	}
	if meta.ResponseModelFull != "" {
		return meta.ResponseModelFull
	}
	if normalized := normalizeAgDisplayName(meta.DisplayName); normalized != "" {
		return normalized
	}
	if meta.PlannerModel != "" {
		return meta.PlannerModel
	}
	switch meta.ModelEnum {
	case 246:
		return "gemini-2.5-pro"
	case 281, 282:
		return "claude-sonnet-4"
	case 290, 291:
		return "claude-opus-4"
	case 312, 313:
		return "gemini-2.5-flash"
	case 330:
		return "gemini-2.5-flash-lite"
	case 333, 334:
		return "claude-sonnet-4-5"
	case 340, 341:
		return "claude-haiku-4-5"
	case 342:
		return "gpt-oss-120b-medium"
	}
	if meta.LabelModelEnum != "" {
		return strings.ToLower(meta.LabelModelEnum)
	}
	return "unknown"
}

// fileURIToPath 将 file URI 转换为规范本地目录路径
func fileURIToPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.HasPrefix(strings.ToLower(raw), "file://") {
		return raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	path := parsed.Path
	if parsed.Host != "" && !strings.EqualFold(parsed.Host, "localhost") {
		return filepath.FromSlash("//" + parsed.Host + path)
	}
	if len(path) >= 3 && path[0] == '/' && ((path[1] >= 'a' && path[1] <= 'z') || (path[1] >= 'A' && path[1] <= 'Z')) && path[2] == ':' {
		path = strings.ToUpper(path[1:2]) + path[2:]
	}
	return filepath.FromSlash(path)
}

func parseSummaryWorkspace(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return ""
	}
	var uris []string
	if json.Unmarshal([]byte(raw), &uris) == nil {
		for _, uri := range uris {
			if resolved := fileURIToPath(uri); resolved != "" {
				return resolved
			}
		}
	}
	return ""
}

// isSQLiteBusy 判断只读查询是否因目标客户端正持写锁而暂时不可读
func isSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "database is locked") || strings.Contains(msg, "sqlite_busy") || strings.Contains(msg, "sqlite_locked")
}

// loadAgSummaries 只读汇总桌面端与 CLI 的 conversation_summaries.db
func loadAgSummaries(ctx context.Context, home string) map[string]agSummary {
	summaries := map[string]agSummary{}
	for _, dir := range []string{"antigravity", "antigravity-cli"} {
		path := filepath.Join(home, ".gemini", dir, "conversation_summaries.db")
		if _, err := os.Stat(path); err != nil {
			continue
		}
		read, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro&_pragma=busy_timeout(2000)")
		if err != nil {
			continue
		}
		read.SetMaxOpenConns(1)
		rows, err := read.QueryContext(ctx, "SELECT conversation_id,title,preview,workspace_uris,parent_conversation_id FROM conversation_summaries")
		if err != nil {
			read.Close()
			continue
		}
		for rows.Next() {
			var cid, title, preview, ws, parent sql.NullString
			if rows.Scan(&cid, &title, &preview, &ws, &parent) != nil || cid.String == "" {
				continue
			}
			cur := summaries[cid.String]
			if cur.Title == "" {
				cur.Title = strings.TrimSpace(title.String)
			}
			if cur.Preview == "" {
				cur.Preview = strings.TrimSpace(preview.String)
			}
			if cur.Workspace == "" {
				cur.Workspace = parseSummaryWorkspace(ws.String)
			}
			if cur.Parent == "" {
				cur.Parent = strings.TrimSpace(parent.String)
			}
			summaries[cid.String] = cur
		}
		rows.Close()
		read.Close()
	}
	return summaries
}

func agRootConversation(cid string, meta agTrajectoryMeta, summaries map[string]agSummary) string {
	if meta.RootID != "" && meta.RootID != cid {
		return meta.RootID
	}
	cur := cid
	parent := coalesce(meta.ParentID, summaries[cid].Parent)
	seen := map[string]bool{cid: true}
	for depth := 0; depth < 16 && parent != "" && !seen[parent]; depth++ {
		seen[parent] = true
		cur = parent
		parent = summaries[cur].Parent
	}
	return cur
}

// loadAgOffsets 批量读取已记录的 Antigravity 对话数据库扫描游标
func (store *Store) loadAgOffsets(ctx context.Context) (map[string]int64, error) {
	rows, err := store.db.QueryContext(ctx, "SELECT path,offset FROM scan_file WHERE model='antigravity'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	offsets := map[string]int64{}
	for rows.Next() {
		var path string
		var offset int64
		if err = rows.Scan(&path, &offset); err != nil {
			return nil, err
		}
		offsets[path] = offset
	}
	return offsets, rows.Err()
}

// agFileStamp 组合主库与活跃 WAL 的大小和时间戳作为增量判重签名
func agFileStamp(info, walInfo fs.FileInfo) fileStamp {
	stamp := fileStamp{length: info.Size(), modified: fileTicks(info)}
	if walInfo != nil && walInfo.Size() > 0 {
		stamp.length ^= walInfo.Size()<<1 | 1
		if walTicks := fileTicks(walInfo); walTicks > stamp.modified {
			stamp.modified = walTicks
		}
	}
	return stamp
}

// scanAntigravity 只读增量扫描 Antigravity 与 agy 对话数据库
func (store *Store) scanAntigravity(ctx context.Context, known map[string]fileStamp, rebuild bool, addProgress func(int) int, setProgress func(int, bool)) (bool, error) {
	type candidate struct {
		path         string
		cid          string
		info         fs.FileInfo
		curStamp     fileStamp
		hasActiveWAL bool
	}
	dirty := []candidate{}
	seen := map[string]bool{}
	var problems []error
	for _, root := range AntigravityRoots(store.home) {
		entries, err := os.ReadDir(root)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			problems = append(problems, err)
			continue
		}
		walInfos := map[string]fs.FileInfo{}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			lower := strings.ToLower(entry.Name())
			if strings.HasSuffix(lower, ".db-wal") {
				if info, infoErr := entry.Info(); infoErr == nil && info.Size() > 0 {
					walInfos[strings.TrimSuffix(lower, "-wal")] = info
				}
			}
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			lower := strings.ToLower(name)
			if !strings.HasSuffix(lower, ".db") {
				continue
			}
			path := filepath.Join(root, name)
			key := strings.ToLower(path)
			if seen[key] {
				continue
			}
			seen[key] = true
			info, infoErr := entry.Info()
			if infoErr != nil {
				continue
			}
			walInfo := walInfos[lower]
			wal := walInfo != nil
			curStamp := agFileStamp(info, walInfo)
			if !rebuild && !wal {
				if stamp, ok := known[path]; ok && stamp == curStamp {
					continue
				}
			}
			cid := strings.TrimSuffix(name, filepath.Ext(name))
			dirty = append(dirty, candidate{path: path, cid: cid, info: info, curStamp: curStamp, hasActiveWAL: wal})
		}
	}
	if len(dirty) == 0 {
		return false, errors.Join(problems...)
	}
	sort.Slice(dirty, func(i, j int) bool { return dirty[i].path < dirty[j].path })

	var offsets map[string]int64
	if !rebuild {
		var err error
		offsets, err = store.loadAgOffsets(ctx)
		if err != nil {
			return false, errors.Join(append(problems, err)...)
		}
	}

	var summaries map[string]agSummary
	var batch *writeBatch
	mutated := false
	pendingInBatch := 0
	progressStarted := false
	baseCompleted := 0
	const batchFlushSize = 50

	ensureBatch := func() error {
		if batch != nil {
			return nil
		}
		var err error
		batch, err = store.beginWrite(ctx)
		return err
	}
	flushBatch := func() error {
		if batch == nil {
			return nil
		}
		b := batch
		batch = nil
		pendingInBatch = 0
		if err := b.commit(ctx); err != nil {
			b.rollback()
			return err
		}
		if b.mutated {
			mutated = true
		}
		return nil
	}
	defer func() {
		if batch != nil {
			batch.rollback()
		}
	}()

	for index, item := range dirty {
		if err := ctx.Err(); err != nil {
			return mutated, err
		}
		prevOffset := int64(0)
		if offsets != nil {
			prevOffset = offsets[item.path]
		}
		stamp, hasStamp := known[item.path]
		changed, metaChanged, fatal, err := store.scanAntigravityDB(ctx, item.path, item.cid, item.info, item.curStamp, item.hasActiveWAL, prevOffset, hasStamp, stamp, func() map[string]agSummary {
			if summaries == nil {
				summaries = loadAgSummaries(ctx, store.home)
				if addProgress != nil {
					baseCompleted = addProgress(len(dirty))
					progressStarted = true
				}
			}
			return summaries
		}, func() (*writeBatch, error) {
			if err := ensureBatch(); err != nil {
				return nil, err
			}
			return batch, nil
		})
		if err != nil {
			if fatal {
				return mutated, errors.Join(append(problems, fmt.Errorf("%s: %w", item.path, err))...)
			}
			if isSQLiteBusy(err) {
				continue
			}
			problems = append(problems, fmt.Errorf("%s: %w", item.path, err))
			continue
		}
		if changed || metaChanged {
			if metaChanged {
				mutated = true
			}
			pendingInBatch++
			if pendingInBatch >= batchFlushSize {
				if err = flushBatch(); err != nil {
					return mutated, errors.Join(append(problems, err)...)
				}
				if progressStarted && setProgress != nil {
					setProgress(baseCompleted+index+1, true)
				}
			}
		}
	}
	if err := flushBatch(); err != nil {
		return mutated, errors.Join(append(problems, err)...)
	}
	if progressStarted && setProgress != nil {
		setProgress(baseCompleted+len(dirty), false)
	}
	return mutated, errors.Join(problems...)
}

// scanAntigravityDB 在单一只读事务中解析单个对话数据库的新增 gen_metadata 记录
func (store *Store) scanAntigravityDB(
	ctx context.Context,
	path, cid string,
	info fs.FileInfo,
	curStamp fileStamp,
	hasActiveWAL bool,
	prevOffset int64,
	hasStamp bool,
	stamp fileStamp,
	getSummaries func() map[string]agSummary,
	getBatch func() (*writeBatch, error),
) (bool, bool, bool, error) {
	read, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro&_pragma=busy_timeout(2000)")
	if err != nil {
		return false, false, false, err
	}
	defer read.Close()
	read.SetMaxOpenConns(1)

	rtx, err := read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return false, false, false, err
	}
	defer rtx.Rollback()

	var maxIdx int64
	err = rtx.QueryRowContext(ctx, "SELECT COALESCE(MAX(idx),-1) FROM gen_metadata").Scan(&maxIdx)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no such table") {
			_ = rtx.Rollback()
			_ = read.Close()
			if !hasActiveWAL && (!hasStamp || stamp != curStamp) {
				batch, bErr := getBatch()
				if bErr != nil {
					return false, false, true, bErr
				}
				if err = saveStateStamp(ctx, batch.tx, path, scanState{Model: "antigravity", Speed: "standard", Offset: 0, Session: cid}, curStamp); err != nil {
					return false, false, true, err
				}
				return true, false, false, nil
			}
			return false, false, false, nil
		}
		return false, false, false, err
	}

	if maxIdx < 0 || (prevOffset > 0 && maxIdx+1 <= prevOffset) {
		_ = rtx.Rollback()
		_ = read.Close()
		if !hasActiveWAL && (!hasStamp || stamp != curStamp) {
			batch, bErr := getBatch()
			if bErr != nil {
				return false, false, true, bErr
			}
			if err = saveStateStamp(ctx, batch.tx, path, scanState{Model: "antigravity", Speed: "standard", Offset: max(prevOffset, 0), Session: cid}, curStamp); err != nil {
				return false, false, true, err
			}
			return true, false, false, nil
		}
		return false, false, false, nil
	}

	type parsedItem struct {
		idx  int64
		meta agGenMeta
	}
	rows, err := rtx.QueryContext(ctx, "SELECT idx,data FROM gen_metadata WHERE idx>=? ORDER BY idx", prevOffset)
	if err != nil {
		return false, false, false, err
	}
	items := []parsedItem{}
	minStep, maxStep := int64(-1), int64(-1)
	for rows.Next() {
		var idx int64
		var blob []byte
		if err = rows.Scan(&idx, &blob); err != nil {
			rows.Close()
			return false, false, false, err
		}
		if len(blob) == 0 {
			continue
		}
		meta := parseAgGenMetadata(blob)
		if !meta.HasUsage || meta.Input+meta.Output+meta.CacheRead+meta.CacheWrite == 0 {
			continue
		}
		for _, si := range meta.StepIndices {
			if minStep < 0 || si < minStep {
				minStep = si
			}
			if si > maxStep {
				maxStep = si
			}
		}
		items = append(items, parsedItem{idx: idx, meta: meta})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, false, false, err
	}

	if len(items) == 0 {
		_ = rtx.Rollback()
		_ = read.Close()
		if !hasActiveWAL && (!hasStamp || stamp != curStamp) {
			batch, bErr := getBatch()
			if bErr != nil {
				return false, false, true, bErr
			}
			if err = saveStateStamp(ctx, batch.tx, path, scanState{Model: "antigravity", Speed: "standard", Offset: max(prevOffset, 0), Session: cid}, curStamp); err != nil {
				return false, false, true, err
			}
			return true, false, false, nil
		}
		return false, false, false, nil
	}

	stepTimes := map[int64]time.Time{}
	if minStep >= 0 {
		sRows, sErr := rtx.QueryContext(ctx, "SELECT idx,metadata FROM steps WHERE idx>=? AND idx<=? AND metadata IS NOT NULL", minStep, maxStep)
		if sErr == nil {
			for sRows.Next() {
				var idx int64
				var mblob []byte
				if sRows.Scan(&idx, &mblob) == nil && len(mblob) > 0 {
					if t := parseAgStepTime(mblob); !t.IsZero() {
						stepTimes[idx] = t
					}
				}
			}
			sRows.Close()
		}
	}

	var trajMeta agTrajectoryMeta
	var tblob []byte
	if rtx.QueryRowContext(ctx, "SELECT data FROM trajectory_metadata_blob WHERE id='main'").Scan(&tblob) == nil && len(tblob) > 0 {
		trajMeta = parseAgTrajectoryMeta(tblob)
	}
	_ = rtx.Rollback()
	_ = read.Close()

	summaries := getSummaries()
	batch, err := getBatch()
	if err != nil {
		return false, false, true, err
	}

	rootID := agRootConversation(cid, trajMeta, summaries)
	isSubagent := rootID != cid
	convID := "antigravity:" + rootID
	lastTime := trajMeta.CreatedAt
	if lastTime.IsZero() {
		lastTime = info.ModTime()
	}

	for _, item := range items {
		var eventTime time.Time
		for _, si := range item.meta.StepIndices {
			if t, ok := stepTimes[si]; ok {
				eventTime = t
				break
			}
		}
		if eventTime.IsZero() {
			eventTime = lastTime
		} else {
			lastTime = eventTime
		}
		row := model.UsageRow{
			ID:           fmt.Sprintf("antigravity:%s:%d", cid, item.idx),
			App:          model.UsageAntigravity,
			Conversation: convID,
			Model:        antigravityModelName(item.meta),
			Speed:        "standard",
			Time:         eventTime,
			Input:        item.meta.Input,
			Output:       item.meta.Output,
			CacheRead:    item.meta.CacheRead,
			CacheWrite:   item.meta.CacheWrite,
			Requests:     1,
		}
		row.Cost = store.pricing.Cost(row)
		if err = batch.saveUsage(ctx, row, path); err != nil {
			return false, false, true, err
		}
	}

	nextOffset := items[len(items)-1].idx + 1
	summary := summaries[cid]
	rootSummary := summaries[rootID]
	title := cleanTitle(coalesce(rootSummary.Title, rootSummary.Preview))
	cwd := coalesce(rootSummary.Workspace, trajMeta.Workspace, summary.Workspace)
	source, branch := "", ""
	if !isSubagent {
		title = cleanTitle(coalesce(summary.Title, summary.Preview))
		cwd = coalesce(trajMeta.Workspace, summary.Workspace)
		source = path
		branch = trajMeta.Branch
	}
	convChanged, err := saveConversation(ctx, batch.tx, convID, title, cwd, source, branch, isSubagent, true)
	if err != nil {
		return false, false, true, err
	}
	if err = saveStateStamp(ctx, batch.tx, path, scanState{
		Model:    "antigravity",
		Speed:    "standard",
		Offset:   nextOffset,
		Session:  cid,
		Title:    title,
		Cwd:      cwd,
		Branch:   branch,
		Subtasks: isSubagent,
	}, curStamp); err != nil {
		return false, false, true, err
	}
	return true, convChanged, false, nil
}
