package transfer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"time"
)

type FileManifest struct {
	RelativePath  string
	Size          int64
	ModifiedTime  time.Time
	HashAlgorithm string
	ContentHash   string
	ChunkSize     int64
	Chunks        []ChunkManifest
}

type ChunkManifest struct {
	Index  int64
	Offset int64
	Size   int64
	Hash   string
}

func BuildFileManifest(filePath, relativePath string, chunkSize int64) (FileManifest, error) {
	if chunkSize <= 0 {
		return FileManifest{}, fmt.Errorf("chunk size must be positive, got %d", chunkSize)
	}

	initialInfo, err := os.Stat(filePath)
	if err != nil {
		return FileManifest{}, fmt.Errorf("stat source file: %w", err)
	}
	if !initialInfo.Mode().IsRegular() {
		return FileManifest{}, fmt.Errorf("source path is not a regular file: %s", filePath)
	}

	file, err := os.Open(filePath)
	if err != nil {
		return FileManifest{}, fmt.Errorf("open source file: %w", err)
	}
	defer file.Close()

	chunks, err := PlanFixedChunks(initialInfo.Size(), chunkSize)
	if err != nil {
		return FileManifest{}, err
	}

	wholeHasher := sha256.New()
	manifestChunks := make([]ChunkManifest, 0, len(chunks))
	buffer := make([]byte, minInt64(chunkSize, 1024*1024))
	for _, chunk := range chunks {
		chunkHash, err := hashChunk(file, wholeHasher, buffer, chunk)
		if err != nil {
			return FileManifest{}, err
		}
		manifestChunks = append(manifestChunks, ChunkManifest{
			Index:  chunk.Index,
			Offset: chunk.Offset,
			Size:   chunk.Size,
			Hash:   chunkHash,
		})
	}

	finalInfo, err := os.Stat(filePath)
	if err != nil {
		return FileManifest{}, fmt.Errorf("stat source file after hashing: %w", err)
	}
	if sourceChanged(initialInfo, finalInfo) {
		return FileManifest{}, fmt.Errorf("source file changed while hashing: %s", filePath)
	}

	return FileManifest{
		RelativePath:  relativePath,
		Size:          initialInfo.Size(),
		ModifiedTime:  initialInfo.ModTime().UTC(),
		HashAlgorithm: HashSHA256,
		ContentHash:   hex.EncodeToString(wholeHasher.Sum(nil)),
		ChunkSize:     chunkSize,
		Chunks:        manifestChunks,
	}, nil
}

func hashChunk(file *os.File, wholeHasher io.Writer, buffer []byte, chunk Chunk) (string, error) {
	chunkHasher := sha256.New()
	remaining := chunk.Size
	offset := chunk.Offset
	for remaining > 0 {
		readSize := int64(len(buffer))
		if remaining < readSize {
			readSize = remaining
		}
		n, err := file.ReadAt(buffer[:readSize], offset)
		if n > 0 {
			data := buffer[:n]
			if _, hashErr := chunkHasher.Write(data); hashErr != nil {
				return "", hashErr
			}
			if _, hashErr := wholeHasher.Write(data); hashErr != nil {
				return "", hashErr
			}
			offset += int64(n)
			remaining -= int64(n)
		}
		if err != nil {
			if err == io.EOF && remaining == 0 {
				break
			}
			return "", fmt.Errorf("read chunk %d: %w", chunk.Index, err)
		}
	}
	return hex.EncodeToString(chunkHasher.Sum(nil)), nil
}

func sourceChanged(initial, final os.FileInfo) bool {
	return initial.Size() != final.Size() || !initial.ModTime().Equal(final.ModTime())
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
