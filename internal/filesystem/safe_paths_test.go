package filesystem

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureAndInspectInsideShareRejectSymlinks(t *testing.T) {
	root := t.TempDir()
	resolved, err := EnsureParentDirectoriesInsideShare(root, "safe/nested/file.txt", 0o700)
	if err != nil {
		t.Fatalf("EnsureParentDirectoriesInsideShare: %v", err)
	}
	if err := os.WriteFile(resolved, []byte("safe"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	inspected, info, exists, err := InspectInsideShare(root, "safe/nested/file.txt")
	if err != nil || !exists || !info.Mode().IsRegular() || inspected != resolved {
		t.Fatalf("InspectInsideShare = %q, %+v, %t, %v", inspected, info, exists, err)
	}

	outside := t.TempDir()
	link := filepath.Join(root, "linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := EnsureParentDirectoriesInsideShare(root, "linked/escape.txt", 0o700); err == nil {
		t.Fatal("expected symlink parent rejection")
	}
	if _, _, _, err := InspectInsideShare(root, "linked/escape.txt"); err == nil {
		t.Fatal("expected symlink inspection rejection")
	}
}
