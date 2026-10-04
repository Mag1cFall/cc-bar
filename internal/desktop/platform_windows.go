package desktop

import (
	"os"
	"path/filepath"
	"unsafe"

	"github.com/Mag1cFall/cc-bar/internal/app"
	"github.com/wailsapp/wails/v3/pkg/application"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var user32 = windows.NewLazySystemDLL("user32.dll")
var dwmapi = windows.NewLazySystemDLL("dwmapi.dll")
var setWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")
var showNativeWindow = user32.NewProc("ShowWindow")
var getWindowStyle = user32.NewProc("GetWindowLongPtrW")
var setWindowStyle = user32.NewProc("SetWindowLongPtrW")

const (
	dwmTransitionsForcedDisabled = 3
	dwmCloak                     = 13
	extendedWindowStyle          = ^uintptr(19)
	windowNoActivate             = 0x08000000
)

var wtsapi = windows.NewLazySystemDLL("wtsapi32.dll")
var registerSession = wtsapi.NewProc("WTSRegisterSessionNotification")
var registerPower = user32.NewProc("RegisterPowerSettingNotification")
var displayState = windows.GUID{Data1: 0x6fe69556, Data2: 0x704a, Data3: 0x47a0, Data4: [8]byte{0x8f, 0x24, 0xc2, 0x8d, 0x93, 0x6f, 0xda, 0x47}}

type powerNotification struct {
	Setting windows.GUID
	Length  uint32
	Data    uint32
}

// prepareSurface 在不可见状态预热页面并设置窗口显示行为
func prepareSurface(window *application.WebviewWindow) {
	application.InvokeSync(func() {
		handle := uintptr(window.NativeWindow())
		var enabled int32 = 1
		_, _, _ = setWindowAttribute.Call(handle, dwmTransitionsForcedDisabled, uintptr(unsafe.Pointer(&enabled)), unsafe.Sizeof(enabled))
		if window.IsVisible() {
			return
		}
		_, _, _ = setWindowAttribute.Call(handle, dwmCloak, uintptr(unsafe.Pointer(&enabled)), unsafe.Sizeof(enabled))
		style, _, _ := getWindowStyle.Call(handle, extendedWindowStyle)
		_, _, _ = setWindowStyle.Call(handle, extendedWindowStyle, style|windowNoActivate)
		window.Show()
		_, _, _ = showNativeWindow.Call(handle, windows.SW_HIDE)
		_, _, _ = setWindowStyle.Call(handle, extendedWindowStyle, style)
		var disabled int32
		_, _, _ = setWindowAttribute.Call(handle, dwmCloak, uintptr(unsafe.Pointer(&disabled)), unsafe.Sizeof(disabled))
	})
}

// hideSurface 收起原生窗口并保留 WebView2 已渲染的页面
func hideSurface(window *application.WebviewWindow) {
	application.InvokeSync(func() {
		_, _, _ = showNativeWindow.Call(uintptr(window.NativeWindow()), windows.SW_HIDE)
	})
}

func platformOptions(service *app.Service) application.WindowsOptions {
	return application.WindowsOptions{
		DisableQuitOnLastWindowClosed: true,
		WebviewUserDataPath:           filepath.Join(service.DataDirectory(), "WebView2"),
		WndProcInterceptor: func(_ uintptr, message uint32, wParam, lParam uintptr) (uintptr, bool) {
			if message == 0x02b1 {
				if wParam == 7 {
					service.SetIdle(true)
				}
				if wParam == 8 {
					service.SetIdle(false)
				}
			}
			if message == 0x0218 && wParam == 0x8013 && lParam != 0 {
				var notification powerNotification
				err := windows.ReadProcessMemory(windows.CurrentProcess(), lParam, (*byte)(unsafe.Pointer(&notification)), unsafe.Sizeof(notification), nil)
				if err == nil && notification.Setting == displayState && notification.Length >= 4 {
					service.SetIdle(notification.Data == 0)
				}
			}
			return 0, false
		},
	}
}

func (host *Host) registerPlatformEvents() {
	handle := uintptr(host.main.NativeWindow())
	_, _, _ = registerSession.Call(handle, 0)
	_, _, _ = registerPower.Call(handle, uintptr(unsafe.Pointer(&displayState)), 0)
}

// windowBackground 保持原生窗口首帧与界面主题一致
func windowBackground(theme string) application.RGBA {
	dark := theme == "dark"
	if theme == "system" {
		key, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
		if err == nil {
			value, _, readErr := key.GetIntegerValue("AppsUseLightTheme")
			key.Close()
			dark = readErr == nil && value == 0
		}
	}
	if dark {
		return application.NewRGB(29, 29, 31)
	}
	return application.NewRGB(255, 255, 255)
}

func setLaunchAtLogin(enabled bool) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	if !enabled {
		err = key.DeleteValue("CCBar")
		if err == registry.ErrNotExist {
			return nil
		}
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	return key.SetStringValue("CCBar", `"`+executable+`" --background`)
}

func openDirectory(path string) error {
	file, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}

// OpenURL 用系统默认浏览器打开官方授权页面
func (host *Host) OpenURL(address string) error { return openDirectory(address) }
