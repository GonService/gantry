package server

// Drift: what differs between the project's files and what runs. The app
// shows it on the project page and, when the user turned it on, applies
// the files by itself once they stop changing (off by default: a file
// saved halfway should not take a lab down).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"gantry/internal/composex"
	"gantry/internal/hosts"
	"gantry/internal/labs"
	"gantry/internal/vms"
)

type driftItem struct {
	Name    string `json:"service"`
	Kind    string `json:"kind"`  // service, vm
	State   string `json:"state"` // ok, changed, missing, orphan
	Running bool   `json:"running,omitempty"`
}

type driftReport struct {
	Services []driftItem `json:"services"`
	Error    string      `json:"error,omitempty"`
	// Pending: something differs in a project that has been brought up.
	Pending bool `json:"pending"`
	Auto    bool `json:"auto"`
	// Builds are the services built from a Dockerfile (build:): their code
	// may change without the compose file changing, so the page offers
	// Reconstruir.
	Builds []string `json:"builds,omitempty"`
	// Files fingerprints the project's files (auto-apply waits until it
	// stops changing).
	Files string `json:"-"`
}

func (s *Server) computeDrift(ctx context.Context, h *hosts.Host, l labs.Lab) driftReport {
	rep := driftReport{Services: []driftItem{}, Auto: s.autoApply()[scheduleKey(h, l.ID)].On}
	rep.Files = s.filesPrint(ctx, h, l)
	argv := s.composeBase(h, l)
	// The project went up with the network names too: compare with them.
	// (Once: the engine may already list them among the project's files.)
	if net := path.Join(l.WorkingDir, ".gantry", "rede.yaml"); l.WorkingDir != "" && !slices.Contains(argv, net) && h.Exists(ctx, net) {
		argv = append(argv, "-f", net)
	}
	if t := tapsOverride(l.WorkingDir); l.WorkingDir != "" && !slices.Contains(argv, t) && h.Exists(ctx, t) {
		argv = append(argv, "-f", t)
	}
	p, _, perr := s.loadProject(ctx, h, l.Files)
	if perr != nil {
		rep.Error = perr.Error()
		return rep
	}
	for _, name := range p.Names {
		if sv := p.Services[name]; sv != nil && sv.Build {
			rep.Builds = append(rep.Builds, name)
		}
	}
	created := false
	if len(p.Names) > 0 {
		argv = append(argv, "config", "--hash", "*")
		out, err := h.Run(s.withSecrets(ctx, h, l.ID), l.WorkingDir, argv, nil)
		if err != nil {
			rep.Error = err.Error()
			return rep
		}
		want := parseConfigHashes(out)
		have := map[string]string{}
		running := map[string]bool{}
		for _, c := range l.Containers {
			if c.Service != "" {
				have[c.Service] = c.Hash
				running[c.Service] = running[c.Service] || c.State == "running"
				created = true
			}
		}
		// Services in the network of another one (network_mode: service:x,
		// the cables' sidecars too): compose hashes them with the other's
		// container in place of the name, so "config --hash" never matches;
		// compose itself does not recreate them for that.
		shared := map[string]bool{}
		for n, sv := range p.Services {
			if strings.HasPrefix(sv.NetworkMode, "service:") {
				shared[n] = true
			}
		}
		for _, t := range p.Taps {
			shared[tapSidecar(t.Name)] = true
		}
		for name, hsh := range want {
			got, ok := have[name]
			st := "ok"
			switch {
			case !ok:
				st = "missing"
			case got != "" && got != hsh && !shared[name]:
				st = "changed"
			}
			rep.Services = append(rep.Services, driftItem{name, "service", st, running[name]})
		}
		for name := range have {
			if _, ok := want[name]; !ok {
				rep.Services = append(rep.Services, driftItem{name, "service", "orphan", running[name]})
			}
		}
	}
	// VMs: the definition they were started with against the file's.
	if h.Kind != hosts.TCP {
		sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		st, serr := vms.Status(sctx, h, l.ID)
		cancel()
		if serr == nil {
			by := map[string]vms.State{}
			for _, x := range st {
				by[x.Name] = x
				created = true
			}
			var net *vms.Net
			if len(p.VMs) > 0 {
				net, _ = s.projectNet(ctx, h, l.ID, p)
			}
			for _, v := range p.VMs {
				x, ok := by[v.Name]
				item := driftItem{Name: v.Name, Kind: "vm", State: "ok", Running: ok && x.State == "running"}
				if !ok {
					item.State = "missing"
				} else if env, err := vms.Env(l.ID, v, net); err != nil {
					// A definition that cannot be built (a module that is
					// gone) is not a change: creating or applying says why.
				} else if v.Nix != nil && x.BaseSpec != "" {
					// With nix:, a change only in the module or params is
					// applied inside the running VM (Aplicar), not by a restart.
					if x.BaseSpec != env["GV_BASEHASH"] {
						item.State = "changed"
					} else if x.NixSpec != env["GV_NIXHASH"] {
						item.State = "nix"
					}
				} else if x.Spec != "" && x.Spec != env["GV_HASH"] {
					item.State = "changed"
				}
				rep.Services = append(rep.Services, item)
				delete(by, v.Name)
			}
			for name, x := range by {
				rep.Services = append(rep.Services, driftItem{name, "vm", "orphan", x.State == "running"})
			}
			// The cables: the table of the machine's relay against the file's.
			if vms.HasRelay(p) && len(st) > 0 {
				if have := vms.RelayMap(ctx, h, l.ID); have != vms.RelayTable(l.ID, p) {
					rep.Services = append(rep.Services, driftItem{Name: "cabos", Kind: "cables", State: "changed"})
				}
			}
		}
	}
	sort.Slice(rep.Services, func(i, j int) bool {
		a, b := rep.Services[i], rep.Services[j]
		if a.Kind != b.Kind {
			return a.Kind == "service"
		}
		return a.Name < b.Name
	})
	if created {
		for _, x := range rep.Services {
			if x.State != "ok" {
				rep.Pending = true
			}
		}
	}
	return rep
}

