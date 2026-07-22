package filesystem

import "testing"

func TestNormalizeRelativePath(t *testing.T) {
	tests := map[string]string{
		`docs/report.txt`:    "docs/report.txt",
		`docs\report.txt`:    "docs/report.txt",
		`docs/../report.txt`: "report.txt",
		`./docs/report.txt`:  "docs/report.txt",
		`docs//drafts/a.txt`: "docs/drafts/a.txt",
	}

	for input, want := range tests {
		got, err := NormalizeRelativePath(input)
		if err != nil {
			t.Fatalf("NormalizeRelativePath(%q): %v", input, err)
		}
		if got != want {
			t.Fatalf("NormalizeRelativePath(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNormalizeRelativePathRejectsUnsafePaths(t *testing.T) {
	tests := []string{
		"",
		"..",
		"../secret.txt",
		`C:\Users\owner\secret.txt`,
		`\\server\share\secret.txt`,
		"/etc/passwd",
		"report.txt:ads",
		"CON",
		"folder/NUL.txt",
		"folder/name.",
		` photos /image.jpg `,
	}

	for _, input := range tests {
		if _, err := NormalizeRelativePath(input); err == nil {
			t.Fatalf("expected %q to be rejected", input)
		}
	}
}

func TestResolveInsideShare(t *testing.T) {
	got, err := ResolveInsideShare(`C:\SyncGate\Drop`, `docs\report.txt`)
	if err != nil {
		t.Fatalf("ResolveInsideShare: %v", err)
	}
	if got == "" {
		t.Fatal("expected resolved path")
	}
}
