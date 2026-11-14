//go:build !unix

package daemon

import "os"

// ownedByOther cannot tell on this platform: the user profile is the
// user's alone by default.
func ownedByOther(os.FileInfo) (int, bool) { return 0, false }
