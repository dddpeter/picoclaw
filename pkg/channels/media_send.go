package channels

import "fmt"

// MediaSendErr classifies a multi-part media send failure for the manager's
// retry loop (sendMediaWithRetry retries any error that is not
// ErrNotRunning/ErrSendFailed). Once any part has been delivered, retrying
// the whole batch can only duplicate the already-sent parts in the chat, so
// the error is downgraded to a permanent ErrSendFailed; with nothing
// delivered yet, the original (typically ErrTemporary) error stays
// retryable. Channels report how many parts were delivered before the
// failure.
func MediaSendErr(sent int, err error) error {
	if err == nil {
		return nil
	}
	if sent > 0 {
		return fmt.Errorf("%w (after %d part(s) already delivered; not retried to avoid duplicates): %w", ErrSendFailed, sent, err)
	}
	return err
}
