// Package configlock keeps one long-running signer per agent config: the
// signer holds an exclusive lock on <config>.lock for as long as it runs, and
// a second one started on the same config fails at once, naming the first.
// Two signers on one agent would both poll the engine with its key, and one
// left behind by a bot that died keeps the agent online after the bot is gone.
//
// The lock is the operating system's (flock on Unix, LockFileEx on Windows),
// so it goes with the process however the process ends; the file stays and
// holds the last holder's pid for the message. No cgo, so it builds anywhere.
package configlock

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
)

// Path is where the lock for the config at config lives: next to it.
func Path(config string) string { return config + ".lock" }

// LockedError says another process holds the config's lock.
type LockedError struct {
	Config string
	PID    int // 0 when the holder's pid could not be read
}

func (e *LockedError) Error() string {
	who := "another signer is running"
	if e.PID > 0 {
		who = fmt.Sprintf("another signer is running with %s (pid %d)", e.Config, e.PID)
	} else {
		who += " with " + e.Config
	}
	return who + ": stop it, or attach to it (Signer.connect in the SDKs) instead of starting a second"
}

// Lock is a held lock. Close releases it.
type Lock struct {
	f *os.File
}

// Acquire takes the lock for config, or returns a *LockedError when another
// process holds it. The lock file is created with mode 0600 if missing.
func Acquire(config string) (*Lock, error) {
	f, err := os.OpenFile(Path(config), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("signer: the config's lock: %w", err)
	}
	got, err := tryLock(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("signer: locking %s: %w", Path(config), err)
	}
	if !got {
		pid := readPID(f)
		f.Close()
		return nil, &LockedError{Config: config, PID: pid}
	}
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}
	return &Lock{f: f}, nil
}

// Close releases the lock. The file stays: removing it would race a process
// that opened it a moment before.
func (l *Lock) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := unlock(l.f)
	if cerr := l.f.Close(); err == nil {
		err = cerr
	}
	l.f = nil
	return err
}

// Holder says whether a process holds the lock for config, and its pid when
// it could be read. A config never locked has no holder.
func Holder(config string) (pid int, held bool, err error) {
	f, err := os.OpenFile(Path(config), os.O_RDWR, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, false, nil
		}
		return 0, false, err
	}
	defer f.Close()
	got, err := tryLock(f)
	if err != nil {
		return 0, false, err
	}
	if got {
		return 0, false, unlock(f)
	}
	return readPID(f), true, nil
}

func readPID(f *os.File) int {
	buf := make([]byte, 32)
	n, _ := f.ReadAt(buf, 0)
	pid, err := strconv.Atoi(string(bytes.TrimSpace(buf[:n])))
	if err != nil {
		return 0
	}
	return pid
}
