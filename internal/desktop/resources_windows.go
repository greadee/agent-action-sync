//go:build windows

package desktop

import "golang.org/x/sys/windows"

func diskCapacity(root string) (int64, int64, error) {
	path, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return 0, 0, err
	}
	var available, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(path, &available, &total, &free); err != nil {
		return 0, 0, err
	}
	if total > uint64(^uint64(0)>>1) || available > uint64(^uint64(0)>>1) {
		return 0, 0, windows.ERROR_ARITHMETIC_OVERFLOW
	}
	return int64(total), int64(available), nil
}
