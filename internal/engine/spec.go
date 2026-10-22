package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// ContainerSpec is everything the app lets you set on a container, close
// to what Portainer offers. It converts to the Docker create body and back
// from an inspect, which is how containers are edited and duplicated.
type ContainerSpec struct {
	Name       string   `json:"name"`
	Image      string   `json:"image"`
	Cmd        []string `json:"cmd,omitempty"`
	Entrypoint []string `json:"entrypoint,omitempty"`
	WorkingDir string   `json:"workingDir,omitempty"`
	User       string   `json:"user,omitempty"`
	Tty        bool     `json:"tty,omitempty"`
	OpenStdin  bool     `json:"openStdin,omitempty"`

	Env        []string          `json:"env,omitempty"`    // KEY=value
	Labels     map[string]string `json:"labels,omitempty"` //
	Ports      []string          `json:"ports,omitempty"`  // [ip:]host:container[/proto], or container[/proto]
	PublishAll bool              `json:"publishAll,omitempty"`
	Volumes    []string          `json:"volumes,omitempty"` // volume-or-path:/target[:ro]
	Tmpfs      []string          `json:"tmpfs,omitempty"`   // /path[:options]

	Network    string   `json:"network,omitempty"` // bridge, host, none, a network name, container:<id>
	IPv4       string   `json:"ipv4,omitempty"`    // fixed address on that network
	Hostname   string   `json:"hostname,omitempty"`
	Domainname string   `json:"domainname,omitempty"`
	MacAddress string   `json:"macAddress,omitempty"`
	DNS        []string `json:"dns,omitempty"`
	ExtraHosts []string `json:"extraHosts,omitempty"` // host:ip

	Restart    string `json:"restart,omitempty"` // no, always, unless-stopped, on-failure
	RestartMax int    `json:"restartMax,omitempty"`

	Privileged bool              `json:"privileged,omitempty"`
	Init       bool              `json:"init,omitempty"`
	ReadOnly   bool              `json:"readOnly,omitempty"`
	CapAdd     []string          `json:"capAdd,omitempty"`
	CapDrop    []string          `json:"capDrop,omitempty"`
	Devices    []string          `json:"devices,omitempty"` // /dev/host[:/dev/ctr[:rwm]]
	Sysctls    map[string]string `json:"sysctls,omitempty"`

	Memory            int64   `json:"memory,omitempty"` // bytes
	MemoryReservation int64   `json:"memoryReservation,omitempty"`
	CPUs              float64 `json:"cpus,omitempty"`
	ShmSize           int64   `json:"shmSize,omitempty"`

	LogDriver string            `json:"logDriver,omitempty"`
	LogOpts   map[string]string `json:"logOpts,omitempty"`
}

// SplitPort reads [ip:]host:container[/proto] or container[/proto].
func SplitPort(p string) (ip, host, ctr, proto string) {
	proto = "tcp"
	if i := strings.LastIndex(p, "/"); i > 0 {
		p, proto = p[:i], p[i+1:]
	}
	parts := strings.Split(p, ":")
	switch len(parts) {
	case 1:
		ctr = parts[0]
	case 2:
		host, ctr = parts[0], parts[1]
	default:
		n := len(parts)
		ip, host, ctr = strings.Join(parts[:n-2], ":"), parts[n-2], parts[n-1]
	}
	return
}

func device(d string) map[string]string {
	parts := strings.Split(d, ":")
	m := map[string]string{"PathOnHost": parts[0], "PathInContainer": parts[0], "CgroupPermissions": "rwm"}
	if len(parts) > 1 && parts[1] != "" {
		m["PathInContainer"] = parts[1]
	}
	if len(parts) > 2 && parts[2] != "" {
		m["CgroupPermissions"] = parts[2]
	}
	return m
}

