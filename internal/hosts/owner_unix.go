//go:build !windows

package hosts

import (
	"os"
	"syscall"
)

// ownedByMe says the file belongs to the user running Gantry.
func ownedByMe(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
