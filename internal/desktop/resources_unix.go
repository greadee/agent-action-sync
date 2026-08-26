//go:build !windows

package desktop

import (
	"errors"
	"math"
	"syscall"
)

func diskCapacity(root string) (int64, int64, error) {
	var value syscall.Statfs_t
	if err := syscall.Statfs(root, &value); err != nil {
		return 0, 0, err
	}
	if value.Bsize <= 0 {
		return 0, 0, errors.New("filesystem block size is invalid")
	}
	blockSize := uint64(value.Bsize)
	if uint64(value.Blocks) > uint64(math.MaxInt64)/blockSize || uint64(value.Bavail) > uint64(math.MaxInt64)/blockSize {
		return 0, 0, errors.New("filesystem capacity exceeds supported range")
	}
	return int64(uint64(value.Blocks) * blockSize), int64(uint64(value.Bavail) * blockSize), nil
}
