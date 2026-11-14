//go:build !(darwin || linux || freebsd || netbsd || openbsd || dragonfly)

package signer

// syncDir does nothing here: Windows cannot open a directory to sync it. The
// file itself is synced before it is renamed into place.
func syncDir(string) error { return nil }
