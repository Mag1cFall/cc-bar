package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mag1cFall/cc-bar/internal/accounts"
	"github.com/Mag1cFall/cc-bar/internal/history"
	"github.com/Mag1cFall/cc-bar/internal/model"
	"github.com/Mag1cFall/cc-bar/internal/providers"
	"github.com/Mag1cFall/cc-bar/internal/usage"
)

// Desktop 连接原生窗口和应用服务
type Desktop interface {
	Changed()
	ShowMain(string)
	PresentMain()
	ApplySettings(model.Settings) error
	OpenDirectory(string) error
	OpenURL(string) error
	ResizeSurface(context.Context, int, int)
	HidePopover()
	Quit()
}

// LocalService 表示本地日志来源的检测结果
type LocalService struct {
	Detected bool   `json:"detected"`
	Source   string `json:"source"`
}

// Snapshot 返回全部界面的共享状态
type Snapshot struct {
	Version               string                   `json:"version"`
	Settings              model.Settings           `json:"settings"`
	Providers             []model.ProviderState    `json:"providers"`
	ClaudeAccounts        []accounts.ClaudeProfile `json:"claudeAccounts"`
	ClaudeLogin           *accounts.ClaudeLogin    `json:"claudeLogin,omitempty"`
	ImportedCodexAccounts []accounts.CodexAccount  `json:"importedCodexAccounts"`
	ServiceStatuses       map[string]string        `json:"serviceStatuses"`
	LocalServices         map[string]LocalService  `json:"localServices"`
	Scan                  model.UsageStatus        `json:"scan"`
	Locale                string                   `json:"locale"`
}

type cachedProvider struct {
	AccountKey string               `json:"accountKey"`
	Snapshot   *model.QuotaSnapshot `json:"snapshot"`
}
type quotaCache struct {
	Providers map[model.QuotaApp]cachedProvider `json:"providers"`
}

// Service 承载凭据额度统计及桌面设置接口
type Service struct {
	mu                     sync.RWMutex
	settings               model.Settings
	states                 map[model.QuotaApp]model.ProviderState
	backoff                map[model.QuotaApp]time.Time
	cache                  quotaCache
	statuses               map[string]string
	dataDir, home, version string
	usage                  *usage.Store
	accounts               *accounts.Store
	history                *history.Store
	desktop                Desktop
	ctx                    context.Context
	cancel                 context.CancelFunc
	refreshGate            sync.Mutex
	scanGate               sync.Mutex
	cursorHistoryGate      sync.Mutex
	workers                sync.WaitGroup
	dirty                  atomic.Bool
	paused                 atomic.Bool
	idle                   atomic.Bool
	notifyTimer            *time.Timer
	logger                 *slog.Logger
	logLevel               *slog.LevelVar
}

// New 打开当前用户的数据目录
func New(directory, home, version string, logger *slog.Logger) (*Service, error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	settings, err := readSettings(directory)
	if err != nil {
		return nil, fmt.Errorf("读取设置: %w", err)
	}
	usageStore, err := usage.Open(directory, home)
	if err != nil {
		return nil, err
	}
	accountStore, err := accounts.New(directory, home)
	if err != nil {
		usageStore.Close()
		return nil, err
	}
	historyStore, err := history.Open(directory)
	if err != nil {
		usageStore.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	service := &Service{
		settings: settings,
		states:   map[model.QuotaApp]model.ProviderState{},
		backoff:  map[model.QuotaApp]time.Time{},
		cache:    quotaCache{Providers: map[model.QuotaApp]cachedProvider{}},
		statuses: map[string]string{},
		dataDir:  directory,
		home:     home,
		version:  version,
		usage:    usageStore,
		accounts: accountStore,
		history:  historyStore,
		ctx:      ctx,
		cancel:   cancel,
		logger:   logger,
	}
	if data, readErr := os.ReadFile(filepath.Join(directory, "quota-cache.json")); readErr == nil {
		if decodeErr := json.Unmarshal(data, &service.cache); decodeErr != nil {
			logger.Warn("额度缓存读取失败", "error", decodeErr)
		}
	}
	if service.cache.Providers == nil {
		service.cache.Providers = map[model.QuotaApp]cachedProvider{}
	}
	for app := model.Codex; app <= model.CommandCode; app++ {
		service.states[app] = model.ProviderState{App: app, Name: model.QuotaName(app)}
	}
	usageStore.OnChanged = service.changed
	accountStore.Changed = service.changed
	accountStore.LoginCompleted = func() {
		service.background(func() {
			_ = service.accounts.RefreshClaude(service.ctx)
			service.RefreshQuotas()
		})
	}
	return service, nil
}

// AttachDesktop 接入托盘和窗口控制
func (service *Service) AttachDesktop(desktop Desktop) { service.desktop = desktop }

// SetLogLevel 按详细日志设置调整当前日志级别
func (service *Service) SetLogLevel(level *slog.LevelVar) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.logLevel = level
	service.applyLogLevel()
}

