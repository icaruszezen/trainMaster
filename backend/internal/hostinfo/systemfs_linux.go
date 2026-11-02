//go:build linux

package hostinfo

import (
	"io/fs"
	"os"
)

// SystemFS returns the root of the real file system, where Host finds
// /proc and /sys.
func SystemFS() fs.FS { return os.DirFS("/") }
