// Package engine is a small client for the Docker Engine API.
// It talks to Docker and to Podman's Docker-compatible API.
package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DialFunc opens a raw connection to the engine API.
type DialFunc func(ctx context.Context) (net.Conn, error)

type Client struct {
	dial DialFunc
	http *http.Client
	tr   *http.Transport
}

// CloseIdle drops kept-alive connections, so the next request dials again.
func (c *Client) CloseIdle() {
	if c.tr != nil {
		c.tr.CloseIdleConnections()
	}
}

func New(dial DialFunc) *Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dial(ctx)
		},
		MaxIdleConns:       16,
		IdleConnTimeout:    60 * time.Second,
		DisableCompression: true,
	}
	return &Client{dial: dial, http: &http.Client{Transport: tr}, tr: tr}
}

// APIError is an error answered by the engine.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("engine respondeu %d", e.Status)
	}
	return e.Message
}

func (c *Client) request(ctx context.Context, method, path string, q url.Values, body any) (*http.Response, error) {
	return c.requestH(ctx, method, path, q, body, nil)
}

// requestH is request with extra headers (X-Registry-Auth).
func (c *Client) requestH(ctx context.Context, method, path string, q url.Values, body any, hdr map[string]string) (*http.Response, error) {
	u := "http://docker" + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	ctype := "application/json"
	if r, ok := body.(io.Reader); ok {
		rd, ctype = r, "application/x-tar" // a build context
	} else if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", ctype)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			return nil, ue.Err
		}
		return nil, err
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		var m struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(b, &m)
		if m.Message == "" {
			m.Message = strings.TrimSpace(string(b))
		}
		return nil, &APIError{Status: resp.StatusCode, Message: m.Message}
	}
	return resp, nil
}

