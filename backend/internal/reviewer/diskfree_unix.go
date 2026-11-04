//go:build linux || darwin

package reviewer

import "syscall"

// diskFree returns the bytes free for the user on the volume of dir.
func diskFree(dir string) (int64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, false
	}
	return int64(min(uint64(st.Bavail)*uint64(st.Bsize), 1<<62)), true
}
