package project

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"syncgate/internal/filesystem"
)

type DiscoveredProject struct {
	Layout   Layout
	Manifest ProjectManifest
	Record   DecodedRecord
}

// DiscoverProject walks from an existing file or directory toward its volume
// root and returns the nearest valid Agent Project manifest. It never creates or
// repairs project state.
func DiscoverProject(start string) (DiscoveredProject, error) {
	if start == "" {
		return DiscoveredProject{}, domainError(ErrInvalidRoot, "discover project", errors.New("start path is required"))
	}
	absolute, err := filepath.Abs(start)
	if err != nil {
		return DiscoveredProject{}, domainError(ErrInvalidRoot, "discover project", err)
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return DiscoveredProject{}, domainError(ErrInvalidRoot, "discover project", err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return DiscoveredProject{}, domainError(ErrUnsafePath, "discover project", errors.New("start path is a symlink"))
	}
	candidate := filepath.Clean(absolute)
	if !info.IsDir() {
		candidate = filepath.Dir(candidate)
	}

	for {
		layout, layoutErr := NewLayout(candidate)
		if layoutErr == nil {
			_, manifestInfo, exists, inspectErr := layout.InspectManaged(ManifestRelativePath)
			switch {
			case inspectErr != nil:
				return DiscoveredProject{}, inspectErr
			case exists && !manifestInfo.Mode().IsRegular():
				return DiscoveredProject{}, domainError(ErrUnsafePath, "discover project", errors.New("manifest must be a regular file"))
			case exists:
				if err := verifyDiscoveryStart(layout, absolute); err != nil {
					return DiscoveredProject{}, err
				}
				decoded, readErr := ReadPortableRecord(layout, ManifestRelativePath)
				if readErr != nil {
					return DiscoveredProject{}, readErr
				}
				manifest, ok := decoded.Value.(*ProjectManifest)
				if !ok {
					return DiscoveredProject{}, domainError(ErrRecordIntegrity, "discover project", errors.New("manifest path does not contain a project manifest"))
				}
				return DiscoveredProject{Layout: layout, Manifest: *manifest, Record: decoded}, nil
			}
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			break
		}
		candidate = parent
	}
	return DiscoveredProject{}, domainError(ErrRecordNotFound, "discover project", errors.New("no Agent Project manifest found"))
}

func verifyDiscoveryStart(layout Layout, absoluteStart string) error {
	relative, err := filepath.Rel(layout.root, absoluteStart)
	if err != nil {
		return domainError(ErrUnsafePath, "discover project", err)
	}
	if relative == "." {
		return nil
	}
	relative = filepath.ToSlash(relative)
	_, _, exists, err := filesystem.InspectInsideShare(layout.root, relative)
	if err != nil {
		return domainError(ErrUnsafePath, "discover project", err)
	}
	if !exists {
		return domainError(ErrInvalidRoot, "discover project", errors.New("start path disappeared during discovery"))
	}
	return nil
}

// ReadPortableRecord reads and validates one existing canonical portable
// record without following symlink components.
func ReadPortableRecord(layout Layout, relativePath string) (DecodedRecord, error) {
	if _, err := layout.ResolvePortable(relativePath); err != nil {
		return DecodedRecord{}, err
	}
	resolved, info, exists, err := layout.InspectManaged(relativePath)
	if err != nil {
		return DecodedRecord{}, err
	}
	if !exists {
		return DecodedRecord{}, domainError(ErrRecordNotFound, "read portable record", errors.New("record does not exist"))
	}
	if !info.Mode().IsRegular() {
		return DecodedRecord{}, domainError(ErrUnsafePath, "read portable record", errors.New("record is not a regular file"))
	}
	if info.Size() > MaxRecordBytes {
		return DecodedRecord{}, domainError(ErrRecordIntegrity, "read portable record", fmt.Errorf("record exceeds %d bytes", MaxRecordBytes))
	}
	file, err := os.Open(resolved)
	if err != nil {
		return DecodedRecord{}, domainError(ErrFilesystem, "read portable record", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return DecodedRecord{}, domainError(ErrFilesystem, "read portable record", err)
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return DecodedRecord{}, domainError(ErrUnsafePath, "read portable record", errors.New("record changed during inspection"))
	}
	raw, err := io.ReadAll(io.LimitReader(file, MaxRecordBytes+1))
	if err != nil {
		return DecodedRecord{}, domainError(ErrFilesystem, "read portable record", err)
	}
	if len(raw) > MaxRecordBytes {
		return DecodedRecord{}, domainError(ErrRecordIntegrity, "read portable record", fmt.Errorf("record exceeds %d bytes", MaxRecordBytes))
	}
	decoded, err := DecodeRecord(raw)
	if err != nil {
		return DecodedRecord{}, domainError(ErrRecordIntegrity, "read portable record", err)
	}
	if !bytes.Equal(raw, decoded.Canonical) {
		return DecodedRecord{}, domainError(ErrRecordIntegrity, "read portable record", errors.New("record is not in canonical JSON form"))
	}
	return decoded, nil
}

func VerifyRecord(layout Layout, record any) (DecodedRecord, error) {
	relativePath, err := RecordRelativePath(record)
	if err != nil {
		return DecodedRecord{}, err
	}
	expected, err := MarshalRecord(record)
	if err != nil {
		return DecodedRecord{}, domainError(ErrRecordIntegrity, "verify portable record", err)
	}
	actual, err := ReadPortableRecord(layout, relativePath)
	if err != nil {
		return DecodedRecord{}, err
	}
	if !bytes.Equal(actual.Canonical, expected) {
		return DecodedRecord{}, domainError(ErrRecordConflict, "verify portable record", errors.New("existing record content differs"))
	}
	return actual, nil
}
