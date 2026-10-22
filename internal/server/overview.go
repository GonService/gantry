package server

import (
	"context"
	"net/http"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"gantry/internal/applog"
	"gantry/internal/engine"
	"gantry/internal/hosts"
	"gantry/internal/labs"
	"gantry/internal/sysinfo"
)

var sysCache sync.Map // machine -> sysCached

type sysCached struct {
	at   time.Time
	info sysinfo.Info
	err  string
}

// machineInfo reads the hardware and usage of the host's machine, with a
// short cache so several engines of one server share a reading.
func machineInfo(ctx context.Context, h *hosts.Host) (sysinfo.Info, string) {
	key := h.Machine()
	if c, ok := sysCache.Load(key); ok && time.Since(c.(sysCached).at) < 4*time.Second {
		return c.(sysCached).info, c.(sysCached).err
	}
	var c sysCached
	if h.Kind == hosts.TCP {
		// Only the engine's view: no commands over tcp://.
		if info, err := h.Engine.Info(ctx); err == nil {
			c.info = sysinfo.Info{Hostname: info.Name, OS: info.OperatingSystem, Arch: info.Architecture, CPUs: info.NCPU, MemTotal: info.MemTotal}
		} else {
			c.err = err.Error()
		}
	} else {
		out, err := h.Run(ctx, "", []string{"sh", "-c", sysinfo.Script}, nil)
		if err != nil {
			c.err = err.Error()
		} else {
			c.info = sysinfo.Parse(out)
		}
	}
	c.at = time.Now()
	sysCache.Store(key, c)
	return c.info, c.err
}

func (s *Server) systemInfo(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	info, err := machineInfo(ctx, h)
	writeJSON(w, map[string]any{"info": info, "error": err})
}

type engineView struct {
	Label      string `json:"label"`
	Runtime    string `json:"runtime"`
	Host       string `json:"host"`         // Gantry host to call
	RT         string `json:"rt,omitempty"` // ?rt= for engines that are not hosts of their own
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
	Version    string `json:"version,omitempty"`
	Containers int    `json:"containers"`
	Running    int    `json:"running"`
	Images     int    `json:"images"`
}

type vmView struct {
	labs.VM
	Project string `json:"project"`
	Host    string `json:"host"`
}

type projectView struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Host     string   `json:"host"`
	Dir      string   `json:"dir,omitempty"`
	Running  int      `json:"running"`
	Total    int      `json:"total"`
	VMs      int      `json:"vms"`
	VMsUp    int      `json:"vmsUp"`
	Runtimes []string `json:"runtimes,omitempty"`
	Used     int64    `json:"used,omitempty"`
}

