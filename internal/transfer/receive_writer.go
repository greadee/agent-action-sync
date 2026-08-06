package transfer

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"syncgate/internal/filesystem"
)

const (
	HashSHA256           = "sha256"
	DefaultPartialSuffix = ".sync-part"
	DefaultHistoryDir    = ".sync-history"
)

type ReceiveSpec struct {
	ShareRoot       string
	RelativePath    string
	ExpectedSize    int64
	ExpectedHash    string
	HashAlgorithm   string
	PartialSuffix   string
	ReplaceExisting bool
	HistoryDirName  string
}

type ReceiveWriter struct {
	destinationPath string
	partialPath     string
	expectedSize    int64
	expectedHash    string
	shareRoot       string
	replaceExisting bool
	historyDirName  string
	file            receiveFile
	hasher          hash.Hash
	written         int64
	closed          bool
	committed       bool
}

// receiveFile is the narrow filesystem boundary used by a receiving transfer.
// Keeping it small makes write and flush failures testable without changing the
// production path, which always uses an *os.File.
type receiveFile interface {
	io.Writer
	Sync() error
	Close() error
}

func NewReceiveWriter(spec ReceiveSpec) (*ReceiveWriter, error) {
	return newReceiveWriter(spec, func(path string) (receiveFile, error) {
		return os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	})
}

func newReceiveWriter(spec ReceiveSpec, openPartial func(string) (receiveFile, error)) (*ReceiveWriter, error) {
	if openPartial == nil {
		return nil, errors.New("partial-file opener is required")
	}
	if spec.ExpectedSize < 0 {
		return nil, fmt.Errorf("expected size must be non-negative, got %d", spec.ExpectedSize)
	}
	if spec.HashAlgorithm == "" {
		spec.HashAlgorithm = HashSHA256
	}
	if !strings.EqualFold(spec.HashAlgorithm, HashSHA256) {
		return nil, fmt.Errorf("unsupported hash algorithm %q", spec.HashAlgorithm)
	}
	if spec.PartialSuffix == "" {
		spec.PartialSuffix = DefaultPartialSuffix
	}
	if spec.HistoryDirName == "" {
		spec.HistoryDirName = DefaultHistoryDir
	}

	expectedHash, err := normalizeHexHash(spec.ExpectedHash)
	if err != nil {
		return nil, err
	}

	destinationPath, err := filesystem.EnsureParentDirectoriesInsideShare(spec.ShareRoot, spec.RelativePath, 0o700)
	if err != nil {
		return nil, err
	}

	partialPath := destinationPath + spec.PartialSuffix
	file, err := openPartial(partialPath)
	if err != nil {
		return nil, fmt.Errorf("open partial file: %w", err)
	}

	return &ReceiveWriter{
		destinationPath: destinationPath,
		partialPath:     partialPath,
		expectedSize:    spec.ExpectedSize,
		expectedHash:    expectedHash,
		shareRoot:       spec.ShareRoot,
		replaceExisting: spec.ReplaceExisting,
		historyDirName:  spec.HistoryDirName,
		file:            file,
		hasher:          sha256.New(),
	}, nil
}

func (writer *ReceiveWriter) Write(chunk []byte) (int, error) {
	if writer.closed {
		return 0, errors.New("receive writer is closed")
	}
	n, err := writer.file.Write(chunk)
	if n > 0 {
		writer.written += int64(n)
		if _, hashErr := writer.hasher.Write(chunk[:n]); hashErr != nil && err == nil {
			err = hashErr
		}
	}
	return n, err
}

func (writer *ReceiveWriter) Commit() (string, error) {
	if writer.committed {
		return writer.destinationPath, nil
	}
	if writer.closed {
		return "", errors.New("receive writer is closed")
	}
	if writer.written != writer.expectedSize {
		_ = writer.Close()
		return "", fmt.Errorf("received size %d does not match expected size %d", writer.written, writer.expectedSize)
	}

	actualHash := hex.EncodeToString(writer.hasher.Sum(nil))
	if actualHash != writer.expectedHash {
		_ = writer.Close()
		return "", fmt.Errorf("received hash %s does not match expected hash %s", actualHash, writer.expectedHash)
	}
	if err := writer.file.Sync(); err != nil {
		_ = writer.Close()
		return "", fmt.Errorf("flush partial file: %w", err)
	}
	if err := writer.file.Close(); err != nil {
		writer.closed = true
		return "", fmt.Errorf("close partial file: %w", err)
	}
	writer.closed = true

	if err := writer.prepareDestinationForCommit(); err != nil {
		return "", err
	}
	if err := os.Rename(writer.partialPath, writer.destinationPath); err != nil {
		return "", fmt.Errorf("commit partial file: %w", err)
	}
	writer.committed = true
	return writer.destinationPath, nil
}

func (writer *ReceiveWriter) prepareDestinationForCommit() error {
	if _, err := os.Stat(writer.destinationPath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("check destination: %w", err)
	}
	if !writer.replaceExisting {
		return fmt.Errorf("destination already exists: %s", writer.destinationPath)
	}

	historyRelativePath, err := writer.historyRelativePath()
	if err != nil {
		return err
	}
	historyPath, err := filesystem.EnsureParentDirectoriesInsideShare(writer.shareRoot, historyRelativePath, 0o700)
	if err != nil {
		return fmt.Errorf("create history directory: %w", err)
	}
	if err := os.Rename(writer.destinationPath, historyPath); err != nil {
		return fmt.Errorf("preserve existing destination: %w", err)
	}
	return nil
}

func (writer *ReceiveWriter) historyRelativePath() (string, error) {
	relative, err := filepath.Rel(filepath.Clean(writer.shareRoot), writer.destinationPath)
	if err != nil {
		return "", fmt.Errorf("make history path: %w", err)
	}
	normalized, err := filesystem.NormalizeRelativePath(relative)
	if err != nil {
		return "", fmt.Errorf("normalize history path: %w", err)
	}
	return filepath.ToSlash(filepath.Join(writer.historyDirName, normalized+"."+historyStamp())), nil
}

func historyStamp() string {
	return time.Now().UTC().Format("20060102T150405.000000000Z")
}

func (writer *ReceiveWriter) Close() error {
	if writer.closed {
		return nil
	}
	writer.closed = true
	return writer.file.Close()
}

func (writer *ReceiveWriter) PartialPath() string {
	return writer.partialPath
}

func (writer *ReceiveWriter) DestinationPath() string {
	return writer.destinationPath
}

func normalizeHexHash(raw string) (string, error) {
	hash := strings.ToLower(strings.TrimSpace(raw))
	if hash == "" {
		return "", errors.New("expected hash is required")
	}
	decoded, err := hex.DecodeString(hash)
	if err != nil {
		return "", fmt.Errorf("expected hash must be hex: %w", err)
	}
	if len(decoded) != sha256.Size {
		return "", fmt.Errorf("expected hash must be %d bytes, got %d", sha256.Size, len(decoded))
	}
	return hash, nil
}
