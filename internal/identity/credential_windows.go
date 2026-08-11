//go:build windows

package identity

import (
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	credentialTypeGeneric         = 1
	credentialPersistLocalMachine = 2
)

var (
	advapi32        = windows.NewLazySystemDLL("advapi32.dll")
	procCredReadW   = advapi32.NewProc("CredReadW")
	procCredWriteW  = advapi32.NewProc("CredWriteW")
	procCredDeleteW = advapi32.NewProc("CredDeleteW")
	procCredFree    = advapi32.NewProc("CredFree")
)

type windowsCredentialBackend struct{}

type windowsCredential struct {
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

func newPlatformCredentialBackend() credentialBackend { return windowsCredentialBackend{} }

func (windowsCredentialBackend) Read(target string) ([]byte, error) {
	targetPointer, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return nil, fmt.Errorf("encode credential target: %w", err)
	}
	var credential *windowsCredential
	result, _, callErr := procCredReadW.Call(
		uintptr(unsafe.Pointer(targetPointer)),
		credentialTypeGeneric,
		0,
		uintptr(unsafe.Pointer(&credential)),
	)
	if result == 0 {
		if errors.Is(callErr, windows.ERROR_NOT_FOUND) {
			return nil, ErrIdentityNotFound
		}
		return nil, callErr
	}
	defer procCredFree.Call(uintptr(unsafe.Pointer(credential)))
	if credential.CredentialBlobSize == 0 || credential.CredentialBlob == nil {
		return nil, errors.New("credential contains no private key")
	}
	secret := append([]byte(nil), unsafe.Slice(credential.CredentialBlob, credential.CredentialBlobSize)...)
	return secret, nil
}

func (windowsCredentialBackend) Write(target, username string, secret []byte) error {
	if len(secret) == 0 {
		return errors.New("credential secret is required")
	}
	targetPointer, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return fmt.Errorf("encode credential target: %w", err)
	}
	usernamePointer, err := windows.UTF16PtrFromString(username)
	if err != nil {
		return fmt.Errorf("encode credential username: %w", err)
	}
	credential := windowsCredential{
		Type:               credentialTypeGeneric,
		TargetName:         targetPointer,
		CredentialBlobSize: uint32(len(secret)),
		CredentialBlob:     &secret[0],
		Persist:            credentialPersistLocalMachine,
		UserName:           usernamePointer,
	}
	result, _, callErr := procCredWriteW.Call(uintptr(unsafe.Pointer(&credential)), 0)
	runtime.KeepAlive(secret)
	runtime.KeepAlive(targetPointer)
	runtime.KeepAlive(usernamePointer)
	if result == 0 {
		return callErr
	}
	return nil
}

func (windowsCredentialBackend) Delete(target string) error {
	targetPointer, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return fmt.Errorf("encode credential target: %w", err)
	}
	result, _, callErr := procCredDeleteW.Call(
		uintptr(unsafe.Pointer(targetPointer)),
		credentialTypeGeneric,
		0,
	)
	if result == 0 {
		if errors.Is(callErr, windows.ERROR_NOT_FOUND) {
			return ErrIdentityNotFound
		}
		return callErr
	}
	return nil
}
