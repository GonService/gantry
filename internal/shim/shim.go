// Package shim serves the part of the Docker Engine API that Gantry uses,
// backed by a runtime that only has a command line: containerd through
// nerdctl (including the k8s.io namespace of k3s).
// The rest of the app talks to it like to any Docker socket.
package shim

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"gantry/internal/applog"
	"gantry/internal/engine"
)

// ErrNotFound marks a missing container, image, volume or network.
var ErrNotFound = errors.New("não encontrado")

// ErrUnsupported marks an operation the runtime does not have.
var ErrUnsupported = errors.New("este runtime não oferece essa operação")

// Stats is what a driver reports for one container.
type Stats struct {
	CPU      float64 // percent of one CPU
	Mem      uint64
	MemLimit uint64
	NetRx    uint64
	NetTx    uint64
}

// Driver runs a container runtime through its command line.
type Driver interface {
	Runtime() string // nerdctl, k3s
	Version(ctx context.Context) (string, error)
	List(ctx context.Context) ([]engine.Container, error)
	Action(ctx context.Context, id, action string) error // start, stop, restart, kill
	Remove(ctx context.Context, id string, force bool) error
	Stats(ctx context.Context, id string) (Stats, error)
	LogsCmd(id string, tail int, follow bool) []string
	ExecCmd(id string, cmd []string) []string
	Images(ctx context.Context) ([]engine.Image, error)
	RemoveImage(ctx context.Context, id string, force bool) error
	Volumes(ctx context.Context) ([]engine.Volume, error)
	RemoveVolume(ctx context.Context, name string) error
	Networks(ctx context.Context) ([]engine.NetworkInfo, error)
	RemoveNetwork(ctx context.Context, id string) error
	Prune(ctx context.Context, what string) (int, error)
}

// Updater is a driver that changes the limits of a running container
// (nerdctl update).
type Updater interface {
	Update(ctx context.Context, id string, r engine.Resources) error
}

// Server is the Docker API front of a driver.
type Server struct {
	d     Driver
	execs sync.Map // id -> []string
	ln    net.Listener
	srv   *http.Server

	mu    sync.Mutex
	cache []engine.Container
	at    time.Time
}

func New(d Driver) *Server { return &Server{d: d} }

// Listen serves on a Unix socket until Close.
func (s *Server) Listen(path string) error {
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	_ = os.Chmod(path, 0o600)
	s.ln = ln
	s.srv = &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { defer applog.Recover("shim/shim.go:91"); _ = s.srv.Serve(ln) }()
	return nil
}

func (s *Server) Close() {
	if s.srv != nil {
		_ = s.srv.Close()
	}
}

