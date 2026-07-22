package transfer

import (
	"testing"

	"syncgate/internal/core"
)

func TestMissingChunks(t *testing.T) {
	manifest := FileManifest{Chunks: []ChunkManifest{
		{Index: 0, Offset: 0, Size: 4, Hash: "a"},
		{Index: 1, Offset: 4, Size: 4, Hash: "b"},
		{Index: 2, Offset: 8, Size: 2, Hash: "c"},
	}}
	missing, err := MissingChunks(manifest, []core.TransferChunk{
		{Index: 0, Offset: 0, Size: 4, Hash: "a", State: core.ChunkVerified},
		{Index: 2, Offset: 8, Size: 2, Hash: "c", State: core.ChunkVerified},
	})
	if err != nil {
		t.Fatalf("MissingChunks: %v", err)
	}
	if len(missing) != 1 || missing[0].Index != 1 {
		t.Fatalf("missing = %+v", missing)
	}
}

func TestMissingChunksRejectsMismatchedVerifiedChunk(t *testing.T) {
	manifest := FileManifest{Chunks: []ChunkManifest{{Index: 0, Offset: 0, Size: 4, Hash: "a"}}}
	if _, err := MissingChunks(manifest, []core.TransferChunk{
		{Index: 0, Offset: 0, Size: 4, Hash: "wrong", State: core.ChunkVerified},
	}); err == nil {
		t.Fatal("expected mismatched verified chunk to be rejected")
	}
}