func (c *Client) getJSON(ctx context.Context, path string, q url.Values, out any) error {
	resp, err := c.request(ctx, http.MethodGet, path, q, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) Ping(ctx context.Context) error {
	resp, err := c.request(ctx, http.MethodGet, "/_ping", nil, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

type Info struct {
	Name              string
	OperatingSystem   string
	OSType            string
	Architecture      string
	ServerVersion     string
	NCPU              int
	MemTotal          int64
	Containers        int
	ContainersRunning int
	ContainersPaused  int
	ContainersStopped int
	Images            int
}

func (c *Client) Info(ctx context.Context) (Info, error) {
	var i Info
	err := c.getJSON(ctx, "/info", nil, &i)
	return i, err
}

type Version struct {
	Version    string
	APIVersion string `json:"ApiVersion"`
	Os         string
	Arch       string
	Components []struct {
		Name    string
		Version string
	}
}

// Runtime returns "podman" or "docker".
// ComposeHint is the compose command a shimmed runtime asks for
// (nerdctl compose, maybe with sudo), or nil.
func (v Version) ComposeHint() []string {
	for _, c := range v.Components {
		if c.Name == "Gantry compose" {
			return strings.Fields(c.Version)
		}
	}
	return nil
}

func (v Version) Runtime() string {
	for _, c := range v.Components {
		// Runtimes behind Gantry's shim (nerdctl, k3s) say so.
		if rt, ok := strings.CutPrefix(c.Name, "Gantry shim: "); ok {
			return rt
		}
	}
	for _, c := range v.Components {
		if strings.Contains(strings.ToLower(c.Name), "podman") {
			return "podman"
		}
	}
	return "docker"
}

func (c *Client) Version(ctx context.Context) (Version, error) {
	var v Version
	err := c.getJSON(ctx, "/version", nil, &v)
	return v, err
}

type Port struct {
	IP          string
	PrivatePort int
	PublicPort  int
	Type        string
}

type Container struct {
	ID         string `json:"Id"`
	Names      []string
	Image      string
	ImageID    string
	Mounts     []Mount
	State      string
	Status     string
	Created    int64
	Labels     map[string]string
	Ports      []Port
	HostConfig struct {
		NetworkMode string // container:<id> when it uses another one's network
	}
	NetworkSettings struct {
		Networks map[string]Endpoint
	}
}

// Endpoint is a container's place on one network.
type Endpoint struct {
	IPAddress string
	Aliases   []string
}

// Mount is a volume or folder mounted in a container.
type Mount struct {
	Type        string
	Name        string
	Source      string
	Destination string
}

// Name returns the container name without the leading slash.
func (c Container) Name() string {
	if len(c.Names) == 0 {
		if len(c.ID) > 12 {
			return c.ID[:12]
		}
		return c.ID
	}
	return strings.TrimPrefix(c.Names[0], "/")
}

func (c *Client) Containers(ctx context.Context) ([]Container, error) {
	var out []Container
	err := c.getJSON(ctx, "/containers/json", url.Values{"all": {"1"}}, &out)
	sharedNetworks(out)
	return out, err
}

// sharedNetworks clears the ports of the containers that use another one's
// network (network_mode: service:x): the engine lists that one's published
// ports for them too, and the links would open the other container's page.
// Docker says so in HostConfig.NetworkMode; Podman leaves it out, but such a
// container carries the other one's aliases (its short ID among them).
func sharedNetworks(cs []Container) {
	short := map[string]string{}
	for _, c := range cs {
		if len(c.ID) >= 12 {
			short[c.ID[:12]] = c.ID
		}
	}
	for i := range cs {
		c := &cs[i]
		if c.HostConfig.NetworkMode == "" && len(c.ID) >= 12 {
		nets:
			for _, n := range c.NetworkSettings.Networks {
				for _, a := range n.Aliases {
					if owner, ok := short[a]; ok && owner != c.ID {
						c.HostConfig.NetworkMode = "container:" + owner
						break nets
					}
				}
			}
		}
		if strings.HasPrefix(c.HostConfig.NetworkMode, "container:") {
			c.Ports = nil
		}
	}
}

// Action runs start, stop, restart, kill, pause or unpause on a container.
func (c *Client) Action(ctx context.Context, id, action string) error {
	switch action {
	case "start", "stop", "restart", "kill", "pause", "unpause":
	default:
		return fmt.Errorf("ação desconhecida: %s", action)
	}
	resp, err := c.request(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/"+action, nil, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

type Image struct {
	ID       string `json:"Id"`
	RepoTags []string
	Size     int64
	Created  int64
}

func (c *Client) Images(ctx context.Context) ([]Image, error) {
	var out []Image
	err := c.getJSON(ctx, "/images/json", nil, &out)
	return out, err
}

func (c *Client) del(ctx context.Context, path string, q url.Values) error {
	resp, err := c.request(ctx, http.MethodDelete, path, q, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (c *Client) RemoveImage(ctx context.Context, id string, force bool) error {
	q := url.Values{}
	if force {
		q.Set("force", "1")
	}
	return c.del(ctx, "/images/"+url.PathEscape(id), q)
}

type Volume struct {
	Name       string
	Driver     string
	Mountpoint string
	CreatedAt  string
	Labels     map[string]string
}

func (c *Client) Volumes(ctx context.Context) ([]Volume, error) {
	var out struct{ Volumes []Volume }
	err := c.getJSON(ctx, "/volumes", nil, &out)
	return out.Volumes, err
}

func (c *Client) RemoveVolume(ctx context.Context, name string) error {
	return c.del(ctx, "/volumes/"+url.PathEscape(name), nil)
}

type NetworkInfo struct {
	ID     string `json:"Id"`
	Name   string
	Driver string
	Scope  string
	Labels map[string]string
	IPAM   struct {
		Config []struct{ Subnet, Gateway string }
	}
}

func (c *Client) Networks(ctx context.Context) ([]NetworkInfo, error) {
	var out []NetworkInfo
	err := c.getJSON(ctx, "/networks", nil, &out)
	return out, err
}

// Prune removes unused images (all, not only dangling), volumes or networks
// and returns how many bytes were freed when the engine reports it.
func (c *Client) Prune(ctx context.Context, what string) (int64, int, error) {
	q := url.Values{}
	switch what {
	case "images":
		f, _ := json.Marshal(map[string][]string{"dangling": {"false"}})
		q.Set("filters", string(f))
	case "volumes":
		// Docker 23+ only prunes anonymous volumes unless asked for all.
		f, _ := json.Marshal(map[string][]string{"all": {"true"}})
		q.Set("filters", string(f))
	case "networks":
	default:
		return 0, 0, fmt.Errorf("não sei limpar %s", what)
	}
	resp, err := c.request(ctx, http.MethodPost, "/"+what+"/prune", q, nil)
	var ae *APIError
	if what == "volumes" && errors.As(err, &ae) && (ae.Status == http.StatusBadRequest || strings.Contains(strings.ToLower(ae.Message), "filter")) {
		// Older Docker (400) and Podman (500, "all" is an invalid volume
		// filter) reject the filter, but already prune named volumes.
		resp, err = c.request(ctx, http.MethodPost, "/"+what+"/prune", nil, nil)
	}
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	var out struct {
		ImagesDeleted   []json.RawMessage
		VolumesDeleted  []string
		NetworksDeleted []string
		SpaceReclaimed  int64
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	n := len(out.ImagesDeleted) + len(out.VolumesDeleted) + len(out.NetworksDeleted)
	return out.SpaceReclaimed, n, nil
}

// Remove deletes a container; force also stops it first.
func (c *Client) Remove(ctx context.Context, id string, force bool) error {
	q := url.Values{}
	if force {
		q.Set("force", "1")
	}
	resp, err := c.request(ctx, http.MethodDelete, "/containers/"+url.PathEscape(id), q, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

type Network struct {
	ID   string `json:"Id"`
	Name string
}

// NetworksByLabel lists networks carrying label=value.
func (c *Client) NetworksByLabel(ctx context.Context, label, value string) ([]Network, error) {
	f, _ := json.Marshal(map[string][]string{"label": {label + "=" + value}})
	var out []Network
	err := c.getJSON(ctx, "/networks", url.Values{"filters": {string(f)}}, &out)
	return out, err
}

func (c *Client) RemoveNetwork(ctx context.Context, id string) error {
	resp, err := c.request(ctx, http.MethodDelete, "/networks/"+url.PathEscape(id), nil, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

type Inspect struct {
	ID     string `json:"Id"`
	Name   string
	Config struct {
		Tty   bool
		Env   []string
		Cmd   []string
		Image string
	}
	State struct {
		Running bool
		Status  string
	}
}

// EnvValue returns a variable of the container's environment.
func (i Inspect) EnvValue(k string) string {
	for _, e := range i.Config.Env {
		if v, ok := strings.CutPrefix(e, k+"="); ok {
			return v
		}
	}
	return ""
}

func (c *Client) Inspect(ctx context.Context, id string) (Inspect, error) {
	var i Inspect
	err := c.getJSON(ctx, "/containers/"+url.PathEscape(id)+"/json", nil, &i)
	return i, err
}

type cpuStats struct {
	CPUUsage struct {
		TotalUsage  uint64   `json:"total_usage"`
		PercpuUsage []uint64 `json:"percpu_usage"`
	} `json:"cpu_usage"`
	SystemUsage uint64 `json:"system_cpu_usage"`
	OnlineCPUs  uint32 `json:"online_cpus"`
}

type rawStats struct {
	CPUStats    cpuStats `json:"cpu_stats"`
	PreCPUStats cpuStats `json:"precpu_stats"`
	MemoryStats struct {
		Usage uint64            `json:"usage"`
		Limit uint64            `json:"limit"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
	Networks map[string]struct {
		RxBytes uint64 `json:"rx_bytes"`
		TxBytes uint64 `json:"tx_bytes"`
	} `json:"networks"`
}

type Stats struct {
	CPU      float64 `json:"cpu"`
	Mem      uint64  `json:"mem"`
	MemLimit uint64  `json:"memLimit"`
	NetRx    uint64  `json:"netRx"`
	NetTx    uint64  `json:"netTx"`
	Disk     uint64  `json:"disk,omitempty"` // VMs: space the disk takes
}

// Stats takes one sample. The engine waits for a second sample, so it takes about a second.
func (c *Client) Stats(ctx context.Context, id string) (Stats, error) {
	var r rawStats
	if err := c.getJSON(ctx, "/containers/"+url.PathEscape(id)+"/stats", url.Values{"stream": {"false"}}, &r); err != nil {
		return Stats{}, err
	}
	var s Stats
	cpuDelta := float64(r.CPUStats.CPUUsage.TotalUsage) - float64(r.PreCPUStats.CPUUsage.TotalUsage)
	sysDelta := float64(r.CPUStats.SystemUsage) - float64(r.PreCPUStats.SystemUsage)
	online := float64(r.CPUStats.OnlineCPUs)
	if online == 0 {
		online = float64(len(r.CPUStats.CPUUsage.PercpuUsage))
	}
	if online == 0 {
		online = 1
	}
	if cpuDelta > 0 && sysDelta > 0 {
		s.CPU = cpuDelta / sysDelta * online * 100
	}
	s.Mem = r.MemoryStats.Usage
	if v, ok := r.MemoryStats.Stats["inactive_file"]; ok && v < s.Mem {
		s.Mem -= v
	} else if v, ok := r.MemoryStats.Stats["cache"]; ok && v < s.Mem {
		s.Mem -= v
	}
	s.MemLimit = r.MemoryStats.Limit
	for _, n := range r.Networks {
		s.NetRx += n.RxBytes
		s.NetTx += n.TxBytes
	}
	return s, nil
}

// Logs returns the log stream and whether the container uses a TTY.
// Without a TTY the stream is multiplexed; copy it with Demux.
func (c *Client) Logs(ctx context.Context, id string, tail int, follow bool) (io.ReadCloser, bool, error) {
	return c.LogsWith(ctx, id, LogOptions{Tail: tail, Follow: follow})
}

// LogOptions narrow a log read. Tail < 0 reads it all; Since and Until are
// Unix seconds (0: no limit).
type LogOptions struct {
	Tail         int
	Follow       bool
	Since, Until int64
	Timestamps   bool
}

func (c *Client) LogsWith(ctx context.Context, id string, o LogOptions) (io.ReadCloser, bool, error) {
	ins, err := c.Inspect(ctx, id)
	if err != nil {
		return nil, false, err
	}
	q := url.Values{
		"stdout": {"1"},
		"stderr": {"1"},
		"tail":   {"all"},
	}
	if o.Tail >= 0 {
		q.Set("tail", fmt.Sprint(o.Tail))
	}
	if o.Follow {
		q.Set("follow", "1")
	}
	if o.Since > 0 {
		q.Set("since", fmt.Sprint(o.Since))
	}
	if o.Until > 0 {
		q.Set("until", fmt.Sprint(o.Until))
	}
	if o.Timestamps {
		q.Set("timestamps", "1")
	}
	resp, err := c.request(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/logs", q, nil)
	if err != nil {
		return nil, false, err
	}
	tty := ins.Config.Tty
	// The shim (and Docker itself) say how the stream is framed.
	if strings.Contains(resp.Header.Get("Content-Type"), "raw-stream") {
		tty = true
	}
	return resp.Body, tty, nil
}

// Demux copies a multiplexed stdout/stderr stream into w.
func Demux(w io.Writer, r io.Reader) error {
	hdr := make([]byte, 8)
	for {
		if _, err := io.ReadFull(r, hdr); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return err
		}
		n := binary.BigEndian.Uint32(hdr[4:])
		if _, err := io.CopyN(w, r, int64(n)); err != nil {
			return err
		}
	}
}

type Event struct {
	Type   string
	Action string
	Actor  struct {
		ID         string
		Attributes map[string]string
	}
	Time     int64 `json:"time"`
	TimeNano int64 `json:"timeNano"`
}

// Events streams container events until ctx ends.
func (c *Client) Events(ctx context.Context, fn func(Event)) error {
	f, _ := json.Marshal(map[string][]string{"type": {"container"}})
	resp, err := c.request(ctx, http.MethodGet, "/events", url.Values{"filters": {string(f)}}, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	for {
		var e Event
		if err := dec.Decode(&e); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		fn(e)
	}
}

// Exec is an interactive TTY session inside a container.
type Exec struct {
	ID     string
	client *Client
	conn   net.Conn
	r      *bufio.Reader
}

func (e *Exec) Read(p []byte) (int, error)  { return e.r.Read(p) }
func (e *Exec) Write(p []byte) (int, error) { return e.conn.Write(p) }
func (e *Exec) Close() error                { return e.conn.Close() }

func (e *Exec) Resize(ctx context.Context, cols, rows int) error {
	q := url.Values{"w": {fmt.Sprint(cols)}, "h": {fmt.Sprint(rows)}}
	resp, err := e.client.request(ctx, http.MethodPost, "/exec/"+e.ID+"/resize", q, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// ExecTTY starts cmd with a TTY inside the container and hijacks the connection.
func (c *Client) ExecTTY(ctx context.Context, id string, cmd []string) (*Exec, error) {
	cfg := map[string]any{
		"AttachStdin":  true,
		"AttachStdout": true,
		"AttachStderr": true,
		"Tty":          true,
		"Cmd":          cmd,
		"Env":          []string{"TERM=xterm-256color"},
	}
	resp, err := c.request(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/exec", nil, cfg)
	if err != nil {
		return nil, err
	}
	var created struct {
		ID string `json:"Id"`
	}
	err = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}

	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	body := `{"Detach":false,"Tty":true}`
	req := "POST /exec/" + created.ID + "/start HTTP/1.1\r\n" +
		"Host: docker\r\n" +
		"Content-Type: application/json\r\n" +
		"Connection: Upgrade\r\n" +
		"Upgrade: tcp\r\n" +
		fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body)) + body
	if _, err := io.WriteString(conn, req); err != nil {
		conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	hr, err := http.ReadResponse(br, nil)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if hr.StatusCode != http.StatusSwitchingProtocols && hr.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(hr.Body, 4096))
		conn.Close()
		return nil, &APIError{Status: hr.StatusCode, Message: strings.TrimSpace(string(b))}
	}
	return &Exec{ID: created.ID, client: c, conn: conn, r: br}, nil
}

// CreateVolume creates a named volume.
// VolumeSpec describes a volume to create.
type VolumeSpec struct {
	Name   string            `json:"name"`
	Driver string            `json:"driver,omitempty"`
	Opts   map[string]string `json:"opts,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
}

func (c *Client) CreateVolume(ctx context.Context, v VolumeSpec) error {
	body := map[string]any{"Name": v.Name, "Labels": v.Labels, "DriverOpts": v.Opts}
	if v.Driver != "" {
		body["Driver"] = v.Driver
	}
	resp, err := c.request(ctx, http.MethodPost, "/volumes/create", nil, body)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// NetworkSpec describes a network to create.
type NetworkSpec struct {
	Name       string            `json:"name"`
	Driver     string            `json:"driver,omitempty"`
	Subnet     string            `json:"subnet,omitempty"`
	Gateway    string            `json:"gateway,omitempty"`
	IPRange    string            `json:"ipRange,omitempty"`
	IPv6       bool              `json:"ipv6,omitempty"`
	Subnet6    string            `json:"subnet6,omitempty"`
	Gateway6   string            `json:"gateway6,omitempty"`
	Internal   bool              `json:"internal,omitempty"`
	Attachable bool              `json:"attachable,omitempty"`
	Options    map[string]string `json:"options,omitempty"` // driver options, like parent=eth0 for macvlan
	Labels     map[string]string `json:"labels,omitempty"`
}

func (c *Client) CreateNetwork(ctx context.Context, n NetworkSpec) error {
	body := map[string]any{"Name": n.Name, "Internal": n.Internal, "Labels": n.Labels, "CheckDuplicate": true,
		"Attachable": n.Attachable, "EnableIPv6": n.IPv6 || n.Subnet6 != "", "Options": n.Options}
	if n.Driver != "" {
		body["Driver"] = n.Driver
	}
	var cfgs []map[string]string
	if n.Subnet != "" {
		cfg := map[string]string{"Subnet": n.Subnet}
		if n.Gateway != "" {
			cfg["Gateway"] = n.Gateway
		}
		if n.IPRange != "" {
			cfg["IPRange"] = n.IPRange
		}
		cfgs = append(cfgs, cfg)
	}
	if n.Subnet6 != "" {
		cfg := map[string]string{"Subnet": n.Subnet6}
		if n.Gateway6 != "" {
			cfg["Gateway"] = n.Gateway6
		}
		cfgs = append(cfgs, cfg)
	}
	if len(cfgs) > 0 {
		body["IPAM"] = map[string]any{"Config": cfgs}
	}
	resp, err := c.request(ctx, http.MethodPost, "/networks/create", nil, body)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// BuildOptions are the parameters of an image build.
type BuildOptions struct {
	Tags       []string
	Dockerfile string            // path inside the context
	Remote     string            // git URL used as context instead of a tar
	Args       map[string]string // --build-arg
	Target     string
	NoCache    bool
	Pull       bool
}

// Build builds an image from a tar context (or opts.Remote), calling
// progress with each output line.
func (c *Client) Build(ctx context.Context, tarCtx io.Reader, opts BuildOptions, progress func(string)) error {
	q := url.Values{"rm": {"1"}}
	for _, t := range opts.Tags {
		q.Add("t", t)
	}
	if opts.Dockerfile != "" {
		q.Set("dockerfile", opts.Dockerfile)
	}
	if opts.Remote != "" {
		q.Set("remote", opts.Remote)
	}
	if len(opts.Args) > 0 {
		b, _ := json.Marshal(opts.Args)
		q.Set("buildargs", string(b))
	}
	if opts.Target != "" {
		q.Set("target", opts.Target)
	}
	if opts.NoCache {
		q.Set("nocache", "1")
	}
	if opts.Pull {
		q.Set("pull", "1")
	}
	var body any
	if tarCtx != nil {
		body = tarCtx
	}
	resp, err := c.request(ctx, http.MethodPost, "/build", q, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	var pending string
	for {
		var m struct {
			Stream, Status, Error string
			ErrorDetail           struct{ Message string } `json:"errorDetail"`
		}
		if err := dec.Decode(&m); err != nil {
			if pending != "" && progress != nil {
				progress(pending)
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if m.Error != "" {
			return errors.New(strings.TrimSpace(m.Error))
		}
		text := m.Stream
		if text == "" && m.Status != "" {
			text = m.Status + "\n"
		}
		pending += text
		for {
			i := strings.IndexByte(pending, '\n')
			if i < 0 {
				break
			}
			if progress != nil {
				progress(strings.TrimRight(pending[:i], "\r"))
			}
			pending = pending[i+1:]
		}
	}
}

// Tag gives an image another name.
func (c *Client) Tag(ctx context.Context, id, ref string) error {
	repo, tag := ref, "latest"
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		repo, tag = ref[:i], ref[i+1:]
	}
	resp, err := c.request(ctx, http.MethodPost, "/images/"+url.PathEscape(id)+"/tag", url.Values{"repo": {repo}, "tag": {tag}}, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// Pull downloads an image, calling progress with each status line.
// PullState sums the progress of the layers of an image download.
type PullState struct {
	Ref     string
	Current int64 // bytes downloaded, all layers
	Total   int64 // bytes known so far (layers report their size when they start)
	Layers  int
	Done    int // layers finished (downloaded or already present)
}

func (c *Client) Pull(ctx context.Context, ref string, progress func(string)) error {
	return c.PullWatch(ctx, ref, progress, nil)
}

// PullWatch pulls ref, reporting status lines (when they change) and the
// summed byte progress of its layers.
func (c *Client) PullWatch(ctx context.Context, ref string, progress func(string), state func(PullState)) error {
	image, tag := ref, ""
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		image, tag = ref[:i], ref[i+1:]
	}
	if strings.Contains(image, "@") {
		tag = ""
	}
	q := url.Values{"fromImage": {image}}
	if tag != "" {
		q.Set("tag", tag)
	}
	resp, err := c.requestH(ctx, http.MethodPost, "/images/create", q, nil, authHeader(image))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	type layer struct {
		cur, total int64
		done       bool
	}
	layers := map[string]*layer{}
	var order []string
	report := func() {
		if state == nil {
			return
		}
		st := PullState{Ref: ref, Layers: len(order)}
		for _, id := range order {
			l := layers[id]
			st.Current += l.cur
			st.Total += l.total
			if l.done {
				st.Done++
			}
		}
		state(st)
	}
	last := ""
	for {
		var m struct {
			Status, Progress, ID, Error string
			ErrorDetail                 struct{ Message string }       `json:"errorDetail"`
			ProgressDetail              struct{ Current, Total int64 } `json:"progressDetail"`
		}
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if m.Error != "" {
			return errors.New(m.Error)
		}
		if m.ID != "" && m.ID != tag && len(m.ID) >= 8 {
			l := layers[m.ID]
			if l == nil {
				l = &layer{}
				layers[m.ID] = l
				order = append(order, m.ID)
			}
			switch st := strings.ToLower(m.Status); {
			case strings.HasPrefix(st, "downloading") || strings.HasPrefix(st, "copying"):
				if m.ProgressDetail.Total > 0 {
					l.total = m.ProgressDetail.Total
				}
				if m.ProgressDetail.Current > 0 {
					l.cur = m.ProgressDetail.Current
				}
			case strings.Contains(st, "download complete") || strings.Contains(st, "pull complete") ||
				strings.Contains(st, "already exists") || strings.Contains(st, "skipped"):
				if l.total > 0 {
					l.cur = l.total
				}
				l.done = true
			}
			report()
		}
		if progress == nil {
			continue
		}
		line := strings.TrimSpace(strings.Join([]string{m.ID, m.Status}, " "))
		// Progress bars repeat a lot; report when the status changes.
		key := m.ID + m.Status
		if line != "" && key != last && m.Progress == "" {
			progress(line)
		}
		last = key
	}
}

// Exec runs a command in a container without a terminal and returns its
// output (stdout and stderr together) and exit code. Values that must not
// show in the process list go in env.
func (c *Client) Exec(ctx context.Context, id string, cmd []string, env []string) (string, int, error) {
	cfg := map[string]any{"AttachStdout": true, "AttachStderr": true, "Tty": false, "Cmd": cmd, "Env": env}
	resp, err := c.request(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/exec", nil, cfg)
	if err != nil {
		return "", -1, err
	}
	var created struct {
		ID string `json:"Id"`
	}
	err = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if err != nil {
		return "", -1, err
	}
	resp, err = c.request(ctx, http.MethodPost, "/exec/"+created.ID+"/start", nil, map[string]any{"Detach": false, "Tty": false})
	if err != nil {
		return "", -1, err
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	resp.Body.Close()
	if err != nil {
		return "", -1, err
	}
	out := demux(raw)
	var st struct {
		ExitCode int
		Running  bool
	}
	for i := 0; i < 20; i++ {
		if err := c.getJSON(ctx, "/exec/"+created.ID+"/json", nil, &st); err != nil {
			return out, -1, err
		}
		if !st.Running {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	return out, st.ExitCode, nil
}

// demux joins the frames of a non-terminal exec stream (8-byte headers:
// stream, 3 zero bytes, size). Output that is not framed is kept as is.
func demux(b []byte) string {
	var out []byte
	for len(b) >= 8 {
		if (b[0] != 1 && b[0] != 2 && b[0] != 0) || b[1] != 0 || b[2] != 0 || b[3] != 0 {
			return string(append(out, b...))
		}
		n := int(b[4])<<24 | int(b[5])<<16 | int(b[6])<<8 | int(b[7])
		b = b[8:]
		if n > len(b) {
			n = len(b)
		}
		out = append(out, b[:n]...)
		b = b[n:]
	}
	return string(append(out, b...))
}

// ArchiveGet streams a path of a container (running or not) as a tar.
func (c *Client) ArchiveGet(ctx context.Context, id, p string) (io.ReadCloser, error) {
	resp, err := c.request(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/archive", url.Values{"path": {p}}, nil)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// ArchivePut unpacks a tar into a directory of a container.
func (c *Client) ArchivePut(ctx context.Context, id, dir string, tarball io.Reader) error {
	resp, err := c.request(ctx, http.MethodPut, "/containers/"+url.PathEscape(id)+"/archive", url.Values{"path": {dir}}, tarball)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// ExecTo runs a command in a container like Exec, writing its standard
// output to w as it comes (for dumps, which can be large). Standard error
// is returned for the error message.
func (c *Client) ExecTo(ctx context.Context, id string, cmd []string, env []string, w io.Writer) (string, int, error) {
	cfg := map[string]any{"AttachStdout": true, "AttachStderr": true, "Tty": false, "Cmd": cmd, "Env": env}
	resp, err := c.request(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/exec", nil, cfg)
	if err != nil {
		return "", -1, err
	}
	var created struct {
		ID string `json:"Id"`
	}
	err = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if err != nil {
		return "", -1, err
	}
	resp, err = c.request(ctx, http.MethodPost, "/exec/"+created.ID+"/start", nil, map[string]any{"Detach": false, "Tty": false})
	if err != nil {
		return "", -1, err
	}
	var stderr bytes.Buffer
	br := bufio.NewReaderSize(resp.Body, 64<<10)
	hdr := make([]byte, 8)
	for {
		if _, err = io.ReadFull(br, hdr); err != nil {
			break
		}
		n := int64(hdr[4])<<24 | int64(hdr[5])<<16 | int64(hdr[6])<<8 | int64(hdr[7])
		dst := w
		if hdr[0] == 2 {
			dst = &limited{w: &stderr, n: 64 << 10}
		}
		if _, err = io.CopyN(dst, br, n); err != nil {
			break
		}
	}
	resp.Body.Close()
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return stderr.String(), -1, err
	}
	var st struct {
		ExitCode int
		Running  bool
	}
	for i := 0; i < 50; i++ {
		if err := c.getJSON(ctx, "/exec/"+created.ID+"/json", nil, &st); err != nil {
			return stderr.String(), -1, err
		}
		if !st.Running {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	return stderr.String(), st.ExitCode, nil
}

// limited keeps the first n bytes and drops the rest.
type limited struct {
	w io.Writer
	n int
}

func (l *limited) Write(p []byte) (int, error) {
	if l.n > 0 {
		k := len(p)
		if k > l.n {
			k = l.n
		}
		_, _ = l.w.Write(p[:k])
		l.n -= k
	}
	return len(p), nil
}
