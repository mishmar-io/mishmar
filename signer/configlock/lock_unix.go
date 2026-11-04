//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly

package configlock

import (
	"errors"
	"os"
	"syscall"
)

// tryLock takes an exclusive flock without waiting; false when another
// process holds it. A flock is the open file's, so it goes when the process does.
func tryLock(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return false, nil
	}
	return false, err
}

func unlock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
