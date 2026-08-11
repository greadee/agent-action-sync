//go:build windows

package sync

import (
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

const windowsWatcherBufferSize = 64 * 1024

// NewPlatformWatcher creates the native Windows directory watcher. It uses
// ReadDirectoryChangesW only as a hint source; callers must perform an
// authoritative scan after receiving an event.
func NewPlatformWatcher(root string) (Watcher, error) {
	if err := CheckShareRoot(root); err != nil {
		return nil, err
	}
	root = filepath.Clean(root)
	path, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return nil, fmt.Errorf("encode watcher root: %w", err)
	}

	handle, err := windows.CreateFile(
		path,
		windows.FILE_LIST_DIRECTORY,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OVERLAPPED,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open watcher root: %w", err)
	}
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("create watcher event: %w", err)
	}

	watcher := &windowsWatcher{
		root:   root,
		handle: handle,
		event:  event,
		events: make(chan WatchEvent, 128),
		errs:   make(chan error, 4),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	go watcher.loop()
	return watcher, nil
}

type windowsWatcher struct {
	root   string
	handle windows.Handle
	event  windows.Handle
	events chan WatchEvent
	errs   chan error
	stop   chan struct{}
	done   chan struct{}

	closeOnce sync.Once
	closed    atomic.Bool
}

func (watcher *windowsWatcher) Events() <-chan WatchEvent { return watcher.events }
func (watcher *windowsWatcher) Errors() <-chan error      { return watcher.errs }

func (watcher *windowsWatcher) Close() error {
	watcher.closeOnce.Do(func() {
		watcher.closed.Store(true)
		close(watcher.stop)
		_ = windows.CancelIoEx(watcher.handle, nil)
	})
	<-watcher.done
	return nil
}

func (watcher *windowsWatcher) loop() {
	defer close(watcher.done)
	defer windows.CloseHandle(watcher.event)
	defer windows.CloseHandle(watcher.handle)
	defer close(watcher.events)
	defer close(watcher.errs)

	buffer := make([]byte, windowsWatcherBufferSize)
	for {
		if watcher.closed.Load() {
			return
		}
		if err := windows.ResetEvent(watcher.event); err != nil {
			watcher.reportError(fmt.Errorf("reset watcher event: %w", err))
			return
		}
		overlapped := windows.Overlapped{HEvent: watcher.event}
		var bytesReturned uint32
		err := windows.ReadDirectoryChanges(
			watcher.handle,
			&buffer[0],
			uint32(len(buffer)),
			true,
			windows.FILE_NOTIFY_CHANGE_FILE_NAME|
				windows.FILE_NOTIFY_CHANGE_DIR_NAME|
				windows.FILE_NOTIFY_CHANGE_SIZE|
				windows.FILE_NOTIFY_CHANGE_LAST_WRITE|
				windows.FILE_NOTIFY_CHANGE_CREATION,
			&bytesReturned,
			&overlapped,
			0,
		)
		if err != nil && !errors.Is(err, windows.ERROR_IO_PENDING) {
			if watcher.closed.Load() || errors.Is(err, windows.ERROR_OPERATION_ABORTED) {
				return
			}
			if errors.Is(err, windows.ERROR_NOTIFY_ENUM_DIR) {
				watcher.reportError(ErrWatcherOverflow)
				continue
			}
			watcher.reportError(fmt.Errorf("read directory changes: %w", err))
			return
		}

		err = windows.GetOverlappedResult(watcher.handle, &overlapped, &bytesReturned, true)
		if err != nil {
			if watcher.closed.Load() || errors.Is(err, windows.ERROR_OPERATION_ABORTED) {
				return
			}
			watcher.reportError(fmt.Errorf("complete directory changes: %w", err))
			return
		}
		if bytesReturned == 0 {
			watcher.reportError(ErrWatcherOverflow)
			continue
		}
		if err := watcher.publishChanges(buffer[:bytesReturned]); err != nil {
			watcher.reportError(err)
			return
		}
	}
}

func (watcher *windowsWatcher) publishChanges(buffer []byte) error {
	for offset := uint32(0); offset < uint32(len(buffer)); {
		if uint32(len(buffer))-offset < 12 {
			return ErrWatcherOverflow
		}
		next := binary.LittleEndian.Uint32(buffer[offset:])
		nameLength := binary.LittleEndian.Uint32(buffer[offset+8:])
		nameStart := offset + 12
		nameEnd := nameStart + nameLength
		if nameLength%2 != 0 || nameEnd > uint32(len(buffer)) {
			return ErrWatcherOverflow
		}
		nameWords := make([]uint16, nameLength/2)
		for i := range nameWords {
			nameWords[i] = binary.LittleEndian.Uint16(buffer[nameStart+uint32(i*2):])
		}
		name := string(utf16.Decode(nameWords))
		select {
		case watcher.events <- WatchEvent{Path: filepath.Join(watcher.root, name)}:
		case <-watcher.stop:
			return nil
		}
		if next == 0 {
			return nil
		}
		if next < 12 || offset+next >= uint32(len(buffer)) && offset+next != uint32(len(buffer)) {
			return ErrWatcherOverflow
		}
		offset += next
	}
	return nil
}

func (watcher *windowsWatcher) reportError(err error) {
	select {
	case watcher.errs <- err:
	case <-watcher.stop:
	}
}
