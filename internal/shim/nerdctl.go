package shim

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"gantry/internal/engine"
)

// Nerdctl drives containerd through the nerdctl command.
type Nerdctl struct {
	Namespace string // default, k8s.io (k3s), …
	Address   string // containerd socket; empty for the default
	Sudo      bool   // run as root with sudo -n
	Bin       string // nerdctl by default
	Kind      string // runtime name shown in the app: nerdctl or k3s
}

func (n *Nerdctl) Runtime() string {
	if n.Kind != "" {
		return n.Kind
	}
	return "nerdctl"
}

func (n *Nerdctl) base() []string {
	var argv []string
	if n.Sudo {
		argv = append(argv, "sudo", "-n")
	}
	bin := n.Bin
	if bin == "" {
		bin = "nerdctl"
	}
	argv = append(argv, bin)
	if n.Address != "" {
		argv = append(argv, "--address", n.Address)
	}
	if n.Namespace != "" {
		argv = append(argv, "--namespace", n.Namespace)
	}
	return argv
}

func (n *Nerdctl) run(ctx context.Context, args ...string) ([]byte, error) {
	return run(ctx, append(n.base(), args...)...)
}

// Compose is the compose command for labs on this runtime.
func (n *Nerdctl) Compose() []string { return append(n.base(), "compose") }

func (n *Nerdctl) Version(ctx context.Context) (string, error) {
	out, err := n.run(ctx, "version", "--format", "{{json .}}")
	if err != nil {
		return "", err
	}
	var v struct {
		Client struct{ Version string }
		Server struct {
			Components []struct{ Name, Version string }
		}
	}
	_ = json.Unmarshal(lastJSONLine(out), &v)
	ver := strings.TrimPrefix(v.Client.Version, "v")
	for _, c := range v.Server.Components {
		if c.Name == "containerd" {
			ver += " · containerd " + strings.TrimPrefix(c.Version, "v")
		}
	}
	return ver, nil
}

func lastJSONLine(b []byte) []byte {
	lines := bytes.Split(bytes.TrimSpace(b), []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		if bytes.HasPrefix(bytes.TrimSpace(lines[i]), []byte("{")) {
			return lines[i]
		}
	}
	return nil
}

// dockercompat is the part of `nerdctl inspect` the app needs.
type dockercompat struct {
	ID      string `json:"Id"`
	Name    string
	Created string
	Image   string
	State   struct {
		Status    string
		Running   bool
		ExitCode  int
		StartedAt string
	}
	Config struct {
		Image  string
		Labels map[string]string
	}
	Mounts []struct {
		Type        string
		Name        string
		Source      string
		Destination string
	}
	NetworkSettings struct {
		Ports    map[string][]struct{ HostIp, HostPort string }
		Networks map[string]struct{ IPAddress string }
	}
}

func (n *Nerdctl) List(ctx context.Context) ([]engine.Container, error) {
	ids, err := n.run(ctx, "ps", "-a", "-q", "--no-trunc")
	if err != nil {
		return nil, err
	}
	list := strings.Fields(string(ids))
	if len(list) == 0 {
		return []engine.Container{}, nil
	}
	out, err := n.run(ctx, append([]string{"inspect", "--mode", "dockercompat"}, list...)...)
	if err != nil && len(out) == 0 {
		return nil, err
	}
	var raw []dockercompat
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}
	res := make([]engine.Container, 0, len(raw))
	for _, d := range raw {
		c, ok := n.convert(d)
		if ok {
			res = append(res, c)
		}
	}
	return res, nil
}

