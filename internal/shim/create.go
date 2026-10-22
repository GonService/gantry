package shim

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"gantry/internal/applog"
	"gantry/internal/engine"
)

// Inspector returns a Docker-shaped inspect document.
type Inspector interface {
	InspectRaw(ctx context.Context, id string) (json.RawMessage, error)
}

// Renamer and Connector are the optional container edits.
type Renamer interface {
	Rename(ctx context.Context, id, name string) error
}

type Connector interface {
	Connect(ctx context.Context, network, id, ipv4 string) error
	Disconnect(ctx context.Context, network, id string) error
}

func (s *Server) rename(w http.ResponseWriter, r *http.Request) {
	rn, ok := s.d.(Renamer)
	if !ok {
		writeErr(w, fmt.Errorf("renomear: %w", ErrUnsupported))
		return
	}
	c, err := s.find(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := rn.Rename(r.Context(), c.ID, r.URL.Query().Get("name")); err != nil {
		writeErr(w, err)
		return
	}
	s.forget()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) connect(w http.ResponseWriter, r *http.Request) {
	cn, ok := s.d.(Connector)
	if !ok {
		writeErr(w, fmt.Errorf("conectar rede: %w", ErrUnsupported))
		return
	}
	var b struct {
		Container      string
		EndpointConfig struct {
			IPAMConfig struct{ IPv4Address string }
		}
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&b)
	c, err := s.find(r.Context(), b.Container)
	if err != nil {
		writeErr(w, err)
		return
	}
	if r.PathValue("op") == "connect" {
		err = cn.Connect(r.Context(), r.PathValue("id"), c.ID, b.EndpointConfig.IPAMConfig.IPv4Address)
	} else {
		err = cn.Disconnect(r.Context(), r.PathValue("id"), c.ID)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	s.forget()
	w.WriteHeader(http.StatusOK)
}

// Tagger gives an image another name.
type Tagger interface {
	Tag(ctx context.Context, src, dst string) error
}

// Creator is implemented by drivers that can create things.
type Creator interface {
	CreateVolume(ctx context.Context, v engine.VolumeSpec) error
	CreateNetwork(ctx context.Context, n engine.NetworkSpec) error
	PullCmd(ref string) []string
	Create(ctx context.Context, s engine.ContainerSpec) (string, error)
}

func (s *Server) creator(w http.ResponseWriter) (Creator, bool) {
	c, ok := s.d.(Creator)
	if !ok {
		writeErr(w, fmt.Errorf("criar: %w", ErrUnsupported))
	}
	return c, ok
}

func (s *Server) createRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /build", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, fmt.Errorf("construir imagens por aqui (use %s build no host): %w", s.d.Runtime(), ErrUnsupported))
	})
	mux.HandleFunc("POST /images/{id}/tag", func(w http.ResponseWriter, r *http.Request) {
		tg, ok := s.d.(Tagger)
		if !ok {
			writeErr(w, fmt.Errorf("dar nome a imagens: %w", ErrUnsupported))
			return
		}
		q := r.URL.Query()
		dst := q.Get("repo")
		if t := q.Get("tag"); t != "" {
			dst += ":" + t
		}
		if err := tg.Tag(r.Context(), r.PathValue("id"), dst); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("POST /networks/{id}/{op}", func(w http.ResponseWriter, r *http.Request) {
		switch r.PathValue("op") {
		case "connect", "disconnect":
			s.connect(w, r)
		default:
			writeErr(w, fmt.Errorf("%s: %w", r.PathValue("op"), ErrUnsupported))
		}
	})
	mux.HandleFunc("POST /volumes/create", func(w http.ResponseWriter, r *http.Request) {
		c, ok := s.creator(w)
		if !ok {
			return
		}
		var b struct {
			Name, Driver string
			DriverOpts   map[string]string
			Labels       map[string]string
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&b)
		if err := c.CreateVolume(r.Context(), engine.VolumeSpec{Name: b.Name, Driver: b.Driver, Opts: b.DriverOpts, Labels: b.Labels}); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, map[string]string{"Name": b.Name})
	})
	mux.HandleFunc("POST /networks/create", func(w http.ResponseWriter, r *http.Request) {
		c, ok := s.creator(w)
		if !ok {
			return
		}
		var b struct {
			Name, Driver         string
			Internal, Attachable bool
			EnableIPv6           bool
			Options              map[string]string
			Labels               map[string]string
			IPAM                 struct {
				Config []struct{ Subnet, Gateway, IPRange string }
			}
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&b)
		n := engine.NetworkSpec{Name: b.Name, Driver: b.Driver, Internal: b.Internal, Attachable: b.Attachable, IPv6: b.EnableIPv6, Options: b.Options, Labels: b.Labels}
		for _, c := range b.IPAM.Config {
			if strings.Contains(c.Subnet, ":") {
				n.Subnet6, n.Gateway6 = c.Subnet, c.Gateway
			} else {
				n.Subnet, n.Gateway, n.IPRange = c.Subnet, c.Gateway, c.IPRange
			}
		}
		if err := c.CreateNetwork(r.Context(), n); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, map[string]string{"Id": b.Name})
	})
	mux.HandleFunc("POST /images/create", func(w http.ResponseWriter, r *http.Request) {
		c, ok := s.creator(w)
		if !ok {
			return
		}
		q := r.URL.Query()
		ref := q.Get("fromImage")
		if t := q.Get("tag"); t != "" {
			ref += ":" + t
		}
		argv := c.PullCmd(ref)
		cmd := exec.CommandContext(r.Context(), argv[0], argv[1:]...)
		pr, pw := io.Pipe()
		cmd.Stdout, cmd.Stderr = pw, pw
		if err := cmd.Start(); err != nil {
			writeErr(w, err)
			return
		}
		go func() { defer applog.Recover("shim/create.go:198"); pw.CloseWithError(cmd.Wait()) }()
		w.Header().Set("Content-Type", "application/json")
		fl, _ := w.(http.Flusher)
		enc := json.NewEncoder(w)
		sc := bufio.NewScanner(pr)
		var last string
		for sc.Scan() {
			// Progress bars redraw with carriage returns; keep the last part.
			l := sc.Text()
			if i := strings.LastIndex(l, "\r"); i >= 0 {
				l = l[i+1:]
			}
			if l = strings.TrimSpace(l); l == "" {
				continue
			}
			last = l
			_ = enc.Encode(map[string]string{"status": l})
			if fl != nil {
				fl.Flush()
			}
		}
		if err := sc.Err(); err != nil {
			msg := last
			if msg == "" {
				msg = err.Error()
			}
			_ = enc.Encode(map[string]string{"error": msg})
		}
	})
	mux.HandleFunc("POST /containers/create", func(w http.ResponseWriter, r *http.Request) {
		c, ok := s.creator(w)
		if !ok {
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeErr(w, err)
			return
		}
		spec, err := engine.SpecFromJSON(body)
		if err != nil {
			writeErr(w, err)
			return
		}
		spec.Name = r.URL.Query().Get("name")
		id, err := c.Create(r.Context(), spec)
		if err != nil {
			writeErr(w, err)
			return
		}
		s.forget()
		writeJSON(w, map[string]string{"Id": id})
	})
}

