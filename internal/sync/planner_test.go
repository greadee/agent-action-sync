package sync

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"syncgate/internal/core"
)

func TestScanPlannerPersistsSafeSnapshot(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "new.txt"), []byte("new"))
	store := &memoryFileIndexStore{entries: []core.FileIndexEntry{
		{ShareID: "share-1", RelativePath: "old.txt", EntryType: core.EntryFile, ContentHash: "old", HashAlgorithm: "sha256"},
	}}
	scannedAt := time.Unix(456, 0).UTC()

	plan, err := (ScanPlanner{Index: store}).Plan(context.Background(), PlanScanOptions{
		ScanOptions: ScanOptions{ShareID: "share-1", RootPath: root, Now: func() time.Time { return scannedAt }},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !plan.SnapshotPersisted || !store.saved {
		t.Fatal("expected snapshot to be persisted")
	}
	if !store.scannedAt.Equal(scannedAt) {
		t.Fatalf("saved timestamp = %s", store.scannedAt)
	}
	if len(plan.Reconciliation.ByKind(ChangeAdded)) != 1 || len(plan.Reconciliation.ByKind(ChangeDeleted)) != 1 {
		t.Fatalf("reconciliation = %+v", plan.Reconciliation.Changes)
	}
}

func TestScanPlannerDoesNotPersistBlockedDeletionSnapshot(t *testing.T) {
	root := t.TempDir()
	store := &memoryFileIndexStore{entries: []core.FileIndexEntry{
		{ShareID: "share-1", RelativePath: "one.txt", EntryType: core.EntryFile, ContentHash: "one", HashAlgorithm: "sha256"},
		{ShareID: "share-1", RelativePath: "two.txt", EntryType: core.EntryFile, ContentHash: "two", HashAlgorithm: "sha256"},
	}}

	plan, err := (ScanPlanner{Index: store}).Plan(context.Background(), PlanScanOptions{
		ScanOptions:   ScanOptions{ShareID: "share-1", RootPath: root},
		DeletionGuard: DeletionGuard{MaxCount: 1},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.DeletionDecision.Allowed || plan.SnapshotPersisted || store.saved {
		t.Fatalf("blocked plan = %+v", plan)
	}
}

func TestScanPlannerRequiresIndexAndShare(t *testing.T) {
	_, err := (ScanPlanner{}).Plan(context.Background(), PlanScanOptions{ScanOptions: ScanOptions{ShareID: "share-1"}})
	if err == nil {
		t.Fatal("expected index store error")
	}

	_, err = (ScanPlanner{Index: &memoryFileIndexStore{}}).Plan(context.Background(), PlanScanOptions{ScanOptions: ScanOptions{RootPath: t.TempDir()}})
	if err == nil {
		t.Fatal("expected share ID error")
	}
}

type memoryFileIndexStore struct {
	entries   []core.FileIndexEntry
	saved     bool
	scannedAt time.Time
}

func (store *memoryFileIndexStore) SaveSnapshot(_ context.Context, _ core.ShareID, entries []core.FileIndexEntry, scannedAt time.Time) error {
	store.saved = true
	store.scannedAt = scannedAt
	store.entries = append([]core.FileIndexEntry(nil), entries...)
	return nil
}

func (store *memoryFileIndexStore) Get(_ context.Context, _ core.ShareID, relativePath string) (core.FileIndexEntry, error) {
	for _, entry := range store.entries {
		if entry.RelativePath == relativePath {
			return entry, nil
		}
	}
	return core.FileIndexEntry{}, os.ErrNotExist
}

func (store *memoryFileIndexStore) List(_ context.Context, _ core.ShareID) ([]core.FileIndexEntry, error) {
	return append([]core.FileIndexEntry(nil), store.entries...), nil
}
