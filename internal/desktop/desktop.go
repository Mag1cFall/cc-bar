package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mag1cFall/cc-bar/internal/app"
	"github.com/Mag1cFall/cc-bar/internal/model"
	"github.com/Mag1cFall/cc-bar/web"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// Host 管理主窗口托盘弹窗及悬浮窗
type Host struct {
	application     *application.App
	service         *app.Service
	main            *application.WebviewWindow
	popover         *application.WebviewWindow
	hud             *application.WebviewWindow
	tray            *application.SystemTray
	lastMenu        string
	menuMu          sync.Mutex
	ready           atomic.Bool
	popoverClosedAt atomic.Int64
	background      application.RGBA
}

// Run 运行内嵌前端的单实例桌面应用
func Run(service *app.Service, logger *slog.Logger) error {
	assets, err := web.Assets()
	if err != nil {
		return err
	}
	host := &Host{service: service}
	windows := platformOptions(service)
	windows.AdditionalBrowserArgs = []string{"--disk-cache-size=33554432"}
	if port := os.Getenv("CCBAR_DEBUG_PORT"); port != "" {
		if number, parseErr := strconv.Atoi(port); parseErr == nil && number > 1024 && number < 65536 {
			windows.AdditionalBrowserArgs = append(windows.AdditionalBrowserArgs, "--remote-debugging-address=127.0.0.1", "--remote-debugging-port="+port)
		}
	}
	host.application = application.New(application.Options{
		Name:        "CCBar",
		Description: "AI coding usage and accounts",
		Icon:        appIcon(),
		Windows:     windows,
		Services:    []application.Service{application.NewService(service)},
		Assets:      application.AssetOptions{Handler: application.BundledAssetFileServer(assets), DisableLogging: true},
		Logger:      logger,
		LogLevel:    slog.LevelWarn,
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID:               "ccbar-windows-go",
			OnSecondInstanceLaunch: func(application.SecondInstanceData) { host.ShowMain("overview") },
		},
		OnShutdown: func() { service.Stop() },
	})
	service.AttachDesktop(host)
	settings := service.GetSnapshot().Settings
	host.background = windowBackground(settings.Theme)
	width, height := 1600, 1100
	if screen := host.application.Screen.GetPrimary(); screen != nil {
		width = min(width, screen.WorkArea.Width-32)
		height = min(height, screen.WorkArea.Height-32)
	}
	host.main = host.application.Window.NewWithOptions(application.WebviewWindowOptions{
		Name: "main", Title: "CCBar", URL: "/?window=main", Width: width, Height: height, MinWidth: 1040, MinHeight: 520,
		Frameless:        true,
		Hidden:           settings.DidCompleteOnboarding && hasBackgroundArgument(),
		BackgroundColour: host.background,
	})
	host.main.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) { hideSurface(host.main); event.Cancel() })
	host.main.OnWindowEvent(events.Common.WindowRuntimeReady, func(*application.WindowEvent) { prepareSurface(host.main) })
	host.popover = host.application.Window.NewWithOptions(application.WebviewWindowOptions{
		Name: "popover", Title: "CCBar", URL: "/?window=popover", Width: 360, Height: 520,
		Frameless: true, DisableResize: true, AlwaysOnTop: true, Hidden: true,
		Windows:          application.WindowsWindow{HiddenOnTaskbar: true},
		BackgroundColour: host.background,
	})
	host.popover.OnWindowEvent(events.Common.WindowRuntimeReady, func(*application.WindowEvent) { prepareSurface(host.popover) })
	host.popover.OnWindowEvent(events.Common.WindowLostFocus, func(*application.WindowEvent) {
		if host.popover.IsVisible() {
			host.popoverClosedAt.Store(time.Now().UnixNano())
			hideSurface(host.popover)
		}
	})
	host.hud = host.application.Window.NewWithOptions(application.WebviewWindowOptions{
		Name: "hud", Title: "CCBar HUD", URL: "/?window=hud", Width: 180, Height: 150,
		Frameless: true, DisableResize: true, AlwaysOnTop: true, Hidden: true,
		Windows:        application.WindowsWindow{HiddenOnTaskbar: true, ExStyle: 0x08000000, WindowDidMoveDebounceMS: 250},
		BackgroundType: application.BackgroundTypeTransparent,
	})
	host.hud.OnWindowEvent(events.Windows.WindowEndMove, func(*application.WindowEvent) { host.persistHudPosition() })
	host.tray = host.application.SystemTray.New()
	host.tray.SetIcon(appIcon())
	host.tray.AttachWindow(host.popover).WindowOffset(6)
	host.tray.OnClick(host.togglePopover)
	host.tray.OnDoubleClick(func() { host.ShowMain("overview") })
	host.application.Event.OnApplicationEvent(events.Windows.ApplicationStarted, func(*application.ApplicationEvent) {
		host.ready.Store(true)
		host.registerPlatformEvents()
		_ = host.ApplySettings(settings)
		host.Changed()
		service.Start()
	})
	host.application.Event.OnApplicationEvent(events.Windows.APMSuspend, func(*application.ApplicationEvent) { service.SetPaused(true) })
	host.application.Event.OnApplicationEvent(events.Windows.APMResumeSuspend, func(*application.ApplicationEvent) { service.SetPaused(false) })
	host.application.Event.OnApplicationEvent(events.Windows.APMResumeAutomatic, func(*application.ApplicationEvent) { service.SetPaused(false) })
	return host.application.Run()
}

