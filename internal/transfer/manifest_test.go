package transfer

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBuildFileManifest(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "payload.bin")
	data := []byte("abcdefghijklmnop")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}

	manifest, err := BuildFileManifest(path, "payload.bin", 5)
	if err != nil {
		t.Fatalf("BuildFileManifest: %v", err)
	}

	wantHash := sha256.Sum256(data)
	if manifest.ContentHash != hex.EncodeToString(wantHash[:]) {
		t.Fatalf("content hash = %q", manifest.ContentHash)
	}
	if manifest.Size != int64(len(data)) {
		t.Fatalf("size = %d", manifest.Size)
	}
	if len(manifest.Chunks) != 4 {
		t.Fatalf("chunk count = %d", len(manifest.Chunks))
	}
	if manifest.Chunks[3].Offset != 15 || manifest.Chunks[3].Size != 1 {
		t.Fatalf("last chunk = %+v", manifest.Chunks[3])
	}
}

func TestBuildFileManifestHandlesEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.bin")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}

	manifest, err := BuildFileManifest(path, "empty.bin", DefaultChunkSize)
	if err != nil {
		t.Fatalf("BuildFileManifest: %v", err)
	}
	if manifest.Size != 0 {
		t.Fatalf("size = %d", manifest.Size)
	}
	if len(manifest.Chunks) != 1 || manifest.Chunks[0].Size != 0 {
		t.Fatalf("chunks = %+v", manifest.Chunks)
	}
}

func TestBuildFileManifestRejectsDirectory(t *testing.T) {
	if _, err := BuildFileManifest(t.TempDir(), "dir", DefaultChunkSize); err == nil {
		t.Fatal("expected directory to be rejected")
	}
}

func TestSourceChanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "payload.bin")
	if err := os.WriteFile(path, []byte("first"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	initial, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat source: %v", err)
	}
	later := initial.ModTime().Add(time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	final, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat source again: %v", err)
	}
	if !sourceChanged(initial, final) {
		t.Fatal("expected modified time change to be detected")
	}
}
