//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly

package signer

import "os"

// syncDir syncs a directory, so a file renamed into it stays there after a
// crash.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}
