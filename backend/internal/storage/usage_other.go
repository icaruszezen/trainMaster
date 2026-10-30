//go:build !linux

package storage

// diskUsage is only implemented on Linux, the platform the service is
// deployed on; elsewhere the API reports zero capacity.
func diskUsage(string) (Usage, error) { return Usage{}, ErrUsageUnsupported }
