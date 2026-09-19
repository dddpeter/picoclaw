//go:build !windows

package fileutil

// isTransientRenameError is always false off Windows: POSIX rename(2) is
// atomic and never fails with a sharing violation.
func isTransientRenameError(err error) bool {
	return false
}
