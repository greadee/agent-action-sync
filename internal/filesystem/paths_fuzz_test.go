package filesystem

import (
	"path/filepath"
	"strings"
	"testing"
)

func FuzzNormalizeRelativePath(f *testing.F) {
	for _, seed := range []string{"docs/report.txt", `docs\\report.txt`, "../secret", "/etc/passwd", "CON", "a/../../b", "\x00"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		normalized, err := NormalizeRelativePath(raw)
		if err != nil {
			return
		}
		if normalized == "" || looksAbsolute(normalized) || normalized == ".." || strings.HasPrefix(normalized, "../") {
			t.Fatalf("unsafe normalized path %q from %q", normalized, raw)
		}
		root := t.TempDir()
		resolved, err := ResolveInsideShare(root, normalized)
		if err != nil {
			t.Fatalf("accepted path did not resolve: %q: %v", normalized, err)
		}
		relative, err := filepath.Rel(filepath.Clean(root), resolved)
		if err == nil && (relative == ".." || strings.HasPrefix(filepath.ToSlash(relative), "../")) {
			t.Fatalf("resolved path escaped root: %q", resolved)
		}
	})
}
