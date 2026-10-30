//go:build linux

package storage

import "syscall"

func diskUsage(path string) (Usage, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return Usage{}, err
	}
	// Block counts are in fragment-size units (as statvfs and df use);
	// Bsize is only the preferred I/O size and differs on some FUSE mounts.
	bsize := uint64(st.Frsize)
	if bsize == 0 {
		bsize = uint64(st.Bsize)
	}
	u := Usage{
		TotalBytes: st.Blocks * bsize,
		FreeBytes:  st.Bavail * bsize,
	}
	u.UsedBytes = u.TotalBytes - u.FreeBytes
	return u, nil
}
