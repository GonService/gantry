// Package hosts connects to local and remote engines and runs commands on them.
// Remote hosts use the system ssh binary, so ~/.ssh/config, the agent,
// known_hosts and jump hosts work as they do in the terminal.
package hosts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gantry/internal/agent"
	"gantry/internal/applog"
	"gantry/internal/config"
	"gantry/internal/engine"
	"gantry/internal/shim"
)

type Kind string

const (
	Unix Kind = "unix"
	TCP  Kind = "tcp"
	SSH  Kind = "ssh"
	WSL  Kind = "wsl" // a WSL2 distribution on Windows, through wsl.exe
)

// Remote tells whether the host's files and commands are on another system
// (reached by commands, not by the local file system).
func (h *Host) Remote() bool { return h.Kind == SSH || h.Kind == WSL }

type Host struct {
	Name    string
	URL     string
	Source  string
	Kind    Kind
	Engine  *engine.Client
	Agent   *agent.Client // nil unless the host is reached over ssh
	compose []string

	// agentExec: the agent runs the commands (protocol 3), so they take no
	// SSH session; set when the app learns the agent's version.
	agentExec atomic.Bool

	path        string // unix socket or tcp address
	dest        string // ssh destination
	remote      string // engine socket on the ssh host
	remoteFixed bool   // socket given in the config, not detected
	fix         *Fix
	idle        *exec.Cmd // logged-in master kept for running a fix
	idleDone    chan struct{}

	mu          sync.Mutex
	tunnel      *exec.Cmd
	tunDone     chan struct{}
	tunSock     string
	agentSock   string                  // local end of the agent socket forward
	agentRemote string                  // agent socket on the server
	sockets     []string                // every usable engine socket found on the server
	extra       map[string]*extraEngine // other engines on the server, by socket
	tcpFwd      map[string]*tcpForward  // TCP services on the server, by address
	id          string
	ctlPath     string
	tunError    string

	wanted      bool          // remote hosts connect only after being opened
	parked      time.Time     // when the user disconnected it (see Want)
	attempt     chan struct{} // open while connecting
	attemptCmd  *exec.Cmd
	lastErr     error
	lastFail    time.Time
	canceled    bool
	manualRetry bool

	rtMu        sync.Mutex
	runtime     string
	composeHint []string // from a shimmed engine (nerdctl compose)

	shim     *shim.Server // CLI runtimes (nerdctl, k3s) served as a Docker API
	shimOnce sync.Once
	shimErr  error

	wsl *wslState // wsl:// hosts

	// Direct connection to the agent (TCP + mutual TLS), once paired. SSH
	// stays for commands and as the fallback when the port is unreachable.
	directMu   sync.Mutex
	directAddr string
	directTLS  *tls.Config
	directDown time.Time // last failure; SSH is used for a while after it
	agentVia   string    // "tls" or "ssh": how the agent was last reached
	engineVia  string
}

// directRetry is how long SSH is used after a direct connection failed.
const directRetry = time.Minute

// SetDirect enables (cfg != nil) or disables the direct connection.
func (h *Host) SetDirect(addr string, cfg *tls.Config) {
	h.directMu.Lock()
	h.directAddr, h.directTLS, h.directDown = addr, cfg, time.Time{}
	h.directMu.Unlock()
	h.closeIdle()
}

// closeIdle makes the next requests dial again, picking the current way.
func (h *Host) closeIdle() {
	if h.Agent != nil {
		h.Agent.CloseIdle()
	}
	if h.Engine != nil {
		h.Engine.CloseIdle()
	}
}

// ResetDirect forgets a recent failure, so the next call tries again.
func (h *Host) ResetDirect() {
	h.directMu.Lock()
	h.directDown = time.Time{}
	h.directMu.Unlock()
	h.closeIdle()
}

// Direct returns the paired address, if any.
func (h *Host) Direct() string {
	h.directMu.Lock()
	defer h.directMu.Unlock()
	if h.directTLS == nil {
		return ""
	}
	return h.directAddr
}

// Via tells how the agent and the engine API were last reached.
func (h *Host) Via() (agentVia, engineVia string) {
	h.directMu.Lock()
	defer h.directMu.Unlock()
	return h.agentVia, h.engineVia
}