// list caches the container list briefly: the app asks for it, for stats
// and for events within the same second.
func (s *Server) list(ctx context.Context) ([]engine.Container, error) {
	s.mu.Lock()
	if time.Since(s.at) < 1500*time.Millisecond && s.cache != nil {
		out := s.cache
		s.mu.Unlock()
		return out, nil
	}
	s.mu.Unlock()
	cs, err := s.d.List(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.cache, s.at = cs, time.Now()
	s.mu.Unlock()
	return cs, nil
}

func (s *Server) forget() {
	s.mu.Lock()
	s.at = time.Time{}
	s.mu.Unlock()
}

// find resolves a full or short id, or a name.
func (s *Server) find(ctx context.Context, ref string) (engine.Container, error) {
	cs, err := s.list(ctx)
	if err != nil {
		return engine.Container{}, err
	}
	for _, c := range cs {
		if c.ID == ref || c.Name() == ref || (len(ref) >= 6 && strings.HasPrefix(c.ID, ref)) {
			return c, nil
		}
	}
	return engine.Container{}, fmt.Errorf("container %s: %w", ref, ErrNotFound)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	switch {
	case errors.Is(err, ErrNotFound):
		code = http.StatusNotFound
	case errors.Is(err, ErrUnsupported):
		code = http.StatusNotImplemented
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"message": err.Error()})
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /_ping", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "OK") })
	mux.HandleFunc("HEAD /_ping", func(w http.ResponseWriter, r *http.Request) {})
	mux.HandleFunc("GET /version", s.version)
	mux.HandleFunc("GET /info", s.info)
	mux.HandleFunc("GET /containers/json", func(w http.ResponseWriter, r *http.Request) {
		// Always fresh: compose changes containers behind the shim's back.
		s.forget()
		cs, err := s.list(r.Context())
		if err != nil {
			writeErr(w, err)
			return
		}
		if r.URL.Query().Get("all") == "" || r.URL.Query().Get("all") == "0" {
			var run []engine.Container
			for _, c := range cs {
				if c.State == "running" {
					run = append(run, c)
				}
			}
			cs = run
		}
		if cs == nil {
			cs = []engine.Container{}
		}
		writeJSON(w, cs)
	})
	mux.HandleFunc("GET /containers/{id}/json", func(w http.ResponseWriter, r *http.Request) {
		c, err := s.find(r.Context(), r.PathValue("id"))
		if err != nil {
			writeErr(w, err)
			return
		}
		if in, ok := s.d.(Inspector); ok {
			if raw, err := in.InspectRaw(r.Context(), c.ID); err == nil {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(raw)
				return
			}
		}
		writeJSON(w, map[string]any{
			"Id": c.ID, "Name": "/" + c.Name(), "Image": c.ImageID,
			"Config":     map[string]any{"Tty": true, "Labels": c.Labels, "Image": c.Image},
			"HostConfig": map[string]any{},
			"State":      map[string]any{"Running": c.State == "running", "Status": c.State},
		})
	})
	mux.HandleFunc("POST /containers/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		action := r.PathValue("action")
		if action == "exec" {
			s.execCreate(w, r)
			return
		}
		if action == "rename" {
			s.rename(w, r)
			return
		}
		if action == "update" {
			s.update(w, r)
			return
		}
		switch action {
		case "start", "stop", "restart", "kill", "pause", "unpause":
		default:
			writeErr(w, fmt.Errorf("ação %s: %w", action, ErrUnsupported))
			return
		}
		c, err := s.find(r.Context(), r.PathValue("id"))
		if err != nil {
			writeErr(w, err)
			return
		}
		if err := s.d.Action(r.Context(), c.ID, action); err != nil {
			writeErr(w, err)
			return
		}
		s.forget()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /containers/{id}", func(w http.ResponseWriter, r *http.Request) {
		c, err := s.find(r.Context(), r.PathValue("id"))
		if err != nil {
			writeErr(w, err)
			return
		}
		f := r.URL.Query().Get("force")
		if err := s.d.Remove(r.Context(), c.ID, f == "1" || f == "true"); err != nil {
			writeErr(w, err)
			return
		}
		s.forget()
		w.WriteHeader(http.StatusNoContent)
	})
	s.createRoutes(mux)
	mux.HandleFunc("GET /containers/{id}/stats", s.stats)
	mux.HandleFunc("GET /containers/{id}/logs", s.logs)
	mux.HandleFunc("GET /events", s.events)
	mux.HandleFunc("POST /exec/{id}/start", s.execStart)
	mux.HandleFunc("POST /exec/{id}/resize", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	mux.HandleFunc("GET /images/json", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.d.Images(r.Context())
		respond(w, v, err, []engine.Image{})
	})
	mux.HandleFunc("DELETE /images/{id...}", func(w http.ResponseWriter, r *http.Request) {
		f := r.URL.Query().Get("force")
		done(w, s.d.RemoveImage(r.Context(), r.PathValue("id"), f == "1" || f == "true"))
	})
	mux.HandleFunc("GET /volumes", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.d.Volumes(r.Context())
		if err != nil && !errors.Is(err, ErrUnsupported) {
			writeErr(w, err)
			return
		}
		if v == nil {
			v = []engine.Volume{}
		}
		writeJSON(w, map[string]any{"Volumes": v})
	})
	mux.HandleFunc("DELETE /volumes/{name}", func(w http.ResponseWriter, r *http.Request) {
		done(w, s.d.RemoveVolume(r.Context(), r.PathValue("name")))
	})
	mux.HandleFunc("GET /networks", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.d.Networks(r.Context())
		if err != nil && !errors.Is(err, ErrUnsupported) {
			writeErr(w, err)
			return
		}
		if f := r.URL.Query().Get("filters"); f != "" {
			var flt map[string][]string
			_ = json.Unmarshal([]byte(f), &flt)
			var keep []engine.NetworkInfo
			for _, n := range v {
				ok := true
				for _, l := range flt["label"] {
					k, val, _ := strings.Cut(l, "=")
					if n.Labels[k] != val {
						ok = false
					}
				}
				if ok {
					keep = append(keep, n)
				}
			}
			v = keep
		}
		if v == nil {
			v = []engine.NetworkInfo{}
		}
		writeJSON(w, v)
	})
	mux.HandleFunc("DELETE /networks/{id}", func(w http.ResponseWriter, r *http.Request) {
		done(w, s.d.RemoveNetwork(r.Context(), r.PathValue("id")))
	})
	mux.HandleFunc("POST /{what}/prune", func(w http.ResponseWriter, r *http.Request) {
		what := r.PathValue("what")
		n, err := s.d.Prune(r.Context(), what)
		if err != nil {
			writeErr(w, err)
			return
		}
		key := map[string]string{"images": "ImagesDeleted", "volumes": "VolumesDeleted", "networks": "NetworksDeleted", "containers": "ContainersDeleted"}[what]
		writeJSON(w, map[string]any{key: make([]string, n), "SpaceReclaimed": 0})
	})
	// Some clients prefix the API version: /v1.43/containers/json.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1.") {
			if i := strings.Index(r.URL.Path[1:], "/"); i > 0 {
				r.URL.Path = r.URL.Path[i+1:]
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func respond[T any](w http.ResponseWriter, v []T, err error, empty []T) {
	if err != nil && !errors.Is(err, ErrUnsupported) {
		writeErr(w, err)
		return
	}
	if v == nil {
		v = empty
	}
	writeJSON(w, v)
}

func done(w http.ResponseWriter, err error) {
	if err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) version(w http.ResponseWriter, r *http.Request) {
	v, err := s.d.Version(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	comps := []map[string]string{{"Name": "Gantry shim: " + s.d.Runtime(), "Version": v}}
	if c, ok := s.d.(interface{ Compose() []string }); ok {
		comps = append(comps, map[string]string{"Name": "Gantry compose", "Version": strings.Join(c.Compose(), " ")})
	}
	writeJSON(w, map[string]any{
		"Version": v, "ApiVersion": "1.43", "Os": runtime.GOOS, "Arch": runtime.GOARCH,
		"Components": comps,
	})
}

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	cs, _ := s.list(r.Context())
	run, stopped := 0, 0
	for _, c := range cs {
		if c.State == "running" {
			run++
		} else {
			stopped++
		}
	}
	imgs, _ := s.d.Images(r.Context())
	host, _ := os.Hostname()
	v, _ := s.d.Version(r.Context())
	writeJSON(w, map[string]any{
		"Name": host, "OperatingSystem": osName(), "OSType": runtime.GOOS, "Architecture": archName(),
		"ServerVersion": v, "NCPU": runtime.NumCPU(), "MemTotal": memTotal(),
		"Containers": len(cs), "ContainersRunning": run, "ContainersStopped": stopped, "Images": len(imgs),
	})
}

func archName() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	}
	return runtime.GOARCH
}

