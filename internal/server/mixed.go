package server

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"gantry/internal/composex"
	"gantry/internal/engine"
	"gantry/internal/hosts"
	"gantry/internal/i18n"
	"gantry/internal/labs"
	"gantry/internal/vms"
)

// labsOf groups the host's containers into labs. Labs that x-gantry spreads
// over several engines also get the containers they have in the other
// engines of the same machine.
func (s *Server) labsOf(ctx context.Context, h *hosts.Host) ([]labs.Lab, error) {
	cs, err := h.Engine.Containers(ctx)
	if err != nil {
		return nil, err
	}
	ls := labs.Group(cs, s.store.Known(h.Name))
	rt := h.Runtime()
	for i := range ls {
		for j := range ls[i].Containers {
			ls[i].Containers[j].Runtime = rt
		}
	}
	s.store.Remember(h.Name, ls)

	need := map[string]bool{}
	for i, l := range ls {
		if l.Loose {
			continue
		}
		if rts := s.store.MixedRuntimes(l.ID, l.WorkingDir); len(rts) > 0 {
			ls[i].Runtimes = rts
			for _, r := range rts {
				if r != rt {
					need[r] = true
				}
			}
		}
	}
	defer s.attachVMs(ctx, h, ls)
	if len(need) == 0 {
		return ls, nil
	}
	for _, e := range s.machineEngines(ctx, h) {
		if !need[e.Runtime] || e.Engine == h.Engine {
			continue
		}
		ecs, err := e.Engine.Containers(ctx)
		if err != nil {
			continue
		}
		for _, o := range labs.Group(ecs, nil) {
			for i := range ls {
				l := &ls[i]
				if len(l.Runtimes) == 0 || o.ID != l.ID || (o.WorkingDir != "" && l.WorkingDir != "" && o.WorkingDir != l.WorkingDir) {
					continue
				}
				for _, c := range o.Containers {
					c.Runtime = e.Runtime
					l.Containers = append(l.Containers, c)
					l.Total++
					if c.State == "running" {
						l.Running++
					}
				}
				if l.WorkingDir == "" {
					l.WorkingDir, l.Files = o.WorkingDir, o.Files
				}
			}
		}
	}
	for i := range ls {
		sort.SliceStable(ls[i].Containers, func(a, b int) bool {
			x, y := ls[i].Containers[a], ls[i].Containers[b]
			if x.Service != y.Service {
				return x.Service < y.Service
			}
			return x.Name < y.Name
		})
	}
	return ls, nil
}

// loadProject reads and parses the lab's compose files on the host.
func (s *Server) loadProject(ctx context.Context, h *hosts.Host, files []string) (*composex.Project, []byte, error) {
	var docs [][]byte
	var all []byte
	for _, f := range files {
		b, err := h.ReadFile(ctx, f)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %v", f, err)
		}
		docs = append(docs, b)
		all = append(all, b...)
	}
	if len(docs) == 0 {
		return nil, nil, errors.New("projeto sem arquivo compose")
	}
	// Variables come from the .env next to the compose file, as in compose.
	env := map[string]string{}
	if envFile := path.Join(path.Dir(files[0]), ".env"); h.Exists(ctx, envFile) {
		if b, err := h.ReadFile(ctx, envFile); err == nil {
			env = composex.ParseEnv(b)
		}
	}
	// Secrets kept in the keychain count as variables too.
	for k, v := range hosts.EnvOf(ctx) {
		env[k] = v
	}
	for i := range docs {
		docs[i] = composex.Interpolate(docs[i], env)
	}
	p, err := composex.Parse(docs...)
	if p != nil {
		vms.AssignPorts(path.Dir(files[0]), p)
		s.resolveImages(ctx, h, p)
		for _, v := range p.VMs {
			if v.Nix != nil {
				readNixModule(ctx, h, path.Dir(files[0]), v)
			}
			if v.CloudInitFile != "" {
				readCloudInit(ctx, h, path.Dir(files[0]), v)
			}
			if v.ConfigFile != "" {
				readIOLConfig(ctx, h, path.Dir(files[0]), v)
			}
		}
	}
	return p, all, err
}