// ServerName is the host name or address of an ssh:// host.
func (h *Host) ServerName() string {
	if h.Kind == WSL {
		return "127.0.0.1" // WSL2 forwards its localhost ports to Windows
	}
	if h.Kind != SSH {
		return ""
	}
	u, err := url.Parse(h.dest)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// tryDirect dials the agent directly, unless it is off or failed recently.
func (h *Host) tryDirect(ctx context.Context, engineSock string, engine bool) (net.Conn, bool) {
	h.directMu.Lock()
	cfg, addr, down := h.directTLS, h.directAddr, h.directDown
	h.directMu.Unlock()
	if cfg == nil || addr == "" || (h.Kind != WSL && time.Since(down) < directRetry) {
		return nil, false
	}
	c, err := agent.DialDirect(ctx, addr, cfg, engineSock, engine)
	h.directMu.Lock()
	defer h.directMu.Unlock()
	if err != nil {
		if ctx.Err() == nil {
			h.directDown = time.Now()
		}
		return nil, false
	}
	if engine {
		h.engineVia = "tls"
	} else {
		h.agentVia = "tls"
	}
	return c, true
}

func (h *Host) setVia(engine bool, v string) {
	h.directMu.Lock()
	if engine {
		h.engineVia = v
	} else {
		h.agentVia = v
	}
	h.directMu.Unlock()
}

func New(c config.Host) (*Host, error) {
	h := &Host{Name: c.Name, URL: c.URL, Source: c.Source, compose: c.Compose}
	switch {
	case strings.HasPrefix(c.URL, "unix://"):
		h.Kind = Unix
		h.path = strings.TrimPrefix(c.URL, "unix://")
	case strings.HasPrefix(c.URL, "tcp://"):
		h.Kind = TCP
		u, err := url.Parse(c.URL)
		if err != nil {
			return nil, err
		}
		h.path = u.Host
	case strings.HasPrefix(c.URL, "ssh://"):
		h.Kind = SSH
		h.dest = c.URL
		// Empty means: find the engine socket on the server when connecting.
		h.remote = c.Socket
		h.remoteFixed = c.Socket != ""
		sum := sha256.Sum256([]byte(c.Name + "\x00" + c.URL))
		id := hex.EncodeToString(sum[:5])
		dir := tunnelDir()
		h.tunSock = filepath.Join(dir, id+".sock")
		h.ctlPath = filepath.Join(dir, id+".ctl")
		h.agentSock = filepath.Join(dir, id+"-a.sock")
		h.id = id
	case strings.HasPrefix(c.URL, "wsl://"):
		h.Kind = WSL
		d := strings.TrimPrefix(c.URL, "wsl://")
		if i := strings.IndexAny(d, "/?"); i >= 0 {
			d = d[:i]
		}
		if d == "" {
			return nil, errors.New("informe a distribuição: wsl://Ubuntu")
		}
		h.wsl = &wslState{distro: d}
		h.remote = c.Socket
		h.remoteFixed = c.Socket != ""
	case strings.HasPrefix(c.URL, "nerdctl://"), strings.HasPrefix(c.URL, "k3s://"):
		d, compose, err := shim.FromURL(c.URL)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256([]byte(c.Name + "\x00" + c.URL))
		h.Kind = Unix
		h.path = filepath.Join(tunnelDir(), hex.EncodeToString(sum[:5])+"-shim.sock")
		h.shim = shim.New(d) // starts listening on the first use
		if len(h.compose) == 0 {
			h.compose = compose
		}
	default:
		return nil, fmt.Errorf("URL não suportada: %q (use unix://, tcp://, ssh://, nerdctl:// ou k3s://)", c.URL)
	}
	h.Engine = engine.New(h.dial)
	if h.Kind == SSH || h.Kind == WSL {
		h.Agent = agent.NewClient(h.dialAgent)
	}
	return h, nil
}

var (
	tunnelOnce sync.Once
	tunnelPath string
)

// tunnelDir holds the SSH control sockets and the forwarded engine and
// agent sockets. Unix socket paths are short on macOS, so it stays out of
// $TMPDIR, in /tmp, which other users can write to: the folder must be a
// real folder of this user, closed to others. If someone else made it (to
// put their own sockets in the way), Gantry uses a new, private one.
func tunnelDir() string {
	tunnelOnce.Do(func() {
		base := "/tmp"
		if runtime.GOOS == "windows" {
			base = os.TempDir()
		}
		d := filepath.Join(base, "gantry-"+strconv.Itoa(os.Getuid()))
		if safeDir(d) {
			tunnelPath = d
			return
		}
		slog.Warn("pasta de túneis de outro usuário ou aberta; usando uma nova", "pasta", d)
		if nd, err := os.MkdirTemp(base, "gantry-"+strconv.Itoa(os.Getuid())+"-"); err == nil {
			tunnelPath = nd
			return
		}
		tunnelPath = d
	})
	return tunnelPath
}

func safeDir(d string) bool {
	_ = os.Mkdir(d, 0o700)
	fi, err := os.Lstat(d)
	if err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 || !ownedByMe(fi) {
		return false
	}
	if fi.Mode().Perm()&0o077 != 0 {
		// Ours, but open to others (made by an old version or by hand).
		if os.Chmod(d, 0o700) != nil {
			return false
		}
	}
	return true
}

func (h *Host) dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	switch h.Kind {
	case Unix:
		if h.shim != nil {
			h.shimOnce.Do(func() { h.shimErr = h.shim.Listen(h.path) })
			if h.shimErr != nil {
				return nil, h.shimErr
			}
		}
		return d.DialContext(ctx, "unix", h.path)
	case TCP:
		if !h.Wanted() {
			return nil, ErrIdle
		}
		return d.DialContext(ctx, "tcp", h.path)
	case WSL:
		c, err := h.wslDirect(ctx, true)
		if err == nil {
			h.setVia(true, "tls")
		}
		return c, err
	default:
		// Once connected over SSH the engine socket is known; the API then
		// goes straight to the agent when paired.
		h.mu.Lock()
		remote, up := h.remote, h.tunnel != nil
		h.mu.Unlock()
		if up && remote != "" {
			if c, ok := h.tryDirect(ctx, remote, true); ok {
				return c, nil
			}
		}
		if err := h.ensureTunnel(ctx); err != nil {
			return nil, err
		}
		h.setVia(true, "ssh")
		return d.DialContext(ctx, "unix", h.tunSock)
	}
}

func (h *Host) dialAgent(ctx context.Context) (net.Conn, error) {
	if !h.Wanted() {
		return nil, ErrIdle
	}
	if h.Kind == WSL {
		return h.wslDirect(ctx, false)
	}
	if c, ok := h.tryDirect(ctx, "", false); ok {
		return c, nil
	}
	if err := h.ensureTunnel(ctx); err != nil {
		return nil, err
	}
	h.setVia(false, "ssh")
	h.mu.Lock()
	sock := h.agentSock
	fwd := h.agentRemote != ""
	h.mu.Unlock()
	if !fwd {
		if err := h.forwardAgent(ctx); err != nil {
			return nil, err
		}
	}
	var d net.Dialer
	return d.DialContext(ctx, "unix", sock)
}

// forwardAgent forwards the agent socket of the server, whether or not the
// agent is running yet: ssh only connects to it when the app does.
func (h *Host) forwardAgent(ctx context.Context) error {
	out, err := h.ctl(ctx, "sh -c "+shq(agent.SocketScript))
	if err != nil {
		return err
	}
	remote := strings.TrimSpace(out)
	_ = os.Remove(h.agentSock)
	fwd := exec.CommandContext(ctx, "ssh", "-S", h.ctlPath, "-o", "BatchMode=yes", "-O", "forward", "-L", h.agentSock+":"+remote, h.dest)
	if b, err := fwd.CombinedOutput(); err != nil {
		return fmt.Errorf("ssh: não consegui encaminhar o socket do agente: %s", strings.TrimSpace(string(b)))
	}
	h.mu.Lock()
	h.agentRemote = remote
	h.mu.Unlock()
	return nil
}

