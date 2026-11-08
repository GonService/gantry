package cvm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SSHArgs are the ssh options to reach the machine through its vsock
// socket (no IP, no port on the network), with its own key and known_hosts.
func SSHArgs(name string) []string {
	dir := Dir(name)
	return []string{
		"-i", filepath.Join(dir, fKey),
		"-o", "IdentitiesOnly=yes",
		"-o", "ProxyCommand=/usr/bin/nc -U " + filepath.Join(dir, fSSH),
		"-o", "UserKnownHostsFile=" + filepath.Join(dir, fKnown),
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=5",
		"-o", "LogLevel=ERROR",
		"-o", "ServerAliveInterval=15",
		"gantry@" + name,
	}
}

// Run runs a shell line in the machine.
func Run(ctx context.Context, name, line string) (string, error) {
	args := append([]string{"-o", "BatchMode=yes"}, SSHArgs(name)...)
	args = append(args, "--", line)
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "ssh", args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return string(out), errors.New("ssh na máquina: " + msg)
	}
	return string(out), nil
}

// Log sources.
var logLines = map[string]string{
	"sistema":         "sudo journalctl -b --no-pager -o short-iso -n %d",
	"docker":          "sudo journalctl -b --no-pager -o short-iso -n %d -u docker.service -u containerd.service",
	"podman":          "sudo journalctl -b --no-pager -o short-iso -n %d -u podman.service -u podman.socket",
	"provisionamento": "sudo tail -n %d /var/log/gantry-provision.log",
}

// Logs returns the last lines of a source: sistema, docker, podman,
// provisionamento (in the machine) or console and vfkit (on the Mac, they
// work with the machine stopped too).
func Logs(ctx context.Context, name, src string, n int) (string, error) {
	if n <= 0 || n > 5000 {
		n = 400
	}
	switch src {
	case "console":
		return tailN(filepath.Join(Dir(name), fConsole), n), nil
	case "vfkit":
		return tailN(filepath.Join(Dir(name), fVfkitLog), n), nil
	}
	f, ok := logLines[src]
	if !ok {
		return "", errors.New("log desconhecido")
	}
	return Run(ctx, name, fmt.Sprintf(f, n))
}

func tailN(p string, n int) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	l := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(l) > n {
		l = l[len(l)-n:]
	}
	return strings.Join(l, "\n")
}

// Signal sends a signal to a process of the machine.
func Signal(ctx context.Context, name string, pid int, sig string) error {
	switch sig {
	case "TERM", "KILL", "INT", "HUP", "STOP", "CONT":
	default:
		return errors.New("sinal desconhecido")
	}
	if pid <= 1 {
		return errors.New("esse processo não pode receber sinal")
	}
	_, err := Run(ctx, name, "sudo kill -"+sig+" "+strconv.Itoa(pid))
	return err
}

// agent keeps the Mac's ssh-agent forwarded into the machine while it runs.
var agent struct {
	sync.Mutex
	cmd map[string]*fwd
}

type fwd struct {
	cmd  *exec.Cmd
	done chan struct{}
}

// ForwardAgent starts (once) the ssh -R of the Mac's ssh-agent to
// /run/host-services/ssh-auth.sock in the machine.
func ForwardAgent(name string) {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return
	}
	agent.Lock()
	defer agent.Unlock()
	if agent.cmd == nil {
		agent.cmd = map[string]*fwd{}
	}
	if f := agent.cmd[name]; f != nil {
		select {
		case <-f.done:
		default:
			return // still up
		}
	}
	args := append([]string{"-N", "-o", "BatchMode=yes", "-o", "ExitOnForwardFailure=yes",
		"-R", "/run/host-services/ssh-auth.sock:" + sock}, SSHArgs(name)...)
	c := exec.Command("ssh", args...)
	if c.Start() != nil {
		return
	}
	f := &fwd{cmd: c, done: make(chan struct{})}
	agent.cmd[name] = f
	go func() { _ = c.Wait(); close(f.done) }()
}

// StopAgent ends the forward of a machine.
func StopAgent(name string) {
	agent.Lock()
	defer agent.Unlock()
	if f := agent.cmd[name]; f != nil {
		_ = f.cmd.Process.Kill()
	}
	delete(agent.cmd, name)
}