// CreateBody is the body of POST /containers/create.
func (s ContainerSpec) CreateBody() map[string]any {
	exposed := map[string]any{}
	binds := map[string][]map[string]string{}
	for _, p := range s.Ports {
		ip, host, ctr, proto := SplitPort(p)
		key := ctr + "/" + proto
		exposed[key] = map[string]any{}
		if host != "" || ip != "" {
			binds[key] = append(binds[key], map[string]string{"HostIp": ip, "HostPort": host})
		}
	}
	hc := map[string]any{"PortBindings": binds, "Binds": s.Volumes, "PublishAllPorts": s.PublishAll}
	if s.Restart != "" && s.Restart != "no" {
		rp := map[string]any{"Name": s.Restart}
		if s.Restart == "on-failure" && s.RestartMax > 0 {
			rp["MaximumRetryCount"] = s.RestartMax
		}
		hc["RestartPolicy"] = rp
	}
	if s.Network != "" {
		hc["NetworkMode"] = s.Network
	}
	if len(s.Tmpfs) > 0 {
		t := map[string]string{}
		for _, x := range s.Tmpfs {
			path, opts, _ := strings.Cut(x, ":")
			t[path] = opts
		}
		hc["Tmpfs"] = t
	}
	if len(s.DNS) > 0 {
		hc["Dns"] = s.DNS
	}
	if len(s.ExtraHosts) > 0 {
		hc["ExtraHosts"] = s.ExtraHosts
	}
	if s.Privileged {
		hc["Privileged"] = true
	}
	if s.Init {
		hc["Init"] = true
	}
	if s.ReadOnly {
		hc["ReadonlyRootfs"] = true
	}
	if len(s.CapAdd) > 0 {
		hc["CapAdd"] = s.CapAdd
	}
	if len(s.CapDrop) > 0 {
		hc["CapDrop"] = s.CapDrop
	}
	if len(s.Devices) > 0 {
		var ds []map[string]string
		for _, d := range s.Devices {
			ds = append(ds, device(d))
		}
		hc["Devices"] = ds
	}
	if len(s.Sysctls) > 0 {
		hc["Sysctls"] = s.Sysctls
	}
	if s.Memory > 0 {
		hc["Memory"] = s.Memory
	}
	if s.MemoryReservation > 0 {
		hc["MemoryReservation"] = s.MemoryReservation
	}
	if s.CPUs > 0 {
		hc["NanoCpus"] = int64(s.CPUs * 1e9)
	}
	if s.ShmSize > 0 {
		hc["ShmSize"] = s.ShmSize
	}
	if s.LogDriver != "" {
		hc["LogConfig"] = map[string]any{"Type": s.LogDriver, "Config": s.LogOpts}
	}
	body := map[string]any{"Image": s.Image, "Env": s.Env, "ExposedPorts": exposed, "HostConfig": hc, "Labels": s.Labels,
		"Tty": s.Tty, "OpenStdin": s.OpenStdin, "AttachStdin": s.OpenStdin}
	if len(s.Cmd) > 0 {
		body["Cmd"] = s.Cmd
	}
	if len(s.Entrypoint) > 0 {
		body["Entrypoint"] = s.Entrypoint
	}
	for k, v := range map[string]string{"WorkingDir": s.WorkingDir, "User": s.User, "Hostname": s.Hostname, "Domainname": s.Domainname, "MacAddress": s.MacAddress} {
		if v != "" {
			body[k] = v
		}
	}
	if s.IPv4 != "" && s.Network != "" {
		body["NetworkingConfig"] = map[string]any{"EndpointsConfig": map[string]any{
			s.Network: map[string]any{"IPAMConfig": map[string]string{"IPv4Address": s.IPv4}},
		}}
	}
	return body
}

// rawContainer is the part of a create body or an inspect that maps back
// to a spec (both share these names).
type rawContainer struct {
	ID           string `json:"Id"`
	Name         string
	Image        string
	Cmd          []string
	Entrypoint   []string
	WorkingDir   string
	User         string
	Tty          bool
	OpenStdin    bool
	Env          []string
	Labels       map[string]string
	ExposedPorts map[string]any
	Hostname     string
	Domainname   string
	MacAddress   string
	Config       *rawContainer // inspect puts the above under Config
	HostConfig   struct {
		Binds           []string
		PortBindings    map[string][]struct{ HostIp, HostPort string }
		PublishAllPorts bool
		NetworkMode     string
		RestartPolicy   struct {
			Name              string
			MaximumRetryCount int
		}
		Tmpfs             map[string]string
		Dns               []string
		ExtraHosts        []string
		Privileged        bool
		Init              *bool
		ReadonlyRootfs    bool
		CapAdd            []string
		CapDrop           []string
		Devices           []struct{ PathOnHost, PathInContainer, CgroupPermissions string }
		Sysctls           map[string]string
		Memory            int64
		MemoryReservation int64
		NanoCpus          int64
		ShmSize           int64
		LogConfig         struct {
			Type   string
			Config map[string]string
		}
	}
	Mounts []struct {
		Type, Name, Source, Destination string
		RW                              bool
	}
	NetworkingConfig *struct {
		EndpointsConfig map[string]struct {
			IPAMConfig *struct{ IPv4Address string }
		}
	}
	NetworkSettings *struct {
		Ports    map[string][]struct{ HostIp, HostPort string }
		Networks map[string]struct {
			IPAMConfig *struct{ IPv4Address string }
		}
	}
}

