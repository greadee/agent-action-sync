package sync

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"syncgate/internal/core"
	"syncgate/internal/filesystem"
	"syncgate/internal/transfer"
)

var DefaultIgnorePatterns = []string{
	".sync-history",
	".sync-history/**",
	DefaultOneWayIncomingDir,
	DefaultOneWayIncomingDir + "/**",
	"*.sync-part",
	"**/*.sync-part",
}

type ScanOptions struct {
	ShareID        core.ShareID
	RootPath       string
	IgnorePatterns []string
	Now            func() time.Time
}

type ScanResult struct {
	Entries   []core.FileIndexEntry
	ScannedAt time.Time
}

func ScanShare(options ScanOptions) (ScanResult, error) {
	root := filepath.Clean(strings.TrimSpace(options.RootPath))
	if root == "" || root == "." {
		return ScanResult{}, fmt.Errorf("root path is required")
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	scannedAt := now().UTC()
	ignorePatterns := append([]string{}, DefaultIgnorePatterns...)
	ignorePatterns = append(ignorePatterns, options.IgnorePatterns...)

	var entries []core.FileIndexEntry
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}

		relativePath, err := relativeSharePath(root, path)
		if err != nil {
			return err
		}
		if ignored(relativePath, ignorePatterns) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect %s: %w", path, err)
		}
		indexEntry := core.FileIndexEntry{
			ShareID:       options.ShareID,
			RelativePath:  relativePath,
			Size:          info.Size(),
			ModifiedTime:  info.ModTime().UTC(),
			FileIdentity:  fileIdentity(info),
			HashAlgorithm: transfer.HashSHA256,
			LastScannedAt: scannedAt,
		}

		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			indexEntry.EntryType = core.EntrySymlinkUnsupported
		case info.IsDir():
			indexEntry.EntryType = core.EntryDirectory
			indexEntry.Size = 0
			indexEntry.HashAlgorithm = ""
		case info.Mode().IsRegular():
			indexEntry.EntryType = core.EntryFile
			hash, err := hashFile(path)
			if err != nil {
				return err
			}
			indexEntry.ContentHash = hash
		default:
			return fmt.Errorf("unsupported filesystem entry: %s", path)
		}

		entries = append(entries, indexEntry)
		return nil
	})
	if err != nil {
		return ScanResult{}, err
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].RelativePath < entries[j].RelativePath
	})
	return ScanResult{Entries: entries, ScannedAt: scannedAt}, nil
}

func relativeSharePath(root, path string) (string, error) {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return "", fmt.Errorf("make relative path: %w", err)
	}
	normalized, err := filesystem.NormalizeRelativePath(relative)
	if err != nil {
		return "", err
	}
	return normalized, nil
}

func ignored(relativePath string, patterns []string) bool {
	for _, pattern := range patterns {
		pattern = filepath.ToSlash(strings.TrimSpace(pattern))
		if pattern == "" {
			continue
		}
		if matchIgnorePattern(relativePath, pattern) {
			return true
		}
	}
	return false
}

func matchIgnorePattern(relativePath, pattern string) bool {
	if pattern == relativePath {
		return true
	}
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		return relativePath == prefix || strings.HasPrefix(relativePath, prefix+"/")
	}
	if strings.HasPrefix(pattern, "**/") {
		suffix := strings.TrimPrefix(pattern, "**/")
		if ok, _ := filepath.Match(suffix, filepath.Base(relativePath)); ok {
			return true
		}
		return strings.HasSuffix(relativePath, "/"+suffix)
	}
	if ok, _ := filepath.Match(pattern, filepath.Base(relativePath)); ok {
		return true
	}
	if ok, _ := filepath.Match(pattern, relativePath); ok {
		return true
	}
	return false
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open file for hashing: %w", err)
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", fmt.Errorf("hash file: %w", err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func fileIdentity(info fs.FileInfo) string {
	return fmt.Sprintf("mode=%s,size=%d,mod=%d", info.Mode().String(), info.Size(), info.ModTime().UnixNano())
}
