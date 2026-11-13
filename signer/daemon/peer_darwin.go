package daemon

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerUID is the user id of the process at the other end of a Unix socket
// connection (getpeereid: LOCAL_PEERCRED on macOS).
func peerUID(c *net.UnixConn) (int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return -1, err
	}
	uid, serr := -1, error(nil)
	if err := raw.Control(func(fd uintptr) {
		var cred *unix.Xucred
		cred, serr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if serr == nil {
			uid = int(cred.Uid)
		}
	}); err != nil {
		return -1, err
	}
	return uid, serr
}
