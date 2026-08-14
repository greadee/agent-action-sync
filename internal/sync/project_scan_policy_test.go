package sync_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"syncgate/internal/core"
	"syncgate/internal/project"
	syncengine "syncgate/internal/sync"
)

func TestProjectScanPolicyKeepsPortableControlRecordsAndExplainsLocalExclusions(t *testing.T) {
	root := t.TempDir()
	for relativePath, content := range map[string]string{
		"workspace/source.txt":                   "source",
		".agent-project/manifest.json":           "manifest",
		".agent-project/local/device-state.json": "local",
		".git/config":                            "git",
	} {
		path := filepath.Join(root, filepath.FromSlash(relativePath))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	policy := project.NewProjectScanPolicy(nil)
	result, err := syncengine.ScanShare(syncengine.ScanOptions{
		ShareID:        core.ShareID("share-1"),
		RootPath:       root,
		IgnorePatterns: policy.EffectiveIgnorePatterns,
		Now:            func() time.Time { return time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	entries := make(map[string]bool, len(result.Entries))
	for _, entry := range result.Entries {
		entries[entry.RelativePath] = true
	}
	if !entries[".agent-project/manifest.json"] || !entries["workspace/source.txt"] {
		t.Fatalf("portable project records were not scanned: %#v", entries)
	}
	if entries[".agent-project/local/device-state.json"] || entries[".git/config"] {
		t.Fatalf("local state or git metadata was scanned: %#v", entries)
	}
	diagnostics := syncengine.ExplainIgnoredPaths(core.ShareID("share-1"), []string{
		".agent-project/manifest.json", ".agent-project/local/device-state.json", ".git/config",
	}, policy.EffectiveIgnorePatterns)
	if len(diagnostics) != 2 || diagnostics[0].Pattern != ".agent-project/local/**" || diagnostics[1].Pattern != ".git/**" {
		t.Fatalf("ignore diagnostics = %#v", diagnostics)
	}
}
