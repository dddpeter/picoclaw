//go:build windows

package fileutil

import (
	"os"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWithTransientRenameRetry_RetriesThenSucceeds(t *testing.T) {
	attempts := 0
	err := WithTransientRenameRetry(func() error {
		attempts++
		if attempts <= 2 {
			return &os.PathError{Op: "rename", Err: syscall.Errno(windows.ERROR_SHARING_VIOLATION)}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
}

func TestWithTransientRenameRetry_GivesUpAfterMaxRetries(t *testing.T) {
	attempts := 0
	err := WithTransientRenameRetry(func() error {
		attempts++
		return &os.PathError{Op: "rename", Err: syscall.Errno(windows.ERROR_LOCK_VIOLATION)}
	})
	if err == nil {
		t.Fatal("err = nil, want persistent lock violation")
	}
	if attempts != renameMaxRetries+1 {
		t.Fatalf("attempts = %d, want %d", attempts, renameMaxRetries+1)
	}
}

func TestWithTransientRenameRetry_NonTransientFailsImmediately(t *testing.T) {
	attempts := 0
	err := WithTransientRenameRetry(func() error {
		attempts++
		return os.ErrNotExist
	})
	if err == nil || attempts != 1 {
		t.Fatalf("err = %v, attempts = %d; want immediate failure", err, attempts)
	}
}

func TestIsTransientRenameError_ClassifiesWindowsErrnos(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{&os.PathError{Err: syscall.Errno(windows.ERROR_SHARING_VIOLATION)}, true},
		{&os.PathError{Err: syscall.Errno(windows.ERROR_LOCK_VIOLATION)}, true},
		{&os.PathError{Err: syscall.Errno(windows.ERROR_ACCESS_DENIED)}, true},
		{&os.PathError{Err: syscall.Errno(windows.ERROR_FILE_NOT_FOUND)}, false},
		{os.ErrNotExist, false},
	}
	for i, tc := range cases {
		if got := isTransientRenameError(tc.err); got != tc.want {
			t.Errorf("case %d: isTransientRenameError(%v) = %v, want %v", i, tc.err, got, tc.want)
		}
	}
}
