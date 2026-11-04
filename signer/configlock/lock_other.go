//go:build !(darwin || linux || freebsd || netbsd || openbsd || dragonfly || windows)

package configlock

import "os"

// No file locking on this platform: every signer gets the lock.
func tryLock(*os.File) (bool, error) { return true, nil }

func unlock(*os.File) error { return nil }
