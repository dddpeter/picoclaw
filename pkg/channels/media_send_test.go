package channels

import (
	"errors"
	"testing"
)

func TestMediaSendErrNothingSentStaysRetryable(t *testing.T) {
	orig := errors.New("boom")
	got := MediaSendErr(0, orig)
	if !errors.Is(got, orig) {
		t.Fatalf("sent=0 must return the original error, got %v", got)
	}
	if errors.Is(got, ErrSendFailed) {
		t.Fatalf("sent=0 must not be classified permanent, got %v", got)
	}
}

func TestMediaSendErrPartialDeliveryIsPermanent(t *testing.T) {
	orig := errors.New("feishu send media: temporary")
	got := MediaSendErr(2, orig)
	if !errors.Is(got, ErrSendFailed) {
		t.Fatalf("sent>0 must be classified ErrSendFailed so the manager does not retry, got %v", got)
	}
	if !errors.Is(got, orig) {
		t.Errorf("downgraded error should keep wrapping the cause, got %v", got)
	}
}

func TestMediaSendErrNilPassesThrough(t *testing.T) {
	if got := MediaSendErr(3, nil); got != nil {
		t.Fatalf("nil error must stay nil, got %v", got)
	}
}