func (service *Service) applyLogLevel() {
	if service.logLevel == nil {
		return
	}
	level := slog.LevelInfo
	if service.settings.VerboseLogging {
		level = slog.LevelDebug
	}
	service.logLevel.Set(level)
}

// Start 启动后台刷新与日志监听
func (service *Service) Start() {
	service.background(service.refresh)
	service.background(func() { _ = service.ScanUsage(service.ctx, false) })
	service.background(service.schedule)
	service.background(service.watchLogs)
}

// Stop 等待后台任务结束再关闭存储
func (service *Service) Stop() {
	service.cancel()
	service.accounts.StopLogin()
	service.workers.Wait()
	service.mu.Lock()
	if service.notifyTimer != nil {
		service.notifyTimer.Stop()
	}
	service.mu.Unlock()
	_ = service.history.Flush()
	_ = service.usage.Close()
}

func (service *Service) background(work func()) {
	service.workers.Add(1)
	go func() { defer service.workers.Done(); work() }()
}

func (service *Service) changed() {
	service.mu.Lock()
	if service.notifyTimer == nil {
		service.notifyTimer = time.AfterFunc(100*time.Millisecond, func() {
			if service.desktop != nil {
				service.desktop.Changed()
			}
		})
	} else {
		service.notifyTimer.Reset(100 * time.Millisecond)
	}
	service.mu.Unlock()
}

// GetSnapshot 读取所有窗口所需的即时状态
func (service *Service) GetSnapshot() Snapshot {
	service.mu.RLock()
	result := Snapshot{Version: service.version, Settings: cloneSettings(service.settings), Providers: []model.ProviderState{}, ServiceStatuses: map[string]string{}, Locale: service.settings.Language}
	for app := model.Codex; app <= model.CommandCode; app++ {
		result.Providers = append(result.Providers, service.states[app])
	}
	for name, status := range service.statuses {
		result.ServiceStatuses[name] = status
	}
	service.mu.RUnlock()
	if result.Locale == "system" {
		result.Locale = systemLocale()
	}
	result.ClaudeAccounts = service.accounts.ListClaude()
	result.ClaudeLogin = service.accounts.LoginStatus()
	result.ImportedCodexAccounts = service.accounts.ListCodex()
	result.Scan = service.usage.Status()
	result.LocalServices = service.localServices()
	return result
}

func (service *Service) localServices() map[string]LocalService {
	locations := map[string]string{
		"3": filepath.Join(service.home, ".pi", "agent", "sessions"),
		"4": filepath.Join(service.home, ".local", "share", "opencode"),
		"5": filepath.Join(service.home, ".dsh", "sessions"),
		"6": filepath.Join(service.home, ".omp", "agent", "sessions"),
	}
	result := map[string]LocalService{}
	for name, path := range locations {
		_, err := os.Stat(path)
		result[name] = LocalService{Detected: err == nil, Source: path}
	}
	for _, app := range []model.UsageApp{model.UsagePi, model.UsageOmp} {
		paths := []string{}
		for _, path := range usage.SessionRoots(service.home, app) {
			if info, err := os.Stat(path); err == nil && info.IsDir() {
				paths = append(paths, path)
			}
		}
		if len(paths) > 0 {
			result[fmt.Sprint(app)] = LocalService{Detected: true, Source: strings.Join(paths, "; ")}
		}
	}
	return result
}