func osName() string {
	if b, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(l, "PRETTY_NAME="); ok {
				return strings.Trim(v, `"`)
			}
		}
	}
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("sw_vers", "-productVersion").Output(); err == nil {
			return "macOS " + strings.TrimSpace(string(out))
		}
	}
	return runtime.GOOS
}

func memTotal() int64 {
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(l, "MemTotal:"); ok {
				f := strings.Fields(v)
				if len(f) > 0 {
					n, _ := strconv.ParseInt(f[0], 10, 64)
					return n * 1024
				}
			}
		}
	}
	if out, err := exec.Command("sysctl", "-n", "hw.memsize").Output(); err == nil {
		n, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
		return n
	}
	return 0
}

// stats answers in the Docker format; the CPU deltas are chosen so the
// client computes the driver's percentage.
func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	c, err := s.find(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	st, err := s.d.Stats(r.Context(), c.ID)
	if err != nil && !errors.Is(err, ErrUnsupported) {
		writeErr(w, err)
		return
	}
	const sys = 1_000_000_000
	writeJSON(w, map[string]any{
		"cpu_stats": map[string]any{
			"cpu_usage":        map[string]any{"total_usage": uint64(st.CPU / 100 * sys)},
			"system_cpu_usage": uint64(sys), "online_cpus": 1,
		},
		"precpu_stats": map[string]any{"cpu_usage": map[string]any{"total_usage": 0}, "system_cpu_usage": 0},
		"memory_stats": map[string]any{"usage": st.Mem, "limit": st.MemLimit},
		"networks":     map[string]any{"eth0": map[string]any{"rx_bytes": st.NetRx, "tx_bytes": st.NetTx}},
	})
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	c, err := s.find(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	q := r.URL.Query()
	tail, err := strconv.Atoi(q.Get("tail"))
	if err != nil {
		tail = -1
	}
	follow := q.Get("follow") == "1" || q.Get("follow") == "true"
	argv := s.d.LogsCmd(c.ID, tail, follow)
	cmd := exec.CommandContext(r.Context(), argv[0], argv[1:]...)
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		writeErr(w, err)
		return
	}
	go func() { defer applog.Recover("shim/shim.go:480"); pw.CloseWithError(cmd.Wait()) }()
	w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, err := pr.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if fl != nil {
				fl.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

// events turns changes in the container list into Docker events, so the
// app refreshes on its own.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	if fl != nil {
		fl.Flush()
	}
	enc := json.NewEncoder(w)
	prev := map[string]string{}
	first := true
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		cs, err := s.d.List(r.Context())
		if err == nil {
			cur := map[string]string{}
			for _, c := range cs {
				cur[c.ID] = c.State
				old, ok := prev[c.ID]
				if first {
					continue
				}
				action := ""
				switch {
				case !ok:
					action = "create"
				case old != c.State && c.State == "running":
					action = "start"
				case old != c.State:
					action = "die"
				}
				if action != "" {
					_ = enc.Encode(eventOf(c, action))
				}
			}
			if !first {
				for id := range prev {
					if _, ok := cur[id]; !ok {
						e := engine.Event{Type: "container", Action: "destroy", Time: time.Now().Unix()}
						e.Actor.ID = id
						_ = enc.Encode(e)
					}
				}
			}
			prev, first = cur, false
			if fl != nil {
				fl.Flush()
			}
		}
		select {
		case <-r.Context().Done():
			return
		case <-t.C:
		}
	}
}

