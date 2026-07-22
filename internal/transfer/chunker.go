package transfer

import "fmt"

const DefaultChunkSize int64 = 4 * 1024 * 1024

type Chunk struct {
	Index  int64
	Offset int64
	Size   int64
}

func PlanFixedChunks(fileSize, chunkSize int64) ([]Chunk, error) {
	if fileSize < 0 {
		return nil, fmt.Errorf("file size must be non-negative, got %d", fileSize)
	}
	if chunkSize <= 0 {
		return nil, fmt.Errorf("chunk size must be positive, got %d", chunkSize)
	}
	if fileSize == 0 {
		return []Chunk{{Index: 0, Offset: 0, Size: 0}}, nil
	}

	count := (fileSize + chunkSize - 1) / chunkSize
	chunks := make([]Chunk, 0, count)
	for i := int64(0); i < count; i++ {
		offset := i * chunkSize
		size := chunkSize
		if remaining := fileSize - offset; remaining < chunkSize {
			size = remaining
		}
		chunks = append(chunks, Chunk{Index: i, Offset: offset, Size: size})
	}
	return chunks, nil
}
