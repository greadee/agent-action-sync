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
