// Package agent is the optional helper that runs on a server as a user
// service. It listens on a Unix socket only; the app reaches it through the
// same SSH connection it already uses, so no port is opened.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"gantry/internal/engine"
	"gantry/internal/metrics"
	"gantry/internal/shim"
	"gantry/internal/vmscript"
)

// SocketName is the agent socket inside $XDG_RUNTIME_DIR.
const SocketName = "gantry/agent.sock"

type Info struct {
	Version      string `json:"version"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	Hostname     string `json:"hostname"`
	PID          int    `json:"pid"`
	Started      int64  `json:"started"`
	Engine       string `json:"engine,omitempty"`
	EngineOK     bool   `json:"engineOk"`
	MetricsSince int64  `json:"metricsSince"`
	Interval     int    `json:"interval"`
	Watch        bool   `json:"watch"`
	Protocol     int    `json:"protocol"`
	VMs          bool   `json:"vms"`              // samples the VMs of the machine too
	Direct       string `json:"direct,omitempty"` // address of the direct (TLS) listener
}

type Options struct {
	Version  string
	Socket   string
	State    string // metrics history file
	Engine   string // engine socket; empty means find it
	Interval time.Duration
	Keep     time.Duration
	Log      func(format string, args ...any)
}

type Agent struct {
	opt     Options
	started time.Time
	eng     *engine.Client
	col     *metrics.Collector

	mu     sync.Mutex
	engine string
	shims  []string // sockets of the CLI runtimes this agent serves
	direct string   // address of the direct listener, when on
}

// DefaultSocket is where the agent listens unless told otherwise:
// $XDG_RUNTIME_DIR/gantry/agent.sock, or the state folder on servers without
// a runtime dir. SocketScript must find the same path.
func DefaultSocket() string {
	rt := os.Getenv("XDG_RUNTIME_DIR")
	if rt == "" {
		rt = filepath.Join("/run/user", strconv.Itoa(os.Getuid()))
	}
	if st, err := os.Stat(rt); err == nil && st.IsDir() {
		return filepath.Join(rt, SocketName)
	}
	return filepath.Join(filepath.Dir(DefaultState()), "agent.sock")
}

// SocketScript prints DefaultSocket when run by sh on the server.
const SocketScript = `rt=${XDG_RUNTIME_DIR:-/run/user/$(id -u)}; if [ -d "$rt" ]; then echo "$rt/gantry/agent.sock"; else echo "${XDG_STATE_HOME:-$HOME/.local/state}/gantry/agent.sock"; fi`

// DefaultState is the metrics history file.
func DefaultState() string {
	d := os.Getenv("XDG_STATE_HOME")
	if d == "" {
		home, _ := os.UserHomeDir()
		d = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(d, "gantry", "metrics.gob")
}

func New(opt Options) *Agent {
	if opt.Interval <= 0 {
		opt.Interval = 15 * time.Second
	}
	if opt.Keep <= 0 {
		opt.Keep = 24 * time.Hour
	}
	if opt.Log == nil {
		opt.Log = func(string, ...any) {}
	}
	a := &Agent{opt: opt, started: time.Now(), engine: opt.Engine}
	a.eng = engine.New(a.dialEngine)
	a.col = metrics.New(a.eng, opt.Interval, opt.Keep)
	a.col.VMs = a.vmSamples
	return a
}

func engineCandidates() []string {
	rt := os.Getenv("XDG_RUNTIME_DIR")
	if rt == "" {
		rt = filepath.Join("/run/user", strconv.Itoa(os.Getuid()))
	}
	home, _ := os.UserHomeDir()
	return []string{
		"/var/run/docker.sock",
		filepath.Join(rt, "podman", "podman.sock"),
		"/run/podman/podman.sock",
		filepath.Join(home, ".docker", "run", "docker.sock"),
	}
}

// dialEngine connects to the engine, looking for its socket again when the
// one in use stops answering (the engine may start after the agent).
func (a *Agent) dialEngine(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	a.mu.Lock()
	cur := a.engine
	a.mu.Unlock()
	if cur != "" {
		c, err := d.DialContext(ctx, "unix", cur)
		if err == nil || a.opt.Engine != "" {
			return c, err
		}
	}
	a.mu.Lock()
	cands := append(engineCandidates(), a.shims...)
	a.mu.Unlock()
	for _, s := range cands {
		c, err := d.DialContext(ctx, "unix", s)
		if err == nil {
			a.mu.Lock()
			if a.engine != s {
				a.opt.Log("engine em %s", s)
			}
			a.engine = s
			a.mu.Unlock()
			return c, nil
		}
	}
	return nil, errors.New("nenhum engine (Docker ou Podman) respondeu neste servidor")
}

func (a *Agent) info(ctx context.Context) Info {
	host, _ := os.Hostname()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	ok := a.eng.Ping(ctx) == nil
	a.mu.Lock()
	eng := a.engine
	a.mu.Unlock()
	return Info{
		Version: a.opt.Version, OS: runtime.GOOS, Arch: runtime.GOARCH, Hostname: host,
		PID: os.Getpid(), Started: a.started.Unix(), Engine: eng, EngineOK: ok,
		MetricsSince: a.col.Since(), Interval: int(a.opt.Interval / time.Second),
		Watch: watchSupported, Protocol: Protocol, VMs: true, Direct: a.directAddr(),
	}
}

func (a *Agent) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/info", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, a.info(r.Context()))
	})
	mux.HandleFunc("GET /v1/metrics", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		since, _ := strconv.ParseInt(q.Get("since"), 10, 64)
		max, _ := strconv.Atoi(q.Get("max"))
		writeJSON(w, a.col.Query(since, q.Get("lab"), max))
	})
	mux.HandleFunc("GET /v1/watch", a.watch)
	mux.HandleFunc("GET /v1/exec", a.exec)
	return mux
}

// watch streams a line per change of the given files, as server-sent events.
func (a *Agent) watch(w http.ResponseWriter, r *http.Request) {
	paths := r.URL.Query()["path"]
	if len(paths) == 0 {
		http.Error(w, "informe path", http.StatusBadRequest)
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming não suportado", http.StatusInternalServerError)
		return
	}
	events := make(chan string, 16)
	stop, err := watchFiles(paths, events)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotImplemented)
		return
	}
	defer stop()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	fmt.Fprint(w, ": ok\n\n")
	fl.Flush()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case p, ok := <-events:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", strings.ReplaceAll(p, "\n", " "))
			fl.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		}
	}
}

// Run serves until ctx ends, then saves the metrics history.
func (a *Agent) Run(ctx context.Context) error {
	if a.opt.State != "" {
		if err := a.col.Load(a.opt.State); err != nil {
			a.opt.Log("histórico de métricas ignorado: %v", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(a.opt.Socket), 0o700); err != nil {
		return err
	}
	if c, err := net.DialTimeout("unix", a.opt.Socket, time.Second); err == nil {
		c.Close()
		return fmt.Errorf("já existe um agente rodando em %s", a.opt.Socket)
	}
	_ = os.Remove(a.opt.Socket)
	ln, err := net.Listen("unix", a.opt.Socket)
	if err != nil {
		return err
	}
	_ = os.Chmod(a.opt.Socket, 0o600)
	a.opt.Log("gantry-agent %s ouvindo em %s", a.opt.Version, a.opt.Socket)
	// containerd (nerdctl) and k3s have no Docker API; serve one for each,
	// next to the agent socket, so the app reaches them like any engine.
	for _, f := range shim.Detect() {
		d, _, err := shim.FromURL(f.URL)
		if err != nil {
			continue
		}
		path := filepath.Join(filepath.Dir(a.opt.Socket), d.Runtime()+".sock")
		srv := shim.New(d)
		if err := srv.Listen(path); err != nil {
			a.opt.Log("%s: %v", f.Name, err)
			continue
		}
		a.mu.Lock()
		a.shims = append(a.shims, path)
		a.mu.Unlock()
		a.opt.Log("%s servido em %s", f.Name, path)
		defer func() { srv.Close(); _ = os.Remove(path) }()
	}

	srv := &http.Server{Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second}
	// Direct connections (TCP + mutual TLS), once paired with an app.
	if cfg, addr, err := serverTLS(TLSDir()); err != nil {
		a.opt.Log("conexão direta desligada: %v", err)
	} else if cfg != nil {
		dl, err := net.Listen("tcp", addr)
		if err != nil {
			a.opt.Log("conexão direta: não consegui ouvir em %s: %v", addr, err)
		} else {
			api := &connListener{ch: make(chan net.Conn), done: make(chan struct{}), addr: dl.Addr()}
			a.mu.Lock()
			a.direct = dl.Addr().String()
			a.mu.Unlock()
			a.opt.Log("conexão direta (TLS mútuo) em %s", dl.Addr())
			go a.serveDirect(ctx, dl, cfg, api)
			dsrv := &http.Server{Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second}
			go func() { _ = dsrv.Serve(api) }()
			defer dsrv.Close()
		}
	}
	go a.col.Run(ctx)
	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				a.save()
			}
		}
	}()
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
	case err = <-errc:
	}
	sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
	_ = os.Remove(a.opt.Socket)
	a.save()
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return err
}

func (a *Agent) directAddr() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.direct
}

// vmSamples reads the VMs of the machine with the same script the app uses.
// Without a VM folder there is nothing to run.
func (a *Agent) vmSamples(ctx context.Context) ([]metrics.VMSample, error) {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "state")
	}
	if st, err := os.Stat(filepath.Join(dir, "gantry", "vms")); err != nil || !st.IsDir() {
		return nil, nil
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", vmscript.Script, "vmctl", "stats")
	cmd.Env = append(os.Environ(), "GV_PROJECT=")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	us := vmscript.ParseUsages(string(out))
	res := make([]metrics.VMSample, 0, len(us))
	for _, u := range us {
		res = append(res, metrics.VMSample{Lab: u.Project, Name: u.Name, CPUSec: u.CPUSec, Mem: u.RSS, Disk: u.Disk})
	}
	return res, nil
}

func (a *Agent) save() {
	if a.opt.State == "" {
		return
	}
	if err := a.col.Save(a.opt.State); err != nil {
		a.opt.Log("não consegui salvar as métricas: %v", err)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
