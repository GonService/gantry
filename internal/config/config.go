// Package config loads the app configuration and detects local engines.
package config

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"gantry/internal/local"
	"gantry/internal/shim"
)

// Host is one engine endpoint the app manages.
type Host struct {
	Name string `json:"name"`
	// URL is unix:///path, tcp://host:port or ssh://[user@]host[:port].
	// For SSH any alias from ~/.ssh/config works: ssh://homelab.
	URL string `json:"url"`
	// Socket is the engine socket on the remote side (SSH only).
	Socket string `json:"socket,omitempty"`
	// Compose is the compose command, e.g. ["docker","compose"] or ["podman","compose"].
	Compose []string `json:"compose,omitempty"`
	// Source tells where the host came from: config, context or detected.
	Source string `json:"-"`
}

type Config struct {
	Listen     string `json:"listen,omitempty"`
	Autodetect *bool  `json:"autodetect,omitempty"`
	Hosts      []Host `json:"hosts"`
	// Hidden lists detected hosts (by URL) the user chose not to see.
	Hidden []string `json:"hidden,omitempty"`
	// Language of the app: "pt", "en", or "" to follow the system.
	Language string `json:"language,omitempty"`
	// Observability holds Prometheus, Loki and Grafana addresses per host
	// name, when they are not found on their own.
	Observability map[string]Obs `json:"observability,omitempty"`
	// VMImages says, per machine (hosts.Host.Machine: "local",
	// "ssh:user@server:22"…), where the images Gantry does not download are
	// on it (nixos-lab-26.05 -> /caminho/nixos-lab-base.qcow2).
	VMImages map[string]map[string]string `json:"vmImages,omitempty"`
	// ImageDirs are, per machine, the folders where Gantry finds the images
	// on its own (qcow2, ISO, the .bin of IOL and Dynamips, the iourc); the
	// defaults when a machine has none.
	ImageDirs map[string][]string `json:"imageDirs,omitempty"`
	// MCP lets an assistant (an MCP client running gantry mcp) use this
	// Gantry: read always, change only with Write.
	MCP *MCP `json:"mcp,omitempty"`

	mu   sync.Mutex
	path string
}

func DefaultDir() string {
	d, err := os.UserConfigDir()
	if err != nil {
		d = filepath.Join(os.Getenv("HOME"), ".config")
	}
	dir := filepath.Join(d, "gantry")
	// The app was called Hangar before; keep the hosts and labs saved there.
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		if _, err := os.Stat(filepath.Join(d, "hangar")); err == nil {
			_ = os.Rename(filepath.Join(d, "hangar"), dir)
		}
	}
	return dir
}

func Load(path string) (*Config, error) {
	c := &Config{path: path}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, err
	}
	for i := range c.Hosts {
		c.Hosts[i].Source = "config"
	}
	return c, nil
}

func (c *Config) Path() string { return c.path }

// Obs are the observability endpoints of a host. The URLs are as seen from
// the host's machine (http://127.0.0.1:9090 on a server is that server).
type Obs struct {
	Prometheus   string `json:"prometheus,omitempty"`
	Loki         string `json:"loki,omitempty"`
	Grafana      string `json:"grafana,omitempty"`
	GrafanaToken string `json:"grafanaToken,omitempty"`
}

// ObsFor returns the configured endpoints of a host.
func (c *Config) ObsFor(host string) Obs {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Observability[host]
}

// SetObs stores (or, when empty, clears) the endpoints of a host.
func (c *Config) SetObs(host string, o Obs) error {
	c.mu.Lock()
	if c.Observability == nil {
		c.Observability = map[string]Obs{}
	}
	if o == (Obs{}) {
		delete(c.Observability, host)
	} else {
		c.Observability[host] = o
	}
	c.mu.Unlock()
	return c.Save()
}

// RenameObs follows a host rename.
func (c *Config) RenameObs(from, to string) {
	c.mu.Lock()
	o, ok := c.Observability[from]
	if ok {
		delete(c.Observability, from)
		c.Observability[to] = o
	}
	c.mu.Unlock()
	if ok {
		_ = c.Save()
	}
}

// VMImage is where an image is on a machine ("" when not set).
func (c *Config) VMImage(machine, name string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.VMImages[machine][name]
}

// VMImagesOf lists the images set for a machine.
func (c *Config) VMImagesOf(machine string) map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]string{}
	for k, v := range c.VMImages[machine] {
		out[k] = v
	}
	return out
}

