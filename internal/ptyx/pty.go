// Package ptyx starts a command on a pseudo-terminal of this computer, so a
// terminal of the app can drive a program that wants a real tty (an ssh -t
// to a server): keystrokes in, output out and the window size followed.
package ptyx

import (
	"errors"
	"os"
	"os/exec"
	"sync"
)

// ErrUnsupported is the answer where there are no pseudo-terminals (Windows).
var ErrUnsupported = errors.New("terminal local indisponível neste sistema")

// Session is a command running on a pseudo-terminal.
type Session struct {
	master *os.File
	cmd    *exec.Cmd
	once   sync.Once
}

func (s *Session) Read(p []byte) (int, error)  { return s.master.Read(p) }
func (s *Session) Write(p []byte) (int, error) { return s.master.Write(p) }

// Close ends the command and frees the terminal.
func (s *Session) Close() error {
	s.once.Do(func() {
		_ = s.master.Close()
		if s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
			_ = s.cmd.Wait()
		}
	})
	return nil
}
