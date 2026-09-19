//go:build !windows

package fstools

// validateWritePath is a no-op off Windows: reserved device names, trailing
// dots/spaces, and alternate data streams are Windows-only concerns, and
// Unix filesystems legitimately host files named "NUL" or "a:b".
func validateWritePath(path string) error {
	return nil
}

// validateReadPath is a no-op off Windows; see validateWritePath.
func validateReadPath(path string) error {
	return nil
}
