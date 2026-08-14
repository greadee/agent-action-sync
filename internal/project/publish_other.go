//go:build !windows

package project

import "os"

// Creating a hard link is atomic and fails when destinationPath already exists.
// The caller removes the temporary name only after the final name is visible.
func publishNoReplace(temporaryPath, destinationPath string) error {
	return os.Link(temporaryPath, destinationPath)
}
