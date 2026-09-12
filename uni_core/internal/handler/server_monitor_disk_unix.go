//go:build !windows

package handler

import "syscall"

// diskUsage returns the total and free bytes of the filesystem hosting path,
// via statfs(2). Windows has no Statfs; see server_monitor_disk_windows.go.
func diskUsage(path string) (totalBytes, freeBytes uint64, err error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0, err
	}
	return stat.Blocks * uint64(stat.Frsize), stat.Bfree * uint64(stat.Frsize), nil
}
