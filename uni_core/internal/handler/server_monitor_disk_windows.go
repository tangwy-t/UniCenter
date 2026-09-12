//go:build windows

package handler

import "golang.org/x/sys/windows"

// diskUsage returns the total and free bytes of the volume hosting path,
// via GetDiskFreeSpaceExW. golang.org/x/sys 已是间接依赖(此处转正为
// 直接使用),不引入新模块。
func diskUsage(path string) (totalBytes, freeBytes uint64, err error) {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, err
	}
	var freeAvailable, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(pathPtr, &freeAvailable, &total, &totalFree); err != nil {
		return 0, 0, err
	}
	return total, totalFree, nil
}
