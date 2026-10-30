// Package buildinfo holds version metadata injected at build time, e.g.
//
//	go build -ldflags "-X github.com/icaruszezen/trainmaster/backend/internal/buildinfo.Version=1.2.3"
//
// The Dockerfile sets all three fields from its VERSION, COMMIT and
// BUILD_TIME build args.
package buildinfo

import "runtime"

// Name is the product name reported by the API.
const Name = "trainMaster"

var (
	// Version is the release version. This default is the single source of
	// truth for development builds; releases override it via -ldflags.
	Version = "0.1.0-dev"
	// Commit is the git commit the binary was built from.
	Commit = "unknown"
	// BuildTime is the build timestamp (RFC 3339, UTC).
	BuildTime = "unknown"
)

// GoVersion returns the Go toolchain version the binary was built with.
func GoVersion() string { return runtime.Version() }
