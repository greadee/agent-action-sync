package sync

import (
	"testing"

	"syncgate/internal/filesystem"
)

func FuzzMatchIgnorePattern(f *testing.F) {
	for _, seed := range [][2]string{{"notes.txt", "*.tmp"}, {"build/output.bin", "build/**"}, {".sync-history/a", ".sync-history/**"}, {"docs/report.txt", "**/*.txt"}} {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, relativePath, pattern string) {
		normalized, err := filesystem.NormalizeRelativePath(relativePath)
		if err != nil {
			return
		}
		_ = matchIgnorePattern(normalized, pattern)
		_ = ignored(normalized, []string{pattern})
	})
}
