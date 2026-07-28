package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"syncgate/internal/core"
	"syncgate/internal/storage"
)

func TestStorePersistsDevicesSharesAndPermissions(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	device := storage.Device{
		ID:          "DEVICE-1",
		DisplayName: "Laptop",
		PublicKey:   []byte("public"),
		Fingerprint: "ABCD",
		TrustState:  storage.TrustTrusted,
	}
	if err := store.Devices().TrustDevice(ctx, device); err != nil {
		t.Fatalf("TrustDevice: %v", err)
	}
	got, err := store.Devices().GetDevice(ctx, device.ID)
	if err != nil {
		t.Fatalf("GetDevice: %v", err)
	}
	if got.DisplayName != device.DisplayName {
		t.Fatalf("display name = %q, want %q", got.DisplayName, device.DisplayName)
	}

	share := storage.Share{
		ID:       "share-1",
		Name:     "Drop",
		RootPath: t.TempDir(),
		Mode:     storage.ShareUploadOnly,
	}
	if err := store.Shares().SaveShare(ctx, share); err != nil {
		t.Fatalf("SaveShare: %v", err)
	}
	if err := store.Shares().SetPermission(ctx, core.SharePermission{
		ShareID:  share.ID,
		DeviceID: device.ID,
		Capabilities: map[core.Capability]bool{
			core.CapabilityUpload: true,
		},
		LANOnly: true,
	}); err != nil {
		t.Fatalf("SetPermission: %v", err)
	}
	if err := store.Shares().Authorize(ctx, device.ID, share.ID, core.CapabilityUpload, false); err != nil {
		t.Fatalf("Authorize upload: %v", err)
	}
	if err := store.Shares().Authorize(ctx, device.ID, share.ID, core.CapabilityRead, false); err == nil {
		t.Fatal("expected read authorization to fail")
	}
	if err := store.Shares().Authorize(ctx, device.ID, share.ID, core.CapabilityUpload, true); err == nil {
		t.Fatal("expected remote access to LAN-only share to fail")
	}
}

