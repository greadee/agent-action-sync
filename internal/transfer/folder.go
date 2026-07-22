package transfer

import (
	"fmt"
	"io/fs"
	"path/filepath"

	"syncgate/internal/filesystem"
)

type FolderFile struct {
	SourcePath   string
	RelativePath string
	Size         int64
}

func CollectFolderFiles(root string) ([]FolderFile, error) {
	root = filepath.Clean(root)
	var files []FolderFile
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect %s: %w", path, err)
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("symlink is unsupported in folder transfer: %s", path)
		}
		if entry.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported non-regular file: %s", path)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("make relative path: %w", err)
		}
		normalized, err := filesystem.NormalizeRelativePath(relative)
		if err != nil {
			return err
		}
		files = append(files, FolderFile{
			SourcePath:   path,
			RelativePath: normalized,
			Size:         info.Size(),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}