// Files reports whether compose files on this host can be read and written.
func (h *Host) Files() bool { return h.Kind != TCP }

// SetComposeHint records the compose command the engine asked for.
func (h *Host) SetComposeHint(c []string) {
	h.rtMu.Lock()
	h.composeHint = c
	h.rtMu.Unlock()
}

// SetRuntime records what the engine reported, docker or podman.
func (h *Host) SetRuntime(rt string) {
	h.rtMu.Lock()
	h.runtime = rt
	h.rtMu.Unlock()
}

func (h *Host) Runtime() string {
	h.rtMu.Lock()
	defer h.rtMu.Unlock()
	return h.runtime
}

// Compose returns the compose command for this host.
func (h *Host) Compose() []string {
	if len(h.compose) > 0 {
		return h.compose
	}
	h.rtMu.Lock()
	hint := h.composeHint
	h.rtMu.Unlock()
	if len(hint) > 0 {
		return hint
	}
	if h.Runtime() == "podman" {
		return []string{"podman", "compose"}
	}
	return []string{"docker", "compose"}
}

// AskpassEnv, when set, returns extra environment for ssh so that password
// and passphrase prompts reach the user through the app (SSH_ASKPASS).
var AskpassEnv func(host string) []string

// OnTunnelFailed is called when opening a tunnel fails, e.g. to drop cached
// passwords that may be wrong.
var OnTunnelFailed func(host string)

// OnTunnelUp is called when a tunnel opens.
var OnTunnelUp func(host string)

const retryAfter = 45 * time.Second

// ensureTunnel makes sure the ssh tunnel is up. The connection attempt runs
// on its own, so a password prompt can wait for the user longer than the
// request that triggered it.
func (h *Host) ensureTunnel(ctx context.Context) error {
	h.mu.Lock()
	if !h.wanted {
		h.mu.Unlock()
		return ErrIdle
	}
	if h.tunnel != nil {
		select {
		case <-h.tunDone:
			h.tunnel = nil
		default:
			h.mu.Unlock()
			return nil
		}
	}
	if h.attempt == nil {
		// Auth and server-side problems wait for the user (Atualizar), so a
		// wrong password is not asked again and again; network errors retry.
		if h.lastErr != nil && (h.manualRetry || time.Since(h.lastFail) < retryAfter) {
			err := h.lastErr
			h.mu.Unlock()
			return err
		}
		h.attempt = make(chan struct{})
		go func() { defer applog.Recover("hosts/hosts.go:434"); h.connect(h.attempt) }()
	}
	ch := h.attempt
	h.mu.Unlock()

	select {
	case <-ch:
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.tunnel != nil {
			return nil
		}
		return h.lastErr
	case <-ctx.Done():
		return errors.New("ainda conectando via ssh; se o servidor pedir senha, responda na janela de autenticação")
	}
}

// ErrIdle is returned for remote hosts nobody opened yet: they connect
// only when the user asks, not when the app starts.
var ErrIdle = errors.New("não conectado: abra o host para conectar")

// parkFor is how long a host the user just disconnected ignores Want:
// requests already on the way when they disconnected (a refresh of the
// page, a stream reconnecting) must not open the connection again.
const parkFor = 5 * time.Second

// Want allows the host to connect (the user opened it).
func (h *Host) Want() {
	h.mu.Lock()
	if time.Since(h.parked) >= parkFor {
		h.wanted = true
	}
	h.mu.Unlock()
}

// Wanted reports whether a remote host may connect. Local ones always may.
func (h *Host) Wanted() bool {
	if h.Kind == Unix {
		return true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.wanted
}

// Disconnect closes the connection and keeps the host idle until opened again.
func (h *Host) Disconnect() {
	h.mu.Lock()
	h.wanted = false
	h.lastErr = nil
	h.parked = time.Now()
	h.mu.Unlock()
	h.Close()
}

// Machine identifies the computer behind the host, so the engines of one
// server (Docker, Podman, nerdctl) can be shown together.
func (h *Host) Machine() string {
	switch h.Kind {
	case SSH:
		u, err := url.Parse(h.dest)
		if err != nil {
			return "ssh:" + h.dest
		}
		port := u.Port()
		if port == "" {
			port = "22"
		}
		user := ""
		if u.User != nil {
			user = u.User.Username() + "@"
		}
		return "ssh:" + user + u.Hostname() + ":" + port
	case TCP:
		host, _, err := net.SplitHostPort(h.path)
		if err != nil {
			host = h.path
		}
		return "tcp:" + host
	case WSL:
		return "wsl:" + h.wsl.distro
	}
	return "local"
}

// Retry clears the backoff after a failed connection.
func (h *Host) Retry() {
	h.mu.Lock()
	h.lastErr, h.manualRetry = nil, false
	h.parked, h.wanted = time.Time{}, true
	h.mu.Unlock()
}

// CancelConnect stops a connection attempt, e.g. when the user cancels the
// password prompt.
func (h *Host) CancelConnect() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.attemptCmd != nil && h.attemptCmd.Process != nil {
		h.canceled = true
		_ = h.attemptCmd.Process.Kill()
	}
}

// Connecting reports whether a connection attempt is in progress.
func (h *Host) Connecting() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.attempt != nil
}

