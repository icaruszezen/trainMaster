//go:build linux || darwin

package reviewer

import (
	"errors"
	"os"
	"syscall"
)

// lockFile locks the lock file of a package folder (flock); errLocked when
// another program has it locked.
func lockFile(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return errLocked
	}
	return err
}