// SpecFromJSON reads a create body or an inspect result.
func SpecFromJSON(b []byte) (ContainerSpec, error) {
	var r rawContainer
	if err := json.Unmarshal(b, &r); err != nil {
		return ContainerSpec{}, err
	}
	cfg := r
	if r.Config != nil {
		cfg = *r.Config
	}
	hc := r.HostConfig
	s := ContainerSpec{
		Name: strings.TrimPrefix(r.Name, "/"), Image: cfg.Image, Cmd: cfg.Cmd, Entrypoint: cfg.Entrypoint,
		WorkingDir: cfg.WorkingDir, User: cfg.User, Tty: cfg.Tty, OpenStdin: cfg.OpenStdin, Env: cfg.Env, Labels: cfg.Labels,
		Hostname: cfg.Hostname, Domainname: cfg.Domainname, MacAddress: cfg.MacAddress,
		PublishAll: hc.PublishAllPorts, Volumes: hc.Binds, Network: hc.NetworkMode,
		Restart: hc.RestartPolicy.Name, RestartMax: hc.RestartPolicy.MaximumRetryCount,
		DNS: hc.Dns, ExtraHosts: hc.ExtraHosts, Privileged: hc.Privileged, ReadOnly: hc.ReadonlyRootfs,
		CapAdd: hc.CapAdd, CapDrop: hc.CapDrop, Sysctls: hc.Sysctls,
		Memory: hc.Memory, MemoryReservation: hc.MemoryReservation, CPUs: float64(hc.NanoCpus) / 1e9, ShmSize: hc.ShmSize,
	}
	// Docker names the host after the id; a copy should get its own.
	if s.Hostname != "" && (len(r.ID) >= 12 && s.Hostname == r.ID[:12] || s.Network == "host") {
		s.Hostname = ""
	}
	if s.ShmSize == 64<<20 { // the engines' default
		s.ShmSize = 0
	}
	if hc.Init != nil {
		s.Init = *hc.Init
	}
	if s.Restart == "no" {
		s.Restart = ""
	}
	if hc.LogConfig.Type != "" {
		s.LogDriver, s.LogOpts = hc.LogConfig.Type, hc.LogConfig.Config
	}
	if len(hc.PortBindings) == 0 && r.NetworkSettings != nil {
		// Some runtimes (nerdctl) only report the live bindings.
		for k, bs := range r.NetworkSettings.Ports {
			if hc.PortBindings == nil {
				hc.PortBindings = map[string][]struct{ HostIp, HostPort string }{}
			}
			hc.PortBindings[k] = bs
		}
	}
	keys := make([]string, 0, len(cfg.ExposedPorts))
	for k := range cfg.ExposedPorts {
		keys = append(keys, k)
	}
	for k := range hc.PortBindings {
		if _, ok := cfg.ExposedPorts[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		port, proto, _ := strings.Cut(k, "/")
		if proto == "" {
			proto = "tcp"
		}
		bs := hc.PortBindings[k]
		if len(bs) == 0 {
			s.Ports = append(s.Ports, port+"/"+proto)
		}
		for _, b := range bs {
			p := b.HostPort + ":" + port + "/" + proto
			if b.HostPort == "" && (b.HostIp == "" || b.HostIp == "0.0.0.0" || b.HostIp == "::") {
				p = port + "/" + proto
			}
			if b.HostIp != "" && b.HostIp != "0.0.0.0" && b.HostIp != "::" {
				p = b.HostIp + ":" + p
			}
			s.Ports = append(s.Ports, p)
		}
	}
	s.Ports = dedupe(s.Ports)
	// Named volumes made through the Mounts API are not in Binds.
	have := map[string]bool{}
	for _, b := range s.Volumes {
		parts := strings.Split(b, ":")
		if len(parts) > 1 {
			have[parts[1]] = true
		}
	}
	for _, m := range r.Mounts {
		if have[m.Destination] || (m.Type != "volume" && m.Type != "bind") {
			continue
		}
		src := m.Name
		if m.Type == "bind" || src == "" {
			src = m.Source
		}
		v := src + ":" + m.Destination
		if !m.RW {
			v += ":ro"
		}
		s.Volumes = append(s.Volumes, v)
	}
	for path, opts := range hc.Tmpfs {
		t := path
		if opts != "" {
			t += ":" + opts
		}
		s.Tmpfs = append(s.Tmpfs, t)
	}
	sort.Strings(s.Tmpfs)
	for _, d := range hc.Devices {
		v := d.PathOnHost
		if d.PathInContainer != "" && d.PathInContainer != d.PathOnHost || d.CgroupPermissions != "" && d.CgroupPermissions != "rwm" {
			v += ":" + d.PathInContainer
		}
		if d.CgroupPermissions != "" && d.CgroupPermissions != "rwm" {
			v += ":" + d.CgroupPermissions
		}
		s.Devices = append(s.Devices, v)
	}
	if r.NetworkingConfig != nil {
		if ep, ok := r.NetworkingConfig.EndpointsConfig[s.Network]; ok && ep.IPAMConfig != nil {
			s.IPv4 = ep.IPAMConfig.IPv4Address
		}
	}
	if r.NetworkSettings != nil {
		if ep, ok := r.NetworkSettings.Networks[s.Network]; ok && ep.IPAMConfig != nil {
			s.IPv4 = ep.IPAMConfig.IPv4Address
		}
	}
	return s, nil
}

// Create creates (does not start) a container and returns its id.
func (c *Client) Create(ctx context.Context, s ContainerSpec) (string, error) {
	q := url.Values{}
	if s.Name != "" {
		q.Set("name", s.Name)
	}
	resp, err := c.request(ctx, http.MethodPost, "/containers/create", q, s.CreateBody())
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		ID string `json:"Id"`
	}
	err = json.NewDecoder(resp.Body).Decode(&out)
	return out.ID, err
}

