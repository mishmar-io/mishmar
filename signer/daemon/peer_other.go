//go:build !darwin && !linux

package daemon

import (
	"errors"
	"net"
)

// peerUID is not built on this platform: every connection is refused, so
// the daemon is of no use here and the MCP server runs the signer itself.
func peerUID(*net.UnixConn) (int, error) {
	return -1, errors.New("daemon: no peer credentials on this platform")
}
