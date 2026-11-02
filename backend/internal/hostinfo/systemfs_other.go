//go:build !linux

package hostinfo

import "io/fs"

// SystemFS returns nil: /proc and /sys exist only on Linux, the platform
// the service is deployed on. Elsewhere (development machines) Host
// reports only what the Go runtime knows.
func SystemFS() fs.FS { return nil }
