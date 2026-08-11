//go:build !windows

package sync

import (
	"errors"
	"fmt"
)

var ErrPlatformWatcherUnavailable = errors.New("platform filesystem watcher unavailable")

func NewPlatformWatcher(root string) (Watcher, error) {
	if root == "" {
		return nil, fmt.Errorf("watcher root is required")
	}
	return nil, fmt.Errorf("%w: Windows ReadDirectoryChangesW is required", ErrPlatformWatcherUnavailable)
}
