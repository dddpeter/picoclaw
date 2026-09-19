package fileutil

import (
	"os"
	"sync"
	"time"
)

// renameMu serializes atomic replacements within this process. Concurrent
// renames over the same target keep hitting each other's delete-pending
// windows, which Windows surfaces as spurious "Access is denied"; with
// in-process writers serialized, retries only need to absorb cross-process
// transients (antivirus/indexer scans). The critical section is a single
// rename syscall, so serialization costs nothing at tool-call frequencies.
var renameMu sync.Mutex

const (
	// renameMaxRetries bounds retries for transient Windows sharing/lock
	// violations; with the 5ms base delay the worst case wait is ~155ms.
	renameMaxRetries = 5
	renameRetryDelay = 5 * time.Millisecond
)

// RenameWithRetry renames oldpath to newpath, retrying transient Windows
// sharing violations (target briefly held open by an editor, antivirus or
// indexer) with exponential backoff instead of failing the whole write.
func RenameWithRetry(oldpath, newpath string) error {
	return WithTransientRenameRetry(func() error {
		return os.Rename(oldpath, newpath)
	})
}

// WithTransientRenameRetry runs fn, retrying while it fails with a transient
// Windows rename error (sharing, lock, or access-denied violation).
func WithTransientRenameRetry(fn func() error) error {
	renameMu.Lock()
	defer renameMu.Unlock()

	var err error
	for attempt := 0; ; attempt++ {
		err = fn()
		if err == nil || !isTransientRenameError(err) {
			return err
		}
		if attempt >= renameMaxRetries {
			return err
		}
		time.Sleep(renameRetryDelay << attempt)
	}
}