func eventOf(c engine.Container, action string) engine.Event {
	e := engine.Event{Type: "container", Action: action, Time: time.Now().Unix()}
	e.Actor.ID = c.ID
	e.Actor.Attributes = map[string]string{"name": c.Name()}
	for k, v := range c.Labels {
		e.Actor.Attributes[k] = v
	}
	return e
}

func (s *Server) execCreate(w http.ResponseWriter, r *http.Request) {
	c, err := s.find(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	var cfg struct{ Cmd []string }
	_ = json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&cfg)
	if len(cfg.Cmd) == 0 {
		cfg.Cmd = []string{"/bin/sh"}
	}
	id := strconv.FormatInt(time.Now().UnixNano(), 36)
	s.execs.Store(id, s.d.ExecCmd(c.ID, cfg.Cmd))
	writeJSON(w, map[string]string{"Id": id})
}

// execStart hijacks the connection and runs the exec under a terminal.
func (s *Server) execStart(w http.ResponseWriter, r *http.Request) {
	v, ok := s.execs.LoadAndDelete(r.PathValue("id"))
	if !ok {
		writeErr(w, fmt.Errorf("exec: %w", ErrNotFound))
		return
	}
	argv := v.([]string)
	// The start options are in the body; read them so they don't end up
	// as keystrokes once the connection is hijacked.
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 64<<10))
	hj, ok := w.(http.Hijacker)
	if !ok {
		writeErr(w, errors.New("hijack indisponível"))
		return
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	_, _ = io.WriteString(conn, "HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.raw-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
	cmd := withTTY(argv)
	in, err := cmd.StdinPipe()
	if err != nil {
		return
	}
	cmd.Stdout, cmd.Stderr = conn, conn
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(conn, "erro: %v\r\n", err)
		return
	}
	go func() {
		defer applog.Recover("shim/shim.go:618")
		_, _ = io.Copy(in, bufio.NewReader(buf))
		_ = in.Close()
	}()
	_ = cmd.Wait()
}