func (n *Nerdctl) convert(d dockercompat) (engine.Container, bool) {
	labels := map[string]string{}
	for k, v := range d.Config.Labels {
		if strings.HasPrefix(k, "nerdctl/") {
			continue
		}
		labels[k] = v
	}
	name := strings.TrimPrefix(d.Name, "/")
	// Kubernetes containers (k3s): skip the pod sandboxes and group the
	// rest by namespace, as labs.
	if ns := labels["io.kubernetes.pod.namespace"]; ns != "" {
		if labels["io.cri-containerd.kind"] == "sandbox" {
			return engine.Container{}, false
		}
		ctr := labels["io.kubernetes.container.name"]
		pod := labels["io.kubernetes.pod.name"]
		labels["com.docker.compose.project"] = "k8s-" + ns
		labels["com.docker.compose.service"] = pod + "/" + ctr
		if pod != "" {
			name = pod + "_" + ctr
		}
	}
	if name == "" {
		name = d.ID[:min(12, len(d.ID))]
	}
	c := engine.Container{ID: d.ID, Names: []string{"/" + name}, Image: d.Image, ImageID: d.Image, Labels: labels, State: d.State.Status}
	if c.Image == "" {
		c.Image = d.Config.Image
	}
	if c.State == "" {
		c.State = map[bool]string{true: "running", false: "exited"}[d.State.Running]
	}
	if c.State == "created" || c.State == "stopped" {
		c.State = "exited"
	}
	if t, err := time.Parse(time.RFC3339Nano, d.Created); err == nil {
		c.Created = t.Unix()
	}
	started, _ := time.Parse(time.RFC3339Nano, d.State.StartedAt)
	c.Status = uptime(c.State == "running", started, d.State.ExitCode)
	for key, binds := range d.NetworkSettings.Ports {
		port, proto, _ := strings.Cut(key, "/")
		priv, _ := strconv.Atoi(port)
		if proto == "" {
			proto = "tcp"
		}
		if len(binds) == 0 {
			c.Ports = append(c.Ports, engine.Port{PrivatePort: priv, Type: proto})
		}
		for _, b := range binds {
			pub, _ := strconv.Atoi(b.HostPort)
			c.Ports = append(c.Ports, engine.Port{IP: b.HostIp, PrivatePort: priv, PublicPort: pub, Type: proto})
		}
	}
	c.NetworkSettings.Networks = map[string]engine.Endpoint{}
	for k, v := range d.NetworkSettings.Networks {
		c.NetworkSettings.Networks[k] = engine.Endpoint{IPAddress: v.IPAddress}
	}
	for _, m := range d.Mounts {
		c.Mounts = append(c.Mounts, engine.Mount{Type: m.Type, Name: m.Name, Source: m.Source, Destination: m.Destination})
	}
	return c, true
}

func (n *Nerdctl) Action(ctx context.Context, id, action string) error {
	_, err := n.run(ctx, action, id)
	return err
}

// Update changes the limits of a container without recreating it. Only
// the values given are passed, so the others stay as they are.
func (n *Nerdctl) Update(ctx context.Context, id string, r engine.Resources) error {
	args := updateArgs(r)
	if len(args) == 0 {
		return nil
	}
	_, err := n.run(ctx, append(append([]string{"update"}, args...), id)...)
	return err
}

func updateArgs(r engine.Resources) []string {
	var args []string
	if r.NanoCPUs > 0 {
		args = append(args, "--cpus", strconv.FormatFloat(float64(r.NanoCPUs)/1e9, 'f', -1, 64))
	}
	if r.Memory > 0 {
		args = append(args, "--memory", strconv.FormatInt(r.Memory, 10))
		if r.MemorySwap > 0 {
			args = append(args, "--memory-swap", strconv.FormatInt(r.MemorySwap, 10))
		}
	}
	if r.MemoryReservation > 0 {
		args = append(args, "--memory-reservation", strconv.FormatInt(r.MemoryReservation, 10))
	}
	if r.PidsLimit > 0 {
		args = append(args, "--pids-limit", strconv.FormatInt(r.PidsLimit, 10))
	}
	if r.Restart != "" {
		args = append(args, "--restart", r.Restart)
	}
	return args
}

func (n *Nerdctl) Remove(ctx context.Context, id string, force bool) error {
	args := []string{"rm"}
	if force {
		args = append(args, "-f")
	}
	_, err := n.run(ctx, append(args, id)...)
	return err
}

func (n *Nerdctl) Stats(ctx context.Context, id string) (Stats, error) {
	out, err := n.run(ctx, "stats", "--no-stream", "--format", "{{json .}}", id)
	if err != nil {
		return Stats{}, err
	}
	var r struct{ CPUPerc, MemUsage, NetIO string }
	if err := json.Unmarshal(lastJSONLine(out), &r); err != nil {
		return Stats{}, err
	}
	var st Stats
	st.CPU, _ = strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(r.CPUPerc), "%"), 64)
	used, limit, _ := strings.Cut(r.MemUsage, "/")
	st.Mem, st.MemLimit = parseSize(used), parseSize(limit)
	rx, tx, _ := strings.Cut(r.NetIO, "/")
	st.NetRx, st.NetTx = parseSize(rx), parseSize(tx)
	return st, nil
}

func (n *Nerdctl) LogsCmd(id string, tail int, follow bool) []string {
	argv := append(n.base(), "logs")
	if tail >= 0 {
		argv = append(argv, "--tail", strconv.Itoa(tail))
	}
	if follow {
		argv = append(argv, "-f")
	}
	return append(argv, id)
}

