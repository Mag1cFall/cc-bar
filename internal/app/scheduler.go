package app

import (
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Mag1cFall/cc-bar/internal/model"
	"github.com/Mag1cFall/cc-bar/internal/providers"
	"github.com/Mag1cFall/cc-bar/internal/usage"
	"github.com/fsnotify/fsnotify"
)

// SetIdle 在锁屏或熄屏时降低后台刷新频率
func (service *Service) SetIdle(idle bool) {
	service.idle.Store(idle)
	if !idle {
		service.RefreshQuotas()
	}
}

// SetPaused 在系统休眠时暂停后台任务
func (service *Service) SetPaused(paused bool) {
	service.paused.Store(paused)
	if !paused {
		service.dirty.Store(true)
		service.RefreshQuotas()
	}
}

func networkState() string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	active := []string{}
	for _, network := range interfaces {
		if network.Flags&net.FlagUp != 0 && network.Flags&net.FlagLoopback == 0 {
			active = append(active, network.Name)
		}
	}
	sort.Strings(active)
	return strings.Join(active, "|")
}

func (service *Service) schedule() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	lastQuota, lastUsage, lastForcedScan := time.Now(), time.Now(), time.Now()
	lastStatus := time.Time{}
	lastPrices := time.Time{}
	previousNetwork := networkState()
	for {
		select {
		case <-service.ctx.Done():
			return
		case now := <-ticker.C:
			if service.paused.Load() {
				continue
			}
			service.mu.RLock()
			quotaInterval := time.Duration(service.settings.QuotaIntervalMinutes) * time.Minute
			usageInterval := time.Duration(service.settings.UsageIntervalMinutes) * time.Minute
			showStatus := service.settings.ShowServiceStatus
			service.mu.RUnlock()
			if service.idle.Load() {
				quotaInterval *= 4
				usageInterval *= 4
			}
			currentNetwork := networkState()
			if currentNetwork != previousNetwork && currentNetwork != "" {
				lastQuota = time.Time{}
			}
			previousNetwork = currentNetwork
			if now.Sub(lastQuota) >= quotaInterval {
				lastQuota = now
				service.background(service.refresh)
			}
			if now.Sub(lastUsage) >= usageInterval && service.dirty.Swap(false) || now.Sub(lastForcedScan) >= 30*time.Minute {
				lastUsage = now
				lastForcedScan = now
				service.background(func() { _ = service.ScanUsage(service.ctx, false) })
			}
			if showStatus && now.Sub(lastStatus) >= 5*time.Minute {
				lastStatus = now
				service.background(service.refreshStatuses)
			}
			if now.Sub(lastPrices) >= 30*time.Minute {
				lastPrices = now
				service.background(func() {
					if err := service.usage.RefreshPricingIfNeeded(service.ctx); err != nil {
						service.logger.Debug("价格目录更新失败", "error", err)
					}
					service.changed()
				})
			}
		}
	}
}

func (service *Service) refreshStatuses() {
	statuses, err := providers.Statuses(service.ctx)
	service.mu.Lock()
	for app := model.Codex; app <= model.CommandCode; app++ {
		if status, found := statuses[model.QuotaName(app)]; found {
			service.statuses[fmt.Sprint(app)] = status
		}
	}
	service.mu.Unlock()
	if err != nil {
		service.logger.Debug("服务状态刷新失败", "error", err)
	}
	service.changed()
}

func (service *Service) watchLogs() {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		service.logger.Warn("日志监听启动失败", "error", err)
		return
	}
	defer watcher.Close()
	roots := []string{
		filepath.Join(service.home, ".codex"),
		filepath.Join(service.home, ".claude"),
		filepath.Join(service.home, ".local", "share", "opencode"),
		filepath.Join(service.home, ".dsh", "sessions"),
		filepath.Join(service.home, ".pi"),
		filepath.Join(service.home, ".omp"),
	}
	for _, app := range []model.UsageApp{model.UsagePi, model.UsageOmp} {
		roots = append(roots, usage.SessionRoots(service.home, app)...)
	}
	watchDirectory := func(root string) {
		parent := root
		for {
			if _, err := os.Stat(parent); err == nil {
				_ = watcher.Add(parent)
				break
			} else if !os.IsNotExist(err) || filepath.Dir(parent) == parent {
				return
			}
			parent = filepath.Dir(parent)
		}
		_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return fs.SkipDir
			}
			if entry.IsDir() {
				_ = watcher.Add(path)
			}
			return nil
		})
	}
	for _, root := range roots {
		watchDirectory(root)
	}
	for {
		select {
		case <-service.ctx.Done():
			return
		case event, open := <-watcher.Events:
			if !open {
				return
			}
			relevant := false
			for _, root := range roots {
				child, childErr := filepath.Rel(root, event.Name)
				parent, parentErr := filepath.Rel(event.Name, root)
				if childErr == nil && filepath.IsLocal(child) || parentErr == nil && filepath.IsLocal(parent) {
					relevant = true
					break
				}
			}
			if !relevant {
				continue
			}
			if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) || event.Has(fsnotify.Rename) || event.Has(fsnotify.Remove) {
				service.dirty.Store(true)
			}
			if event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename) {
				if restoreErr := service.accounts.RestoreSharedDirectory(event.Name); restoreErr != nil {
					service.logger.Warn("Claude 共享记录目录重建失败", "path", event.Name, "error", restoreErr)
				}
			}
			if event.Has(fsnotify.Create) {
				if info, statErr := os.Stat(event.Name); statErr == nil && info.IsDir() {
					watchDirectory(event.Name)
				}
			}
		case watchErr, open := <-watcher.Errors:
			if !open {
				return
			}
			service.logger.Debug("日志监听异常", "error", watchErr)
			service.dirty.Store(true)
		}
	}
}