func TestStorePersistsTransfersAndVerifiedChunks(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	device := storage.Device{ID: "DEVICE-1", DisplayName: "Laptop", PublicKey: []byte("public"), Fingerprint: "ABCD", TrustState: storage.TrustTrusted}
	if err := store.Devices().TrustDevice(ctx, device); err != nil {
		t.Fatalf("TrustDevice: %v", err)
	}

	transfer := core.Transfer{
		ID:            "transfer-1",
		Direction:     core.TransferReceive,
		PeerDeviceID:  device.ID,
		RelativePath:  "payload.bin",
		State:         core.TransferTransferring,
		Size:          16,
		ChunkSize:     8,
		ContentHash:   "hash",
		HashAlgorithm: "sha256",
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	if err := store.Transfers().SaveTransfer(ctx, transfer); err != nil {
		t.Fatalf("SaveTransfer: %v", err)
	}
	if err := store.Transfers().SaveChunk(ctx, core.TransferChunk{
		TransferID: transfer.ID,
		Index:      0,
		Offset:     0,
		Size:       8,
		Hash:       "chunk-0",
		State:      core.ChunkVerified,
		VerifiedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SaveChunk verified: %v", err)
	}
	if err := store.Transfers().SaveChunk(ctx, core.TransferChunk{
		TransferID: transfer.ID,
		Index:      1,
		Offset:     8,
		Size:       8,
		Hash:       "chunk-1",
		State:      core.ChunkPending,
	}); err != nil {
		t.Fatalf("SaveChunk pending: %v", err)
	}

	got, err := store.Transfers().GetTransfer(ctx, transfer.ID)
	if err != nil {
		t.Fatalf("GetTransfer: %v", err)
	}
	if got.State != core.TransferTransferring {
		t.Fatalf("transfer state = %q", got.State)
	}
	chunks, err := store.Transfers().VerifiedChunks(ctx, transfer.ID)
	if err != nil {
		t.Fatalf("VerifiedChunks: %v", err)
	}
	if len(chunks) != 1 || chunks[0].Index != 0 {
		t.Fatalf("verified chunks = %+v", chunks)
	}
}

func TestStorePersistsRevisionsAndAuditEvents(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	share := storage.Share{ID: "share-1", Name: "Drop", RootPath: t.TempDir(), Mode: storage.ShareUploadOnly}
	if err := store.Shares().SaveShare(ctx, share); err != nil {
		t.Fatalf("SaveShare: %v", err)
	}

	revision := core.Revision{
		ID:             "revision-1",
		ShareID:        share.ID,
		RelativePath:   "payload.bin",
		EntryType:      core.EntryFile,
		Size:           12,
		ContentHash:    "hash",
		HashAlgorithm:  "sha256",
		OriginDeviceID: "DEVICE-1",
		Sequence:       1,
		CreatedAt:      time.Now().UTC(),
	}
	if err := store.Revisions().RecordRevision(ctx, revision); err != nil {
		t.Fatalf("RecordRevision: %v", err)
	}
	got, err := store.Revisions().GetRevision(ctx, revision.ID)
	if err != nil {
		t.Fatalf("GetRevision: %v", err)
	}
	if got.RelativePath != revision.RelativePath {
		t.Fatalf("relative path = %q", got.RelativePath)
	}

	if err := store.Audit().Record(ctx, storage.AuditEvent{
		ID:         "audit-1",
		EventName:  "transfer.completed",
		ShareID:    share.ID,
		Severity:   "info",
		Metadata:   map[string]string{"transfer_id": "transfer-1"},
		OccurredAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Record audit event: %v", err)
	}
}

func TestStorePersistsFileIndexSnapshots(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	share := storage.Share{ID: "share-1", Name: "Drop", RootPath: t.TempDir(), Mode: storage.ShareOneWaySource}
	if err := store.Shares().SaveShare(ctx, share); err != nil {
		t.Fatalf("SaveShare: %v", err)
	}

	scannedAt := time.Unix(100, 0).UTC()
	entries := []core.FileIndexEntry{
		{
			ShareID:       share.ID,
			RelativePath:  "docs",
			EntryType:     core.EntryDirectory,
			LastScannedAt: scannedAt,
		},
		{
			ShareID:       share.ID,
			RelativePath:  "docs/readme.txt",
			EntryType:     core.EntryFile,
			Size:          12,
			ModifiedTime:  time.Unix(90, 0).UTC(),
			FileIdentity:  "file-id",
			ContentHash:   "hash-a",
			HashAlgorithm: "sha256",
			LastScannedAt: scannedAt,
		},
	}
	if err := store.FileIndex().SaveSnapshot(ctx, share.ID, entries, scannedAt); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	got, err := store.FileIndex().Get(ctx, share.ID, "docs/readme.txt")
	if err != nil {
		t.Fatalf("Get file index entry: %v", err)
	}
	if got.ContentHash != "hash-a" || got.IsDeleted {
		t.Fatalf("file index entry = %+v", got)
	}
	if !got.LastScannedAt.Equal(scannedAt) {
		t.Fatalf("last scanned at = %s, want %s", got.LastScannedAt, scannedAt)
	}

	listed, err := store.FileIndex().List(ctx, share.ID)
	if err != nil {
		t.Fatalf("List file index: %v", err)
	}
	if len(listed) != 2 || listed[0].RelativePath != "docs" || listed[1].RelativePath != "docs/readme.txt" {
		t.Fatalf("listed entries = %+v", listed)
	}
}

func TestStoreMarksMissingFileIndexEntriesDeleted(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	share := storage.Share{ID: "share-1", Name: "Drop", RootPath: t.TempDir(), Mode: storage.ShareOneWaySource}
	if err := store.Shares().SaveShare(ctx, share); err != nil {
		t.Fatalf("SaveShare: %v", err)
	}

	firstScan := time.Unix(100, 0).UTC()
	if err := store.FileIndex().SaveSnapshot(ctx, share.ID, []core.FileIndexEntry{
		{ShareID: share.ID, RelativePath: "keep.txt", EntryType: core.EntryFile, Size: 1, ContentHash: "keep", HashAlgorithm: "sha256"},
		{ShareID: share.ID, RelativePath: "gone.txt", EntryType: core.EntryFile, Size: 2, ContentHash: "gone", HashAlgorithm: "sha256"},
	}, firstScan); err != nil {
		t.Fatalf("SaveSnapshot first: %v", err)
	}

	secondScan := time.Unix(200, 0).UTC()
	if err := store.FileIndex().SaveSnapshot(ctx, share.ID, []core.FileIndexEntry{
		{ShareID: share.ID, RelativePath: "keep.txt", EntryType: core.EntryFile, Size: 3, ContentHash: "keep-new", HashAlgorithm: "sha256"},
	}, secondScan); err != nil {
		t.Fatalf("SaveSnapshot second: %v", err)
	}

	keep, err := store.FileIndex().Get(ctx, share.ID, "keep.txt")
	if err != nil {
		t.Fatalf("Get keep: %v", err)
	}
	if keep.IsDeleted || keep.ContentHash != "keep-new" {
		t.Fatalf("keep entry = %+v", keep)
	}

	gone, err := store.FileIndex().Get(ctx, share.ID, "gone.txt")
	if err != nil {
		t.Fatalf("Get gone: %v", err)
	}
	if !gone.IsDeleted || gone.EntryType != core.EntryDeleted {
		t.Fatalf("gone entry = %+v", gone)
	}
	if !gone.DeletedAt.Equal(secondScan) {
		t.Fatalf("deleted at = %s, want %s", gone.DeletedAt, secondScan)
	}
}

func TestCommitSnapshotRecordsRevisionsAndAdvancesCurrentPointers(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	shareID := core.ShareID("share-1")
	saveTestShare(t, store, shareID)

	firstScan := time.Unix(100, 0).UTC()
	firstEntries := []core.FileIndexEntry{
		testFileIndexEntry(shareID, "change.txt", "old"),
		testFileIndexEntry(shareID, "keep.txt", "keep"),
	}
	firstRevisions := []core.Revision{
		testRevision("revision-change-1", shareID, "change.txt", "old", "", 1),
		testRevision("revision-keep-1", shareID, "keep.txt", "keep", "", 2),
	}
	if err := store.FileIndex().CommitSnapshot(ctx, shareID, firstEntries, firstRevisions, firstScan); err != nil {
		t.Fatalf("CommitSnapshot first: %v", err)
	}

	secondScan := time.Unix(200, 0).UTC()
	secondEntries := []core.FileIndexEntry{
		testFileIndexEntry(shareID, "change.txt", "new"),
		testFileIndexEntry(shareID, "keep.txt", "keep"),
	}
	secondRevisions := []core.Revision{
		testRevision("revision-change-2", shareID, "change.txt", "new", "revision-change-1", 3),
	}
	if err := store.FileIndex().CommitSnapshot(ctx, shareID, secondEntries, secondRevisions, secondScan); err != nil {
		t.Fatalf("CommitSnapshot second: %v", err)
	}

	changed, err := store.FileIndex().Get(ctx, shareID, "change.txt")
	if err != nil {
		t.Fatalf("Get changed entry: %v", err)
	}
	if changed.ContentHash != "new" || changed.CurrentRevisionID != "revision-change-2" {
		t.Fatalf("changed entry = %+v", changed)
	}
	kept, err := store.FileIndex().Get(ctx, shareID, "keep.txt")
	if err != nil {
		t.Fatalf("Get kept entry: %v", err)
	}
	if kept.CurrentRevisionID != "revision-keep-1" {
		t.Fatalf("kept current revision = %s", kept.CurrentRevisionID)
	}
}

func TestCommitSnapshotCurrentRevisionLookup(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	shareID := core.ShareID("share-1")
	saveTestShare(t, store, shareID)

	entry := testFileIndexEntry(shareID, "payload.bin", "hash")
	revision := testRevision("revision-1", shareID, entry.RelativePath, entry.ContentHash, "", 1)
	if err := store.FileIndex().CommitSnapshot(ctx, shareID, []core.FileIndexEntry{entry}, []core.Revision{revision}, time.Unix(100, 0).UTC()); err != nil {
		t.Fatalf("CommitSnapshot: %v", err)
	}

	current, err := store.Revisions().GetCurrentRevision(ctx, shareID, entry.RelativePath)
	if err != nil {
		t.Fatalf("GetCurrentRevision: %v", err)
	}
	if current.ID != revision.ID || current.ContentHash != revision.ContentHash {
		t.Fatalf("current revision = %+v", current)
	}
}

func TestCommitSnapshotRollsBackRevisionAndIndexOnError(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	shareID := core.ShareID("share-1")
	saveTestShare(t, store, shareID)

	oldEntry := testFileIndexEntry(shareID, "existing.txt", "old")
	oldRevision := testRevision("revision-old", shareID, oldEntry.RelativePath, oldEntry.ContentHash, "", 1)
	if err := store.FileIndex().CommitSnapshot(ctx, shareID, []core.FileIndexEntry{oldEntry}, []core.Revision{oldRevision}, time.Unix(100, 0).UTC()); err != nil {
		t.Fatalf("CommitSnapshot initial: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
CREATE TRIGGER fail_existing_index_update
BEFORE UPDATE ON file_index
WHEN NEW.relative_path = 'existing.txt'
BEGIN
    SELECT RAISE(ABORT, 'forced index failure');
END`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}

	changedEntry := testFileIndexEntry(shareID, oldEntry.RelativePath, "new")
	addedEntry := testFileIndexEntry(shareID, "added.txt", "added")
	revisions := []core.Revision{
		testRevision("revision-added", shareID, addedEntry.RelativePath, addedEntry.ContentHash, "", 2),
		testRevision("revision-changed", shareID, changedEntry.RelativePath, changedEntry.ContentHash, oldRevision.ID, 3),
	}
	err := store.FileIndex().CommitSnapshot(
		ctx,
		shareID,
		[]core.FileIndexEntry{addedEntry, changedEntry},
		revisions,
		time.Unix(200, 0).UTC(),
	)
	if err == nil {
		t.Fatal("expected forced index failure")
	}

	current, err := store.FileIndex().Get(ctx, shareID, oldEntry.RelativePath)
	if err != nil {
		t.Fatalf("Get existing entry: %v", err)
	}
	if current.ContentHash != oldEntry.ContentHash || current.CurrentRevisionID != oldRevision.ID {
		t.Fatalf("existing entry changed after rollback: %+v", current)
	}
	if _, err := store.FileIndex().Get(ctx, shareID, addedEntry.RelativePath); err == nil {
		t.Fatal("added entry survived rollback")
	}
	for _, revision := range revisions {
		if _, err := store.Revisions().GetRevision(ctx, revision.ID); err == nil {
			t.Fatalf("inserted revision %s survived rollback", revision.ID)
		}
	}
}

func TestCommitSnapshotPersistsDeletionRevision(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	shareID := core.ShareID("share-1")
	saveTestShare(t, store, shareID)

	entry := testFileIndexEntry(shareID, "gone.txt", "old")
	initialRevision := testRevision("revision-1", shareID, entry.RelativePath, entry.ContentHash, "", 1)
	if err := store.FileIndex().CommitSnapshot(ctx, shareID, []core.FileIndexEntry{entry}, []core.Revision{initialRevision}, time.Unix(100, 0).UTC()); err != nil {
		t.Fatalf("CommitSnapshot initial: %v", err)
	}

	deletedAt := time.Unix(200, 0).UTC()
	deletionRevision := core.Revision{
		ID:               "revision-delete",
		ShareID:          shareID,
		RelativePath:     entry.RelativePath,
		EntryType:        core.EntryDeleted,
		ParentRevisionID: initialRevision.ID,
		OriginDeviceID:   "DEVICE-1",
		Sequence:         2,
		IsDeleted:        true,
		CreatedAt:        deletedAt,
	}
	if err := store.FileIndex().CommitSnapshot(ctx, shareID, nil, []core.Revision{deletionRevision}, deletedAt); err != nil {
		t.Fatalf("CommitSnapshot deletion: %v", err)
	}

	deleted, err := store.FileIndex().Get(ctx, shareID, entry.RelativePath)
	if err != nil {
		t.Fatalf("Get deleted entry: %v", err)
	}
	if !deleted.IsDeleted || deleted.EntryType != core.EntryDeleted || deleted.CurrentRevisionID != deletionRevision.ID {
		t.Fatalf("deleted entry = %+v", deleted)
	}
	if !deleted.DeletedAt.Equal(deletedAt) {
		t.Fatalf("deleted at = %s, want %s", deleted.DeletedAt, deletedAt)
	}
	current, err := store.Revisions().GetCurrentRevision(ctx, shareID, entry.RelativePath)
	if err != nil {
		t.Fatalf("GetCurrentRevision deletion: %v", err)
	}
	if current.ID != deletionRevision.ID || !current.IsDeleted {
		t.Fatalf("current deletion revision = %+v", current)
	}
}

func TestCommitSnapshotRejectsInvalidAndMismatchedShareIDs(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	shareID := core.ShareID("share-1")
	saveTestShare(t, store, shareID)
	scannedAt := time.Unix(100, 0).UTC()

	tests := []struct {
		name       string
		commitID   core.ShareID
		entryID    core.ShareID
		revisionID core.ShareID
	}{
		{name: "unknown share", commitID: "missing", entryID: "missing", revisionID: "missing"},
		{name: "entry mismatch", commitID: shareID, entryID: "other", revisionID: shareID},
		{name: "revision mismatch", commitID: shareID, entryID: shareID, revisionID: "other"},
		{name: "empty entry share", commitID: shareID, revisionID: shareID},
		{name: "empty revision share", commitID: shareID, entryID: shareID},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry := testFileIndexEntry(test.entryID, "file.txt", "hash")
			revision := testRevision(core.RevisionID("revision-"+test.name), test.revisionID, entry.RelativePath, entry.ContentHash, "", 1)
			if err := store.FileIndex().CommitSnapshot(ctx, test.commitID, []core.FileIndexEntry{entry}, []core.Revision{revision}, scannedAt); err == nil {
				t.Fatal("expected commit to fail")
			}
		})
	}
}

func saveTestShare(t *testing.T, store *Store, shareID core.ShareID) {
	t.Helper()
	if err := store.Shares().SaveShare(context.Background(), storage.Share{
		ID:       shareID,
		Name:     "Source",
		RootPath: t.TempDir(),
		Mode:     storage.ShareOneWaySource,
	}); err != nil {
		t.Fatalf("SaveShare: %v", err)
	}
}

func testFileIndexEntry(shareID core.ShareID, relativePath, hash string) core.FileIndexEntry {
	return core.FileIndexEntry{
		ShareID:       shareID,
		RelativePath:  relativePath,
		EntryType:     core.EntryFile,
		Size:          int64(len(hash)),
		ContentHash:   hash,
		HashAlgorithm: "sha256",
	}
}

func testRevision(id core.RevisionID, shareID core.ShareID, relativePath, hash string, parent core.RevisionID, sequence int64) core.Revision {
	return core.Revision{
		ID:               id,
		ShareID:          shareID,
		RelativePath:     relativePath,
		EntryType:        core.EntryFile,
		Size:             int64(len(hash)),
		ContentHash:      hash,
		HashAlgorithm:    "sha256",
		ParentRevisionID: parent,
		OriginDeviceID:   "DEVICE-1",
		Sequence:         sequence,
		CreatedAt:        time.Unix(100+sequence, 0).UTC(),
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "syncgate.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return store
}
