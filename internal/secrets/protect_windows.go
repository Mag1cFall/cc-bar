//go:build windows

package secrets

import (
	"encoding/base64"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Protect 使用当前用户 DPAPI 保存原有兼容格式
func Protect(value string) (string, error) {
	data, err := ProtectBytes([]byte(value))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

// Unprotect 读取当前用户 DPAPI 的原有令牌
func Unprotect(value string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return "", err
	}
	plain, err := UnprotectBytes(data)
	return string(plain), err
}

// ProtectBytes 加密本机凭据字节
func ProtectBytes(data []byte) ([]byte, error) {
	input := windows.DataBlob{Size: uint32(len(data))}
	if len(data) != 0 {
		input.Data = &data[0]
	}
	var output windows.DataBlob
	if err := windows.CryptProtectData(&input, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(uintptr(unsafe.Pointer(output.Data))))
	return append([]byte(nil), unsafe.Slice(output.Data, output.Size)...), nil
}

// UnprotectBytes 解密 Electron 与已有 Windows 凭据
func UnprotectBytes(data []byte) ([]byte, error) {
	input := windows.DataBlob{Size: uint32(len(data))}
	if len(data) != 0 {
		input.Data = &data[0]
	}
	var output windows.DataBlob
	if err := windows.CryptUnprotectData(&input, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(uintptr(unsafe.Pointer(output.Data))))
	return append([]byte(nil), unsafe.Slice(output.Data, output.Size)...), nil
}