// InspectRaw returns the full inspect document of a container.
func (c *Client) InspectRaw(ctx context.Context, id string) (json.RawMessage, error) {
	var raw json.RawMessage
	err := c.getJSON(ctx, "/containers/"+url.PathEscape(id)+"/json", nil, &raw)
	return raw, err
}

// Spec reads a container back into a spec, for editing or duplicating.
func (c *Client) Spec(ctx context.Context, id string) (ContainerSpec, error) {
	raw, err := c.InspectRaw(ctx, id)
	if err != nil {
		return ContainerSpec{}, err
	}
	return SpecFromJSON(raw)
}

func (c *Client) Rename(ctx context.Context, id, name string) error {
	resp, err := c.request(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/rename", url.Values{"name": {name}}, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// Connect attaches a container to a network, optionally with a fixed IP.
func (c *Client) Connect(ctx context.Context, network, id, ipv4 string) error {
	body := map[string]any{"Container": id}
	if ipv4 != "" {
		body["EndpointConfig"] = map[string]any{"IPAMConfig": map[string]string{"IPv4Address": ipv4}}
	}
	resp, err := c.request(ctx, http.MethodPost, "/networks/"+url.PathEscape(network)+"/connect", nil, body)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (c *Client) Disconnect(ctx context.Context, network, id string) error {
	resp, err := c.request(ctx, http.MethodPost, "/networks/"+url.PathEscape(network)+"/disconnect", nil, map[string]any{"Container": id, "Force": true})
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// ParseBytes reads sizes like 512m, 2g, 1.5G or a plain number of bytes.
func ParseBytes(s string) (int64, error) {
	t := strings.ToLower(strings.TrimSpace(s))
	if t == "" {
		return 0, nil
	}
	t = strings.TrimSuffix(strings.TrimSuffix(t, "b"), "i")
	mul := 1.0
	switch {
	case strings.HasSuffix(t, "k"):
		mul, t = 1<<10, t[:len(t)-1]
	case strings.HasSuffix(t, "m"):
		mul, t = 1<<20, t[:len(t)-1]
	case strings.HasSuffix(t, "g"):
		mul, t = 1<<30, t[:len(t)-1]
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("tamanho inválido: %q (use 512m ou 2g)", s)
	}
	return int64(f * mul), nil
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	out := xs[:0]
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