func hasBackgroundArgument() bool {
	for _, argument := range os.Args[1:] {
		if argument == "--background" {
			return true
		}
	}
	return false
}

// Changed 通知三个界面并更新托盘菜单
func (host *Host) Changed() {
	if !host.ready.Load() {
		return
	}
	host.menuMu.Lock()
	defer host.menuMu.Unlock()
	snapshot := host.service.GetSnapshot()
	background := windowBackground(snapshot.Settings.Theme)
	if background != host.background {
		host.background = background
		host.main.SetBackgroundColour(background)
		host.popover.SetBackgroundColour(background)
	}
	host.application.Event.Emit("ccbar:changed")
	tooltip := []string{"CCBar"}
	for _, provider := range snapshot.Providers {
		if !snapshot.Settings.Providers[provider.App].Enabled || !snapshot.Settings.Providers[provider.App].MenuBar {
			continue
		}
		tooltip = append(tooltip, provider.Name+" "+quotaLabel(provider, snapshot.Settings.MenuBarWindow))
	}
	host.tray.SetTooltip(strings.Join(tooltip, "\n"))
	menuState, _ := json.Marshal(struct {
		Profiles any
		Privacy  bool
		Locale   string
	}{snapshot.ClaudeAccounts, snapshot.Settings.PrivacyMode, snapshot.Locale})
	if string(menuState) == host.lastMenu {
		return
	}
	host.lastMenu = string(menuState)
	label := func(english, chinese string) string {
		if strings.HasPrefix(snapshot.Locale, "en") {
			return english
		}
		return chinese
	}
	menu := host.application.NewMenu()
	menu.Add(label("Statistics", "统计")).OnClick(func(*application.Context) { host.ShowMain("overview") })
	menu.Add(label("Settings", "设置")).OnClick(func(*application.Context) { host.ShowMain("services") })
	menu.Add(label("Refresh quotas", "刷新额度")).OnClick(func(*application.Context) { host.service.RefreshQuotas() })
	if len(snapshot.ClaudeAccounts) > 0 {
		accountsMenu := menu.AddSubmenu(label("Claude accounts", "Claude 账号"))
		for index, profile := range snapshot.ClaudeAccounts {
			label := profile.Name
			if snapshot.Settings.PrivacyMode {
				label = fmt.Sprintf("Claude %d", index+1)
			}
			if profile.IsActive {
				label = "✓ " + label
			}
			accountsMenu.Add(label).OnClick(func(*application.Context) {
				if err := host.service.SwitchClaudeAccount(profile.ID); err != nil {
					host.application.Dialog.Error().SetTitle("Claude Code").SetMessage(err.Error()).Show()
				}
			})
		}
	}
	menu.AddSeparator()
	menu.Add(label("Quit", "退出")).OnClick(func(*application.Context) { host.Quit() })
	host.tray.SetMenu(menu)
}

