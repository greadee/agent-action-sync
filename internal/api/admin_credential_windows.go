//go:build windows

package api

import (
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	adminCredentialTypeGeneric         = 1
	adminCredentialPersistLocalMachine = 2
)

var (
	adminAdvapi32        = windows.NewLazySystemDLL("advapi32.dll")
	adminProcCredReadW   = adminAdvapi32.NewProc("CredReadW")
	adminProcCredWriteW  = adminAdvapi32.NewProc("CredWriteW")
	adminProcCredDeleteW = adminAdvapi32.NewProc("CredDeleteW")
	adminProcCredFree    = adminAdvapi32.NewProc("CredFree")
)

type windowsAdminCredentialBackend struct{}

type windowsAdminCredential struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        windows.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         unsafe.Pointer
	TargetAlias        *uint16
	UserName           *uint16
}

func newAdminCredentialBackend() adminCredentialBackend { return windowsAdminCredentialBackend{} }

func (windowsAdminCredentialBackend) Read(target string) ([]byte, error) {
	targetPointer, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return nil, fmt.Errorf("encode administration credential target: %w", err)
	}
	var credential *windowsAdminCredential
	result, _, callErr := adminProcCredReadW.Call(
		uintptr(unsafe.Pointer(targetPointer)), adminCredentialTypeGeneric, 0,
		uintptr(unsafe.Pointer(&credential)),
	)
	if result == 0 {
		if errors.Is(callErr, windows.ERROR_NOT_FOUND) {
			return nil, ErrAdminCredentialNotFound
		}
		return nil, callErr
	}
	defer adminProcCredFree.Call(uintptr(unsafe.Pointer(credential)))
	if credential.CredentialBlobSize == 0 || credential.CredentialBlob == nil {
		return nil, errors.New("administration credential contains no secret")
	}
	return append([]byte(nil), unsafe.Slice(credential.CredentialBlob, credential.CredentialBlobSize)...), nil
}

func (windowsAdminCredentialBackend) Write(target, username string, secret []byte) error {
	if len(secret) == 0 {
		return errors.New("administration credential secret is required")
	}
	targetPointer, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	usernamePointer, err := windows.UTF16PtrFromString(username)
	if err != nil {
		return err
	}
	credential := windowsAdminCredential{
		Type: adminCredentialTypeGeneric, TargetName: targetPointer,
		CredentialBlobSize: uint32(len(secret)), CredentialBlob: &secret[0],
		Persist: adminCredentialPersistLocalMachine, UserName: usernamePointer,
	}
	result, _, callErr := adminProcCredWriteW.Call(uintptr(unsafe.Pointer(&credential)), 0)
	runtime.KeepAlive(secret)
	runtime.KeepAlive(targetPointer)
	runtime.KeepAlive(usernamePointer)
	if result == 0 {
		return callErr
	}
	return nil
}

func (windowsAdminCredentialBackend) Delete(target string) error {
	targetPointer, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	result, _, callErr := adminProcCredDeleteW.Call(
		uintptr(unsafe.Pointer(targetPointer)), adminCredentialTypeGeneric, 0,
	)
	if result == 0 {
		if errors.Is(callErr, windows.ERROR_NOT_FOUND) {
			return ErrAdminCredentialNotFound
		}
		return callErr
	}
	return nil
}