// nixModulePath is where the module of a VM with nix: is: relative to the
// project folder, or absolute.
func nixModulePath(dir, module string) string {
	if path.IsAbs(module) {
		return path.Clean(module)
	}
	return path.Join(dir, module)
}

// readCloudInit reads the extra cloud-config of a VM from its project file.
func readCloudInit(ctx context.Context, h *hosts.Host, dir string, v *composex.VM) {
	f := nixModulePath(dir, v.CloudInitFile)
	if !h.Exists(ctx, f) {
		v.CloudInitErr = fmt.Sprintf("%s não existe na pasta do projeto (%s)", v.CloudInitFile, f)
		return
	}
	b, err := h.ReadFile(ctx, f)
	if err != nil {
		v.CloudInitErr = fmt.Sprintf("não consegui ler %s: %v", f, err)
		return
	}
	v.CloudInit = string(b)
}

// readIOLConfig reads the startup configuration of an IOL node from the
// project folder (config: equipamentos/R1.cfg).
func readIOLConfig(ctx context.Context, h *hosts.Host, dir string, v *composex.VM) {
	f := nixModulePath(dir, v.ConfigFile)
	if !h.Exists(ctx, f) {
		v.ConfigErr = fmt.Sprintf("%s não existe na pasta do projeto (%s)", v.ConfigFile, f)
		return
	}
	b, err := h.ReadFile(ctx, f)
	if err != nil {
		v.ConfigErr = fmt.Sprintf("não consegui ler %s: %v", f, err)
		return
	}
	v.Config = string(b)
}

// readNixModule reads the NixOS module of a VM from the project folder; a
// module that cannot be read becomes an error when the VM is created or
// applied (the rest of the project still loads).
func readNixModule(ctx context.Context, h *hosts.Host, dir string, v *composex.VM) {
	f := nixModulePath(dir, v.Nix.Module)
	if !h.Exists(ctx, f) {
		v.Nix.ReadErr = fmt.Sprintf("%s não existe na pasta do projeto (%s)", v.Nix.Module, f)
		return
	}
	b, err := h.ReadFile(ctx, f)
	if err != nil {
		v.Nix.ReadErr = fmt.Sprintf("não consegui ler %s: %v", f, err)
		return
	}
	v.Nix.Content = string(b)
}

// composeLab runs a compose operation (up, pull) for a lab. A lab that uses
// x-gantry to pick engines runs once per engine, with DOCKER_HOST set.
func (s *Server) composeLab(ctx context.Context, h *hosts.Host, id, dir string, files []string, op string) (string, string, error) {
	out, cmd, err := s.composeLabRun(ctx, h, id, dir, files, op)
	if err != nil && isUpOp(op) {
		if hint := s.portHint(ctx, h, id, dir, files, out+"\n"+err.Error()); hint != "" {
			out = strings.TrimRight(out, "\n") + "\n\n" + hint + "\n"
		}
	}
	return out, cmd, err
}

var busyPort = []*regexp.Regexp{
	regexp.MustCompile(`:(\d+): bind: address already in use`),
	regexp.MustCompile(`(?i)bind for [^ ]*:(\d+) failed: port is already allocated`),
	regexp.MustCompile(`(?i)port (\d+) is already (?:in use|allocated)`),
}