func (n *Nerdctl) ExecCmd(id string, cmd []string) []string {
	return append(append(n.base(), "exec", "-it", "-e", "TERM=xterm-256color", id), cmd...)
}

func jsonLines(b []byte, fn func([]byte)) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if l := bytes.TrimSpace(sc.Bytes()); len(l) > 0 && l[0] == '{' {
			fn(l)
		}
	}
}

func (n *Nerdctl) Images(ctx context.Context) ([]engine.Image, error) {
	out, err := n.run(ctx, "images", "--format", "{{json .}}")
	if err != nil {
		return nil, err
	}
	var res []engine.Image
	byID := map[string]int{}
	jsonLines(out, func(l []byte) {
		var r struct{ ID, Repository, Tag, Name, Size, CreatedAt, Digest string }
		if json.Unmarshal(l, &r) != nil {
			return
		}
		// The full reference is what containers point at (no image ids
		// in nerdctl's inspect), so it is the id here too.
		id := r.Name
		if id == "" {
			id = r.Digest
		}
		tag := r.Repository + ":" + r.Tag
		if r.Repository == "" || r.Repository == "<none>" {
			tag = "<none>:<none>"
		}
		if i, ok := byID[id]; ok {
			res[i].RepoTags = append(res[i].RepoTags, tag)
			return
		}
		img := engine.Image{ID: id, RepoTags: []string{tag}, Size: int64(parseSize(r.Size))}
		if t, err := time.Parse("2006-01-02 15:04:05 -0700 MST", r.CreatedAt); err == nil {
			img.Created = t.Unix()
		}
		byID[id] = len(res)
		res = append(res, img)
	})
	return res, nil
}

func (n *Nerdctl) RemoveImage(ctx context.Context, id string, force bool) error {
	args := []string{"rmi"}
	if force {
		args = append(args, "-f")
	}
	_, err := n.run(ctx, append(args, id)...)
	return err
}

func (n *Nerdctl) Volumes(ctx context.Context) ([]engine.Volume, error) {
	out, err := n.run(ctx, "volume", "ls", "--format", "{{json .}}")
	if err != nil {
		return nil, err
	}
	var res []engine.Volume
	jsonLines(out, func(l []byte) {
		var r struct{ Name, Driver, Mountpoint, Labels string }
		if json.Unmarshal(l, &r) == nil && r.Name != "" {
			res = append(res, engine.Volume{Name: r.Name, Driver: r.Driver, Mountpoint: r.Mountpoint, Labels: parseLabels(r.Labels)})
		}
	})
	return res, nil
}

func (n *Nerdctl) RemoveVolume(ctx context.Context, name string) error {
	_, err := n.run(ctx, "volume", "rm", name)
	return err
}

func (n *Nerdctl) Networks(ctx context.Context) ([]engine.NetworkInfo, error) {
	out, err := n.run(ctx, "network", "ls", "--format", "{{json .}}")
	if err != nil {
		return nil, err
	}
	var res []engine.NetworkInfo
	jsonLines(out, func(l []byte) {
		var r struct{ ID, Name, Labels string }
		if json.Unmarshal(l, &r) == nil && r.Name != "" {
			id := r.ID
			if id == "" {
				id = r.Name
			}
			res = append(res, engine.NetworkInfo{ID: id, Name: r.Name, Driver: "bridge", Labels: parseLabels(r.Labels)})
		}
	})
	return res, nil
}

func (n *Nerdctl) RemoveNetwork(ctx context.Context, id string) error {
	_, err := n.run(ctx, "network", "rm", id)
	return err
}

func (n *Nerdctl) Prune(ctx context.Context, what string) (int, error) {
	var args []string
	switch what {
	case "images":
		args = []string{"image", "prune", "-a", "-f"}
	case "volumes":
		args = []string{"volume", "prune", "-a", "-f"}
	case "networks":
		args = []string{"network", "prune", "-f"}
	default:
		return 0, ErrUnsupported
	}
	out, err := n.run(ctx, args...)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, l := range strings.Split(string(out), "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "Deleted") && !strings.HasPrefix(l, "Total") && !strings.HasSuffix(l, ":") {
			count++
		}
	}
	return count, nil
}

// parseLabels reads "k=v,k2=v2".
func parseLabels(s string) map[string]string {
	out := map[string]string{}
	for _, kv := range strings.Split(s, ",") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			out[k] = v
		}
	}
	return out
}
