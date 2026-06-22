//go:build windows

package main

import "golang.org/x/sys/windows"

// getDiskSpace returns the total and free bytes of the filesystem holding path.
// Returns 0, 0 if it can't be determined.
func getDiskSpace(path string) (total, free uint64) {
	var freeBytes, totalBytes, totalFreeBytes uint64
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0
	}
	if err := windows.GetDiskFreeSpaceEx(pathPtr, &freeBytes, &totalBytes, &totalFreeBytes); err != nil {
		return 0, 0
	}
	return totalBytes, freeBytes
}