// createArgs turns a spec into nerdctl run-style flags (nearly all of
// Docker's).
func createArgs(s engine.ContainerSpec) []string {
	var a []string
	add := func(flag string, vs ...string) {
		for _, v := range vs {
			if v != "" {
				a = append(a, flag, v)
			}
		}
	}
	add("--name", s.Name)
	add("-p", s.Ports...)
	add("-e", s.Env...)
	add("-v", s.Volumes...)
	add("--network", s.Network)
	add("--workdir", s.WorkingDir)
	add("--user", s.User)
	add("--dns", s.DNS...)
	if len(s.Entrypoint) > 0 {
		add("--entrypoint", s.Entrypoint[0])
	}
	if s.Tty {
		a = append(a, "-t")
	}
	if s.OpenStdin {
		a = append(a, "-i")
	}
	if s.Memory > 0 {
		add("--memory", fmt.Sprint(s.Memory))
	}
	if s.CPUs > 0 {
		add("--cpus", strconv.FormatFloat(s.CPUs, 'f', -1, 64))
	}
	if s.Restart != "" && s.Restart != "no" {
		r := s.Restart
		if r == "on-failure" && s.RestartMax > 0 {
			r += ":" + fmt.Sprint(s.RestartMax)
		}
		add("--restart", r)
	}
	add("--hostname", s.Hostname)
	add("--mac-address", s.MacAddress)
	add("--ip", s.IPv4)
	add("--add-host", s.ExtraHosts...)
	add("--cap-add", s.CapAdd...)
	add("--cap-drop", s.CapDrop...)
	add("--device", s.Devices...)
	add("--tmpfs", s.Tmpfs...)
	add("--log-driver", s.LogDriver)
	add("--log-opt", sortedKV(s.LogOpts)...)
	add("--sysctl", sortedKV(s.Sysctls)...)
	if s.MemoryReservation > 0 {
		add("--memory-reservation", fmt.Sprint(s.MemoryReservation))
	}
	if s.ShmSize > 0 {
		add("--shm-size", fmt.Sprint(s.ShmSize))
	}
	for _, f := range []struct {
		flag string
		on   bool
	}{{"--privileged", s.Privileged}, {"--init", s.Init}, {"--read-only", s.ReadOnly}, {"-P", s.PublishAll}} {
		if f.on {
			a = append(a, f.flag)
		}
	}
	add("--label", sortedKV(s.Labels)...)
	a = append(a, s.Image)
	if len(s.Entrypoint) > 1 {
		a = append(a, s.Entrypoint[1:]...)
	}
	return append(a, s.Cmd...)
}