// SetVMImage stores (or, when empty, clears) where an image is on a machine.
func (c *Config) SetVMImage(machine, name, path string) error {
	c.mu.Lock()
	if c.VMImages == nil {
		c.VMImages = map[string]map[string]string{}
	}
	if path == "" {
		delete(c.VMImages[machine], name)
		if len(c.VMImages[machine]) == 0 {
			delete(c.VMImages, machine)
		}
	} else {
		if c.VMImages[machine] == nil {
			c.VMImages[machine] = map[string]string{}
		}
		c.VMImages[machine][name] = path
	}
	c.mu.Unlock()
	return c.Save()
}

// DefaultImageDirs are where Gantry looks for images on a machine whose
// folders were never set: its own folder and the layout of EVE-NG/PNetLab.
var DefaultImageDirs = []string{"~/gantry/imagens", "/opt/unetlab/addons"}

// ImageDirsOf are the image folders of a machine (the defaults when unset).
func (c *Config) ImageDirsOf(machine string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if d, ok := c.ImageDirs[machine]; ok {
		return append([]string{}, d...)
	}
	return append([]string{}, DefaultImageDirs...)
}

// SetImageDirs stores the image folders of a machine (nil: the defaults).
func (c *Config) SetImageDirs(machine string, dirs []string) error {
	c.mu.Lock()
	if c.ImageDirs == nil {
		c.ImageDirs = map[string][]string{}
	}
	if dirs == nil {
		delete(c.ImageDirs, machine)
	} else {
		c.ImageDirs[machine] = dirs
	}
	c.mu.Unlock()
	return c.Save()
}

// Lang is the language chosen in Settings ("" follows the system).
func (c *Config) Lang() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Language
}

// SetLang stores the language chosen in Settings.
func (c *Config) SetLang(l string) error {
	c.mu.Lock()
	c.Language = l
	c.mu.Unlock()
	return c.Save()
}

// MCP is the access of assistants (gantry mcp).
type MCP struct {
	On    bool `json:"on"`
	Write bool `json:"write,omitempty"`
}

// MCPSettings is the MCP access as set (off when never set).
func (c *Config) MCPSettings() MCP {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.MCP == nil {
		return MCP{}
	}
	return *c.MCP
}

// SetMCP stores the MCP access.
func (c *Config) SetMCP(m MCP) error {
	c.mu.Lock()
	c.MCP = &m
	c.mu.Unlock()
	return c.Save()
}

func (c *Config) AutodetectEnabled() bool { return c.Autodetect == nil || *c.Autodetect }

func (c *Config) Save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

func (c *Config) AddHost(h Host) error {
	c.mu.Lock()
	for _, x := range c.Hosts {
		if x.Name == h.Name {
			c.mu.Unlock()
			return errors.New("já existe um host com esse nome")
		}
	}
	h.Source = "config"
	c.Hosts = append(c.Hosts, h)
	c.mu.Unlock()
	return c.Save()
}

func (c *Config) RemoveHost(name string) error {
	c.mu.Lock()
	out := c.Hosts[:0]
	found := false
	for _, x := range c.Hosts {
		if x.Name == name {
			found = true
			continue
		}
		out = append(out, x)
	}
	c.Hosts = out
	c.mu.Unlock()
	if !found {
		return errors.New("host não está no arquivo de configuração")
	}
	return c.Save()
}

// UpdateHost replaces the host called name (it may be renamed).
func (c *Config) UpdateHost(name string, h Host) error {
	c.mu.Lock()
	idx := -1
	for i, x := range c.Hosts {
		if x.Name == name {
			idx = i
		} else if x.Name == h.Name {
			c.mu.Unlock()
			return errors.New("já existe um host com esse nome")
		}
	}
	if idx < 0 {
		c.mu.Unlock()
		return errors.New("host não está no arquivo de configuração")
	}
	h.Source = "config"
	c.Hosts[idx] = h
	c.mu.Unlock()
	return c.Save()
}

// Find returns the configured host called name.
func (c *Config) Find(name string) (Host, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, x := range c.Hosts {
		if x.Name == name {
			return x, true
		}
	}
	return Host{}, false
}

func (c *Config) IsHidden(url string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, u := range c.Hidden {
		if u == url {
			return true
		}
	}
	return false
}

func (c *Config) Hide(url string) error {
	if c.IsHidden(url) {
		return nil
	}
	c.mu.Lock()
	c.Hidden = append(c.Hidden, url)
	c.mu.Unlock()
	return c.Save()
}

