//go:build !windows && !linux && !darwin

package reviewer

import "os"

// lockFile does not lock here: a package can be opened by two programs at
// once.
func lockFile(*os.File) error { return nil }
