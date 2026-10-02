package app

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var getLocaleName = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetUserDefaultLocaleName")

// systemLocale 读取 Windows 当前用户的语言
func systemLocale() string {
	var name [85]uint16
	length, _, _ := getLocaleName.Call(uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)))
	if length == 0 {
		return "zh-CN"
	}
	return windows.UTF16ToString(name[:])
}