func (h *Host) connect(ch chan struct{}) {
	h.dropIdle()
	_ = os.Remove(h.tunSock)
	_ = os.Remove(h.ctlPath)

	cmd := exec.Command("ssh", "-N",
		"-M", "-S", h.ctlPath,
		"-o", "ControlPersist=no",
		"-o", "ConnectTimeout=10",
		"-o", "NumberOfPasswordPrompts=2",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-o", "StreamLocalBindUnlink=yes",
		h.dest)
	cmd.Stdin = nil
	if AskpassEnv != nil {
		cmd.Env = append(os.Environ(), AskpassEnv(h.Name)...)
	} else {
		cmd.Args = append(cmd.Args[:1], append([]string{"-o", "BatchMode=yes"}, cmd.Args[1:]...)...)
	}
	setNoTTY(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	loggedIn := false
	fail := func(err error) {
		var fe *FixError
		h.mu.Lock()
		h.manualRetry = loggedIn || h.canceled || strings.Contains(err.Error(), "acesso negado") ||
			strings.Contains(err.Error(), "cancelada") || strings.Contains(err.Error(), "chave do servidor")
		h.fix = nil
		if errors.As(err, &fe) {
			h.fix = fe.Fix
		}
		h.attempt, h.attemptCmd = nil, nil
		h.lastErr, h.lastFail = err, time.Now()
		h.tunError = err.Error()
		h.mu.Unlock()
		slog.Warn("conexão ssh falhou", "máquina", h.Name, "erro", err)
		close(ch)
		// Drop remembered passwords only when the login itself failed.
		if OnTunnelFailed != nil && !loggedIn {
			OnTunnelFailed(h.Name)
		}
	}

	if err := cmd.Start(); err != nil {
		fail(fmt.Errorf("não consegui executar ssh: %w", err))
		return
	}
	h.mu.Lock()
	h.attemptCmd = cmd
	h.mu.Unlock()
	done := make(chan struct{})
	go func() { defer applog.Recover("hosts/hosts.go:590"); _ = cmd.Wait(); close(done) }()

	// Long enough for the user to type a password.
	deadline := time.NewTimer(3 * time.Minute)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-done:
			msg := sshError(stderr.String())
			h.mu.Lock()
			if h.canceled {
				msg = "ssh: autenticação cancelada. Use Atualizar para tentar de novo."
				h.canceled = false
			}
			h.mu.Unlock()
			fail(errors.New(msg))
			return
		case <-deadline.C:
			_ = cmd.Process.Kill()
			<-done
			fail(errors.New("ssh: tempo esgotado ao conectar"))
			return
		case <-tick.C:
			if _, err := os.Stat(h.ctlPath); err != nil {
				continue
			}
			// Logged in. Find the engine socket and forward it.
			loggedIn = true
			if err := h.forward(done); err != nil {
				var fe *FixError
				// The password was right even if the server is not ready.
				if OnTunnelUp != nil {
					OnTunnelUp(h.Name)
				}
				if errors.As(err, &fe) {
					// Login worked: keep it so the fix can run without
					// asking for the password again.
					h.mu.Lock()
					h.idle, h.idleDone = cmd, done
					h.mu.Unlock()
				} else {
					_ = cmd.Process.Kill()
					<-done
				}
				fail(err)
				return
			}
			{
				h.mu.Lock()
				h.tunnel, h.tunDone, h.tunError = cmd, done, ""
				h.attempt, h.attemptCmd, h.lastErr = nil, nil, nil
				h.mu.Unlock()
				close(ch)
				if OnTunnelUp != nil {
					OnTunnelUp(h.Name)
				}
				return
			}
		}
	}
}

// Fix is something the app can run on the host to solve a connection problem.
type Fix struct {
	ID    string   `json:"id"`
	Label string   `json:"label"`
	Cmd   []string `json:"-"`
}

// FixError is an error that comes with a suggested fix.
type FixError struct {
	Msg string
	Fix *Fix
}

func (e *FixError) Error() string { return e.Msg }

// Fix returns the suggested fix for the last connection error, if any.
func (h *Host) Fix() *Fix {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fix
}

// RunFix runs a suggested fix over the existing ssh login.
func (h *Host) RunFix(ctx context.Context, id string) (string, error) {
	f := h.Fix()
	if f == nil || f.ID != id {
		return "", errors.New("nada a corrigir")
	}
	out, err := h.ctl(ctx, strings.Join(f.Cmd, " "))
	h.dropIdle()
	h.mu.Lock()
	h.fix = nil
	h.mu.Unlock()
	h.Retry()
	return out, err
}

func (h *Host) dropIdle() {
	h.mu.Lock()
	cmd, done := h.idle, h.idleDone
	h.idle, h.idleDone = nil, nil
	h.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
		<-done
	}
}

// ctl runs a shell line on the host through the master connection.
func (h *Host) ctl(ctx context.Context, line string) (string, error) {
	cmd := exec.CommandContext(ctx, "ssh", "-S", h.ctlPath, "-o", "BatchMode=yes", h.dest, "--", line)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return string(out), errors.New(msg)
	}
	return string(out), nil
}

const probeScript = `uid=$(id -u)
rt=${XDG_RUNTIME_DIR:-/run/user/$uid}
st=${XDG_STATE_HOME:-$HOME/.local/state}/gantry
for s in "$GANTRY_SOCK" /var/run/docker.sock "$rt/podman/podman.sock" /run/podman/podman.sock "$HOME/.docker/run/docker.sock" "$rt/gantry/nerdctl.sock" "$rt/gantry/k3s.sock" "$st/nerdctl.sock" "$st/k3s.sock"; do
  [ -n "$s" ] && [ -S "$s" ] || continue
  if [ -r "$s" ] && [ -w "$s" ]; then echo "OK $s"; else echo "NOPERM $s"; fi
done
command -v podman >/dev/null 2>&1 && echo PODMAN
command -v docker >/dev/null 2>&1 && echo DOCKER
command -v systemctl >/dev/null 2>&1 && echo SYSTEMD
echo "USER $(id -un)"
exit 0`

