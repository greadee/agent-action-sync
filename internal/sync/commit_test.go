package sync

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"syncgate/internal/core"
	"syncgate/internal/storage"
)

func TestScanCommitServiceCommitsRevisionsAndTombstones(t *testing.T) {
	ctx := context.Background()
	shareID := core.ShareID("share-1")
	clock := time.Unix(500, 0).UTC()
	previous := []core.FileIndexEntry{
		serviceTestEntry(shareID, "gone.txt", "gone", "revision-gone-old"),
		serviceTestEntry(shareID, "keep.txt", "keep", "revision-keep"),
	}
	current := []core.FileIndexEntry{
		serviceTestEntry(shareID, "added.txt", "added", ""),
		serviceTestEntry(shareID, "keep.txt", "keep", ""),
	}
	store := &memoryAuthoritativeStateStore{entries: previous}
	service := ScanCommitService{
		Planner: ScanPlanner{
			Index: store,
			Scan: func(options ScanOptions) (ScanResult, error) {
				if options.ShareID != shareID {
					t.Fatalf("scan share ID = %s, want %s", options.ShareID, shareID)
				}
				return ScanResult{Entries: current, ScannedAt: clock}, nil
			},
		},
		Committer:      store,
		Now:            func() time.Time { return clock },
		NewRevisionID:  sequenceRevisionIDs("revision-added", "revision-gone-delete"),
		NewTombstoneID: sequenceTombstoneIDs("tombstone-gone"),
	}

	result, err := service.Run(ctx, ScanCommitOptions{
		Plan: PlanScanOptions{
			ScanOptions:   ScanOptions{ShareID: shareID, RootPath: t.TempDir()},
			DeletionGuard: DeletionGuard{MaxCount: 10, MaxPercent: 100},
		},
		OriginDeviceID:     "SOURCE-1",
		SequenceStart:      10,
		TombstoneRetention: 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.Committed || result.Blocked || !result.CommittedAt.Equal(clock) {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Revisions) != 2 {
		t.Fatalf("revisions = %+v", result.Revisions)
	}
	if result.Revisions[0].RelativePath != "added.txt" || result.Revisions[0].Sequence != 11 {
		t.Fatalf("added revision = %+v", result.Revisions[0])
	}
	if result.Revisions[1].RelativePath != "gone.txt" || !result.Revisions[1].IsDeleted || result.Revisions[1].ParentRevisionID != "revision-gone-old" {
		t.Fatalf("deletion revision = %+v", result.Revisions[1])
	}
	if len(result.Tombstones) != 1 || result.Tombstones[0].ID != "tombstone-gone" {
		t.Fatalf("tombstones = %+v", result.Tombstones)
	}
	if !result.Tombstones[0].ExpiresAt.Equal(clock.Add(24 * time.Hour)) {
		t.Fatalf("tombstone expiry = %s", result.Tombstones[0].ExpiresAt)
	}
	if store.commitCalls != 1 || len(store.lastRevisions) != 2 || len(store.lastRequests) != 1 {
		t.Fatalf("store commit state: calls=%d revisions=%d requests=%d", store.commitCalls, len(store.lastRevisions), len(store.lastRequests))
	}
}

func TestScanCommitServiceBlockedPlanDoesNotPersistOrGenerateIDs(t *testing.T) {
	ctx := context.Background()
	shareID := core.ShareID("share-1")
	store := &memoryAuthoritativeStateStore{entries: []core.FileIndexEntry{
		serviceTestEntry(shareID, "one.txt", "one", "revision-one"),
		serviceTestEntry(shareID, "two.txt", "two", "revision-two"),
	}}
	service := ScanCommitService{
		Planner: ScanPlanner{
			Index: store,
			Scan: func(ScanOptions) (ScanResult, error) {
				return ScanResult{Entries: nil, ScannedAt: time.Unix(100, 0).UTC()}, nil
			},
		},
		Committer: store,
		NewRevisionID: func() (core.RevisionID, error) {
			t.Fatal("revision ID generated for blocked scan")
			return "", nil
		},
		NewTombstoneID: func() (core.TombstoneID, error) {
			t.Fatal("tombstone ID generated for blocked scan")
			return "", nil
		},
	}

	result, err := service.Run(ctx, ScanCommitOptions{
		Plan: PlanScanOptions{
			ScanOptions:   ScanOptions{ShareID: shareID, RootPath: t.TempDir()},
			DeletionGuard: DeletionGuard{MaxCount: 1},
		},
		OriginDeviceID: "SOURCE-1",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.Blocked || result.Committed || len(result.Revisions) != 0 || len(result.Tombstones) != 0 {
		t.Fatalf("blocked result = %+v", result)
	}
	if store.commitCalls != 0 {
		t.Fatalf("commit calls = %d, want 0", store.commitCalls)
	}
}

func TestScanCommitServiceBuildOrCommitFailureDoesNotReportCommit(t *testing.T) {
	ctx := context.Background()
	shareID := core.ShareID("share-1")
	clock := time.Unix(500, 0).UTC()
	current := []core.FileIndexEntry{serviceTestEntry(shareID, "new.txt", "new", "")}
	newStore := func() *memoryAuthoritativeStateStore {
		return &memoryAuthoritativeStateStore{}
	}
	newService := func(store *memoryAuthoritativeStateStore, revisionID func() (core.RevisionID, error)) ScanCommitService {
		return ScanCommitService{
			Planner: ScanPlanner{
				Index: store,
				Scan: func(ScanOptions) (ScanResult, error) {
					return ScanResult{Entries: current, ScannedAt: clock}, nil
				},
			},
			Committer:     store,
			Now:           func() time.Time { return clock },
			NewRevisionID: revisionID,
		}
	}
	options := ScanCommitOptions{
		Plan:           PlanScanOptions{ScanOptions: ScanOptions{ShareID: shareID, RootPath: t.TempDir()}},
		OriginDeviceID: "SOURCE-1",
	}

	buildFailureStore := newStore()
	result, err := newService(buildFailureStore, func() (core.RevisionID, error) {
		return "", errors.New("id source unavailable")
	}).Run(ctx, options)
	if err == nil || result.Committed || buildFailureStore.commitCalls != 0 {
		t.Fatalf("build failure result=%+v err=%v calls=%d", result, err, buildFailureStore.commitCalls)
	}

	commitFailureStore := newStore()
	commitFailureStore.err = errors.New("database unavailable")
	result, err = newService(commitFailureStore, sequenceRevisionIDs("revision-new")).Run(ctx, options)
	if err == nil || result.Committed || commitFailureStore.commitCalls != 1 {
		t.Fatalf("commit failure result=%+v err=%v calls=%d", result, err, commitFailureStore.commitCalls)
	}
}

type memoryAuthoritativeStateStore struct {
	entries       []core.FileIndexEntry
	commitCalls   int
	lastRevisions []core.Revision
	lastRequests  []storage.TombstoneRequest
	err           error
}

func (store *memoryAuthoritativeStateStore) List(_ context.Context, _ core.ShareID) ([]core.FileIndexEntry, error) {
	return append([]core.FileIndexEntry(nil), store.entries...), nil
}

func (store *memoryAuthoritativeStateStore) CommitSnapshotAndTombstones(
	_ context.Context,
	_ core.ShareID,
	entries []core.FileIndexEntry,
	revisions []core.Revision,
	requests []storage.TombstoneRequest,
	_ time.Time,
) ([]storage.Tombstone, error) {
	store.commitCalls++
	store.lastRevisions = append([]core.Revision(nil), revisions...)
	store.lastRequests = append([]storage.TombstoneRequest(nil), requests...)
	if store.err != nil {
		return nil, store.err
	}
	store.entries = append([]core.FileIndexEntry(nil), entries...)
	byID := make(map[core.RevisionID]core.Revision, len(revisions))
	for _, revision := range revisions {
		byID[revision.ID] = revision
	}
	tombstones := make([]storage.Tombstone, 0, len(requests))
	for _, request := range requests {
		revision := byID[request.TombstoneRevisionID]
		tombstones = append(tombstones, storage.Tombstone{
			ID:                  request.ID,
			ShareID:             revision.ShareID,
			RelativePath:        revision.RelativePath,
			DeletedByDeviceID:   revision.OriginDeviceID,
			BaseRevisionID:      revision.ParentRevisionID,
			TombstoneRevisionID: revision.ID,
			DeletedAt:           revision.CreatedAt,
			ExpiresAt:           request.ExpiresAt,
		})
	}
	return tombstones, nil
}

func serviceTestEntry(shareID core.ShareID, path, hash string, revisionID core.RevisionID) core.FileIndexEntry {
	return core.FileIndexEntry{
		ShareID:           shareID,
		RelativePath:      filepath.ToSlash(path),
		EntryType:         core.EntryFile,
		Size:              int64(len(hash)),
		ContentHash:       hash,
		HashAlgorithm:     "sha256",
		CurrentRevisionID: revisionID,
	}
}

func sequenceRevisionIDs(ids ...core.RevisionID) func() (core.RevisionID, error) {
	index := 0
	return func() (core.RevisionID, error) {
		if index >= len(ids) {
			return "", errors.New("revision ID sequence exhausted")
		}
		id := ids[index]
		index++
		return id, nil
	}
}

func sequenceTombstoneIDs(ids ...core.TombstoneID) func() (core.TombstoneID, error) {
	index := 0
	return func() (core.TombstoneID, error) {
		if index >= len(ids) {
			return "", errors.New("tombstone ID sequence exhausted")
		}
		id := ids[index]
		index++
		return id, nil
	}
}
