package transfer

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

const MaxManifestHeaderBytes = 4 * 1024 * 1024

type SendResult struct {
	Manifest  FileManifest
	BytesSent int64
}

type ReceiveResult struct {
	Manifest        FileManifest
	DestinationPath string
	BytesReceived   int64
}

func SendFile(stream io.Writer, sourcePath, relativePath string, chunkSize int64) (SendResult, error) {
	manifest, err := BuildFileManifest(sourcePath, relativePath, chunkSize)
	if err != nil {
		return SendResult{}, err
	}
	if err := writeManifest(stream, manifest); err != nil {
		return SendResult{}, err
	}

	file, err := os.Open(sourcePath)
	if err != nil {
		return SendResult{}, fmt.Errorf("open source file for transfer: %w", err)
	}
	defer file.Close()

	var sent int64
	buffer := make([]byte, minInt64(chunkSize, 1024*1024))
	for _, chunk := range manifest.Chunks {
		remaining := chunk.Size
		offset := chunk.Offset
		for remaining > 0 {
			readSize := int64(len(buffer))
			if remaining < readSize {
				readSize = remaining
			}
			n, err := file.ReadAt(buffer[:readSize], offset)
			if n > 0 {
				if _, writeErr := stream.Write(buffer[:n]); writeErr != nil {
					return SendResult{}, fmt.Errorf("write chunk %d: %w", chunk.Index, writeErr)
				}
				offset += int64(n)
				remaining -= int64(n)
				sent += int64(n)
			}
			if err != nil {
				if err == io.EOF && remaining == 0 {
					break
				}
				return SendResult{}, fmt.Errorf("read chunk %d: %w", chunk.Index, err)
			}
		}
	}
	return SendResult{Manifest: manifest, BytesSent: sent}, nil
}

func ReceiveFile(stream io.Reader, shareRoot string) (ReceiveResult, error) {
	manifest, err := readManifest(stream)
	if err != nil {
		return ReceiveResult{}, err
	}
	writer, err := NewReceiveWriter(ReceiveSpec{
		ShareRoot:     shareRoot,
		RelativePath:  manifest.RelativePath,
		ExpectedSize:  manifest.Size,
		ExpectedHash:  manifest.ContentHash,
		HashAlgorithm: manifest.HashAlgorithm,
	})
	if err != nil {
		return ReceiveResult{}, err
	}
	defer writer.Close()

	buffer := make([]byte, minInt64(manifest.ChunkSize, 1024*1024))
	var received int64
	for _, chunk := range manifest.Chunks {
		if err := receiveChunk(stream, writer, buffer, chunk); err != nil {
			return ReceiveResult{}, err
		}
		received += chunk.Size
	}
	destinationPath, err := writer.Commit()
	if err != nil {
		return ReceiveResult{}, err
	}
	return ReceiveResult{
		Manifest:        manifest,
		DestinationPath: destinationPath,
		BytesReceived:   received,
	}, nil
}

func writeManifest(stream io.Writer, manifest FileManifest) error {
	raw, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("marshal transfer manifest: %w", err)
	}
	if len(raw) > MaxManifestHeaderBytes {
		return fmt.Errorf("manifest header exceeds %d bytes", MaxManifestHeaderBytes)
	}
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(raw)))
	if _, err := stream.Write(length[:]); err != nil {
		return fmt.Errorf("write manifest length: %w", err)
	}
	if _, err := stream.Write(raw); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	return nil
}

func readManifest(stream io.Reader) (FileManifest, error) {
	var length [4]byte
	if _, err := io.ReadFull(stream, length[:]); err != nil {
		return FileManifest{}, fmt.Errorf("read manifest length: %w", err)
	}
	size := binary.BigEndian.Uint32(length[:])
	if size == 0 || size > MaxManifestHeaderBytes {
		return FileManifest{}, fmt.Errorf("invalid manifest header size %d", size)
	}
	raw := make([]byte, size)
	if _, err := io.ReadFull(stream, raw); err != nil {
		return FileManifest{}, fmt.Errorf("read manifest: %w", err)
	}
	var manifest FileManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return FileManifest{}, fmt.Errorf("parse manifest: %w", err)
	}
	if manifest.HashAlgorithm != HashSHA256 {
		return FileManifest{}, fmt.Errorf("unsupported hash algorithm %q", manifest.HashAlgorithm)
	}
	if manifest.ChunkSize <= 0 {
		return FileManifest{}, fmt.Errorf("manifest chunk size must be positive")
	}
	return manifest, nil
}

func receiveChunk(stream io.Reader, writer io.Writer, buffer []byte, chunk ChunkManifest) error {
	hasher := sha256.New()
	remaining := chunk.Size
	for remaining > 0 {
		readSize := int64(len(buffer))
		if remaining < readSize {
			readSize = remaining
		}
		if _, err := io.ReadFull(stream, buffer[:readSize]); err != nil {
			return fmt.Errorf("read chunk %d: %w", chunk.Index, err)
		}
		data := buffer[:readSize]
		if _, err := hasher.Write(data); err != nil {
			return err
		}
		if _, err := writer.Write(data); err != nil {
			return fmt.Errorf("write chunk %d: %w", chunk.Index, err)
		}
		remaining -= readSize
	}
	actualHash := hex.EncodeToString(hasher.Sum(nil))
	if actualHash != chunk.Hash {
		return fmt.Errorf("chunk %d hash %s does not match expected hash %s", chunk.Index, actualHash, chunk.Hash)
	}
	return nil
}
