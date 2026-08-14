package project

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"syncgate/internal/filesystem"
)

type PublishResult struct {
	RelativePath      string
	Digest            string
	Created           bool
	AlreadyPresent    bool
	TemporaryRetained bool
}

type temporaryFile interface {
	io.Writer
	Sync() error
	Close() error
	Name() string
}

type publishOperations struct {
	createTemp func(string, string) (temporaryFile, error)
	publish    func(string, string) error
	remove     func(string) error
}

func defaultPublishOperations() publishOperations {
	return publishOperations{
		createTemp: func(directory, pattern string) (temporaryFile, error) {
			return os.CreateTemp(directory, pattern)
		},
		publish: publishNoReplace,
		remove:  os.Remove,
	}
}

// PublishRecord atomically creates the immutable path derived from record. It
// never accepts a caller-selected destination and never replaces an existing
// record. Identical content is an idempotent success; different content at the
// same path is a conflict.
func PublishRecord(layout Layout, record any) (PublishResult, error) {
	return publishRecord(layout, record, defaultPublishOperations())
}

func publishRecord(layout Layout, record any, operations publishOperations) (PublishResult, error) {
	if operations.createTemp == nil || operations.publish == nil || operations.remove == nil {
		return PublishResult{}, domainError(ErrFilesystem, "publish portable record", errors.New("filesystem operations are incomplete"))
	}
	relativePath, err := RecordRelativePath(record)
	if err != nil {
		return PublishResult{}, err
	}
	if _, err := layout.ResolvePortable(relativePath); err != nil {
		return PublishResult{}, err
	}
	canonical, err := MarshalRecord(record)
	if err != nil {
		return PublishResult{}, domainError(ErrRecordIntegrity, "publish portable record", err)
	}
	decoded, err := DecodeRecord(canonical)
	if err != nil {
		return PublishResult{}, domainError(ErrRecordIntegrity, "publish portable record", err)
	}
	result := PublishResult{RelativePath: relativePath, Digest: decoded.Digest}

	if existing, exists, err := inspectExistingRecord(layout, relativePath); err != nil {
		return PublishResult{}, err
	} else if exists {
		if !bytes.Equal(existing.Canonical, canonical) {
			return PublishResult{}, domainError(ErrRecordConflict, "publish portable record", errors.New("immutable path contains different content"))
		}
		result.AlreadyPresent = true
		return result, nil
	}

	destination, err := filesystem.EnsureParentDirectoriesInsideShare(layout.root, relativePath, 0o700)
	if err != nil {
		return PublishResult{}, domainError(ErrUnsafePath, "publish portable record", err)
	}
	if _, _, exists, err := layout.InspectManaged(relativePath); err != nil {
		return PublishResult{}, err
	} else if exists {
		return resolvePublishRace(layout, record, canonical, result)
	}

	pattern := "." + filepath.Base(destination) + "-*" + TemporaryRecordSuffix
	temporary, err := operations.createTemp(filepath.Dir(destination), pattern)
	if err != nil {
		return PublishResult{}, domainError(ErrFilesystem, "create portable record temporary", err)
	}
	temporaryPath := temporary.Name()
	closed := false
	cleanup := true
	defer func() {
		if !closed {
			_ = temporary.Close()
		}
		if cleanup {
			_ = operations.remove(temporaryPath)
		}
	}()

	if err := writeAll(temporary, canonical); err != nil {
		return PublishResult{}, domainError(ErrFilesystem, "write portable record temporary", err)
	}
	if err := temporary.Sync(); err != nil {
		return PublishResult{}, domainError(ErrFilesystem, "flush portable record temporary", err)
	}
	if err := temporary.Close(); err != nil {
		closed = true
		return PublishResult{}, domainError(ErrFilesystem, "close portable record temporary", err)
	}
	closed = true

	// Reinspect after the write so a local path replacement or concurrent
	// publisher cannot turn publication into overwrite.
	if _, _, exists, err := layout.InspectManaged(relativePath); err != nil {
		return PublishResult{}, err
	} else if exists {
		return resolvePublishRace(layout, record, canonical, result)
	}
	if err := operations.publish(temporaryPath, destination); err != nil {
		if _, _, exists, inspectErr := layout.InspectManaged(relativePath); inspectErr == nil && exists {
			return resolvePublishRace(layout, record, canonical, result)
		}
		return PublishResult{}, domainError(ErrFilesystem, "publish portable record", err)
	}
	result.Created = true
	if err := operations.remove(temporaryPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		result.TemporaryRetained = true
	} else {
		cleanup = false
	}
	return result, nil
}

func inspectExistingRecord(layout Layout, relativePath string) (DecodedRecord, bool, error) {
	_, _, exists, err := layout.InspectManaged(relativePath)
	if err != nil {
		return DecodedRecord{}, false, err
	}
	if !exists {
		return DecodedRecord{}, false, nil
	}
	decoded, err := ReadPortableRecord(layout, relativePath)
	if err != nil {
		if errors.Is(err, ErrRecordIntegrity) {
			return DecodedRecord{}, false, domainError(ErrRecordConflict, "inspect existing portable record", err)
		}
		return DecodedRecord{}, false, err
	}
	return decoded, true, nil
}

func resolvePublishRace(layout Layout, record any, canonical []byte, result PublishResult) (PublishResult, error) {
	relativePath, err := RecordRelativePath(record)
	if err != nil {
		return PublishResult{}, err
	}
	existing, err := ReadPortableRecord(layout, relativePath)
	if err != nil {
		return PublishResult{}, domainError(ErrRecordConflict, "publish portable record", err)
	}
	if !bytes.Equal(existing.Canonical, canonical) {
		return PublishResult{}, domainError(ErrRecordConflict, "publish portable record", errors.New("immutable path contains different content"))
	}
	result.AlreadyPresent = true
	return result, nil
}

func writeAll(writer io.Writer, content []byte) error {
	for len(content) > 0 {
		written, err := writer.Write(content)
		if written < 0 || written > len(content) {
			return fmt.Errorf("invalid write count %d", written)
		}
		content = content[written:]
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}
