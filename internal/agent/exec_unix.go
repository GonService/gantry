//go:build !windows

package agent

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// runShell runs st.Line with sh -c in its own process group, killed whole
// when ctx ends (a tail -f, a compose up…).
func runShell(ctx context.Context, st execStart, stdin io.Reader, stdout, stderr io.Writer) (int, string) {
	cmd := exec.Command(loginShell(), "-c", st.Line)
	home, _ := os.UserHomeDir()
	cmd.Dir = st.Dir
	if cmd.Dir == "" {
		cmd.Dir = home
	}
	// The service starts with systemd's short PATH; ssh would also see the
	// user's own bin folders.
	env := os.Environ()
	if home != "" {
		for i, kv := range env {
			if strings.HasPrefix(kv, "PATH=") {
				env[i] = "PATH=" + home + "/.local/bin:" + home + "/bin:" + kv[5:]
			}
		}
	}
	cmd.Env = append(env, st.Env...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// A program left in the background with the output still open (a nohup
	// without redirect) does not hold the command after the shell ends.
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return 127, err.Error()
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		case <-done:
		}
	}()
	err := cmd.Wait()
	close(done)
	if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil {
		err = nil
		if c := cmd.ProcessState.ExitCode(); c != 0 {
			return c, ""
		}
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal()), ""
		}
		return ee.ExitCode(), ""
	}
	if err != nil {
		return 127, err.Error()
	}
	return 0, ""
}

// loginShell is the shell ssh would use: the user's own, else bash, else sh.
func loginShell() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		if _, err := os.Stat(sh); err == nil {
			return sh
		}
	}
	if p, err := exec.LookPath("bash"); err == nil {
		return p
	}
	return "sh"
}
