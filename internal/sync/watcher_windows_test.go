//go:build windows

package sync

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPlatformWatcherReportsFileChanges(t *testing.T) {
	root := t.TempDir()
	watcher, err := NewPlatformWatcher(root)
	if err != nil {
		t.Fatalf("NewPlatformWatcher: %v", err)
	}
	defer watcher.Close()

	path := filepath.Join(root, "created.txt")
	if err := os.WriteFile(path, []byte("watch me"), 0o600); err != nil {
		t.Fatalf("write watched file: %v", err)
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case event, ok := <-watcher.Events():
			if !ok {
				t.Fatal("watcher closed before reporting file change")
			}
			if filepath.Clean(event.Path) == filepath.Clean(path) {
				return
			}
		case err := <-watcher.Errors():
			if err != nil {
				t.Fatalf("watcher error: %v", err)
			}
		case <-deadline:
			t.Fatal("timed out waiting for file change")
		}
	}
}
