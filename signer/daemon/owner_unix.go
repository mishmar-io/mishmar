//go:build unix

package daemon

import (
	"os"
	"syscall"
)

// ownedByOther says whether a file belongs to another user than this
// process's, and whose it is.
func ownedByOther(info os.FileInfo) (int, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), int(st.Uid) != os.Getuid()
}
