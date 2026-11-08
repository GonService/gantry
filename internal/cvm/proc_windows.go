//go:build windows

package cvm

import (
	"os"
	"os/exec"
)

func detach(cmd *exec.Cmd) {}

func alive(pid int) bool { return false }

func kill(pid int) {
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}

func occupied(fi os.FileInfo) int64 { return fi.Size() }