// portHint explains a port already taken on the machine: who holds it and
// which variable of the project sets it.
func (s *Server) portHint(ctx context.Context, h *hosts.Host, _, dir string, files []string, out string) string {
	port := 0
	for _, re := range busyPort {
		if m := re.FindStringSubmatch(out); m != nil {
			port, _ = strconv.Atoi(m[1])
			break
		}
	}
	if port == 0 {
		return ""
	}
	// Built in pieces: each one in the app's language.
	msg := i18n.T(fmt.Sprintf("A porta %d já está em uso nesta máquina", port))
	if who := s.busyPorts(ctx, h)[port]; strings.HasPrefix(who, "container ") {
		msg += i18n.T(", pelo ") + who
	} else {
		msg += i18n.T(" por outro programa")
	}
	msg += "."
	fix := " Troque %s no .env do projeto (aba Arquivo) por uma porta livre e clique em Subir."
	if env, err := h.ReadFile(ctx, path.Join(dir, ".env")); err == nil {
		if m := regexp.MustCompile(`(?m)^\s*([A-Za-z0-9_]+)\s*=\s*["']?` + strconv.Itoa(port) + `["']?\s*$`).FindSubmatch(env); m != nil {
			return msg + i18n.T(fmt.Sprintf(fix, m[1]))
		}
	}
	if _, raw, err := s.loadProject(ctx, h, files); err == nil {
		if m := regexp.MustCompile(`\$\{([A-Za-z0-9_]+):-` + strconv.Itoa(port) + `\}`).FindSubmatch(raw); m != nil {
			return msg + i18n.T(fmt.Sprintf(fix, m[1]))
		}
	}
	return msg + i18n.T(" Troque a porta do host no compose (aba Arquivo) e clique em Subir.")
}

func (s *Server) composeLabRun(ctx context.Context, h *hosts.Host, id, dir string, files []string, op string) (string, string, error) {
	ctx = s.withSecrets(ctx, h, id)
	p, raw, perr := s.loadProject(ctx, h, files)
	if perr != nil && (strings.Contains(string(raw), "x-gantry") || strings.Contains(string(raw), "x-vms")) {
		return "", "", fmt.Errorf("não consegui ler o compose: %v", perr)
	}
	var pre, preCmd string
	if perr == nil {
		var rts []string
		if p.Gantry && p.Mixed(h.Runtime()) {
			for _, g := range p.Plan(h.Runtime()) {
				rts = append(rts, g.Runtime)
			}
		}
		s.store.SetLab(h.Name, id, dir, files, rts, vmNames(p))
		// VMs first: they take longer to boot, and services may need them.
		if isUpOp(op) && len(p.VMs) > 0 {
			out, err := s.vmsUp(ctx, h, id, p)
			pre, preCmd = out, "vmctl up "+strings.Join(vmNames(p), " ")
			if err != nil {
				return pre, preCmd, err
			}
		}
		if len(p.Names) == 0 {
			if len(p.VMs) == 0 {
				return "", "", errors.New("o compose não tem serviços nem VMs")
			}
			return pre, preCmd, nil
		}
		// Containers on cables: the relay with their ports, and a sidecar
		// each (the override); every operation takes the override, so down
		// and --remove-orphans see the sidecars.
		if isUpOp(op) {
			if len(p.VMs) == 0 && vms.HasRelay(p) {
				if out, err := vms.SyncRelay(ctx, h, id, p); err != nil {
					return pre, preCmd, fmt.Errorf("cabos: %v", err)
				} else if strings.TrimSpace(out) != "" {
					pre += out
				}
			}
			if f, err := s.writeTaps(ctx, h, id, dir, p); err != nil {
				return pre, preCmd, err
			} else if f != "" && !slices.Contains(files, f) {
				files = append(append([]string{}, files...), f)
			}
		} else if f := tapsOverride(dir); !slices.Contains(files, f) && h.Exists(ctx, f) {
			files = append(append([]string{}, files...), f)
		}
		// The containers learn the names of the VMs on the project network.
		if isUpOp(op) && p.UsesLAN() {
			if f, err := s.writeNetOverride(ctx, h, id, dir, p); err != nil {
				return pre, preCmd, err
			} else if f != "" && !slices.Contains(files, f) {
				files = append(append([]string{}, files...), f)
			}
		}
		if len(rts) > 0 {
			out, cmd, err := s.composeMixed(ctx, h, id, dir, files, p, p.Plan(h.Runtime()), op)
			return pre + out, joinCmd(preCmd, cmd), err
		}
	}
	argv := append([]string{}, h.Compose()...)
	argv = append(argv, "-p", id)
	for _, f := range files {
		argv = append(argv, "-f", f)
	}
	argv = append(argv, labOps[op]...)
	if pre != "" {
		pre += "── containers\n"
	}
	out, err := h.RunLive(ctx, dir, argv, nil)
	return pre + out, joinCmd(preCmd, strings.Join(argv, " ")), err
}

