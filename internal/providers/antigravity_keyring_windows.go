//go:build windows

package providers

import (
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	antigravityKeyringTarget = "gemini:antigravity"
	antigravityKeyringUser   = "antigravity"
	antigravityKeyringSource = "keyring:gemini:antigravity"
	credTypeGeneric          = 1
	credPersistLocalMachine  = 2
	maxWinCredentialBlob     = 2560
	errWinElementNotFound    = windows.Errno(1168)
)

var (
	modAdvapi32   = windows.NewLazySystemDLL("advapi32.dll")
	procCredRead  = modAdvapi32.NewProc("CredReadW")
	procCredWrite = modAdvapi32.NewProc("CredWriteW")
	procCredFree  = modAdvapi32.NewProc("CredFree")
)

type winCredential struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        windows.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

var (
	readAntigravityKeyring    = readWindowsAntigravityKeyring
	writeAntigravityKeyring   = writeWindowsAntigravityKeyring
	findAntigravityInstallDir = readWindowsAntigravityInstallDir
)

// readWindowsAntigravityKeyring 从 Windows 凭据管理器读取官方 Antigravity 令牌原文
func readWindowsAntigravityKeyring() ([]byte, error) {
	target, err := windows.UTF16PtrFromString(antigravityKeyringTarget)
	if err != nil {
		return nil, err
	}
	var cred *winCredential
	ok, _, callErr := procCredRead.Call(
		uintptr(unsafe.Pointer(target)),
		credTypeGeneric,
		0,
		uintptr(unsafe.Pointer(&cred)),
	)
	runtime.KeepAlive(target)
	if ok == 0 {
		if errors.Is(callErr, errWinElementNotFound) {
			return nil, nil
		}
		return nil, callErr
	}
	defer procCredFree.Call(uintptr(unsafe.Pointer(cred)))
	if cred == nil || cred.CredentialBlob == nil || cred.CredentialBlobSize == 0 {
		return nil, nil
	}
	return append([]byte(nil), unsafe.Slice(cred.CredentialBlob, cred.CredentialBlobSize)...), nil
}

// writeWindowsAntigravityKeyring 保留原有属性并将续期后的官方 JSON 写回 Windows 凭据管理器
func writeWindowsAntigravityKeyring(data []byte) error {
	if len(data) > maxWinCredentialBlob {
		return errors.New("Antigravity keyring credential exceeds Windows limit")
	}
	target, err := windows.UTF16PtrFromString(antigravityKeyringTarget)
	if err != nil {
		return err
	}
	persist := uint32(credPersistLocalMachine)
	userName := antigravityKeyringUser
	comment := ""
	alias := ""
	var existing *winCredential
	ok, _, _ := procCredRead.Call(
		uintptr(unsafe.Pointer(target)),
		credTypeGeneric,
		0,
		uintptr(unsafe.Pointer(&existing)),
	)
	if ok != 0 && existing != nil {
		if existing.Persist != 0 {
			persist = existing.Persist
		}
		if existing.UserName != nil {
			if value := windows.UTF16PtrToString(existing.UserName); value != "" {
				userName = value
			}
		}
		if existing.Comment != nil {
			comment = windows.UTF16PtrToString(existing.Comment)
		}
		if existing.TargetAlias != nil {
			alias = windows.UTF16PtrToString(existing.TargetAlias)
		}
		procCredFree.Call(uintptr(unsafe.Pointer(existing)))
	}
	userPtr, err := windows.UTF16PtrFromString(userName)
	if err != nil {
		return err
	}
	var commentPtr *uint16
	if comment != "" {
		if commentPtr, err = windows.UTF16PtrFromString(comment); err != nil {
			return err
		}
	}
	var aliasPtr *uint16
	if alias != "" {
		if aliasPtr, err = windows.UTF16PtrFromString(alias); err != nil {
			return err
		}
	}
	blob := append([]byte(nil), data...)
	cred := winCredential{
		Type:               credTypeGeneric,
		TargetName:         target,
		Comment:            commentPtr,
		CredentialBlobSize: uint32(len(blob)),
		Persist:            persist,
		TargetAlias:        aliasPtr,
		UserName:           userPtr,
	}
	if len(blob) > 0 {
		cred.CredentialBlob = &blob[0]
	}
	wrote, _, writeErr := procCredWrite.Call(uintptr(unsafe.Pointer(&cred)), 0)
	runtime.KeepAlive(target)
	runtime.KeepAlive(userPtr)
	runtime.KeepAlive(commentPtr)
	runtime.KeepAlive(aliasPtr)
	runtime.KeepAlive(blob)
	runtime.KeepAlive(&cred)
	if wrote == 0 {
		return writeErr
	}
	return nil
}

// readWindowsAntigravityInstallDir 从协议注册表定位官方 Antigravity 桌面客户端安装目录
func readWindowsAntigravityInstallDir() string {
	key, err := registry.OpenKey(registry.CLASSES_ROOT, `antigravity\shell\open\command`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer key.Close()
	command, _, err := key.GetStringValue("")
	if err != nil {
		return ""
	}
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}
	executable := ""
	if strings.HasPrefix(command, `"`) {
		if end := strings.Index(command[1:], `"`); end > 0 {
			executable = command[1 : 1+end]
		}
	} else {
		executable = strings.Fields(command)[0]
	}
	if executable == "" {
		return ""
	}
	return filepath.Dir(executable)
}
