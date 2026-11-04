//go:build windows

package configlock

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// The locked range is one byte at 4 GiB, past the pid the file holds:
// Windows locks are mandatory, and a lock over the pid would keep the next
// signer from reading whose it is.
func lockRange() *windows.Overlapped { return &windows.Overlapped{OffsetHigh: 1} }

// tryLock takes an exclusive LockFileEx without waiting; false when another
// process holds it. Windows releases it when the process ends.
func tryLock(f *os.File) (bool, error) {
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, lockRange())
	if err == nil {
		return true, nil
	}
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
		return false, nil
	}
	return false, err
}

func unlock(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, lockRange())
}
