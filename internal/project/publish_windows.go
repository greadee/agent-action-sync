//go:build windows

package project

import "golang.org/x/sys/windows"

// MoveFileEx without MOVEFILE_REPLACE_EXISTING gives immutable publication an
// atomic create-or-fail boundary. WRITE_THROUGH asks Windows not to report
// success until the move is flushed to disk.
func publishNoReplace(temporaryPath, destinationPath string) error {
	from, err := windows.UTF16PtrFromString(temporaryPath)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(destinationPath)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_WRITE_THROUGH)
}
