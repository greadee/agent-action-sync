package desktop

import (
	"path/filepath"
	"testing"
)

func TestRootsUnderSeparatesMutableState(t *testing.T) {
	roots, err := RootsUnder(t.TempDir())
	if err != nil {
		t.Fatalf("resolve roots: %v", err)
	}
	values := []string{roots.ConfigDir, roots.DataDir, roots.LogDir, roots.RuntimeCacheDir, roots.WorktreeRoot}
	seen := make(map[string]bool)
	for _, value := range values {
		if !filepath.IsAbs(value) {
			t.Fatalf("root is not absolute: %q", value)
		}
		if seen[value] {
			t.Fatalf("duplicate root: %q", value)
		}
		seen[value] = true
	}
	if filepath.Dir(roots.ConfigPath) != roots.ConfigDir {
		t.Fatalf("config path %q is outside %q", roots.ConfigPath, roots.ConfigDir)
	}
}

func TestWindowsRootsRequiresBothKnownFolders(t *testing.T) {
	if _, err := WindowsRoots("", t.TempDir()); err == nil {
		t.Fatal("expected missing roaming AppData to fail")
	}
	if _, err := WindowsRoots(t.TempDir(), ""); err == nil {
		t.Fatal("expected missing local AppData to fail")
	}
}

func TestValidateRootsRejectsNestedMutableDirectories(t *testing.T) {
	base := t.TempDir()
	roots := Roots{
		ConfigDir: filepath.Join(base, "config"), ConfigPath: filepath.Join(base, "config", "config.json"),
		DataDir: filepath.Join(base, "data"), LogDir: filepath.Join(base, "data", "logs"),
		RuntimeCacheDir: filepath.Join(base, "cache"), WorktreeRoot: filepath.Join(base, "worktrees"),
	}
	if _, err := validateRoots(roots); err == nil {
		t.Fatal("expected nested data and log roots to fail")
	}
}
