package project

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPublishArtifactBlobCreatesAndReplaysContentAddressedFile(t *testing.T) {
	root := t.TempDir()
	layout, err := NewLayout(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, WorkspaceDirectory), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, WorkspaceDirectory, "result.txt"), []byte("verified result\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	first, err := PublishArtifactBlob(layout, "workspace/result.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created || first.AlreadyPresent || first.Size != 16 {
		t.Fatalf("unexpected first result: %+v", first)
	}
	second, err := PublishArtifactBlob(layout, "workspace/result.txt")
	if err != nil {
		t.Fatal(err)
	}
	if second.Created || !second.AlreadyPresent || second.ContentHash != first.ContentHash || second.RelativePath != first.RelativePath {
		t.Fatalf("unexpected replay: %+v", second)
	}
}

func TestPublishArtifactBlobRejectsSymlinkSource(t *testing.T) {
	root := t.TempDir()
	layout, err := NewLayout(root)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked.txt")
	if err := os.Symlink(outside, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlinks unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if _, err := PublishArtifactBlob(layout, "linked.txt"); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("expected unsafe path, got %v", err)
	}
}
