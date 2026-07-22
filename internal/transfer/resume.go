package transfer

import (
	"fmt"

	"syncgate/internal/core"
)

func MissingChunks(manifest FileManifest, verified []core.TransferChunk) ([]ChunkManifest, error) {
	verifiedByIndex := map[int64]core.TransferChunk{}
	for _, chunk := range verified {
		if chunk.State != core.ChunkVerified {
			continue
		}
		verifiedByIndex[chunk.Index] = chunk
	}

	missing := make([]ChunkManifest, 0, len(manifest.Chunks))
	for _, chunk := range manifest.Chunks {
		verifiedChunk, ok := verifiedByIndex[chunk.Index]
		if !ok {
			missing = append(missing, chunk)
			continue
		}
		if verifiedChunk.Offset != chunk.Offset || verifiedChunk.Size != chunk.Size || verifiedChunk.Hash != chunk.Hash {
			return nil, fmt.Errorf("verified chunk %d does not match manifest", chunk.Index)
		}
	}
	return missing, nil
}
