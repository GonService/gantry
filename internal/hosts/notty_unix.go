//go:build !windows

package hosts

import (
	"os/exec"
	"syscall"
)

// setNoTTY detaches ssh from the terminal so it asks through SSH_ASKPASS
// instead of a tty that nobody is looking at.
func setNoTTY(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
