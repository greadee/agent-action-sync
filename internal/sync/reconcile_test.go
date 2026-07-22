package sync

import (
	"strings"
	"testing"

	"syncgate/internal/core"
)

func TestReconcileScanClassifiesChanges(t *testing.T) {
	previous := []core.FileIndexEntry{
		fileEntry("deleted.txt", 10, "gone"),
		fileEntry("modified.txt", 10, "old"),
		fileEntry("same.txt", 5, "same"),
		{RelativePath: "dir", EntryType: core.EntryDirectory},
	}
	current := []core.FileIndexEntry{
		fileEntry("added.txt", 1, "new"),
		fileEntry("modified.txt", 10, "new"),
		fileEntry("same.txt", 5, "same"),
		{RelativePath: "dir", EntryType: core.EntryDirectory},
	}

	result, err := ReconcileScan(previous, current)
	if err != nil {
		t.Fatalf("ReconcileScan: %v", err)
	}

	got := changesByPath(result.Changes)
	assertKind(t, got, "added.txt", ChangeAdded)
	assertKind(t, got, "modified.txt", ChangeModified)
	assertKind(t, got, "deleted.txt", ChangeDeleted)
	assertKind(t, got, "same.txt", ChangeUnchanged)
	assertKind(t, got, "dir", ChangeUnchanged)
	if got["modified.txt"].Previous.ContentHash != "old" || got["modified.txt"].Current.ContentHash != "new" {
		t.Fatalf("modified change = %+v", got["modified.txt"])
	}
}

func TestReconcileScanTreatsPreviouslyDeletedPathAsUnchangedUntilRestored(t *testing.T) {
	previous := []core.FileIndexEntry{
		{RelativePath: "gone.txt", EntryType: core.EntryDeleted, IsDeleted: true},
		{RelativePath: "restored.txt", EntryType: core.EntryDeleted, IsDeleted: true},
	}
	current := []core.FileIndexEntry{
		fileEntry("restored.txt", 4, "back"),
	}

	result, err := ReconcileScan(previous, current)
	if err != nil {
		t.Fatalf("ReconcileScan: %v", err)
	}

	got := changesByPath(result.Changes)
	assertKind(t, got, "gone.txt", ChangeUnchanged)
	assertKind(t, got, "restored.txt", ChangeAdded)
}

func TestReconcileScanReturnsDeterministicOrderAndKindFilters(t *testing.T) {
	result, err := ReconcileScan(
		[]core.FileIndexEntry{fileEntry("z.txt", 1, "z")},
		[]core.FileIndexEntry{fileEntry("a.txt", 1, "a"), fileEntry("z.txt", 2, "zz")},
	)
	if err != nil {
		t.Fatalf("ReconcileScan: %v", err)
	}

	paths := make([]string, 0, len(result.Changes))
	for _, change := range result.Changes {
		paths = append(paths, change.RelativePath)
	}
	if strings.Join(paths, ",") != "a.txt,z.txt" {
		t.Fatalf("paths = %v", paths)
	}

	modified := result.ByKind(ChangeModified)
	if len(modified) != 1 || modified[0].RelativePath != "z.txt" {
		t.Fatalf("modified changes = %+v", modified)
	}
}

func TestReconcileScanRejectsDuplicatePaths(t *testing.T) {
	_, err := ReconcileScan(
		[]core.FileIndexEntry{fileEntry("same.txt", 1, "a"), fileEntry("same.txt", 2, "b")},
		nil,
	)
	if err == nil {
		t.Fatal("expected duplicate previous path to fail")
	}
	if !strings.Contains(err.Error(), "duplicate path same.txt") {
		t.Fatalf("error = %v", err)
	}
}

func fileEntry(path string, size int64, hash string) core.FileIndexEntry {
	return core.FileIndexEntry{
		RelativePath:  path,
		EntryType:     core.EntryFile,
		Size:          size,
		ContentHash:   hash,
		HashAlgorithm: "sha256",
	}
}

func changesByPath(changes []Change) map[string]Change {
	byPath := map[string]Change{}
	for _, change := range changes {
		byPath[change.RelativePath] = change
	}
	return byPath
}

func assertKind(t *testing.T, changes map[string]Change, path string, want ChangeKind) {
	t.Helper()
	change, ok := changes[path]
	if !ok {
		t.Fatalf("missing change for %s in %+v", path, changes)
	}
	if change.Kind != want {
		t.Fatalf("%s kind = %s, want %s", path, change.Kind, want)
	}
}