// withTTY runs argv inside a pseudo terminal made by script(1), so shells
// and programs behave as in a terminal.
func withTTY(argv []string) *exec.Cmd {
	setup := "stty cols 120 rows 32 2>/dev/null; exec " + shellJoin(argv)
	if runtime.GOOS == "darwin" {
		return exec.Command("script", "-q", "/dev/null", "sh", "-c", setup)
	}
	return exec.Command("script", "-qfec", setup, "/dev/null")
}

func shellJoin(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(parts, " ")
}

// run executes a CLI and maps "not found" errors.
func run(ctx context.Context, argv ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var errb strings.Builder
	cmd.Stderr = &errb
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		// Keep the last meaningful line of logrus-style output.
		lines := strings.Split(msg, "\n")
		msg = lines[len(lines)-1]
		if i := strings.Index(msg, `msg="`); i >= 0 {
			msg = strings.TrimSuffix(msg[i+5:], `"`)
		}
		low := strings.ToLower(msg)
		if strings.Contains(low, "no such") || strings.Contains(low, "not found") {
			return out, fmt.Errorf("%s: %w", msg, ErrNotFound)
		}
		return out, errors.New(msg)
	}
	return out, nil
}

// parseSize reads sizes like "17.43MB", "736KiB" or "1.2 GiB".
func parseSize(s string) uint64 {
	s = strings.TrimSpace(s)
	i := 0
	for i < len(s) && (s[i] == '.' || (s[i] >= '0' && s[i] <= '9')) {
		i++
	}
	f, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0
	}
	unit := strings.ToLower(strings.TrimSpace(s[i:]))
	mul := 1.0
	switch strings.TrimSuffix(unit, "b") {
	case "k", "ki":
		mul = 1024
	case "m", "mi":
		mul = 1 << 20
	case "g", "gi":
		mul = 1 << 30
	case "t", "ti":
		mul = 1 << 40
	case "e", "ei":
		return 0 // "unlimited" in nerdctl
	}
	if !strings.Contains(unit, "i") && unit != "b" && unit != "" {
		// Decimal units (MB) are close enough to binary for a display.
		mul = map[string]float64{"kb": 1e3, "mb": 1e6, "gb": 1e9, "tb": 1e12}[unit]
		if mul == 0 {
			mul = 1
		}
	}
	return uint64(f * mul)
}

// uptime formats the Status text like Docker does.
func uptime(running bool, since time.Time, exit int) string {
	if !running {
		if exit != 0 {
			return fmt.Sprintf("Exited (%d)", exit)
		}
		return "Exited"
	}
	if since.IsZero() {
		return "Up"
	}
	d := time.Since(since)
	switch {
	case d < time.Minute:
		return "Up " + strconv.Itoa(int(d.Seconds())) + " seconds"
	case d < time.Hour:
		return "Up " + strconv.Itoa(int(d.Minutes())) + " minutes"
	case d < 48*time.Hour:
		return "Up " + strconv.Itoa(int(d.Hours())) + " hours"
	}
	return "Up " + strconv.Itoa(int(d.Hours()/24)) + " days"
}

// update takes the body of the Docker API (/containers/{id}/update).
func (s *Server) update(w http.ResponseWriter, r *http.Request) {
	u, ok := s.d.(Updater)
	if !ok {
		writeErr(w, fmt.Errorf("mudar recursos sem recriar: %w", ErrUnsupported))
		return
	}
	var body struct {
		NanoCpus          int64
		Memory            int64
		MemorySwap        int64
		MemoryReservation int64
		PidsLimit         int64
		RestartPolicy     struct{ Name string }
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil {
		writeErr(w, err)
		return
	}
	c, err := s.find(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	res := engine.Resources{NanoCPUs: body.NanoCpus, Memory: body.Memory, MemorySwap: body.MemorySwap,
		MemoryReservation: body.MemoryReservation, PidsLimit: body.PidsLimit, Restart: body.RestartPolicy.Name}
	if err := u.Update(r.Context(), c.ID, res); err != nil {
		writeErr(w, err)
		return
	}
	s.forget()
	_ = json.NewEncoder(w).Encode(map[string]any{"Warnings": []string{}})
}
