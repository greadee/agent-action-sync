package integration

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"syncgate/internal/core"
	"syncgate/internal/storage"
	sqlitestore "syncgate/internal/storage/sqlite"
	"syncgate/internal/transfer"
)

func TestTransferStateSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "syncgate.db")
	device := storage.Device{
		ID:          "DEVICE-1",
		DisplayName: "Laptop",
		PublicKey:   []byte("public"),
		Fingerprint: "ABCD",
		TrustState:  storage.TrustTrusted,
	}
	manifest := transfer.FileManifest{
		RelativePath:  "payload.bin",
		Size:          10,
		HashAlgorithm: transfer.HashSHA256,
		ChunkSize:     4,
		Chunks: []transfer.ChunkManifest{
			{Index: 0, Offset: 0, Size: 4, Hash: "chunk-0"},
			{Index: 1, Offset: 4, Size: 4, Hash: "chunk-1"},
			{Index: 2, Offset: 8, Size: 2, Hash: "chunk-2"},
		},
	}

	firstStore, err := sqlitestore.Open(dbPath)
	if err != nil {
		t.Fatalf("Open first store: %v", err)
	}
	if err := firstStore.Migrate(ctx); err != nil {
		t.Fatalf("Migrate first store: %v", err)
	}
	if err := firstStore.Devices().TrustDevice(ctx, device); err != nil {
		t.Fatalf("TrustDevice: %v", err)
	}
	transferID := core.TransferID("transfer-1")
	if err := firstStore.Transfers().SaveTransfer(ctx, core.Transfer{
		ID:            transferID,
		Direction:     core.TransferReceive,
		PeerDeviceID:  device.ID,
		RelativePath:  manifest.RelativePath,
		State:         core.TransferTransferring,
		Size:          manifest.Size,
		ChunkSize:     manifest.ChunkSize,
		ContentHash:   "whole",
		HashAlgorithm: transfer.HashSHA256,
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SaveTransfer: %v", err)
	}
	for _, chunk := range manifest.Chunks[:2] {
		if err := firstStore.Transfers().SaveChunk(ctx, core.TransferChunk{
			TransferID: transferID,
			Index:      chunk.Index,
			Offset:     chunk.Offset,
			Size:       chunk.Size,
			Hash:       chunk.Hash,
			State:      core.ChunkVerified,
			VerifiedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("SaveChunk: %v", err)
		}
	}
	if err := firstStore.Close(); err != nil {
		t.Fatalf("Close first store: %v", err)
	}

	secondStore, err := sqlitestore.Open(dbPath)
	if err != nil {
		t.Fatalf("Open second store: %v", err)
	}
	defer secondStore.Close()
	if err := secondStore.Migrate(ctx); err != nil {
		t.Fatalf("Migrate second store: %v", err)
	}
	verified, err := secondStore.Transfers().VerifiedChunks(ctx, transferID)
	if err != nil {
		t.Fatalf("VerifiedChunks: %v", err)
	}
	missing, err := transfer.MissingChunks(manifest, verified)
	if err != nil {
		t.Fatalf("MissingChunks: %v", err)
	}
	if len(missing) != 1 || missing[0].Index != 2 {
		t.Fatalf("missing chunks = %+v", missing)
	}
}
