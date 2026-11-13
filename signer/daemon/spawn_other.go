//go:build !darwin && !linux

package daemon

import "errors"

// Supported says whether a background signer runs on this platform: not on
// this one in this version; the MCP server runs the signer itself, and an
// approved order goes out only while the app is open.
const Supported = false

// ErrUnsupported is Spawn's error here.
var ErrUnsupported = errors.New("daemon: no background signer on this platform yet")

// Spawn is not built on this platform.
func Spawn(Home, string, ...string) error { return ErrUnsupported }