func sortedKV(m map[string]string) []string {
	var out []string
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

func labelArgs(labels map[string]string) []string {
	var a []string
	for k, v := range labels {
		a = append(a, "--label", k+"="+v)
	}
	sort.Strings(a)
	return a
}

/* nerdctl */

func (n *Nerdctl) CreateVolume(ctx context.Context, v engine.VolumeSpec) error {
	args := append([]string{"volume", "create"}, labelArgs(v.Labels)...)
	_, err := n.run(ctx, append(args, v.Name)...)
	return err
}

func (n *Nerdctl) CreateNetwork(ctx context.Context, s engine.NetworkSpec) error {
	args := []string{"network", "create"}
	if s.Driver != "" {
		args = append(args, "--driver", s.Driver)
	}
	if s.Subnet != "" {
		args = append(args, "--subnet", s.Subnet)
	}
	if s.Gateway != "" {
		args = append(args, "--gateway", s.Gateway)
	}
	if s.IPRange != "" {
		args = append(args, "--ip-range", s.IPRange)
	}
	if s.IPv6 || s.Subnet6 != "" {
		args = append(args, "--ipv6")
		if s.Subnet6 != "" {
			args = append(args, "--subnet", s.Subnet6)
		}
	}
	if s.Internal {
		args = append(args, "--internal")
	}
	for _, o := range sortedKV(s.Options) {
		args = append(args, "-o", o)
	}
	args = append(args, labelArgs(s.Labels)...)
	_, err := n.run(ctx, append(args, s.Name)...)
	return err
}

func (n *Nerdctl) PullCmd(ref string) []string { return append(n.base(), "pull", ref) }

func (n *Nerdctl) Create(ctx context.Context, s engine.ContainerSpec) (string, error) {
	out, err := n.run(ctx, append([]string{"create"}, createArgs(s)...)...)
	if err != nil {
		return "", err
	}
	return lastLine(out), nil
}

func (n *Nerdctl) InspectRaw(ctx context.Context, id string) (json.RawMessage, error) {
	out, err := n.run(ctx, "inspect", "--mode", "dockercompat", id)
	if err != nil {
		return nil, err
	}
	var arr []map[string]any
	if err := json.Unmarshal(out, &arr); err != nil || len(arr) == 0 {
		return nil, fmt.Errorf("inspect de %s: %w", id, ErrNotFound)
	}
	return json.Marshal(dockerize(arr[0]))
}

// dockerize fills what nerdctl's dockercompat inspect leaves in its own
// labels into the places Docker uses, and drops those labels, so the
// container can be read back into a spec and recreated.
func dockerize(d map[string]any) map[string]any {
	obj := func(m map[string]any, k string) map[string]any {
		if v, ok := m[k].(map[string]any); ok {
			return v
		}
		v := map[string]any{}
		m[k] = v
		return v
	}
	cfg, hc := obj(d, "Config"), obj(d, "HostConfig")
	labels, _ := cfg["Labels"].(map[string]any)
	clean := map[string]any{}
	for k, v := range labels {
		if !strings.HasPrefix(k, "nerdctl/") && !strings.HasPrefix(k, "containerd.io/") && !strings.HasPrefix(k, "io.containerd.") {
			clean[k] = v
		}
	}
	cfg["Labels"] = clean
	label := func(k string) string { s, _ := labels[k].(string); return s }
	if p := label("containerd.io/restart.policy"); p != "" {
		name, max, _ := strings.Cut(p, ":")
		n, _ := strconv.Atoi(max)
		hc["RestartPolicy"] = map[string]any{"Name": name, "MaximumRetryCount": n}
	}
	var nets []string
	if json.Unmarshal([]byte(label("nerdctl/networks")), &nets) == nil && len(nets) > 0 {
		if nm, _ := hc["NetworkMode"].(string); nm == "" {
			hc["NetworkMode"] = nets[0]
		}
		if nets[0] == "host" {
			cfg["Hostname"] = ""
		}
	}
	if cmd, _ := cfg["Cmd"].([]any); len(cmd) == 0 {
		if ep, _ := cfg["Entrypoint"].([]any); len(ep) == 0 {
			if path, _ := d["Path"].(string); path != "" {
				cfg["Entrypoint"] = []any{path}
				cfg["Cmd"] = d["Args"]
			}
		}
	}
	q, _ := hc["CpuQuota"].(float64)
	per, _ := hc["CpuPeriod"].(float64)
	if n, _ := hc["NanoCpus"].(float64); n == 0 && q > 0 && per > 0 {
		hc["NanoCpus"] = int64(q / per * 1e9)
	}
	if env, ok := cfg["Env"].([]any); ok {
		var keep []any
		for _, e := range env {
			if s, _ := e.(string); !strings.HasPrefix(s, "HOSTNAME=") {
				keep = append(keep, e)
			}
		}
		cfg["Env"] = keep
	}
	return d
}

func (n *Nerdctl) Tag(ctx context.Context, src, dst string) error {
	_, err := n.run(ctx, "tag", src, dst)
	return err
}

func (n *Nerdctl) Rename(ctx context.Context, id, name string) error {
	_, err := n.run(ctx, "rename", id, name)
	return err
}

func lastLine(b []byte) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