func quotaLabel(state model.ProviderState, window string) string {
	if state.Snapshot == nil {
		return "—"
	}
	labels := []string{}
	if window == "weekly" {
		for _, limit := range []*model.QuotaLimit{state.Snapshot.PrimaryLimit, state.Snapshot.SecondaryLimit} {
			if limit != nil && limit.Kind == model.Weekly {
				return fmt.Sprintf("%.0f%%", math.Max(0, 100-limit.Window.UsedPercent))
			}
		}
		return "—"
	}
	for _, limit := range []*model.QuotaLimit{state.Snapshot.PrimaryLimit, state.Snapshot.SecondaryLimit} {
		if limit == nil {
			continue
		}
		labels = append(labels, fmt.Sprintf("%.0f%%", math.Max(0, 100-limit.Window.UsedPercent)))
		if window != "both" {
			break
		}
	}
	return strings.Join(labels, " / ")
}

// ShowMain 打开主窗口并定位到指定页面
func (host *Host) ShowMain(page string) {
	if host.main == nil {
		return
	}
	host.application.Event.Emit("ccbar:navigate", map[string]string{"page": page})
}

// PresentMain 显示已经完成导航的主窗口并收起托盘弹窗
func (host *Host) PresentMain() {
	if !host.main.IsVisible() {
		host.main.Show()
	}
	host.main.Focus()
	hideSurface(host.popover)
}

// togglePopover 复用已加载的弹窗并抑制托盘点击引起的失焦重开
func (host *Host) togglePopover() {
	if host.popover.IsVisible() {
		hideSurface(host.popover)
		return
	}
	if closed := host.popoverClosedAt.Load(); closed != 0 && time.Since(time.Unix(0, closed)) < 200*time.Millisecond {
		return
	}
	_ = host.tray.PositionWindow(host.popover, 6)
	host.popover.Show().Focus()
}

// ApplySettings 更新登录启动与悬浮窗显示
func (host *Host) ApplySettings(settings model.Settings) error {
	if err := setLaunchAtLogin(settings.LaunchAtLogin); err != nil {
		return err
	}
	if settings.FloatingEnabled {
		if settings.HudLeft != nil && settings.HudTop != nil {
			host.hud.SetPosition(int(*settings.HudLeft), int(*settings.HudTop))
		} else {
			host.placeHud()
		}
		host.hud.Show()
	} else {
		host.hud.Hide()
	}
	host.Changed()
	return nil
}

func (host *Host) placeHud() {
	screen, err := host.hud.GetScreen()
	if err != nil || screen == nil {
		return
	}
	width, height := host.hud.Size()
	host.hud.SetPosition(screen.WorkArea.X+screen.WorkArea.Width-width-16, screen.WorkArea.Y+screen.WorkArea.Height-height-16)
}

func (host *Host) persistHudPosition() {
	x, y := host.hud.Position()
	width, height := host.hud.Size()
	if screen, err := host.hud.GetScreen(); err == nil && screen != nil {
		area := screen.WorkArea
		left, right := area.X+8, area.X+area.Width-width-8
		top, bottom := area.Y+8, area.Y+area.Height-height-8
		if math.Abs(float64(x-left)) < 16 {
			x = left
		}
		if math.Abs(float64(x-right)) < 16 {
			x = right
		}
		if math.Abs(float64(y-top)) < 16 {
			y = top
		}
		if math.Abs(float64(y-bottom)) < 16 {
			y = bottom
		}
		x = max(area.X, min(x, area.X+area.Width-width))
		y = max(area.Y, min(y, area.Y+area.Height-height))
	}
	host.hud.SetPosition(x, y)
	_ = host.service.SetHudPosition(float64(x), float64(y))
}

// ResizeSurface 调整调用窗口的内容尺寸
func (host *Host) ResizeSurface(ctx context.Context, width, height int) {
	window, _ := ctx.Value(application.WindowKey).(application.Window)
	if window == nil || window.Name() == "main" {
		return
	}
	width, height = max(120, min(width, 480)), max(60, min(height, 900))
	currentWidth, currentHeight := window.Size()
	if currentWidth == width && currentHeight == height {
		return
	}
	window.SetSize(width, height)
	if window.Name() == "popover" {
		_ = host.tray.PositionWindow(window, 6)
	}
}

// HidePopover 收起托盘弹窗
func (host *Host) HidePopover() { hideSurface(host.popover) }

// OpenDirectory 在资源管理器中打开数据目录
func (host *Host) OpenDirectory(path string) error { return openDirectory(path) }

// Quit 结束桌面事件循环
func (host *Host) Quit() { host.application.Quit() }
