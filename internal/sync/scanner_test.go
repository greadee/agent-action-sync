package sync

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"syncgate/internal/core"
)

func TestScanShareIndexesFilesAndDirectories(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), []byte("alpha"))
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o700); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	writeFile(t, filepath.Join(root, "nested", "b.txt"), []byte("beta"))

	scannedAt := time.Unix(123, 0).UTC()
	result, err := ScanShare(ScanOptions{
		ShareID:  "share-1",
		RootPath: root,
		Now: func() time.Time {
			return scannedAt
		},
	})
	if err != nil {
		t.Fatalf("ScanShare: %v", err)
	}

	entries := entriesByPath(result.Entries)
	if len(entries) != 3 {
		t.Fatalf("entry count = %d, entries=%+v", len(entries), result.Entries)
	}
	if entries["nested"].EntryType != core.EntryDirectory {
		t.Fatalf("nested entry = %+v", entries["nested"])
	}
	file := entries["a.txt"]
	if file.EntryType != core.EntryFile {
		t.Fatalf("file entry type = %q", file.EntryType)
	}
	wantHash := sha256.Sum256([]byte("alpha"))
	if file.ContentHash != hex.EncodeToString(wantHash[:]) {
		t.Fatalf("file hash = %q", file.ContentHash)
	}
	if !file.LastScannedAt.Equal(scannedAt) {
		t.Fatalf("last scanned at = %s", file.LastScannedAt)
	}
	if !result.ScannedAt.Equal(scannedAt) {
		t.Fatalf("scan timestamp = %s", result.ScannedAt)
	}
}

func TestScanShareIgnoresHistoryAndPartials(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "keep.txt"), []byte("keep"))
	writeFile(t, filepath.Join(root, "skip.sync-part"), []byte("partial"))
	if err := os.MkdirAll(filepath.Join(root, ".sync-history"), 0o700); err != nil {
		t.Fatalf("mkdir history: %v", err)
	}
	writeFile(t, filepath.Join(root, ".sync-history", "old.txt"), []byte("old"))

	result, err := ScanShare(ScanOptions{ShareID: "share-1", RootPath: root})
	if err != nil {
		t.Fatalf("ScanShare: %v", err)
	}
	entries := entriesByPath(result.Entries)
	if _, ok := entries["keep.txt"]; !ok {
		t.Fatal("expected keep.txt")
	}
	if _, ok := entries["skip.sync-part"]; ok {
		t.Fatal("partial file should be ignored")
	}
	if _, ok := entries[".sync-history"]; ok {
		t.Fatal("history directory should be ignored")
	}
}

func TestScanShareAppliesCustomIgnores(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "keep.txt"), []byte("keep"))
	writeFile(t, filepath.Join(root, "skip.log"), []byte("skip"))

	result, err := ScanShare(ScanOptions{
		ShareID:        "share-1",
		RootPath:       root,
		IgnorePatterns: []string{"*.log"},
	})
	if err != nil {
		t.Fatalf("ScanShare: %v", err)
	}
	entries := entriesByPath(result.Entries)
	if _, ok := entries["skip.log"]; ok {
		t.Fatal("custom ignored file should be absent")
	}
}

func TestScanShareRecordsSymlinkUnsupported(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.txt")
	writeFile(t, target, []byte("target"))
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	result, err := ScanShare(ScanOptions{ShareID: "share-1", RootPath: root})
	if err != nil {
		t.Fatalf("ScanShare: %v", err)
	}
	entries := entriesByPath(result.Entries)
	if entries["link.txt"].EntryType != core.EntrySymlinkUnsupported {
		t.Fatalf("link entry = %+v", entries["link.txt"])
	}
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func entriesByPath(entries []core.FileIndexEntry) map[string]core.FileIndexEntry {
	byPath := map[string]core.FileIndexEntry{}
	for _, entry := range entries {
		byPath[entry.RelativePath] = entry
	}
	return byPath
}
