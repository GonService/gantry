package cvm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// States of a machine.
const (
	StateMissing  = "missing"
	StateStopped  = "stopped"
	StateStarting = "starting"
	StateRunning  = "running"
	StateStopping = "stopping"
	StatePaused   = "paused"
)

// rest calls the machine's control API (state and stop) on its unix socket.
func rest(ctx context.Context, name, method, body string) ([]byte, error) {
	return restPath(ctx, name, method, "/vm/state", body)
}

func restPath(ctx context.Context, name, method, path, body string) ([]byte, error) {
	sock := filepath.Join(Dir(name), fREST)
	c := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}, Timeout: 5 * time.Second}
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, _ := http.NewRequestWithContext(ctx, method, "http://cvm"+path, rd)
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return b, fmt.Errorf("máquina: %s %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return b, nil
}

func pid(name string) int {
	b, err := os.ReadFile(filepath.Join(Dir(name), fPID))
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if n <= 0 || !alive(n) {
		return 0
	}
	return n
}

// State asks the machine's process how the machine is.
func State(ctx context.Context, name string) string {
	if !Exists(name) {
		return StateMissing
	}
	if pid(name) == 0 {
		return StateStopped
	}
	b, err := rest(ctx, name, http.MethodGet, "")
	if err != nil {
		return StateStarting // the process is up, the API not yet
	}
	var st struct{ State string }
	_ = json.Unmarshal(b, &st)
	switch s := strings.ToLower(st.State); {
	case strings.Contains(s, "running"):
		return StateRunning
	case strings.Contains(s, "starting"):
		return StateStarting
	case strings.Contains(s, "stopping"):
		return StateStopping
	case strings.Contains(s, "paused"):
		return StatePaused
	case strings.Contains(s, "stopped"), strings.Contains(s, "error"):
		return StateStopped
	}
	return StateStarting
}

// RunnerName is the helper that runs the machines: a binary of its own,
// next to the app's (Gantry.app/Contents/MacOS), because Apple's
// Virtualization binding and the window's toolkit cannot share one binary
// (both define an AppDelegate), and only it needs the virtualization
// entitlement.
const RunnerName = "gantry-vm"