func (c *Config) Unhide(url string) error {
	c.mu.Lock()
	out := c.Hidden[:0]
	for _, u := range c.Hidden {
		if u != url {
			out = append(out, u)
		}
	}
	c.Hidden = out
	c.mu.Unlock()
	return c.Save()
}

func (c *Config) HiddenList() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string{}, c.Hidden...)
}

// Detect finds local engine sockets and docker contexts.
func Detect() []Host {
	var hosts []Host
	seen := map[string]bool{}
	add := func(h Host) {
		key := normalize(h.URL)
		if key == "" || seen[key] {
			return
		}
		if strings.HasPrefix(h.URL, "unix://") {
			if _, err := os.Stat(strings.TrimPrefix(h.URL, "unix://")); err != nil {
				return
			}
		}
		seen[key] = true
		hosts = append(hosts, h)
	}

	if dh := os.Getenv("DOCKER_HOST"); dh != "" {
		add(Host{Name: "DOCKER_HOST", URL: dh, Source: "detected"})
	}

	home, _ := os.UserHomeDir()
	type cand struct{ name, path string }
	cands := []cand{
		{"orbstack", filepath.Join(home, ".orbstack/run/docker.sock")},
		{"docker desktop", filepath.Join(home, ".docker/run/docker.sock")},
		{"colima", filepath.Join(home, ".colima/default/docker.sock")},
		{"rancher desktop", filepath.Join(home, ".rd/docker.sock")},
		{"docker", "/var/run/docker.sock"},
	}
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		cands = append(cands, cand{"podman", filepath.Join(xdg, "podman/podman.sock")})
	} else if runtime.GOOS == "linux" {
		cands = append(cands, cand{"podman", filepath.Join("/run/user", strconv.Itoa(os.Getuid()), "podman/podman.sock")})
	}
	cands = append(cands, cand{"podman (root)", "/run/podman/podman.sock"})
	if runtime.GOOS == "darwin" {
		matches, _ := filepath.Glob(filepath.Join(home, ".local/share/containers/podman/machine/*/podman.sock"))
		matches2, _ := filepath.Glob(filepath.Join(os.TempDir(), "podman/*-api.sock"))
		for _, m := range append(matches, matches2...) {
			cands = append(cands, cand{"podman machine", m})
		}
	}
	for _, sock := range local.PodmanMachineSockets(context.Background()) {
		cands = append([]cand{{"podman", sock}}, cands...)
	}
	for _, c := range cands {
		h := Host{Name: c.name, URL: "unix://" + c.path, Source: "detected"}
		if strings.HasPrefix(c.name, "podman") {
			h.Compose = []string{"podman", "compose"}
		}
		add(h)
	}

	for _, h := range cliRuntimes() {
		add(h)
	}
	for _, d := range wslDistros() {
		add(Host{Name: "WSL " + d, URL: "wsl://" + d, Source: "detected"})
	}
	for _, ctx := range dockerContexts(home) {
		add(ctx)
	}
	return hosts
}

// cliRuntimes finds runtimes that only have a command line (nerdctl, k3s).
// The look-up runs commands, so it is cached.
func cliRuntimes() []Host {
	cliMu.Lock()
	defer cliMu.Unlock()
	if time.Since(cliAt) > time.Minute {
		cliFound = nil
		for _, d := range shim.Detect() {
			cliFound = append(cliFound, Host{Name: d.Name, URL: d.URL, Source: "detected"})
		}
		cliAt = time.Now()
	}
	return cliFound
}

// ForgetDetected makes the next Detect look for CLI runtimes again, after
// one was installed or started.
func ForgetDetected() {
	cliMu.Lock()
	cliAt = time.Time{}
	cliMu.Unlock()
}

var (
	cliMu    sync.Mutex
	cliAt    time.Time
	cliFound []Host
)

func dockerContexts(home string) []Host {
	metas, _ := filepath.Glob(filepath.Join(home, ".docker/contexts/meta/*/meta.json"))
	var out []Host
	for _, m := range metas {
		b, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		var meta struct {
			Name      string
			Endpoints map[string]struct {
				Host string
			}
		}
		if json.Unmarshal(b, &meta) != nil {
			continue
		}
		ep, ok := meta.Endpoints["docker"]
		if !ok || ep.Host == "" {
			continue
		}
		out = append(out, Host{Name: meta.Name, URL: ep.Host, Source: "context"})
	}
	return out
}

func normalize(u string) string {
	if p, ok := strings.CutPrefix(u, "unix://"); ok {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return "unix://" + r
		}
		return u
	}
	return u
}
