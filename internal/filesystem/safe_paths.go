package filesystem

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// InspectInsideShare resolves a canonical relative path and rejects symlinks in
// the share root, every existing parent, and the leaf itself. A missing leaf is
// reported with exists=false; missing parents are permitted but never followed.
func InspectInsideShare(rootPath, relativePath string) (resolved string, info fs.FileInfo, exists bool, err error) {
	normalized, err := NormalizeRelativePath(relativePath)
	if err != nil {
		return "", nil, false, err
	}
	resolved, err = ResolveInsideShare(rootPath, normalized)
	if err != nil {
		return "", nil, false, err
	}

	root := filepath.Clean(rootPath)
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return "", nil, false, fmt.Errorf("inspect share root: %w", err)
	}
	if rootInfo.Mode()&fs.ModeSymlink != 0 || !rootInfo.IsDir() {
		return "", nil, false, fmt.Errorf("share root must be a real directory: %s", root)
	}

	current := root
	parts := strings.Split(normalized, "/")
	for index, part := range parts {
		current = filepath.Join(current, filepath.FromSlash(part))
		currentInfo, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			return resolved, nil, false, nil
		}
		if statErr != nil {
			return "", nil, false, fmt.Errorf("inspect path component %s: %w", current, statErr)
		}
		if currentInfo.Mode()&fs.ModeSymlink != 0 {
			return "", nil, false, fmt.Errorf("path component is a symlink: %s", current)
		}
		if index < len(parts)-1 && !currentInfo.IsDir() {
			return "", nil, false, fmt.Errorf("path parent is not a directory: %s", current)
		}
		if index == len(parts)-1 {
			return resolved, currentInfo, true, nil
		}
	}
	return resolved, nil, false, nil
}

// EnsureParentDirectoriesInsideShare creates only missing parent directories,
// checking every component with Lstat so a symlink cannot redirect creation or
// a later rename outside the configured share root.
func EnsureParentDirectoriesInsideShare(rootPath, relativePath string, permission fs.FileMode) (string, error) {
	normalized, err := NormalizeRelativePath(relativePath)
	if err != nil {
		return "", err
	}
	resolved, err := ResolveInsideShare(rootPath, normalized)
	if err != nil {
		return "", err
	}

	root := filepath.Clean(rootPath)
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return "", fmt.Errorf("inspect share root: %w", err)
	}
	if rootInfo.Mode()&fs.ModeSymlink != 0 || !rootInfo.IsDir() {
		return "", fmt.Errorf("share root must be a real directory: %s", root)
	}

	parts := strings.Split(normalized, "/")
	current := root
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, filepath.FromSlash(part))
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			if err := os.Mkdir(current, permission); err != nil && !errors.Is(err, os.ErrExist) {
				return "", fmt.Errorf("create path parent %s: %w", current, err)
			}
			info, statErr = os.Lstat(current)
		}
		if statErr != nil {
			return "", fmt.Errorf("inspect path parent %s: %w", current, statErr)
		}
		if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("path parent must be a real directory: %s", current)
		}
	}
	return resolved, nil
}
