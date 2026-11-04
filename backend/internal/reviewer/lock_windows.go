//go:build windows

package reviewer

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockFile locks the lock file of a package folder (LockFileEx on its
// first byte); errLocked when another program has it locked. The file is
// hidden: Explorer does not hide names starting with a dot.
func lockFile(f *os.File) error {
	h := windows.Handle(f.Fd())
	err := windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0,
		new(windows.Overlapped))
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return errLocked
	}
	if err != nil {
		return err
	}
	if name, err := windows.UTF16PtrFromString(f.Name()); err == nil {
		_ = windows.SetFileAttributes(name, windows.FILE_ATTRIBUTE_HIDDEN)
	}
	return nil
}