// overview is the host page: the machine's engines with their container
// counts, every VM and the projects, newest first.
func (s *Server) overview(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	group := s.machineHosts(h)
	for _, x := range group {
		x.Want()
	}
	var (
		mu       sync.Mutex
		engines  []engineView
		vms      []vmView
		projects []projectView
		wg       sync.WaitGroup
	)
	sockets := map[string]bool{}
	for _, x := range group {
		sockets[x.Socket()] = true
	}
	countInto := func(e *engineView, c *engine.Client) {
		cs, err := c.Containers(ctx)
		if err != nil {
			e.Error = err.Error()
			return
		}
		e.OK, e.Containers = true, len(cs)
		for _, ct := range cs {
			if ct.State == "running" {
				e.Running++
			}
		}
		if v, err := c.Version(ctx); err == nil {
			e.Version = v.Version
			if e.Runtime == "" {
				e.Runtime = v.Runtime()
			}
		}
	}
	for _, x := range group {
		wg.Add(1)
		go func() {
			defer applog.Recover("server/overview.go:134")
			defer wg.Done()
			e := engineView{Label: x.Name, Runtime: x.Runtime(), Host: x.Name}
			countInto(&e, x.Engine)
			var ls []labs.Lab
			if e.OK {
				x.SetRuntime(e.Runtime)
				ls, _ = s.labsOf(ctx, x)
			}
			mu.Lock()
			defer mu.Unlock()
			engines = append(engines, e)
			for _, l := range ls {
				if l.Loose {
					continue
				}
				// Total and Running of a lab also count its VMs; keep them apart.
				p := projectView{ID: l.ID, Name: l.Name, Host: x.Name, Dir: l.WorkingDir, Total: len(l.Containers), VMs: len(l.VMs), Runtimes: l.Runtimes, Used: l.Used}
				for _, c := range l.Containers {
					if c.State == "running" {
						p.Running++
					}
				}
				for _, v := range l.VMs {
					if v.State == "running" {
						p.VMsUp++
					}
					vms = append(vms, vmView{VM: v, Project: l.ID, Host: x.Name})
				}
				projects = append(projects, p)
			}
		}()
	}
	wg.Wait()
	// Engines found on the server that are not hosts of their own
	// (containerd through the agent, Podman next to Docker…).
	if h.Kind == hosts.SSH {
		for _, ref := range h.Engines(ctx) {
			if sockets[ref.Socket] || ref.Host != nil && ref.Host != h {
				continue
			}
			e := engineView{Label: ref.Runtime, Runtime: ref.Runtime, Host: h.Name, RT: ref.Runtime}
			countInto(&e, ref.Engine)
			engines = append(engines, e)
		}
	}
	// The same project is listed by each engine of a mixed lab: keep one.
	seen := map[string]int{}
	var uniq []projectView
	for _, p := range projects {
		if i, ok := seen[p.ID+"\x00"+p.Dir]; ok {
			if p.Used > uniq[i].Used {
				uniq[i].Used = p.Used
			}
			continue
		}
		seen[p.ID+"\x00"+p.Dir] = len(uniq)
		uniq = append(uniq, p)
	}
	sort.Slice(uniq, func(i, j int) bool {
		if uniq[i].Used != uniq[j].Used {
			return uniq[i].Used > uniq[j].Used
		}
		return uniq[i].Name < uniq[j].Name
	})
	sort.SliceStable(engines, func(i, j int) bool {
		return engines[i].Host == h.Name && engines[i].RT == "" && !(engines[j].Host == h.Name && engines[j].RT == "")
	})
	vseen := map[string]bool{}
	var uv []vmView
	for _, v := range vms {
		k := v.Project + "/" + v.Name
		if !vseen[k] {
			vseen[k] = true
			uv = append(uv, v)
		}
	}
	sort.Slice(uv, func(i, j int) bool { return uv[i].Project+uv[i].Name < uv[j].Project+uv[j].Name })
	if uv == nil {
		uv = []vmView{}
	}
	if uniq == nil {
		uniq = []projectView{}
	}
	writeJSON(w, map[string]any{"machine": h.Machine(), "kind": string(h.Kind), "engines": engines, "vms": uv, "projects": uniq})
}

type ctrView struct {
	ID      string        `json:"id"`
	Name    string        `json:"name"`
	Image   string        `json:"image"`
	State   string        `json:"state"`
	Status  string        `json:"status"`
	Project string        `json:"project,omitempty"`
	Service string        `json:"service,omitempty"`
	Ports   []engine.Port `json:"ports"`
	IPs     []string      `json:"ips"`
	Created int64         `json:"created"`
}

// listContainers lists one engine's containers, for the host page.
func (s *Server) listContainers(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	eng, ok := s.reqEngine(w, r, h)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	cs, err := eng.Containers(ctx)
	if err != nil {
		writeErr(w, engineStatus(err), err)
		return
	}
	out := make([]ctrView, 0, len(cs))
	for _, c := range cs {
		v := ctrView{ID: c.ID, Name: c.Name(), Image: c.Image, State: c.State, Status: c.Status, Ports: c.Ports, Created: c.Created,
			Project: c.Labels[labs.LabelProject], Service: c.Labels[labs.LabelService], IPs: []string{}}
		if v.Project == "" {
			v.Project = c.Labels[labs.PodmanProject]
		}
		if v.Ports == nil {
			v.Ports = []engine.Port{}
		}
		for _, n := range c.NetworkSettings.Networks {
			if n.IPAddress != "" {
				v.IPs = append(v.IPs, n.IPAddress)
			}
		}
		sort.Strings(v.IPs)
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].State == "running") != (out[j].State == "running") {
			return out[i].State == "running"
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	writeJSON(w, out)
}

// localSystem is this computer's hardware, for the page of the local
// machine (it exists even with no engine installed).
func (s *Server) localSystem(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if c, ok := sysCache.Load("local"); ok && time.Since(c.(sysCached).at) < 4*time.Second {
		writeJSON(w, map[string]any{"info": c.(sysCached).info, "error": c.(sysCached).err})
		return
	}
	var c sysCached
	out, err := exec.CommandContext(ctx, "sh", "-c", sysinfo.Script).Output()
	if err != nil {
		c.err = err.Error()
	} else {
		c.info = sysinfo.Parse(string(out))
	}
	c.at = time.Now()
	sysCache.Store("local", c)
	writeJSON(w, map[string]any{"info": c.info, "error": c.err})
}
