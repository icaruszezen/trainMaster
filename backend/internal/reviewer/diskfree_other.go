//go:build !windows && !linux && !darwin

package reviewer

// diskFree cannot tell the free space here: the check is skipped.
func diskFree(string) (int64, bool) { return 0, false }
