//go:build windows

package hosts

import "os/exec"

func setNoTTY(*exec.Cmd) {}
