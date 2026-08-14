package project

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"syncgate/internal/filesystem"
)

// BlobPublishResult describes a content-addressed artifact blob without
// exposing an absolute local path.
type BlobPublishResult struct {
	RelativePath   string
	ContentHash    string
	Size           int64
	Created        bool
	AlreadyPresent bool
}

// PublishArtifactBlob copies one regular project file into the immutable,
// content-addressed artifact store. The caller selects only the source; the
// destination is derived from the verified content digest.
func PublishArtifactBlob(layout Layout, sourceRelativePath string) (BlobPublishResult, error) {
	if err := ValidateProjectRelativePath(sourceRelativePath); err != nil {
		return BlobPublishResult{}, err
	}
	sourcePath, sourceInfo, exists, err := inspectProjectFile(layout, sourceRelativePath)
	if err != nil {
		return BlobPublishResult{}, err
	}
	if !exists {
		return BlobPublishResult{}, domainError(ErrRecordNotFound, "publish artifact blob", errors.New("source does not exist"))
	}
	if !sourceInfo.Mode().IsRegular() {
		return BlobPublishResult{}, domainError(ErrUnsafePath, "publish artifact blob", errors.New("source must be a regular file"))
	}

	digest, size, err := hashStableFile(sourcePath, sourceInfo)
	if err != nil {
		return BlobPublishResult{}, err
	}
	relativePath, err := ArtifactBlobRelativePath(digest)
	if err != nil {
		return BlobPublishResult{}, err
	}
	result := BlobPublishResult{RelativePath: relativePath, ContentHash: digest, Size: size}
	if present, err := verifyExistingBlob(layout, relativePath, digest, size); err != nil {
		return BlobPublishResult{}, err
	} else if present {
		result.AlreadyPresent = true
		return result, nil
	}

	destination, err := filesystem.EnsureParentDirectoriesInsideShare(layout.root, relativePath, 0o700)
	if err != nil {
		return BlobPublishResult{}, domainError(ErrUnsafePath, "publish artifact blob", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".blob-*"+TemporaryRecordSuffix)
	if err != nil {
		return BlobPublishResult{}, domainError(ErrFilesystem, "create artifact blob temporary", err)
	}
	temporaryPath := temporary.Name()
	closed := false
	defer func() {
		if !closed {
			_ = temporary.Close()
		}
		_ = os.Remove(temporaryPath)
	}()

	opened, err := os.Open(sourcePath)
	if err != nil {
		return BlobPublishResult{}, domainError(ErrFilesystem, "open artifact source", err)
	}
	openedInfo, statErr := opened.Stat()
	if statErr != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(sourceInfo, openedInfo) {
		_ = opened.Close()
		return BlobPublishResult{}, domainError(ErrUnsafePath, "publish artifact blob", errors.New("source changed during inspection"))
	}
	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temporary, hasher), opened)
	closeErr := opened.Close()
	if copyErr != nil || closeErr != nil {
		return BlobPublishResult{}, domainError(ErrFilesystem, "copy artifact blob", errors.Join(copyErr, closeErr))
	}
	if written != size || hex.EncodeToString(hasher.Sum(nil)) != digest {
		return BlobPublishResult{}, domainError(ErrRecordConflict, "publish artifact blob", errors.New("source changed while being copied"))
	}
	if err := temporary.Sync(); err != nil {
		return BlobPublishResult{}, domainError(ErrFilesystem, "flush artifact blob temporary", err)
	}
	if err := temporary.Close(); err != nil {
		closed = true
		return BlobPublishResult{}, domainError(ErrFilesystem, "close artifact blob temporary", err)
	}
	closed = true

	if err := publishNoReplace(temporaryPath, destination); err != nil {
		if present, verifyErr := verifyExistingBlob(layout, relativePath, digest, size); verifyErr == nil && present {
			result.AlreadyPresent = true
			return result, nil
		}
		return BlobPublishResult{}, domainError(ErrFilesystem, "publish artifact blob", err)
	}
	result.Created = true
	return result, nil
}

func inspectProjectFile(layout Layout, relativePath string) (string, fs.FileInfo, bool, error) {
	resolved, info, exists, err := filesystem.InspectInsideShare(layout.root, relativePath)
	if err != nil {
		return "", nil, false, domainError(ErrUnsafePath, "inspect artifact source", err)
	}
	return resolved, info, exists, nil
}

func hashStableFile(path string, inspected fs.FileInfo) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, domainError(ErrFilesystem, "open artifact source", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return "", 0, domainError(ErrFilesystem, "inspect artifact source", err)
	}
	if !opened.Mode().IsRegular() || !os.SameFile(inspected, opened) {
		return "", 0, domainError(ErrUnsafePath, "inspect artifact source", errors.New("source changed during inspection"))
	}
	hasher := sha256.New()
	size, err := io.Copy(hasher, file)
	if err != nil {
		return "", 0, domainError(ErrFilesystem, "hash artifact source", err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), size, nil
}

func verifyExistingBlob(layout Layout, relativePath, digest string, size int64) (bool, error) {
	path, info, exists, err := layout.InspectManaged(relativePath)
	if err != nil || !exists {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() != size {
		return false, domainError(ErrRecordConflict, "verify artifact blob", errors.New("content-addressed path contains different content"))
	}
	actual, actualSize, err := hashStableFile(path, info)
	if err != nil {
		return false, err
	}
	if actualSize != size || actual != digest {
		return false, domainError(ErrRecordConflict, "verify artifact blob", fmt.Errorf("content-addressed path failed integrity verification"))
	}
	return true, nil
}
