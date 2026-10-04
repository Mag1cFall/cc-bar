package desktop

import (
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/w32"
	"golang.org/x/sys/windows"
)

var messageBox = user32.NewProc("MessageBoxW")

// ReportStartupError 在网页初始化失败时显示原生错误
func ReportStartupError(err error) {
	text := "CCBar 启动失败\n\n" + err.Error() + "\n\n日志：%LOCALAPPDATA%\\CCBar\\Logs\\ccbar.log"
	message, _ := windows.UTF16PtrFromString(strings.ReplaceAll(text, "\x00", ""))
	title, _ := windows.UTF16PtrFromString("CCBar")
	_, _, _ = messageBox.Call(0, uintptr(unsafe.Pointer(message)), uintptr(unsafe.Pointer(title)), 0x10|0x10000)
}

// ShowStartupProgress 在首次释放运行库时立即显示原生准备窗口
func ShowStartupProgress() func() {
	created := make(chan w32.HWND, 1)
	finished := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(finished)
		instance := w32.GetModuleHandle("")
		class := w32.MustStringToUTF16Ptr("CCBarStartup")
		callback := syscall.NewCallback(func(handle w32.HWND, message uint32, first w32.WPARAM, second w32.LPARAM) w32.LRESULT {
			if message == w32.WM_DESTROY {
				w32.PostQuitMessage(0)
				return 0
			}
			return w32.DefWindowProc(handle, message, first, second)
		})
		definition := w32.WNDCLASSEX{
			Size: uint32(unsafe.Sizeof(w32.WNDCLASSEX{})), WndProc: callback,
			Instance: instance, Background: w32.HBRUSH(w32.COLOR_WINDOW + 1), ClassName: class,
		}
		w32.RegisterClassEx(&definition)
		width, height := 460, 150
		left := (w32.GetSystemMetrics(w32.SM_CXSCREEN) - width) / 2
		top := (w32.GetSystemMetrics(w32.SM_CYSCREEN) - height) / 2
		handle := w32.CreateWindowEx(0, class, w32.MustStringToUTF16Ptr("CCBar"),
			w32.WS_CAPTION|w32.WS_BORDER|w32.WS_VISIBLE, left, top, width, height, 0, 0, instance, nil)
		if handle == 0 {
			created <- 0
			return
		}
		label := w32.CreateWindowEx(0, w32.MustStringToUTF16Ptr("STATIC"),
			w32.MustStringToUTF16Ptr("正在准备界面…\n首次启动需要释放运行库，完成后自动打开。"),
			w32.WS_CHILD|w32.WS_VISIBLE, 24, 28, 410, 64, handle, 0, instance, nil)
		w32.SendMessage(label, w32.WM_SETFONT, uintptr(w32.GetStockObject(w32.DEFAULT_GUI_FONT)), 1)
		created <- handle
		var message w32.MSG
		for w32.GetMessage(&message, 0, 0, 0) > 0 {
			w32.TranslateMessage(&message)
			w32.DispatchMessage(&message)
		}
	}()
	handle := <-created
	return sync.OnceFunc(func() {
		if handle != 0 {
			w32.PostMessage(handle, w32.WM_CLOSE, 0, 0)
		}
		<-finished
	})
}
