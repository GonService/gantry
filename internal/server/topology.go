package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"gantry/internal/composex"
	"gantry/internal/hosts"
	"gantry/internal/vms"
)

type topoContainer struct {
	Name  string            `json:"name"`
	State string            `json:"state"`
	IPs   map[string]string `json:"ips"` // compose network key -> IP
}

type topoService struct {
	*composex.Service
	Runtime    string          `json:"runtime"`
	Containers []topoContainer `json:"containers"`
}

type topoRuntime struct {
	Runtime   string `json:"runtime"`
	Available bool   `json:"available"`
	Socket    string `json:"socket,omitempty"`
	Host      string `json:"host,omitempty"` // Gantry host that owns the engine
}

// topology describes a lab for the diagram: engines, networks, services,
// published ports and the dependencies that cross engines.
func (s *Server) topology(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	if !h.Files() {
		writeErr(w, http.StatusBadRequest, errors.New("este host não dá acesso ao arquivo compose"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	l, err := s.findLab(ctx, h, r.PathValue("lab"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if l.Loose || len(l.Files) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("este projeto não tem arquivo compose conhecido"))
		return
	}
	p, _, err := s.loadProject(ctx, h, l.Files)
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err)
		return
	}
	def := h.Runtime()
	// Keep the store in step with the file, so the lab list merges engines.
	var rts []string
	if p.Gantry && p.Mixed(def) {
		for _, g := range p.Plan(def) {
			rts = append(rts, g.Runtime)
		}
	}
	s.store.SetLab(h.Name, l.ID, l.WorkingDir, l.Files, rts, vmNames(p))

	avail := map[string]hosts.EngineRef{}
	for _, e := range s.machineEngines(ctx, h) {
		avail[e.Runtime] = e
	}
	runtimes := []topoRuntime{}
	seen := map[string]bool{}
	for _, g := range p.Plan(def) {
		e, ok := avail[g.Runtime]
		tr := topoRuntime{Runtime: g.Runtime, Available: ok, Socket: e.Socket}
		if ok && e.Host != nil {
			tr.Host = e.Host.Name
		}
		runtimes = append(runtimes, tr)
		seen[g.Runtime] = true
	}

	project := l.ID
	svcs := []topoService{}
	var warnings []string
	for _, n := range p.Names {
		sv := p.Services[n]
		ts := topoService{Service: sv, Runtime: p.RuntimeOf(n, def), Containers: []topoContainer{}}
		for _, c := range l.Containers {
			if c.Service != n || (c.Runtime != "" && c.Runtime != ts.Runtime) {
				continue
			}
			ips := map[string]string{}
			for net, ip := range c.NetIPs {
				ips[netKey(p, project, net)] = ip
			}
			ts.Containers = append(ts.Containers, topoContainer{Name: c.Name, State: c.State, IPs: ips})
		}
		svcs = append(svcs, ts)
	}
	for _, rt := range runtimes {
		if !rt.Available {
			warnings = append(warnings, fmt.Sprintf("O projeto pede o %s, mas não encontrei esse engine nesta máquina.", rt.Runtime))
		}
	}
	links := p.CrossLinks(def)
	for _, lk := range links {
		if len(lk.Via) == 0 {
			warnings = append(warnings, fmt.Sprintf("%s (%s) depende de %s (%s), mas %s não publica porta no host. Engines diferentes não compartilham redes, então eles não se enxergam.", lk.From, lk.FromRuntime, lk.To, lk.ToRuntime, lk.To))
		}
	}
	nets := []*composex.Network{}
	for _, k := range sortedKeys(p.Networks) {
		nets = append(nets, p.Networks[k])
	}
	if links == nil {
		links = []composex.Link{}
	}
	if warnings == nil {
		warnings = []string{}
	}
	type topoVM struct {
		*composex.VM
		State   string `json:"state"`
		SSHPort int    `json:"sshPort"`
		Accel   string `json:"accel,omitempty"`
	}
	tvms := []topoVM{}
	for _, v := range p.VMs {
		tv := topoVM{VM: v, State: "missing", SSHPort: vms.SSHPort(l.ID, v)}
		for _, x := range l.VMs {
			if x.Name == v.Name {
				tv.State, tv.Accel = x.State, x.Accel
			}
		}
		tvms = append(tvms, tv)
	}
	writeJSON(w, map[string]any{
		"vms":      tvms,
		"project":  project,
		"host":     h.Name,
		"default":  def,
		"gantry":   p.Gantry,
		"runtimes": runtimes,
		"networks": nets,
		"services": svcs,
		"links":    links,
		"warnings": warnings,
		// The drawing of the cables, from the same read of the compose.
		"drawing": map[string]any{"vms": cablesOf(p), "redes": redesOf(p), "notas": notasOf(p)},
	})
}

// netKey maps an engine network name (proj_default) back to its compose key.
func netKey(p *composex.Project, project, engineName string) string {
	for k, n := range p.Networks {
		if n.Name != "" && n.Name == engineName {
			return k
		}
	}
	if k, ok := strings.CutPrefix(engineName, project+"_"); ok {
		return k
	}
	return engineName
}

func sortedKeys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