// filesPrint is a fingerprint of the project's compose files and .env.
func (s *Server) filesPrint(ctx context.Context, h *hosts.Host, l labs.Lab) string {
	sum := sha256.New()
	files := append([]string{}, l.Files...)
	if l.WorkingDir != "" {
		files = append(files, path.Join(l.WorkingDir, ".env"))
	}
	for _, f := range files {
		b, _ := h.ReadFile(ctx, f)
		sum.Write([]byte(f + "\x00"))
		sum.Write(b)
	}
	return hex.EncodeToString(sum.Sum(nil)[:12])
}

func (s *Server) drift(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	if !h.Files() {
		writeErr(w, http.StatusBadRequest, errors.New("indisponível para este host"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	l, err := s.findLab(ctx, h, r.PathValue("lab"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, s.computeDrift(ctx, h, l))
}

/* auto-apply */

type autoApplyState struct {
	On bool `json:"on"`
	// The last files applied (or that failed): not tried again until they change.
	Done string `json:"done,omitempty"`
}

var autoMu sync.Mutex

func (s *Server) autoApplyFile() string { return filepath.Join(s.configDir(), "aplicar-sozinho.json") }

func (s *Server) autoApply() map[string]autoApplyState {
	autoMu.Lock()
	defer autoMu.Unlock()
	m := map[string]autoApplyState{}
	if b, err := os.ReadFile(s.autoApplyFile()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func (s *Server) setAutoApplyState(key string, fn func(*autoApplyState)) error {
	autoMu.Lock()
	defer autoMu.Unlock()
	m := map[string]autoApplyState{}
	if b, err := os.ReadFile(s.autoApplyFile()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	st := m[key]
	fn(&st)
	if st.On {
		m[key] = st
	} else {
		delete(m, key)
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	if err := os.MkdirAll(s.configDir(), 0o700); err != nil {
		return err
	}
	return os.WriteFile(s.autoApplyFile(), b, 0o600)
}

func (s *Server) setAutoApply(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		On bool `json:"on"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&req)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	l, err := s.findLab(ctx, h, r.PathValue("lab"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	print := ""
	if req.On {
		// What is there now counts as seen: only later edits apply by themselves.
		print = s.filesPrint(ctx, h, l)
	}
	if err := s.setAutoApplyState(scheduleKey(h, l.ID), func(st *autoApplyState) { st.On, st.Done = req.On, print }); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]bool{"on": req.On})
}

// applyJob brings the project in line with its files: compose up (which
// also creates missing VMs) and, when restartVMs, a restart of the running
// VMs whose definition changed.
func (s *Server) applyJob(h *hosts.Host, l labs.Lab, restartVMs bool, why string) (string, error) {
	key := "lab:" + h.Machine() + "/" + l.ID
	return s.startJob(key, "Aplicar "+l.Name, func(ctx context.Context, out io.Writer) error {
		live := hosts.WithLive(ctx, out)
		fmt.Fprintf(out, "\x1b[1mAplicar os arquivos do projeto %s\x1b[0m em %s%s\n\n", l.Name, h.Name, why)
		defer s.store.Touch(h.Name, l.ID)
		if err := s.prepullImages(live, h, l, false, out); err != nil && ctx.Err() == nil {
			fmt.Fprintf(out, "\x1b[33maviso:\x1b[0m %v. O compose tenta de novo.\n\n", err)
		}
		before := s.computeDrift(ctx, h, l)
		s.dropNetDependents(ctx, h, l, before, out)
		o, cmd, err := s.composeLabRun(live, h, l.ID, l.WorkingDir, l.Files, "up")
		if cmd != "" {
			fmt.Fprintf(out, "\n\x1b[2m$ %s\x1b[0m\n", cmd)
		}
		if err != nil {
			if hint := s.portHint(ctx, h, l.ID, l.WorkingDir, l.Files, o+"\n"+err.Error()); hint != "" {
				fmt.Fprintf(out, "\n\x1b[33m%s\x1b[0m\n", hint)
			}
			return err
		}
		var changed []string
		for _, x := range before.Services {
			if x.Kind == "vm" && x.State == "changed" && x.Running {
				changed = append(changed, x.Name)
			}
		}
		if p, _, perr := s.loadProject(ctx, h, l.Files); perr == nil {
			s.watchKubeLab(h, l, p, func(m string) { fmt.Fprintf(out, "\n\x1b[36m%s\x1b[0m\n", m) })
		}
		if len(changed) == 0 {
			return nil
		}
		if !restartVMs {
			fmt.Fprintf(out, "\n\x1b[33mVMs com definição nova, ainda rodando com a antiga: %s. Reinicie-as para aplicar (o app não reinicia VMs sozinho).\x1b[0m\n", strings.Join(changed, ", "))
			return nil
		}
		p, _, _ := s.loadProject(ctx, h, l.Files)
		for _, name := range changed {
			fmt.Fprintf(out, "\n\x1b[36m==>\x1b[0m reiniciando a VM %s com a definição nova\n", name)
			o, err := s.restartVM(live, h, l.ID, name, p)
			fmt.Fprint(out, o)
			if err != nil {
				return fmt.Errorf("VM %s: %v", name, err)
			}
		}
		return nil
	})
}

func (s *Server) applyLab(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		RestartVMs bool `json:"restartVms"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&req)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	l, err := s.findLab(ctx, h, r.PathValue("lab"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if l.WorkingDir == "" || len(l.Files) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("não sei onde estão os arquivos deste projeto"))
		return
	}
	id, err := s.applyJob(h, l, req.RestartVMs, "")
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, map[string]string{"id": id})
}

// autoApplyLoop applies, by themselves, the projects the user asked for,
// once their files stayed the same for a check (so a half-saved file is
// not applied). Remote machines are only checked while connected.
func (s *Server) autoApplyLoop(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	last := map[string]string{} // key -> fingerprint seen on the previous check
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		for k, st := range s.autoApply() {
			if !st.On {
				continue
			}
			machine, lab, _ := strings.Cut(k, "|")
			h := s.machineHost(machine)
			if h == nil || !h.Files() || (h.Remote() && !h.Wanted()) || s.jobBusy("lab:"+h.Machine()+"/"+lab) {
				continue
			}
			cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
			l, err := s.findLab(cctx, h, lab)
			if err != nil || l.WorkingDir == "" {
				cancel()
				continue
			}
			print := s.filesPrint(cctx, h, l)
			stable := last[k] == print
			last[k] = print
			if !stable || print == st.Done {
				cancel()
				continue
			}
			rep := s.computeDrift(cctx, h, l)
			cancel()
			_ = s.setAutoApplyState(k, func(x *autoApplyState) { x.Done = print })
			if rep.Error != "" || !rep.Pending {
				continue // a broken file is not applied; nothing to do otherwise
			}
			_, _ = s.applyJob(h, l, false, " (aplicar sozinho: os arquivos mudaram)")
		}
	}
}

// netDependents are the services that use the network of one of svcs
// (network_mode: service:x, the cables' sidecars too), and theirs in turn.
func netDependents(p *composex.Project, svcs []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(n string) bool {
		if seen[n] {
			return false
		}
		seen[n] = true
		out = append(out, n)
		return true
	}
	var walk func(n string)
	walk = func(n string) {
		for _, m := range p.Names {
			if sv := p.Services[m]; sv != nil && sv.NetworkMode == "service:"+n && add(m) {
				walk(m)
			}
		}
		for _, t := range p.Taps {
			if t.Name == n {
				add(tapSidecar(n))
			}
		}
	}
	for _, n := range svcs {
		walk(n)
	}
	sort.Strings(out)
	return out
}

// dropNetDependents removes, before applying, the containers that use the
// network of a service about to be recreated: Podman refuses to remove a
// container whose network others still use, and compose stops halfway
// ("has dependent containers"). Compose creates them again right after.
func (s *Server) dropNetDependents(ctx context.Context, h *hosts.Host, l labs.Lab, rep driftReport, out io.Writer) {
	var changed []string
	for _, x := range rep.Services {
		if x.Kind == "service" && x.State == "changed" {
			changed = append(changed, x.Name)
		}
	}
	if len(changed) == 0 {
		return
	}
	p, _, err := s.loadProject(ctx, h, l.Files)
	if err != nil {
		return
	}
	deps := netDependents(p, changed)
	if len(deps) == 0 {
		return
	}
	fmt.Fprintf(out, "Antes de recriar %s: saem os que usam a rede deles (%s); o compose os cria de novo.\n",
		strings.Join(changed, ", "), strings.Join(deps, ", "))
	for _, c := range l.Containers {
		if !slices.Contains(deps, c.Service) {
			continue
		}
		e, err := s.engineFor(ctx, h, c.Runtime)
		if err == nil {
			err = e.Engine.Remove(ctx, c.ID, true)
		}
		if err != nil {
			fmt.Fprintf(out, "Container %s: erro: %v\n", c.Name, err)
		} else {
			fmt.Fprintf(out, "Container %s removido\n", c.Name)
		}
	}
	fmt.Fprintln(out)
}