// SaveSettings 保存设置并更新对应的桌面功能
func (service *Service) SaveSettings(settings model.Settings) error {
	if settings.QuotaIntervalMinutes < 1 || settings.UsageIntervalMinutes < 1 {
		return errors.New("刷新间隔至少为1分钟")
	}
	service.mu.Lock()
	previous := service.settings
	if err := writeJSON(service.dataDir, "settings.json", settings); err != nil {
		service.mu.Unlock()
		return err
	}
	service.settings = cloneSettings(settings)
	service.applyLogLevel()
	service.mu.Unlock()
	if service.desktop != nil && (settings.LaunchAtLogin != previous.LaunchAtLogin || settings.FloatingEnabled != previous.FloatingEnabled) {
		if err := service.desktop.ApplySettings(settings); err != nil {
			return err
		}
	}
	service.changed()
	refresh := settings.CommandCodeCredentialPreference != previous.CommandCodeCredentialPreference
	for app, provider := range settings.Providers {
		refresh = refresh || provider.Enabled && !previous.Providers[app].Enabled
	}
	if refresh {
		service.background(service.refresh)
	}
	return nil
}

// RefreshQuotas 刷新额度并遵守服务退避时间
func (service *Service) RefreshQuotas() { service.background(service.refresh) }

func (service *Service) refresh() {
	if !service.refreshGate.TryLock() {
		return
	}
	defer service.refreshGate.Unlock()
	service.mu.RLock()
	settings := cloneSettings(service.settings)
	service.mu.RUnlock()
	var group sync.WaitGroup
	for app := model.Codex; app <= model.CommandCode; app++ {
		if !settings.Providers[app].Enabled {
			continue
		}
		group.Add(1)
		go func(app model.QuotaApp) {
			defer group.Done()
			service.refreshProvider(app, settings)
		}(app)
	}
	group.Add(1)
	go func() {
		defer group.Done()
		_ = service.accounts.RefreshCodex(service.ctx)
		_ = service.accounts.RefreshClaude(service.ctx)
	}()
	group.Wait()
	for _, account := range service.accounts.ListCodex() {
		if account.Snapshot != nil {
			_ = service.history.Record("codex:imported:"+account.ID, model.Codex, *account.Snapshot, "api", account.Snapshot.FetchedAt)
		}
	}
	service.changed()
}

func accountID(credential *model.Credential) string {
	if credential == nil {
		return ""
	}
	for _, identity := range []string{credential.AccountID, credential.AccountUUID, credential.UserID, credential.Email, credential.Source} {
		if identity != "" {
			return identity
		}
	}
	return ""
}

