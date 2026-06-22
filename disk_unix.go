//go:build !windows

package main

import "syscall"

// getDiskSpace returns the total and free bytes of the filesystem holding path.
// Returns 0, 0 if it can't be determined.
func getDiskSpace(path string) (total, free uint64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0
	}
	return st.Blocks * uint64(st.Bsize), st.Bavail * uint64(st.Bsize)
}
