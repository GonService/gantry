//go:build windows

package hosts

import "os"

// ownedByMe: on Windows the folder lives in the user's own temp folder.
func ownedByMe(os.FileInfo) bool { return true }