// Runner finds the helper ("" if this build has none).
func Runner() string {
	if exe, err := os.Executable(); err == nil {
		if p, err := filepath.EvalSymlinks(exe); err == nil {
			exe = p
		}
		if p := filepath.Join(filepath.Dir(exe), RunnerName); isFile(p) {
			return p
		}
	}
	if p, err := exec.LookPath(RunnerName); err == nil {
		return p
	}
	return ""
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// Start boots the machine. It runs in a process of its own (the helper),
// on its own session, so it stays up when the app closes.
func Start(ctx context.Context, name string, log io.Writer) error {
	s, err := Load(name)
	if err != nil {
		return err
	}
	if pid(name) != 0 {
		return nil
	}
	exe := Runner()
	if exe == "" {
		return errNoRunner
	}
	dir := Dir(name)
	home, _ := os.UserHomeDir()
	for _, p := range []string{dir, home} {
		if err := sanitize(p); err != nil {
			return err
		}
	}
	// The sockets of a machine that did not stop cleanly would be in the way.
	for _, f := range []string{fREST, fSSH, fDocker, fPodman} {
		_ = os.Remove(filepath.Join(dir, f))
	}
	lf, err := os.OpenFile(filepath.Join(dir, fVMLog), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	cmd := exec.Command(exe, name)
	cmd.Stdout, cmd.Stderr = lf, lf
	cmd.Dir = dir
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("máquina: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, fPID), []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o600); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	fmt.Fprintf(log, "Máquina iniciada (pid %d): %d CPU, %d MiB\n", cmd.Process.Pid, s.CPUs, s.MemoryMiB)
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			return fmt.Errorf("a máquina parou ao iniciar (%v): %s", err, tail(filepath.Join(dir, fVMLog), 6))
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
		if State(ctx, name) == StateRunning {
			return nil
		}
	}
	return errors.New("a máquina não respondeu em 45 s: " + tail(filepath.Join(dir, fVMLog), 6))
}

var errNoRunner = errors.New("falta o gantry-vm ao lado do app (Gantry.app/Contents/MacOS); use o app do Mac completo, ou make desktop")

// Memory is the state of the machine's dynamic memory (nil when it is off).
func Memory(ctx context.Context, name string) (*MemoryState, error) {
	if pid(name) == 0 {
		return nil, nil
	}
	b, err := restPath(ctx, name, http.MethodGet, "/vm/memoria", "")
	if err != nil {
		return nil, err
	}
	var m MemoryState
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// Stop asks the guest to power off, and forces it after a while.
func Stop(ctx context.Context, name string, log io.Writer) error {
	p := pid(name)
	if p == 0 {
		return nil
	}
	StopWatch(name)
	UnmountFinder(name)
	if _, err := rest(ctx, name, http.MethodPost, `{"state":"Stop"}`); err != nil {
		fmt.Fprintf(log, "pedido de desligar: %v\n", err)
	} else {
		fmt.Fprintln(log, "Pedindo para a máquina desligar…")
	}
	if waitGone(ctx, p, 40*time.Second) {
		return cleanup(name)
	}
	fmt.Fprintln(log, "A máquina não desligou em 40 s; forçando.")
	_, _ = rest(ctx, name, http.MethodPost, `{"state":"HardStop"}`)
	if waitGone(ctx, p, 10*time.Second) {
		return cleanup(name)
	}
	kill(p)
	waitGone(ctx, p, 5*time.Second)
	return cleanup(name)
}

func cleanup(name string) error {
	for _, f := range []string{fPID, fREST, fSSH, fDocker, fPodman} {
		_ = os.Remove(filepath.Join(Dir(name), f))
	}
	return nil
}

func waitGone(ctx context.Context, p int, d time.Duration) bool {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if !alive(p) {
			return true
		}
		select {
		case <-ctx.Done():
			return !alive(p)
		case <-time.After(300 * time.Millisecond):
		}
	}
	return !alive(p)
}

// tail is the last n lines of a file, joined.
func tail(p string, n int) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	l := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(l) > n {
		l = l[len(l)-n:]
	}
	return strings.Join(l, " · ")
}

// Delete stops the machine and removes its folder (disks included).
func Delete(ctx context.Context, name string, log io.Writer) error {
	if err := Stop(ctx, name, log); err != nil {
		return err
	}
	if !nameRe.MatchString(name) {
		return errors.New("nome inválido")
	}
	return os.RemoveAll(Dir(name))
}

// Grow makes the data disk bigger (never smaller); the guest takes the new
// size on the next boot.
func Grow(name string, gib int) error {
	p := filepath.Join(Dir(name), fData)
	st, err := os.Stat(p)
	if err != nil {
		return err
	}
	if int64(gib)<<30 < st.Size() {
		return errors.New("o disco de dados só aumenta")
	}
	return os.Truncate(p, int64(gib)<<30)
}

// DiskUse is one disk file: its nominal size and what it takes on the Mac
// (the files are sparse).
type DiskUse struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Occupied int64  `json:"occupied"`
}

// followConsole streams the provisioning lines ("gantry: …") the guest
// writes to the serial console, until the end mark or ctx ends.
func followConsole(ctx context.Context, name string, log io.Writer, timeout time.Duration) (int, error) {
	p := filepath.Join(Dir(name), fConsole)
	var off int64
	end := time.Now().Add(timeout)
	var partial []byte
	for time.Now().Before(end) {
		select {
		case <-ctx.Done():
			return -1, ctx.Err()
		case <-time.After(time.Second):
		}
		if pid(name) == 0 {
			return -1, errors.New("a máquina parou durante a preparação: " + tail(filepath.Join(Dir(name), fVMLog), 6))
		}
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		_, _ = f.Seek(off, io.SeekStart)
		b, _ := io.ReadAll(f)
		f.Close()
		off += int64(len(b))
		partial = append(partial, b...)
		cut := bytes.LastIndexByte(partial, '\n')
		if cut < 0 {
			continue
		}
		whole := partial[:cut]
		partial = append([]byte(nil), partial[cut+1:]...)
		sc := bufio.NewScanner(bytes.NewReader(whole))
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			l := strings.TrimSpace(strings.TrimRight(sc.Text(), "\r"))
			i := strings.Index(l, "gantry: ")
			if i < 0 {
				continue
			}
			msg := l[i+len("gantry: "):]
			if rc, ok := strings.CutPrefix(msg, "fim rc="); ok {
				n, _ := strconv.Atoi(strings.TrimSpace(rc))
				return n, nil
			}
			fmt.Fprintf(log, "  %s\n", msg)
		}
	}
	return -1, errors.New("a preparação passou de " + timeout.String())
}
