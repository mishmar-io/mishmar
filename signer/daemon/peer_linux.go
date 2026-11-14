package daemon

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerUID is the user id of the process at the other end of a Unix socket
// connection (SO_PEERCRED).
func peerUID(c *net.UnixConn) (int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return -1, err
	}
	uid, serr := -1, error(nil)
	if err := raw.Control(func(fd uintptr) {
		var cred *unix.Ucred
		cred, serr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if serr == nil {
			uid = int(cred.Uid)
		}
	}); err != nil {
		return -1, err
	}
	return uid, serr
}