func (service *Service) refreshProvider(app model.QuotaApp, settings model.Settings) {
	credential, err := providers.Discover(app, settings)
	if err != nil {
		service.providerError(app, err)
		return
	}
	service.mu.Lock()
	state := service.states[app]
	if accountID(state.Account) != accountID(credential) {
		state.Snapshot = nil
		state.LastSuccessAt = nil
		state.Error = ""
		delete(service.backoff, app)
	}
	if credential != nil {
		published := *credential
		state.Account = &published
	} else {
		state.Account = nil
	}
	if credential != nil {
		state.Source = credential.Source
	}
	if cached := service.cache.Providers[app]; state.Snapshot == nil && cached.AccountKey == accountID(credential) {
		state.Snapshot = cached.Snapshot
	}
	service.states[app] = state
	if credential == nil {
		service.mu.Unlock()
		service.changed()
		return
	}
	if time.Now().Before(service.backoff[app]) || state.LastSuccessAt != nil && time.Since(*state.LastSuccessAt) < time.Minute {
		service.mu.Unlock()
		return
	}
	state.Refreshing = true
	service.states[app] = state
	service.mu.Unlock()
	service.changed()
	ctx, cancel := context.WithTimeout(service.ctx, 45*time.Second)
	defer cancel()
	credential, err = providers.EnsureFresh(ctx, app, credential)
	var snapshot *model.QuotaSnapshot
	if err == nil {
		snapshot, err = providers.Fetch(ctx, app, credential)
	}
	if err != nil {
		service.providerError(app, err)
		return
	}
	current, _ := providers.Discover(app, settings)
	if accountID(current) != accountID(credential) || current != nil && current.Source != credential.Source {
		service.mu.Lock()
		state = service.states[app]
		state.Refreshing = false
		service.states[app] = state
		service.mu.Unlock()
		return
	}
	service.mu.Lock()
	state = service.states[app]
	state.Account = credential
	state.Snapshot = snapshot
	state.LastSuccessAt = &snapshot.FetchedAt
	state.Error = ""
	state.ErrorKind = ""
	state.Refreshing = false
	service.states[app] = state
	service.cache.Providers[app] = cachedProvider{accountID(credential), snapshot}
	cacheErr := writeJSON(service.dataDir, "quota-cache.json", service.cache)
	service.mu.Unlock()
	if cacheErr != nil {
		service.logger.Warn("额度缓存保存失败", "error", cacheErr)
	}
	_ = service.history.Record(history.KeyFor(app, credential), app, *snapshot, "api", snapshot.FetchedAt)
	if app == model.Cursor {
		_ = service.usage.RefreshCursorRecent(ctx, *credential, snapshot)
	}
	service.changed()
}

func (service *Service) providerError(app model.QuotaApp, err error) {
	service.mu.Lock()
	state := service.states[app]
	state.Error = err.Error()
	state.Refreshing = false
	state.ErrorKind = "network"
	var failure *providers.Error
	if errors.As(err, &failure) {
		state.ErrorKind = failure.Kind
		if failure.StatusCode == 429 {
			delay := max(10*time.Minute, failure.RetryAfter)
			service.backoff[app] = time.Now().Add(delay)
		}
	}
	service.states[app] = state
	service.mu.Unlock()
	service.logger.Warn("额度刷新失败", "service", model.QuotaName(app), "error", err)
	service.changed()
}

// ScanUsage 扫描增量日志或重建本地统计
func (service *Service) ScanUsage(ctx context.Context, rebuild bool) error {
	if !service.scanGate.TryLock() {
		return nil
	}
	defer service.scanGate.Unlock()
	err := service.usage.Scan(ctx, rebuild)
	if err != nil {
		service.logger.Warn("日志扫描失败", "error", err)
	}
	service.changed()
	return err
}

// GetOverview 返回完整统计与环比图表
func (service *Service) GetOverview(ctx context.Context, query model.UsageQuery) (model.UsageOverview, error) {
	query.AllowedApps = service.visibleUsageApps()
	service.queueCursorRange(query.Service, query.From, query.To, query.Compare)
	result, err := service.usage.Overview(ctx, query)
	if errors.Is(err, context.Canceled) {
		return model.UsageOverview{}, nil
	}
	return result, err
}

// GetConversations 返回搜索筛选后的对话分页
func (service *Service) GetConversations(ctx context.Context, query model.ConversationQuery) (model.ConversationPage, error) {
	query.AllowedApps = service.visibleUsageApps()
	service.queueCursorRange(query.App, query.From, query.To, false)
	result, err := service.usage.Conversations(ctx, query)
	if errors.Is(err, context.Canceled) {
		return model.ConversationPage{}, nil
	}
	return result, err
}