// writeNetOverride writes .gantry/rede.yaml next to the compose: every
// service gets extra_hosts with the VMs of the project network, so a
// container reaches "web" by name. The user's files are left alone.
func (s *Server) writeNetOverride(ctx context.Context, h *hosts.Host, id, dir string, p *composex.Project) (string, error) {
	net, err := s.projectNet(ctx, h, id, p)
	if err != nil || net == nil {
		return "", err
	}
	f := path.Join(dir, ".gantry", "rede.yaml")
	if err := h.WriteFile(ctx, f, []byte(netOverride(p, net))); err != nil {
		return "", fmt.Errorf("não consegui gravar %s: %v", f, err)
	}
	return f, nil
}

func netOverride(p *composex.Project, net *vms.Net) string {
	names := make([]string, 0, len(net.IPs))
	for n := range net.IPs {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("# Gerado pelo Gantry ao subir o projeto: os nomes das VMs na rede do projeto.\n")
	b.WriteString("# Não edite; ele é refeito a cada Subir.\nservices:\n")
	for _, svc := range p.Names {
		fmt.Fprintf(&b, "  %s:\n    extra_hosts:\n", svc)
		for _, n := range names {
			fmt.Fprintf(&b, "      - \"%s:%s\"\n", n, net.IPs[n])
		}
	}
	return b.String()
}

func joinCmd(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + " && " + b
}

func (s *Server) composeMixed(ctx context.Context, h *hosts.Host, id, dir string, files []string, p *composex.Project, plan []composex.Group, op string) (string, string, error) {
	var out, cmds strings.Builder
	for _, g := range plan {
		var svcs []string
		for _, n := range g.Services {
			// Naming a service turns on its profile; leave those to the user.
			if len(p.Services[n].Profiles) == 0 {
				svcs = append(svcs, n)
			}
		}
		if len(svcs) == 0 {
			continue
		}
		e, err := s.engineFor(ctx, h, g.Runtime)
		if err != nil {
			return out.String(), cmds.String(), fmt.Errorf("%s (serviços: %s)", err, strings.Join(svcs, ", "))
		}
		tool, err := s.composeTool(ctx, h, g.Runtime)
		if err != nil {
			return out.String(), cmds.String(), err
		}
		argv := []string{"env", "DOCKER_HOST=unix://" + e.Socket, "CONTAINER_HOST=unix://" + e.Socket}
		argv = append(argv, tool...)
		argv = append(argv, "-p", id)
		for _, f := range files {
			argv = append(argv, "-f", f)
		}
		argv = append(argv, labOps[op]...)
		if isUpOp(op) {
			// Dependencies in another engine were started by an earlier group.
			argv = append(argv, "--no-deps")
		}
		argv = append(argv, withTapSidecars(p, svcs)...)
		fmt.Fprintf(&out, "── %s: %s\n", g.Runtime, strings.Join(svcs, ", "))
		if cmds.Len() > 0 {
			cmds.WriteString(" && ")
		}
		cmds.WriteString(strings.Join(argv, " "))
		o, err := h.RunLive(ctx, dir, argv, nil)
		out.WriteString(o)
		if !strings.HasSuffix(o, "\n") {
			out.WriteString("\n")
		}
		if err != nil {
			return out.String(), cmds.String(), fmt.Errorf("%s: %v", g.Runtime, err)
		}
	}
	return out.String(), cmds.String(), nil
}

// composeTool picks the compose command for an engine. docker compose can
// drive both Docker and Podman (through DOCKER_HOST), so it is preferred.
func (s *Server) composeTool(ctx context.Context, h *hosts.Host, rt string) ([]string, error) {
	if c, ok := s.cfg.Find(h.Name); ok && len(c.Compose) > 0 {
		return c.Compose, nil
	}
	s.agentMu.Lock()
	have, ok := s.tools[h]
	s.agentMu.Unlock()
	if !ok {
		out, _ := h.Run(ctx, "", []string{"sh", "-c", `command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1 && echo dc
command -v docker-compose >/dev/null 2>&1 && echo dco
command -v podman >/dev/null 2>&1 && echo p
command -v podman-compose >/dev/null 2>&1 && echo pco
true`}, nil)
		have = map[string]bool{}
		for _, l := range strings.Fields(out) {
			have[l] = true
		}
		s.agentMu.Lock()
		s.tools[h] = have
		s.agentMu.Unlock()
	}
	switch {
	case have["dc"]:
		return []string{"docker", "compose"}, nil
	case have["dco"]:
		return []string{"docker-compose"}, nil
	case rt == "podman" && have["p"]:
		return []string{"podman", "compose"}, nil
	case rt == "podman" && have["pco"]:
		return []string{"podman-compose"}, nil
	}
	return nil, fmt.Errorf("não achei um comando compose para o %s neste host (instale o docker compose ou o podman-compose)", rt)
}

// labViaEngine stops, restarts or removes a lab through the engine API of
// each engine its containers live in.
func (s *Server) labViaEngine(ctx context.Context, h *hosts.Host, l labs.Lab, op string) (string, error) {
	var b strings.Builder
	var errs []string
	engines := map[string]*engine.Client{}
	engineOf := func(rt string) (*engine.Client, error) {
		if c, ok := engines[rt]; ok {
			return c, nil
		}
		e, err := s.engineFor(ctx, h, rt)
		if err != nil {
			return nil, err
		}
		engines[rt] = e.Engine
		return e.Engine, nil
	}
	// The containers on another one's network go first: the engine refuses to
	// remove (or stop cleanly) the one whose network others still use.
	ctrs := append([]labs.Container(nil), l.Containers...)
	if op == "down" || op == "stop" {
		sort.SliceStable(ctrs, func(a, b int) bool { return ctrs[a].SharedNet && !ctrs[b].SharedNet })
	}
	for _, c := range ctrs {
		eng, err := engineOf(c.Runtime)
		if err == nil {
			switch op {
			case "stop":
				if c.State != "running" {
					continue
				}
				err = eng.Action(ctx, c.ID, "stop")
			case "restart":
				err = eng.Action(ctx, c.ID, "restart")
			case "down":
				err = eng.Remove(ctx, c.ID, true)
			}
		}
		verb := map[string]string{"stop": "parado", "restart": "reiniciado", "down": "removido"}[op]
		label := c.Name
		if len(l.Runtimes) > 0 && c.Runtime != "" {
			label += " (" + c.Runtime + ")"
		}
		if err != nil {
			errs = append(errs, c.Name+": "+err.Error())
			fmt.Fprintf(&b, "Container %s: erro: %v\n", label, err)
		} else {
			fmt.Fprintf(&b, "Container %s %s\n", label, verb)
		}
	}
	errs = append(errs, s.vmsViaLab(ctx, h, l, op, &b)...)
	if op == "down" {
		rts := l.Runtimes
		if len(rts) == 0 {
			rts = []string{h.Runtime()}
		}
		for _, rt := range rts {
			eng, err := engineOf(rt)
			if err != nil {
				continue
			}
			nets, err := eng.NetworksByLabel(ctx, labs.LabelProject, l.ID)
			if err != nil {
				continue
			}
			for _, n := range nets {
				if err := eng.RemoveNetwork(ctx, n.ID); err != nil {
					fmt.Fprintf(&b, "Rede %s: erro: %v\n", n.Name, err)
				} else {
					fmt.Fprintf(&b, "Rede %s removida\n", n.Name)
				}
			}
		}
	}
	if len(errs) > 0 {
		return b.String(), errors.New(strings.Join(errs, "; "))
	}
	return b.String(), nil
}

// findComposeFiles returns the compose file of a folder.
func (s *Server) findComposeFiles(ctx context.Context, h *hosts.Host, dir string) []string {
	for _, n := range composex.FileNames {
		f := path.Join(dir, n)
		if h.Exists(ctx, f) {
			return []string{f}
		}
	}
	return nil
}
