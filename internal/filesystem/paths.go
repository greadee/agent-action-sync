package filesystem

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

var reservedWindowsNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

func NormalizeRelativePath(raw string) (string, error) {
	path := strings.TrimSpace(raw)
	if path == "" {
		return "", errors.New("path is required")
	}
	if strings.ContainsRune(path, 0) {
		return "", errors.New("path contains NUL byte")
	}
	if looksAbsolute(path) {
		return "", fmt.Errorf("path must be relative: %q", raw)
	}

	path = strings.ReplaceAll(path, "\\", "/")
	cleaned := filepath.ToSlash(filepath.Clean(path))
	if cleaned == "." || cleaned == "" {
		return "", errors.New("path resolves to current directory")
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("path escapes share root: %q", raw)
	}

	parts := strings.Split(cleaned, "/")
	for _, part := range parts {
		if err := validatePathPart(part); err != nil {
			return "", fmt.Errorf("invalid path segment %q: %w", part, err)
		}
	}
	return cleaned, nil
}

func ResolveInsideShare(rootPath, relativePath string) (string, error) {
	if strings.TrimSpace(rootPath) == "" {
		return "", errors.New("share root is required")
	}
	normalized, err := NormalizeRelativePath(relativePath)
	if err != nil {
		return "", err
	}

	root := filepath.Clean(rootPath)
	target := filepath.Clean(filepath.Join(root, filepath.FromSlash(normalized)))

	rel, err := filepath.Rel(root, target)
	if err != nil {
		return "", fmt.Errorf("check share containment: %w", err)
	}
	if rel == ".." || strings.HasPrefix(filepath.ToSlash(rel), "../") {
		return "", fmt.Errorf("resolved path escapes share root: %q", relativePath)
	}

	return target, nil
}

func looksAbsolute(path string) bool {
	if filepath.IsAbs(path) || filepath.VolumeName(path) != "" {
		return true
	}
	return strings.HasPrefix(path, "/") || strings.HasPrefix(path, "\\") ||
		strings.HasPrefix(path, "//") || strings.HasPrefix(path, "\\\\") ||
		(len(path) >= 2 && path[1] == ':')
}

func validatePathPart(part string) error {
	if part == "" || part == "." || part == ".." {
		return errors.New("empty or traversal segment")
	}
	if strings.HasSuffix(part, " ") || strings.HasSuffix(part, ".") {
		return errors.New("segment cannot end with space or dot")
	}
	if strings.Contains(part, ":") {
		return errors.New("segment cannot contain colon")
	}

	name := strings.ToUpper(part)
	if dot := strings.IndexRune(name, '.'); dot >= 0 {
		name = name[:dot]
	}
	if reservedWindowsNames[name] {
		return errors.New("reserved Windows device name")
	}
	return nil
}