// queueCursorRange 展示已保存用量并在后台补全 Cursor 历史区间
func (service *Service) queueCursorRange(app *model.UsageApp, fromText, toText string, compare bool) {
	if app != nil && *app != model.UsageCursor || !service.visibleUsageApps()[model.UsageCursor] {
		return
	}
	from, fromErr := time.Parse(time.RFC3339Nano, fromText)
	to, toErr := time.Parse(time.RFC3339Nano, toText)
	if fromErr != nil || toErr != nil {
		return
	}
	if from.Unix() <= 0 {
		return
	}
	if compare {
		from = from.Add(-to.Sub(from))
	}
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	if to.After(today) {
		to = today
	}
	if !from.Before(to) {
		return
	}
	service.mu.RLock()
	settings := cloneSettings(service.settings)
	service.mu.RUnlock()
	credential, err := providers.Discover(model.Cursor, settings)
	if err != nil || credential == nil {
		return
	}
	if !service.cursorHistoryGate.TryLock() {
		return
	}
	service.background(func() {
		defer service.cursorHistoryGate.Unlock()
		if err := service.usage.RefreshCursorHistory(service.ctx, *credential, from, to); err != nil {
			service.logger.Warn("Cursor历史计量读取失败", "error", err)
		}
	})
}

func (service *Service) visibleUsageApps() map[model.UsageApp]bool {
	service.mu.RLock()
	defer service.mu.RUnlock()
	visible := make(map[model.UsageApp]bool, len(service.settings.UsageVisibility))
	for app, enabled := range service.settings.UsageVisibility {
		visible[app] = enabled && (app != model.UsageCursor || service.settings.Providers[model.Cursor].Enabled)
	}
	return visible
}

// GetConversation 返回对话事件和费用拆分
func (service *Service) GetConversation(ctx context.Context, id string) (model.ConversationDetail, error) {
	result, err := service.usage.Detail(ctx, id)
	if errors.Is(err, context.Canceled) {
		return model.ConversationDetail{}, nil
	}
	return result, err
}

// RefreshPrices 更新价格并重新计算已保存事件
func (service *Service) RefreshPrices(ctx context.Context) error {
	err := service.usage.RefreshPricing(ctx)
	service.changed()
	return err
}

// OpenDataDirectory 打开本机统计与设置目录
func (service *Service) OpenDataDirectory() error {
	return service.desktop.OpenDirectory(service.dataDir)
}

// ShowMain 打开指定主窗口页面
func (service *Service) ShowMain(page string) { service.desktop.ShowMain(page) }

// PresentMain 在页面导航完成后显示主窗口
func (service *Service) PresentMain() { service.desktop.PresentMain() }

// ResizeSurface 根据当前界面内容调整小窗口
func (service *Service) ResizeSurface(ctx context.Context, width, height int) {
	service.desktop.ResizeSurface(ctx, width, height)
}

// HidePopover 收起托盘弹窗
func (service *Service) HidePopover() { service.desktop.HidePopover() }

// Quit 退出应用
func (service *Service) Quit() { service.desktop.Quit() }

// SetHudPosition 记录悬浮窗位置
func (service *Service) SetHudPosition(x, y float64) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	settings := cloneSettings(service.settings)
	settings.HudLeft = &x
	settings.HudTop = &y
	if err := writeJSON(service.dataDir, "settings.json", settings); err != nil {
		return err
	}
	service.settings = settings
	return nil
}

// DataDirectory 返回持久化目录
func (service *Service) DataDirectory() string { return service.dataDir }

func readableAccount(key string, states []model.ProviderState, profiles []accounts.ClaudeProfile, imports []accounts.CodexAccount) string {
	for _, state := range states {
		if history.KeyFor(state.App, state.Account) == key && state.Account != nil {
			if state.Account.Email != "" {
				return state.Account.Email
			}
			return model.QuotaName(state.App)
		}
	}
	for _, profile := range profiles {
		if history.KeyFor(model.Claude, &model.Credential{Email: profile.Email, AccountUUID: profile.AccountUUID}) == key {
			return profile.Name
		}
	}
	for _, account := range imports {
		if key == "codex:imported:"+account.ID {
			return account.DisplayName
		}
	}
	for _, app := range []model.QuotaApp{model.Codex, model.Claude, model.Antigravity} {
		if strings.HasPrefix(key, strings.ToLower(strings.ReplaceAll(model.QuotaName(app), " Code", ""))+":") {
			return model.QuotaName(app)
		}
	}
	return key
}
