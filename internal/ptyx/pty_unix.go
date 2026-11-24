//go:build linux || darwin

package ptyx

import (
	"context"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// Start runs cmd with a new pseudo-terminal as its stdin, stdout, stderr
// and controlling terminal, in a session of its own.
func Start(cmd *exec.Cmd, cols, rows int) (*Session, error) {
	master, name, err := open()
	if err != nil {
		return nil, err
	}
	tty, err := os.OpenFile(name, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		return nil, err
	}
	defer tty.Close()
	if cols > 0 && rows > 0 {
		_ = setSize(master, cols, rows)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
	cmd.SysProcAttr.Setctty = true
	cmd.SysProcAttr.Ctty = 0 // the child's stdin
	if err := cmd.Start(); err != nil {
		master.Close()
		return nil, err
	}
	return &Session{master: master, cmd: cmd}, nil
}

// Resize gives the terminal a new size; the program gets SIGWINCH.
func (s *Session) Resize(_ context.Context, cols, rows int) error {
	return setSize(s.master, cols, rows)
}

func setSize(f *os.File, cols, rows int) error {
	return unix.IoctlSetWinsize(int(f.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: uint16(rows), Col: uint16(cols)})
}