// forward finds the engine socket on the host and forwards it locally.
func (h *Host) forward(done chan struct{}) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	go func() {
		defer applog.Recover("hosts/hosts.go:733")
		select {
		case <-done:
			cancel()
		case <-ctx.Done():
		}
	}()

	h.mu.Lock()
	h.agentRemote = "" // a new master connection has no forwards yet
	for _, x := range h.extra {
		x.forwarded = false
	}
	for _, x := range h.tcpFwd {
		x.forwarded = false
	}
	h.mu.Unlock()
	remote := h.remote
	if !h.remoteFixed || remote == "" {
		out, err := h.ctl(ctx, "GANTRY_SOCK="+shq(h.remote)+" sh -c "+shq(probeScript))
		if err != nil {
			return fmt.Errorf("ssh: conectei, mas não consegui verificar o servidor: %v", err)
		}
		found, noperm, user := "", "", ""
		var socks []string
		hasPodman, hasDocker, hasSystemd := false, false, false
		for _, l := range strings.Split(out, "\n") {
			f := strings.Fields(l)
			if len(f) == 0 {
				continue
			}
			switch f[0] {
			case "OK":
				p := strings.TrimSpace(strings.TrimPrefix(l, "OK "))
				if found == "" {
					found = p
				}
				socks = append(socks, p)
			case "NOPERM":
				if noperm == "" {
					noperm = strings.TrimSpace(strings.TrimPrefix(l, "NOPERM "))
				}
			case "PODMAN":
				hasPodman = true
			case "DOCKER":
				hasDocker = true
			case "SYSTEMD":
				hasSystemd = true
			case "USER":
				if len(f) > 1 {
					user = f[1]
				}
			}
		}
		h.mu.Lock()
		h.sockets = dedupeSockets(socks)
		h.mu.Unlock()
		switch {
		case found != "":
			remote = found
		case hasPodman && hasSystemd:
			return &FixError{
				Msg: "O Podman está instalado no servidor, mas o socket da API está desligado.\nNo servidor: systemctl --user enable --now podman.socket",
				Fix: &Fix{ID: "podman-socket", Label: "Ativar o socket do Podman no servidor",
					Cmd: []string{"systemctl --user enable --now podman.socket && (loginctl enable-linger \"$(id -un)\" 2>/dev/null || true)"}},
			}
		case noperm != "":
			return fmt.Errorf("o usuário %s não tem permissão no socket %s.\nNo servidor: sudo usermod -aG docker %s e depois conecte de novo.", user, noperm, user)
		case hasPodman:
			return errors.New("o Podman está instalado no servidor, mas o socket da API não está ativo. Rode no servidor: podman system service --time=0 &")
		case hasDocker:
			return errors.New("o Docker está instalado no servidor, mas o serviço não está rodando. No servidor: sudo systemctl enable --now docker")
		default:
			return errors.New("não encontrei Docker nem Podman no servidor")
		}
	}

	_ = os.Remove(h.tunSock)
	fwd := exec.CommandContext(ctx, "ssh", "-S", h.ctlPath, "-o", "BatchMode=yes", "-O", "forward", "-L", h.tunSock+":"+remote, h.dest)
	if out, err := fwd.CombinedOutput(); err != nil {
		return fmt.Errorf("ssh: não consegui encaminhar o socket %s: %s", remote, strings.TrimSpace(string(out)))
	}
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(h.tunSock); err == nil {
			h.mu.Lock()
			h.remote = remote
			h.mu.Unlock()
			_ = h.forwardAgent(ctx)
			// The first requests went over SSH while the engine socket was
			// unknown; with a direct connection, let the next ones dial it.
			if h.Direct() != "" {
				time.AfterFunc(2*time.Second, h.closeIdle)
			}
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("ssh: o encaminhamento do socket %s não respondeu", remote)
}

// sshError keeps the meaningful lines of ssh's stderr.
func sshError(out string) string {
	var keep []string
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "Warning: Permanently added") {
			continue
		}
		keep = append(keep, l)
	}
	msg := strings.Join(keep, "\n")
	switch {
	case msg == "":
		msg = "a conexão ssh terminou"
	case strings.Contains(msg, "Permission denied"):
		msg = "ssh: acesso negado (senha, chave ou passphrase incorreta, ou autenticação cancelada)\n" + msg
	case strings.Contains(msg, "Host key verification failed"):
		msg = "ssh: a chave do servidor não foi aceita\n" + msg
	}
	if !strings.HasPrefix(msg, "ssh:") {
		msg = "ssh: " + msg
	}
	return msg
}

// Close stops the ssh tunnel, if any.
func (h *Host) Close() {
	h.closeWSL()
	if h.shim != nil {
		h.shim.Close()
		_ = os.Remove(h.path)
	}
	h.dropIdle()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.attemptCmd != nil && h.attemptCmd.Process != nil {
		_ = h.attemptCmd.Process.Kill()
	}
	if h.tunnel != nil && h.tunnel.Process != nil {
		_ = h.tunnel.Process.Kill()
		<-h.tunDone
	}
	h.tunnel = nil
	if h.Kind == SSH {
		_ = os.Remove(h.tunSock)
		_ = os.Remove(h.ctlPath)
		_ = os.Remove(h.agentSock)
		h.agentRemote = ""
		for _, x := range h.extra {
			_ = os.Remove(x.local)
			x.forwarded = false
		}
		for _, x := range h.tcpFwd {
			if strings.HasSuffix(x.local, ".sock") {
				_ = os.Remove(x.local)
			}
			x.forwarded = false
		}
	}
}

