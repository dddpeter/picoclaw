//go:build windows

package fileutil

import (
	"errors"
	"syscall"

	"golang.org/x/sys/windows"
)

// isTransientRenameError reports whether err is a transient Windows sharing,
// lock, or access-denied violation — the target (or its directory) was
// momentarily held open by another process (editor, antivirus, indexer),
// which a short backoff usually resolves.
func isTransientRenameError(err error) bool {
	var errno syscall.Errno
	return errors.As(err, &errno) &&
		(errno == windows.ERROR_SHARING_VIOLATION ||
			errno == windows.ERROR_LOCK_VIOLATION ||
			errno == windows.ERROR_ACCESS_DENIED)
}
