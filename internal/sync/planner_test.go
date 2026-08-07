package sync

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"syncgate/internal/core"
)

func TestScanPlannerDoesNotPersistBareSnapshot(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "new.txt"), []byte("new"))
	store := &memoryFileIndexReader{entries: []core.FileIndexEntry{
		{ShareID: "share-1", RelativePath: "old.txt", EntryType: core.EntryFile, ContentHash: "old", HashAlgorithm: "sha256"},
	}}
	scannedAt := time.Unix(456, 0).UTC()

	plan, err := (ScanPlanner{Index: store}).Plan(context.Background(), PlanScanOptions{
		ScanOptions: ScanOptions{ShareID: "share-1", RootPath: root, Now: func() time.Time { return scannedAt }},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(store.entries) != 1 || store.entries[0].RelativePath != "old.txt" {
		t.Fatalf("planner mutated persisted entries: %+v", store.entries)
	}
	if len(plan.Reconciliation.ByKind(ChangeAdded)) != 1 || len(plan.Reconciliation.ByKind(ChangeDeleted)) != 1 {
		t.Fatalf("reconciliation = %+v", plan.Reconciliation.Changes)
	}
}

func TestScanPlannerDoesNotPersistBlockedDeletionSnapshot(t *testing.T) {
	root := t.TempDir()
	store := &memoryFileIndexReader{entries: []core.FileIndexEntry{
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
	if plan.DeletionDecision.Allowed {
		t.Fatalf("blocked plan = %+v", plan)
	}
}

func TestScanPlannerRequiresIndexAndShare(t *testing.T) {
	_, err := (ScanPlanner{}).Plan(context.Background(), PlanScanOptions{ScanOptions: ScanOptions{ShareID: "share-1"}})
	if err == nil {
		t.Fatal("expected index store error")
	}

	_, err = (ScanPlanner{Index: &memoryFileIndexReader{}}).Plan(context.Background(), PlanScanOptions{ScanOptions: ScanOptions{RootPath: t.TempDir()}})
	if err == nil {
		t.Fatal("expected share ID error")
	}
}

type memoryFileIndexReader struct {
	entries []core.FileIndexEntry
}

func (store *memoryFileIndexReader) List(_ context.Context, _ core.ShareID) ([]core.FileIndexEntry, error) {
	return append([]core.FileIndexEntry(nil), store.entries...), nil
}
