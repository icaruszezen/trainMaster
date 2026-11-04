//go:build windows

package reviewer

import "golang.org/x/sys/windows"

// diskFree returns the bytes free for the user on the volume of dir.
func diskFree(dir string) (int64, bool) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, false
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree); err != nil {
		return 0, false
	}
	return int64(min(free, 1<<62)), true
}