func shq(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Run executes argv on the host, inside dir when set.
// liveKey carries a writer that receives the output of long commands while
// they run (compose up, image pulls, VM boots), for the progress view.
type liveKey struct{}

// WithLive returns a context whose RunLive calls copy their output to w as
// it is produced.
func WithLive(ctx context.Context, w io.Writer) context.Context {
	return context.WithValue(ctx, liveKey{}, w)
}

// envKey carries variables for the commands run with the context (the
// project's secrets for compose). They never go on the command line: local
// commands get them in their environment, remote ones read them from stdin.
type envKey struct{}

type noAgentKey struct{}

// WithoutAgent makes the commands run with the returned context go over SSH
// even when the agent could run them: installing or restarting the agent
// would kill its own command.
func WithoutAgent(ctx context.Context) context.Context {
	return context.WithValue(ctx, noAgentKey{}, true)
}

func (h *Host) useAgent(ctx context.Context) bool {
	return h.agentExec.Load() && h.Agent != nil && ctx.Value(noAgentKey{}) == nil
}

// WithEnv adds variables to the commands run with the returned context.
func WithEnv(ctx context.Context, env map[string]string) context.Context {
	if len(env) == 0 {
		return ctx
	}
	return context.WithValue(ctx, envKey{}, env)
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// envOf returns the variables of the context, sorted by name.
func envOf(ctx context.Context) [][2]string {
	m, _ := ctx.Value(envKey{}).(map[string]string)
	var out [][2]string
	for k, v := range m {
		if envName.MatchString(k) {
			out = append(out, [2]string{k, v})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

// EnvOf returns the variables set with WithEnv.
func EnvOf(ctx context.Context) map[string]string {
	m, _ := ctx.Value(envKey{}).(map[string]string)
	return m
}

// Live is the writer set by WithLive, or nil.
func Live(ctx context.Context) io.Writer {
	w, _ := ctx.Value(liveKey{}).(io.Writer)
	return w
}

// RunLive is Run that also streams the output to the context's live writer.
// Only commands whose output is meant for the user go through it: never
// file reads, which may hold secrets.
func (h *Host) RunLive(ctx context.Context, dir string, argv []string, stdin []byte) (string, error) {
	return h.run(ctx, dir, argv, stdin, Live(ctx))
}

func (h *Host) Run(ctx context.Context, dir string, argv []string, stdin []byte) (string, error) {
	return h.run(ctx, dir, argv, stdin, nil)
}

// syncBuf is a buffer safe for the two pipes of a command.
type syncBuf struct {
	mu   sync.Mutex
	b    bytes.Buffer
	live io.Writer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.b.Write(p)
	if s.live != nil {
		_, _ = s.live.Write(p)
	}
	return len(p), nil
}

func (h *Host) run(ctx context.Context, dir string, argv []string, stdin []byte, live io.Writer) (string, error) {
	if len(argv) == 0 {
		return "", errors.New("comando vazio")
	}
	var cmd *exec.Cmd
	switch h.Kind {
	case SSH:
		parts0 := make([]string, len(argv))
		for i, a := range argv {
			parts0[i] = shq(a)
		}
		if out, err, ok := h.agentRun(ctx, strings.Join(parts0, " "), dir, stdin, live); ok {
			return out, err
		}
		if err := h.ensureTunnel(ctx); err != nil {
			return "", err
		}
		parts := make([]string, len(argv))
		for i, a := range argv {
			parts[i] = shq(a)
		}
		line := strings.Join(parts, " ")
		if dir != "" {
			line = "cd " + shq(dir) + " && " + line
		}
		cmd = exec.CommandContext(ctx, "ssh", "-S", h.ctlPath, "-o", "BatchMode=yes", h.dest, "--", line)
	case Unix:
		cmd = exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "DOCKER_HOST=unix://"+h.path, "CONTAINER_HOST=unix://"+h.path)
	case WSL:
		if err := h.ensureWSL(ctx); err != nil {
			return "", err
		}
		parts := make([]string, len(argv))
		for i, a := range argv {
			parts[i] = shq(a)
		}
		line := strings.Join(parts, " ")
		if dir != "" {
			line = "cd " + shq(dir) + " && " + line
		}
		// compose and the CLIs talk to the engine the app found.
		h.mu.Lock()
		if h.remote != "" {
			line = "export DOCKER_HOST=unix://" + shq(h.remote) + " CONTAINER_HOST=unix://" + shq(h.remote) + "; " + line
		}
		h.mu.Unlock()
		cmd = h.wslCommand(ctx, line)
	default:
		return "", errors.New("hosts tcp:// não executam comandos; use ssh://")
	}
	if env := envOf(ctx); len(env) > 0 {
		if h.Kind == Unix {
			for _, kv := range env {
				cmd.Env = append(cmd.Env, kv[0]+"="+kv[1])
			}
		} else if stdin == nil {
			// The shell reads the exports from stdin before running the
			// command, so the values stay out of the server's process list.
			var b strings.Builder
			for _, kv := range env {
				b.WriteString("export " + kv[0] + "=" + shq(kv[1]) + "\n")
			}
			stdin = []byte(b.String())
			last := cmd.Args[len(cmd.Args)-1]
			cmd.Args[len(cmd.Args)-1] = `eval "$(cat)"; ` + last
		}
	}
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	buf := &syncBuf{live: live}
	cmd.Stdout, cmd.Stderr = buf, buf
	err := cmd.Run()
	out := buf.b.Bytes()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return string(out), errors.New(msg)
	}
	return string(out), nil
}

// Pipe runs a shell line on the host and returns its standard output as a
// stream; wait reports how it ended (with its stderr on failure).
func (h *Host) Pipe(ctx context.Context, line string) (io.ReadCloser, func() error, error) {
	var cmd *exec.Cmd
	switch h.Kind {
	case SSH:
		if rc, wait, ok := h.agentPipe(ctx, line); ok {
			return rc, wait, nil
		}
		if err := h.ensureTunnel(ctx); err != nil {
			return nil, nil, err
		}
		cmd = exec.CommandContext(ctx, "ssh", "-S", h.ctlPath, "-o", "BatchMode=yes", h.dest, "--", line)
	case Unix:
		cmd = exec.CommandContext(ctx, "sh", "-c", line)
	case WSL:
		if err := h.ensureWSL(ctx); err != nil {
			return nil, nil, err
		}
		cmd = h.wslCommand(ctx, line)
	default:
		return nil, nil, errors.New("hosts tcp:// não executam comandos; use ssh://")
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	wait := func() error {
		if err := cmd.Wait(); err != nil {
			if msg := strings.TrimSpace(stderr.String()); msg != "" {
				return errors.New(msg)
			}
			return err
		}
		return nil
	}
	return out, wait, nil
}

// RunStdin runs a shell line on the host feeding r to its standard input
// as a stream (large uploads: disks, backups).
func (h *Host) RunStdin(ctx context.Context, line string, r io.Reader) error {
	var cmd *exec.Cmd
	switch h.Kind {
	case SSH:
		if h.useAgent(ctx) {
			var buf syncBuf
			code, err := h.Agent.Exec(ctx, line, "", nil, r, &buf, &buf)
			if err == nil && code != 0 {
				msg := strings.TrimSpace(buf.b.String())
				if msg == "" {
					msg = fmt.Sprintf("saiu com %d", code)
				}
				err = errors.New(msg)
			}
			return err
		}
		if err := h.ensureTunnel(ctx); err != nil {
			return err
		}
		cmd = exec.CommandContext(ctx, "ssh", "-S", h.ctlPath, "-o", "BatchMode=yes", h.dest, "--", line)
	case Unix:
		cmd = exec.CommandContext(ctx, "sh", "-c", line)
	case WSL:
		if err := h.ensureWSL(ctx); err != nil {
			return err
		}
		cmd = h.wslCommand(ctx, line)
	default:
		return errors.New("hosts tcp:// não executam comandos; use ssh://")
	}
	cmd.Stdin = r
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return errors.New(msg)
	}
	return nil
}

// Quote quotes s for the host's shell.
func Quote(s string) string { return shq(s) }

func (h *Host) ReadFile(ctx context.Context, path string) ([]byte, error) {
	if h.Kind == Unix {
		return os.ReadFile(path)
	}
	out, err := h.Run(ctx, "", []string{"cat", "--", path}, nil)
	return []byte(out), err
}

func (h *Host) WriteFile(ctx context.Context, path string, data []byte) error {
	if h.Kind == Unix {
		mode := os.FileMode(0o644)
		if st, err := os.Stat(path); err == nil {
			mode = st.Mode().Perm()
		} else if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		return os.WriteFile(path, data, mode)
	}
	_, err := h.Run(ctx, "", []string{"sh", "-c", `mkdir -p "$(dirname "$1")" && cat > "$1"`, "sh", path}, data)
	return err
}

// Exists reports whether path exists on the host.
func (h *Host) Exists(ctx context.Context, path string) bool {
	if h.Kind == Unix {
		_, err := os.Stat(path)
		return err == nil
	}
	_, err := h.Run(ctx, "", []string{"test", "-e", path}, nil)
	return err == nil
}

// dedupeSockets drops paths that are the same socket under two names
// (/var/run is usually a link to /run).
func dedupeSockets(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range in {
		k := strings.TrimPrefix(p, "/var")
		if !seen[k] {
			seen[k] = true
			out = append(out, p)
		}
	}
	return out
}

// RuntimeOfSocket guesses the engine from its socket path.
func RuntimeOfSocket(p string) string {
	switch {
	case strings.HasSuffix(p, "gantry/nerdctl.sock"):
		return "nerdctl"
	case strings.HasSuffix(p, "gantry/k3s.sock"):
		return "k3s"
	}
	if strings.Contains(p, "podman") {
		return "podman"
	}
	return "docker"
}

// EngineRef is one container engine on the host's machine.
type EngineRef struct {
	Runtime string
	Socket  string // path on that machine, for DOCKER_HOST
	Engine  *engine.Client
	Host    *Host // the Gantry host that owns this engine
}

type extraEngine struct {
	remote    string
	local     string
	forwarded bool
	client    *engine.Client
}

// ConfigSocket is the socket set in the config (SSH only), empty when the
// app finds it.
func (h *Host) ConfigSocket() string {
	if h.remoteFixed {
		return h.remote
	}
	return ""
}

// Socket is the engine socket path on the host's machine.
func (h *Host) Socket() string {
	switch h.Kind {
	case Unix:
		return h.path
	case SSH, WSL:
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.remote
	}
	return ""
}

// Engines lists the engines reachable on this host: its own and, over SSH,
// any other Docker or Podman socket found on the server.
func (h *Host) Engines(ctx context.Context) []EngineRef {
	main := EngineRef{Runtime: h.Runtime(), Socket: h.Socket(), Engine: h.Engine, Host: h}
	if main.Runtime == "" {
		if v, err := h.Engine.Version(ctx); err == nil {
			h.SetRuntime(v.Runtime())
			main.Runtime = v.Runtime()
		}
	}
	out := []EngineRef{main}
	if h.Kind != SSH {
		return out
	}
	if err := h.ensureTunnel(ctx); err != nil {
		return out
	}
	h.mu.Lock()
	socks := append([]string(nil), h.sockets...)
	remote := h.remote
	h.mu.Unlock()
	if len(socks) == 0 {
		// The socket came from the config, so the server was never probed.
		socks = h.RefreshSockets(ctx)
	}
	seen := map[string]bool{main.Runtime: true}
	for _, p := range socks {
		if strings.TrimPrefix(p, "/var") == strings.TrimPrefix(remote, "/var") {
			continue
		}
		rt := RuntimeOfSocket(p)
		if seen[rt] {
			continue
		}
		seen[rt] = true
		out = append(out, EngineRef{Runtime: rt, Socket: p, Engine: h.extraClient(p), Host: h})
	}
	return out
}

// RefreshSockets looks for engine sockets on the server again (the agent
// may have started serving containerd since the connection was made).
func (h *Host) RefreshSockets(ctx context.Context) []string {
	if h.Kind != SSH {
		return nil
	}
	if err := h.ensureTunnel(ctx); err != nil {
		return nil
	}
	o, err := h.ctl(ctx, "sh -c "+shq(probeScript))
	if err != nil {
		h.mu.Lock()
		defer h.mu.Unlock()
		return append([]string(nil), h.sockets...)
	}
	var socks []string
	for _, l := range strings.Split(o, "\n") {
		if p, ok := strings.CutPrefix(strings.TrimSpace(l), "OK "); ok {
			socks = append(socks, p)
		}
	}
	socks = dedupeSockets(socks)
	h.mu.Lock()
	h.sockets = socks
	h.mu.Unlock()
	return socks
}

func (h *Host) extraClient(remote string) *engine.Client {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.extra == nil {
		h.extra = map[string]*extraEngine{}
	}
	if x, ok := h.extra[remote]; ok {
		return x.client
	}
	x := &extraEngine{remote: remote, local: filepath.Join(tunnelDir(), h.id+"-e"+strconv.Itoa(len(h.extra))+".sock")}
	x.client = engine.New(func(ctx context.Context) (net.Conn, error) {
		if err := h.ensureTunnel(ctx); err != nil {
			return nil, err
		}
		h.mu.Lock()
		fwd := x.forwarded
		h.mu.Unlock()
		if !fwd {
			_ = os.Remove(x.local)
			cmd := exec.CommandContext(ctx, "ssh", "-S", h.ctlPath, "-o", "BatchMode=yes", "-O", "forward", "-L", x.local+":"+x.remote, h.dest)
			if b, err := cmd.CombinedOutput(); err != nil {
				return nil, fmt.Errorf("ssh: não consegui encaminhar o socket %s: %s", x.remote, strings.TrimSpace(string(b)))
			}
			h.mu.Lock()
			x.forwarded = true
			h.mu.Unlock()
		}
		var d net.Dialer
		return d.DialContext(ctx, "unix", x.local)
	})
	h.extra[remote] = x
	return x.client
}

type tcpForward struct {
	local     string // unix socket, or 127.0.0.1:port for the browser
	forwarded bool
}

// DialTCP connects to a TCP address as seen from the host's machine: on a
// local host directly, on an SSH host through the master connection.
func (h *Host) DialTCP(ctx context.Context, addr string) (net.Conn, error) {
	var d net.Dialer
	if h.Kind != SSH {
		return d.DialContext(ctx, "tcp", addr)
	}
	local, err := h.forwardTCP(ctx, addr, "sock")
	if err != nil {
		return nil, err
	}
	return d.DialContext(ctx, "unix", local)
}

// LocalAddr returns an address on this computer that reaches addr on the
// host's machine, for opening it in a browser.
func (h *Host) LocalAddr(ctx context.Context, addr string) (string, error) {
	if h.Kind != SSH {
		return addr, nil
	}
	return h.forwardTCP(ctx, addr, "port")
}

func (h *Host) forwardTCP(ctx context.Context, addr, kind string) (string, error) {
	if err := h.ensureTunnel(ctx); err != nil {
		return "", err
	}
	key := kind + " " + addr
	h.mu.Lock()
	if h.tcpFwd == nil {
		h.tcpFwd = map[string]*tcpForward{}
	}
	x := h.tcpFwd[key]
	if x == nil {
		x = &tcpForward{}
		if kind == "sock" {
			x.local = filepath.Join(tunnelDir(), h.id+"-t"+strconv.Itoa(len(h.tcpFwd))+".sock")
		}
		h.tcpFwd[key] = x
	}
	if x.forwarded {
		local := x.local
		h.mu.Unlock()
		return local, nil
	}
	h.mu.Unlock()

	local := x.local
	if kind == "port" {
		// Pick a free port; ssh binds it right after.
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return "", err
		}
		local = ln.Addr().String()
		ln.Close()
	} else {
		_ = os.Remove(local)
	}
	cmd := exec.CommandContext(ctx, "ssh", "-S", h.ctlPath, "-o", "BatchMode=yes", "-O", "forward", "-L", local+":"+addr, h.dest)
	if b, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("ssh: não consegui encaminhar %s: %s", addr, strings.TrimSpace(string(b)))
	}
	h.mu.Lock()
	x.local, x.forwarded = local, true
	h.mu.Unlock()
	return local, nil
}

// SetAgentExec tells whether the agent runs this host's commands (it
// speaks protocol 3): they then take no SSH session.
func (h *Host) SetAgentExec(on bool) { h.agentExec.Store(on) }

// AgentExec reports whether commands go through the agent.
func (h *Host) AgentExec() bool { return h.agentExec.Load() }

// agentRun runs a shell line through the agent, as run does over SSH. ok is
// false when the agent is not used or could not be reached: the caller
// goes on over SSH.
func (h *Host) agentRun(ctx context.Context, line, dir string, stdin []byte, live io.Writer) (string, error, bool) {
	if !h.useAgent(ctx) {
		return "", nil, false
	}
	var env []string
	for _, kv := range envOf(ctx) {
		env = append(env, kv[0]+"="+kv[1])
	}
	var in io.Reader
	if stdin != nil {
		in = bytes.NewReader(stdin)
	}
	buf := &syncBuf{live: live}
	code, err := h.Agent.Exec(ctx, line, dir, env, in, buf, buf)
	if errors.Is(err, agent.ErrUnreachable) && ctx.Err() == nil {
		return "", nil, false // the command never started: over SSH
	}
	out := buf.b.String()
	if err == nil && code != 0 {
		msg := strings.TrimSpace(out)
		if msg == "" {
			msg = fmt.Sprintf("saiu com %d", code)
		}
		err = errors.New(msg)
	}
	return out, err, true
}

// agentPipe is Pipe through the agent.
func (h *Host) agentPipe(ctx context.Context, line string) (io.ReadCloser, func() error, bool) {
	if !h.useAgent(ctx) {
		return nil, nil, false
	}
	pr, pw := io.Pipe()
	var stderr syncBuf
	done := make(chan error, 1)
	go func() {
		code, err := h.Agent.Exec(ctx, line, "", nil, nil, pw, &stderr)
		if err == nil && code != 0 {
			msg := strings.TrimSpace(stderr.b.String())
			if msg == "" {
				msg = fmt.Sprintf("saiu com %d", code)
			}
			err = errors.New(msg)
		}
		pw.CloseWithError(err)
		done <- err
	}()
	wait := func() error {
		err := <-done
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	return pr, wait, true
}
