// Package labs groups containers into labs using the Compose labels,
// and remembers labs whose containers are gone.
package labs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gantry/internal/engine"
)

const (
	LabelProject    = "com.docker.compose.project"
	LabelWorkingDir = "com.docker.compose.project.working_dir"
	LabelFiles      = "com.docker.compose.project.config_files"
	LabelService    = "com.docker.compose.service"
	LabelHash       = "com.docker.compose.config-hash"
	PodmanProject   = "io.podman.compose.project"

	// Loose is the id of the group for containers outside any compose project.
	Loose = "_avulsos"
)

type Container struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Service  string            `json:"service,omitempty"`
	Image    string            `json:"image"`
	State    string            `json:"state"`
	Status   string            `json:"status"`
	Ports    []engine.Port     `json:"ports"`
	IPs      []string          `json:"ips"`
	Networks []string          `json:"networks"`
	NetIPs   map[string]string `json:"netIps,omitempty"`  // network -> IP
	Runtime  string            `json:"runtime,omitempty"` // engine it runs in
	// SharedNet: it uses another container's network (network_mode: service:x).
	SharedNet bool   `json:"sharedNet,omitempty"`
	Hash      string `json:"-"`
}

type Lab struct {
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	WorkingDir string      `json:"workingDir,omitempty"`
	Files      []string    `json:"files,omitempty"`
	Loose      bool        `json:"loose,omitempty"`
	Running    int         `json:"running"`
	Total      int         `json:"total"`
	Runtimes   []string    `json:"runtimes,omitempty"` // engines of a mixed lab (x-gantry)
	Containers []Container `json:"containers"`
	VMs        []VM        `json:"vms,omitempty"` // QEMU VMs of the lab (x-vms)
	// Used is when the project was last created, deployed or changed
	// through the app, or else when its newest container was created (unix s).
	Used int64 `json:"used,omitempty"`
}

// VM is a lab virtual machine as the host reports it.
type VM struct {
	Name    string   `json:"name"`
	State   string   `json:"state"` // running, stopped, missing (not created)
	SSHPort int      `json:"sshPort,omitempty"`
	Accel   string   `json:"accel,omitempty"`
	Ports   []string `json:"ports"`
	VNC     int      `json:"vnc,omitempty"` // screen of a VM installed from an ISO
	LAN     string   `json:"lan,omitempty"` // address on the project network
	// Autostart: starts with the machine.
	Autostart bool `json:"autostart,omitempty"`
	// Spec is the hash of the definition it runs with (see vms.Env).
	Spec string `json:"-"`
	// Serial is the port of the interactive serial console.
	Serial int `json:"serial,omitempty"`
	// NoCloudInit: no gantry user inside (network appliances): no Shell.
	NoCloudInit bool `json:"noCloudInit,omitempty"`
	// Rejected: startup configuration lines the device's IOS refused.
	Rejected int `json:"rejected,omitempty"`
	// NoIdlePC: a Dynamips router whose image has no idle-PC yet.
	NoIdlePC bool `json:"noIdlePC,omitempty"`
}

func project(labels map[string]string) string {
	if p := labels[LabelProject]; p != "" {
		return p
	}
	return labels[PodmanProject]
}

