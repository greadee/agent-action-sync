package transfer

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestReceiveWriterCommitsVerifiedFile(t *testing.T) {
	root := t.TempDir()
	data := []byte("private sync payload")
	writer := newTestReceiveWriter(t, root, "nested/payload.txt", data)

	if _, err := writer.Write(data[:8]); err != nil {
		t.Fatalf("write first chunk: %v", err)
	}
	if _, err := writer.Write(data[8:]); err != nil {
		t.Fatalf("write second chunk: %v", err)
	}

	destination, err := writer.Commit()
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("destination = %q, want %q", got, data)
	}
	if _, err := os.Stat(writer.PartialPath()); !os.IsNotExist(err) {
		t.Fatalf("partial file should be gone after commit, err=%v", err)
	}
}

func TestReceiveWriterRejectsHashMismatch(t *testing.T) {
	root := t.TempDir()
	data := []byte("private sync payload")
	wrongHash := sha256.Sum256([]byte("different"))
	writer, err := NewReceiveWriter(ReceiveSpec{
		ShareRoot:     root,
		RelativePath:  "payload.txt",
		ExpectedSize:  int64(len(data)),
		ExpectedHash:  hex.EncodeToString(wrongHash[:]),
		HashAlgorithm: HashSHA256,
	})
	if err != nil {
		t.Fatalf("NewReceiveWriter: %v", err)
	}
	if _, err := writer.Write(data); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := writer.Commit(); err == nil {
		t.Fatal("expected commit to reject hash mismatch")
	}
	if _, err := os.Stat(writer.DestinationPath()); !os.IsNotExist(err) {
		t.Fatalf("destination should not exist after rejected commit, err=%v", err)
	}
}

func TestReceiveWriterRejectsSizeMismatch(t *testing.T) {
	root := t.TempDir()
	data := []byte("payload")
	writer := newTestReceiveWriter(t, root, "payload.txt", data)

	if _, err := writer.Write(data[:len(data)-1]); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := writer.Commit(); err == nil {
		t.Fatal("expected commit to reject size mismatch")
	}
	if _, err := os.Stat(writer.DestinationPath()); !os.IsNotExist(err) {
		t.Fatalf("destination should not exist after rejected commit, err=%v", err)
	}
}

func TestReceiveWriterRejectsExistingDestination(t *testing.T) {
	root := t.TempDir()
	data := []byte("payload")
	destination := filepath.Join(root, "payload.txt")
	if err := os.WriteFile(destination, []byte("existing"), 0o600); err != nil {
		t.Fatalf("write existing destination: %v", err)
	}
	writer := newTestReceiveWriter(t, root, "payload.txt", data)
	if _, err := writer.Write(data); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := writer.Commit(); err == nil {
		t.Fatal("expected commit to reject existing destination")
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("read existing destination: %v", err)
	}
	if string(got) != "existing" {
		t.Fatalf("existing destination was changed to %q", got)
	}
}

func TestReceiveWriterPreservesExistingDestinationWhenReplacementEnabled(t *testing.T) {
	root := t.TempDir()
	data := []byte("new")
	destination := filepath.Join(root, "payload.txt")
	if err := os.WriteFile(destination, []byte("existing"), 0o600); err != nil {
		t.Fatalf("write existing destination: %v", err)
	}
	hash := sha256.Sum256(data)
	writer, err := NewReceiveWriter(ReceiveSpec{
		ShareRoot:       root,
		RelativePath:    "payload.txt",
		ExpectedSize:    int64(len(data)),
		ExpectedHash:    hex.EncodeToString(hash[:]),
		HashAlgorithm:   HashSHA256,
		ReplaceExisting: true,
	})
	if err != nil {
		t.Fatalf("NewReceiveWriter: %v", err)
	}
	if _, err := writer.Write(data); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := writer.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("destination = %q", got)
	}
	historyEntries, err := filepath.Glob(filepath.Join(root, DefaultHistoryDir, "payload.txt.*"))
	if err != nil {
		t.Fatalf("glob history: %v", err)
	}
	if len(historyEntries) != 1 {
		t.Fatalf("history entries = %+v", historyEntries)
	}
	history, err := os.ReadFile(historyEntries[0])
	if err != nil {
		t.Fatalf("read history: %v", err)
	}
	if string(history) != "existing" {
		t.Fatalf("history = %q", history)
	}
}

func TestReceiveWriterRejectsPathTraversal(t *testing.T) {
	hash := sha256.Sum256([]byte("payload"))
	if _, err := NewReceiveWriter(ReceiveSpec{
		ShareRoot:     t.TempDir(),
		RelativePath:  "../payload.txt",
		ExpectedSize:  int64(len("payload")),
		ExpectedHash:  hex.EncodeToString(hash[:]),
		HashAlgorithm: HashSHA256,
	}); err == nil {
		t.Fatal("expected path traversal to be rejected")
	}
}

func newTestReceiveWriter(t *testing.T, root, relativePath string, data []byte) *ReceiveWriter {
	t.Helper()
	hash := sha256.Sum256(data)
	writer, err := NewReceiveWriter(ReceiveSpec{
		ShareRoot:     root,
		RelativePath:  relativePath,
		ExpectedSize:  int64(len(data)),
		ExpectedHash:  hex.EncodeToString(hash[:]),
		HashAlgorithm: HashSHA256,
	})
	if err != nil {
		t.Fatalf("NewReceiveWriter: %v", err)
	}
	t.Cleanup(func() {
		_ = writer.Close()
	})
	return writer
}
