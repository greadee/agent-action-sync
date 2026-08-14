package project

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"

	"syncgate/internal/filesystem"
)

type ContentIdentity struct {
	Size          int64
	HashAlgorithm string
	ContentHash   string
}

// HashProjectFile hashes one existing regular project file after containment
// and symlink checks. It does not interpret the content.
func HashProjectFile(layout Layout, relativePath string) (ContentIdentity, error) {
	if _, err := layout.Resolve(relativePath); err != nil {
		return ContentIdentity{}, err
	}
	resolved, before, exists, err := inspectProjectPath(layout, relativePath)
	if err != nil {
		return ContentIdentity{}, err
	}
	if !exists {
		return ContentIdentity{}, domainError(ErrRecordNotFound, "hash project file", errors.New("file does not exist"))
	}
	if !before.Mode().IsRegular() {
		return ContentIdentity{}, domainError(ErrUnsafePath, "hash project file", errors.New("path is not a regular file"))
	}
	file, err := os.Open(resolved)
	if err != nil {
		return ContentIdentity{}, domainError(ErrFilesystem, "hash project file", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return ContentIdentity{}, domainError(ErrFilesystem, "hash project file", err)
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return ContentIdentity{}, domainError(ErrUnsafePath, "hash project file", errors.New("file changed during inspection"))
	}
	hasher := sha256.New()
	size, err := io.Copy(hasher, file)
	if err != nil {
		return ContentIdentity{}, domainError(ErrFilesystem, "hash project file", err)
	}
	return ContentIdentity{Size: size, HashAlgorithm: HashAlgorithmSHA256, ContentHash: hex.EncodeToString(hasher.Sum(nil))}, nil
}

func VerifyProjectFile(layout Layout, relativePath string, expected ContentIdentity) error {
	if expected.Size < 0 || expected.HashAlgorithm != HashAlgorithmSHA256 || !validSHA256(expected.ContentHash) {
		return domainError(ErrRecordIntegrity, "verify project file", errors.New("expected content identity is invalid"))
	}
	actual, err := HashProjectFile(layout, relativePath)
	if err != nil {
		return err
	}
	if actual != expected {
		return domainError(ErrRecordIntegrity, "verify project file", errors.New("content size or hash differs"))
	}
	return nil
}

func inspectProjectPath(layout Layout, relativePath string) (string, os.FileInfo, bool, error) {
	resolved, info, exists, err := filesystem.InspectInsideShare(layout.root, relativePath)
	if err != nil {
		return "", nil, false, domainError(ErrUnsafePath, "inspect project path", err)
	}
	return resolved, info, exists, nil
}