func splitFiles(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// Group builds the labs from the engine's containers plus the remembered ones.
func Group(cs []engine.Container, known map[string]Known) []Lab {
	byID := map[string]*Lab{}
	for _, c := range cs {
		p := project(c.Labels)
		id := p
		if id == "" {
			id = Loose
		}
		l := byID[id]
		if l == nil {
			l = &Lab{ID: id, Name: p}
			if id == Loose {
				l.Name, l.Loose = "Avulsos", true
			}
			byID[id] = l
		}
		if l.WorkingDir == "" {
			l.WorkingDir = c.Labels[LabelWorkingDir]
		}
		if len(l.Files) == 0 {
			l.Files = splitFiles(c.Labels[LabelFiles])
		}
		ct := Container{
			ID:      c.ID,
			Name:    c.Name(),
			Service: c.Labels[LabelService],
			Image:   c.Image,
			State:   c.State,
			Status:  c.Status,
			Ports:   c.Ports,
			Hash:    c.Labels[LabelHash],

			SharedNet: strings.HasPrefix(c.HostConfig.NetworkMode, "container:"),
		}
		if ct.Ports == nil {
			ct.Ports = []engine.Port{}
		}
		nets := make([]string, 0, len(c.NetworkSettings.Networks))
		for n := range c.NetworkSettings.Networks {
			nets = append(nets, n)
		}
		sort.Strings(nets)
		ct.Networks = nets
		ct.IPs = []string{}
		ct.NetIPs = map[string]string{}
		for _, n := range nets {
			if ip := c.NetworkSettings.Networks[n].IPAddress; ip != "" {
				ct.IPs = append(ct.IPs, ip)
				ct.NetIPs[n] = ip
			}
		}
		l.Containers = append(l.Containers, ct)
		if c.Created > l.Used {
			l.Used = c.Created
		}
		l.Total++
		if c.State == "running" {
			l.Running++
		}
	}
	for id, k := range known {
		if l, ok := byID[id]; ok {
			// Some compose tools (nerdctl compose) don't label the folder;
			// use the one remembered when the lab was opened.
			if l.WorkingDir == "" {
				l.WorkingDir = k.Dir
			}
			if len(l.Files) == 0 {
				l.Files = k.Files
			}
			if k.Used > l.Used {
				l.Used = k.Used
			}
			continue
		}
		byID[id] = &Lab{ID: id, Name: id, WorkingDir: k.Dir, Files: k.Files, Containers: []Container{}, Used: k.Used}
	}
	out := make([]Lab, 0, len(byID))
	for _, l := range byID {
		sort.Slice(l.Containers, func(i, j int) bool {
			a, b := l.Containers[i], l.Containers[j]
			if a.Service != b.Service {
				return a.Service < b.Service
			}
			return a.Name < b.Name
		})
		out = append(out, *l)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Loose != out[j].Loose {
			return !out[i].Loose
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Known is a lab remembered from an earlier run.
type Known struct {
	Dir   string   `json:"dir"`
	Files []string `json:"files,omitempty"`
	// Runtimes is set for labs whose x-gantry spreads them over more than
	// one engine, or puts them in an engine other than the host's.
	Runtimes []string `json:"runtimes,omitempty"`
	// VMs names the lab's x-vms.
	VMs []string `json:"vms,omitempty"`
	// Used is the last time the app created, deployed or changed it (unix s).
	Used int64 `json:"used,omitempty"`
}

// Store keeps the remembered labs per host in a JSON file.
type Store struct {
	mu   sync.Mutex
	path string
	data map[string]map[string]Known
}

func OpenStore(path string) *Store {
	s := &Store{path: path, data: map[string]map[string]Known{}}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &s.data)
	}
	return s
}

func (s *Store) Known(host string) map[string]Known {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]Known{}
	for k, v := range s.data[host] {
		out[k] = v
	}
	return out
}

// Remember records compose labs that have a working dir. It saves only on change.
func (s *Store) Remember(host string, ls []Lab) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.data[host]
	if m == nil {
		m = map[string]Known{}
		s.data[host] = m
	}
	changed := false
	for _, l := range ls {
		if l.Loose || l.WorkingDir == "" || l.Total == 0 {
			continue
		}
		old, ok := m[l.ID]
		k := Known{Dir: l.WorkingDir, Files: l.Files, Runtimes: old.Runtimes, VMs: old.VMs, Used: old.Used}
		if !ok || old.Dir != k.Dir || strings.Join(old.Files, ",") != strings.Join(k.Files, ",") {
			m[l.ID] = k
			changed = true
		}
	}
	if changed {
		s.save()
	}
}

// SetLab records the engines (none for a plain lab) and VMs a lab uses. It
// also remembers the lab, so it shows up before its first container.
func (s *Store) SetLab(host, id, dir string, files, runtimes, vms []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.data[host]
	if m == nil {
		m = map[string]Known{}
		s.data[host] = m
	}
	old, ok := m[id]
	k := Known{Dir: dir, Files: files, Runtimes: runtimes, VMs: vms, Used: time.Now().Unix()}
	if len(k.Files) == 0 {
		k.Files = old.Files
	}
	if ok && old.Dir == k.Dir && strings.Join(old.Files, ",") == strings.Join(k.Files, ",") && strings.Join(old.Runtimes, ",") == strings.Join(k.Runtimes, ",") && strings.Join(old.VMs, ",") == strings.Join(k.VMs, ",") && k.Used-old.Used < 60 {
		return
	}
	m[id] = k
	s.save()
}

// Touch marks a project as just used (an operation through the app).
func (s *Store) Touch(host, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if k, ok := s.data[host][id]; ok {
		k.Used = time.Now().Unix()
		s.data[host][id] = k
		s.save()
	}
}

// MixedRuntimes returns the engines of a mixed lab, known under any host.
func (s *Store) MixedRuntimes(id, dir string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.data {
		if k, ok := m[id]; ok && len(k.Runtimes) > 0 && (dir == "" || k.Dir == dir) {
			return append([]string(nil), k.Runtimes...)
		}
	}
	return nil
}

// AllMixed lists the engines used by any mixed lab.
func (s *Store) AllMixed() map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]bool{}
	for _, m := range s.data {
		for _, k := range m {
			for _, r := range k.Runtimes {
				out[r] = true
			}
		}
	}
	return out
}

// Rename moves the remembered labs of a host to its new name.
func (s *Store) Rename(from, to string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m, ok := s.data[from]; ok {
		s.data[to] = m
		delete(s.data, from)
		s.save()
	}
}

func (s *Store) Forget(host, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := s.data[host]; m != nil {
		delete(m, id)
		s.save()
	}
}

func (s *Store) save() {
	_ = os.MkdirAll(filepath.Dir(s.path), 0o700)
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return
	}
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, s.path)
	}
}
