package transfer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCollectFolderFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatalf("write a: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "b.txt"), []byte("bb"), 0o600); err != nil {
		t.Fatalf("write b: %v", err)
	}

	files, err := CollectFolderFiles(root)
	if err != nil {
		t.Fatalf("CollectFolderFiles: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("len(files) = %d", len(files))
	}
	if files[0].RelativePath != "a.txt" {
		t.Fatalf("first relative path = %q", files[0].RelativePath)
	}
	if files[1].RelativePath != "nested/b.txt" {
		t.Fatalf("second relative path = %q", files[1].RelativePath)
	}
}

func TestCollectFolderFilesRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.txt")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := CollectFolderFiles(root); err == nil {
		t.Fatal("expected symlink to be rejected")
	}
}
