package server

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gantry/internal/composex"
	"gantry/internal/hosts"
	"gantry/internal/labs"
	"gantry/internal/vms"
)

func vmNames(p *composex.Project) []string {
	var out []string
	for _, v := range p.VMs {
		out = append(out, v.Name)
	}
	return out
}

func (s *Server) vmsUp(ctx context.Context, h *hosts.Host, project string, p *composex.Project) (string, error) {
	var b strings.Builder
	net, err := s.projectNet(ctx, h, project, p)
	if err != nil {
		return "", err
	}
	if net != nil {
		fmt.Fprintf(&b, "── rede do projeto (%s.0/24)\n", strings.TrimSuffix(net.Gateway, "."+lastOctet(net.Gateway)))
		for _, v := range p.VMs {
			if ip := net.IPs[v.Name]; ip != "" {
				fmt.Fprintf(&b, "%s: %s\n", v.Name, ip)
			}
		}
	}
	order, err := vmOrder(p.VMs)
	if err != nil {
		return b.String(), err
	}
	ready := map[string]bool{}
	// VMs with nix: are ready only when their configuration is applied; the
	// ones that were already running are left as they are.
	running := map[string]bool{}
	for _, v := range order {
		if v.Nix != nil {
			if st, err := vms.Status(ctx, h, project); err == nil {
				for _, x := range st {
					running[x.Name] = x.State == "running"
				}
			}
			break
		}
	}
	say := func(msg string) {
		b.WriteString(msg)
		if lw := hosts.Live(ctx); lw != nil {
			fmt.Fprint(lw, msg)
		}
	}
	// The cables first: the relay must be there when the VMs start.
	if out, err := vms.SyncRelay(ctx, h, project, p); err != nil {
		return b.String() + out, fmt.Errorf("cabos (relay): %v", err)
	}
	for _, v := range order {
		for _, dep := range v.After {
			if ready[dep] {
				continue
			}
			if dv := p.VM(dep); dv != nil && (dv.NoManagement || dv.NoCloudInit) {
				// No SSH to wait for (a network appliance): it was started
				// before this one, which is what depois: can promise.
				continue
			}
			msg := fmt.Sprintf("── %s espera %s responder…\n", v.Name, dep)
			b.WriteString(msg)
			if lw := hosts.Live(ctx); lw != nil {
				fmt.Fprint(lw, msg)
			}
			if err := s.waitVM(ctx, h, project, dep, 5*time.Minute); err != nil {
				return b.String(), fmt.Errorf("VM %s: %s não respondeu: %v", v.Name, dep, err)
			}
			ready[dep] = true
		}
		env, err := vms.Env(project, v, net)
		if err == nil {
			err = s.vpcsEnv(ctx, h, v, env)
		}
		if err != nil {
			return b.String(), err
		}
		if v.Kind == "iol" {
			fmt.Fprintf(&b, "── nó IOL %s (%s, %d MB)\n", v.Name, v.Image, v.Memory)
		} else if v.Kind == "dyn" {
			fmt.Fprintf(&b, "── nó Dynamips %s (%s, %d MB)\n", v.Name, v.Image, v.Memory)
		} else if v.Kind == "vpcs" {
			fmt.Fprintf(&b, "── VPCS %s\n", v.Name)
		} else {
			fmt.Fprintf(&b, "── VM %s (%s, %d CPU, %d MB)\n", v.Name, v.Image, v.CPUs, v.Memory)
		}
		for _, w := range v.Warnings {
			fmt.Fprintf(&b, "aviso: %s\n", w)
		}
		out, err := vms.Run(ctx, h, "up", env)
		b.WriteString(out)
		if err != nil {
			return b.String(), fmt.Errorf("VM %s: %v", v.Name, err)
		}
		if v.Nix != nil && !running[v.Name] {
			if err := s.waitNixReady(ctx, h, project, v.Name, say); err != nil {
				return b.String(), err
			}
		}
	}
	return b.String(), nil
}

// waitNixReady waits, after a VM with nix: starts, for the first boot to
// apply the Nix configuration the VM has (cloud-init on a new disk).
func (s *Server) waitNixReady(ctx context.Context, h *hosts.Host, project, name string, say func(string)) error {
	say(fmt.Sprintf("── %s: esperando o boot e a configuração Nix (nixos-rebuild)…\n", name))
	if err := s.waitVM(ctx, h, project, name, 10*time.Minute); err != nil {
		return fmt.Errorf("VM %s: não respondeu pelo SSH: %v", name, err)
	}
	want := ""
	if st, err := vms.Status(ctx, h, project); err == nil {
		for _, x := range st {
			if x.Name == name {
				want = x.NixSpec
			}
		}
	}
	if want == "" {
		return fmt.Errorf("VM %s: não sei qual configuração Nix ela tem (falta nix.hash)", name)
	}
	if err := s.waitNix(ctx, h, project, name, want, say); err != nil {
		return err
	}
	say(fmt.Sprintf("%s: pronta (configuração Nix aplicada)\n", name))
	return nil
}

// vmOrder puts the VMs in an order that respects depois: (stable otherwise).
func vmOrder(list []*composex.VM) ([]*composex.VM, error) {
	by := map[string]*composex.VM{}
	for _, v := range list {
		by[v.Name] = v
	}
	var out []*composex.VM
	state := map[string]int{} // 1 visiting, 2 done
	var visit func(v *composex.VM, path []string) error
	visit = func(v *composex.VM, path []string) error {
		switch state[v.Name] {
		case 1:
			return fmt.Errorf("as VMs dependem umas das outras em círculo: %s", strings.Join(append(path, v.Name), " → "))
		case 2:
			return nil
		}
		state[v.Name] = 1
		for _, d := range v.After {
			dv, ok := by[d]
			if !ok {
				return fmt.Errorf("a VM %s espera %s, que não existe no projeto", v.Name, d)
			}
			if err := visit(dv, append(append([]string{}, path...), v.Name)); err != nil {
				return err
			}
		}
		state[v.Name] = 2
		out = append(out, v)
		return nil
	}
	for _, v := range list {
		if err := visit(v, nil); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// vmExec runs a command inside a running VM over SSH.
func (s *Server) vmExec(ctx context.Context, h *hosts.Host, project, name, cmd string) (string, error) {
	return vms.Run(ctx, h, "exec", map[string]string{"GV_PROJECT": project, "GV_NAME": name, "GV_CMD": base64.StdEncoding.EncodeToString([]byte(cmd))})
}

// waitVM waits until the VM answers over SSH (booted, cloud-init user in).
func (s *Server) waitVM(ctx context.Context, h *hosts.Host, project, name string, max time.Duration) error {
	deadline := time.Now().Add(max)
	var last error
	for time.Now().Before(deadline) {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		_, err := s.vmExec(cctx, h, project, name, "true")
		cancel()
		if err == nil {
			return nil
		}
		last = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	return last
}

func lastOctet(ip string) string { return ip[strings.LastIndex(ip, ".")+1:] }

// projectNet reserves the addresses of the project's VMs on the machine's VM
// network, when any uses it (lan: true). Without a network it says how to
// create one.
func (s *Server) projectNet(ctx context.Context, h *hosts.Host, project string, p *composex.Project) (*vms.Net, error) {
	if p == nil || !p.UsesLAN() {
		return nil, nil
	}
	ni, err := vms.Network(ctx, h)
	if err != nil {
		return nil, err
	}
	if ni.Mode == "none" {
		return nil, fmt.Errorf("este projeto usa a rede entre VMs (lan: true), mas a máquina ainda não tem uma. Ative uma vez com: %s", ni.Hint)
	}
	if len(ni.Problem) > 0 {
		return nil, fmt.Errorf("rede entre VMs: %s", strings.Join(ni.Problem, "; "))
	}
	net := &vms.Net{Gateway: ni.Gateway, IPs: map[string]string{}, Services: p.Names}
	for _, v := range p.VMs {
		if !v.LAN {
			continue
		}
		ip, err := vms.LANIP(ctx, h, project, v.Name)
		if err != nil {
			return nil, fmt.Errorf("VM %s: %v", v.Name, err)
		}
		net.IPs[v.Name] = ip
	}
	return net, nil
}

// attachVMs adds the VMs of labs that declare x-vms, with their state.
func (s *Server) attachVMs(ctx context.Context, h *hosts.Host, ls []labs.Lab) {
	if h.Kind == hosts.TCP {
		return
	}
	known := s.store.Known(h.Name)
	for i := range ls {
		k, ok := known[ls[i].ID]
		if !ok || len(k.VMs) == 0 {
			continue
		}
		sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		st, err := vms.Status(sctx, h, ls[i].ID)
		cancel()
		by := map[string]vms.State{}
		if err == nil {
			for _, x := range st {
				by[x.Name] = x
			}
		}
		for _, name := range k.VMs {
			vm := labs.VM{Name: name, State: "missing", Ports: []string{}}
			if x, ok := by[name]; ok {
				vm.State, vm.SSHPort, vm.Accel, vm.Ports, vm.VNC, vm.LAN, vm.Autostart, vm.Spec = x.State, x.SSHPort, x.Accel, x.Ports, x.VNC, x.LAN, x.Autostart, x.Spec
				vm.Serial, vm.NoCloudInit, vm.Rejected, vm.NoIdlePC = x.Serial, x.NoCloudInit, x.Rejected, x.NoIdlePC
			} else if err != nil {
				vm.State = "unknown"
			}
			ls[i].VMs = append(ls[i].VMs, vm)
			// A VM declared in the file but never created (or already
			// removed) is not part of the lab yet: counting it kept a removed
			// project around with nothing to remove and no way to forget it.
			if vm.State != "missing" {
				ls[i].Total++
			}
			if vm.State == "running" {
				ls[i].Running++
			}
		}
	}
}

// vmsViaLab stops, restarts or removes the VMs of a lab.
func (s *Server) vmsViaLab(ctx context.Context, h *hosts.Host, l labs.Lab, op string, b *strings.Builder) []string {
	var errs []string
	var p *composex.Project
	if op == "restart" && len(l.VMs) > 0 {
		p, _, _ = s.loadProject(ctx, h, l.Files)
	}
	for _, vm := range l.VMs {
		var err error
		base := map[string]string{"GV_PROJECT": l.ID, "GV_NAME": vm.Name}
		var out string
		switch op {
		case "stop":
			if vm.State != "running" {
				continue
			}
			out, err = vms.Run(ctx, h, "stop", base)
		case "restart":
			out, err = s.restartVM(ctx, h, l.ID, vm.Name, p)
		case "down":
			if vm.State == "missing" {
				continue
			}
			out, err = vms.Run(ctx, h, "rm", base)
		}
		b.WriteString(out)
		if err != nil {
			errs = append(errs, "VM "+vm.Name+": "+err.Error())
			fmt.Fprintf(b, "VM %s: erro: %v\n", vm.Name, err)
		}
	}
	return errs
}

// restartVM stops the VM and starts it with the definition in the file, so
// changes to CPU, memory and ports apply.
func (s *Server) restartVM(ctx context.Context, h *hosts.Host, project, name string, p *composex.Project) (string, error) {
	base := map[string]string{"GV_PROJECT": project, "GV_NAME": name}
	out, err := vms.Run(ctx, h, "stop", base)
	if err != nil {
		return out, err
	}
	if p != nil {
		if v := p.VM(name); v != nil {
			net, err := s.projectNet(ctx, h, project, p)
			if err != nil {
				return out, err
			}
			env, err := vms.Env(project, v, net)
			if err == nil {
				err = s.vpcsEnv(ctx, h, v, env)
			}
			if err != nil {
				return out, err
			}
			o, err := vms.Run(ctx, h, "up", env)
			return out + o, err
		}
	}
	o, err := vms.Run(ctx, h, "start", base)
	return out + o, err
}

// vmOp starts, stops, restarts or removes one VM.
func (s *Server) vmOp(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	l, err := s.findLab(ctx, h, r.PathValue("lab"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	name := r.PathValue("vm")
	base := map[string]string{"GV_PROJECT": l.ID, "GV_NAME": name}
	var out string
	switch op := r.PathValue("op"); op {
	case "start":
		p, _, perr := s.loadProject(ctx, h, l.Files)
		v := (*composex.VM)(nil)
		if perr == nil {
			v = p.VM(name)
		}
		if v == nil {
			// The file is gone or no longer has it: start as created.
			out, err = vms.Run(ctx, h, "start", base)
			break
		}
		net, e := s.projectNet(ctx, h, l.ID, p)
		if e != nil {
			err = e
			break
		}
		env, e := vms.Env(l.ID, v, net)
		if e == nil {
			e = s.vpcsEnv(ctx, h, v, env)
		}
		if e != nil {
			err = e
			break
		}
		out, err = vms.Run(ctx, h, "up", env)
	case "stop":
		out, err = vms.Run(ctx, h, "stop", base)
	case "restart":
		p, _, _ := s.loadProject(ctx, h, l.Files)
		out, err = s.restartVM(ctx, h, l.ID, name, p)
	case "rm":
		out, err = vms.Run(ctx, h, "rm", base)
	default:
		writeErr(w, http.StatusNotFound, errors.New("operação desconhecida"))
		return
	}
	resp := map[string]any{"ok": err == nil, "output": out}
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSON(w, resp)
}

// vmConsole streams the VM's serial console, read from its log file.
// vmRejected lists the startup configuration lines a network device's IOS
// refused when it last started.
func (s *Server) vmRejected(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	list, err := vms.Rejected(ctx, h, r.PathValue("lab"), r.PathValue("vm"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, list)
}

func (s *Server) vmConsole(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	project, name := r.PathValue("lab"), r.PathValue("vm")
	send, ok := startSSE(w)
	if !ok {
		return
	}
	var pos int64
	// Start near the end of a long log.
	if n, err := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64); err == nil {
		pos = n
	}
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		chunk, err := vms.Console(ctx, h, project, name, pos)
		cancel()
		if err != nil {
			if r.Context().Err() == nil {
				send("end", `{"message":`+strconv.Quote(err.Error())+`}`)
			}
			return
		}
		if len(chunk) > 0 {
			pos += int64(len(chunk))
			_, _ = b64Writer{send}.Write([]byte(chunk))
			if len(chunk) >= 65536 {
				continue
			}
		}
		select {
		case <-r.Context().Done():
			return
		case <-t.C:
		}
	}
}

// vmSSH returns the command that opens a shell in the VM.
func (s *Server) vmSSH(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	out, err := vms.Run(ctx, h, "ssh", map[string]string{"GV_PROJECT": r.PathValue("lab"), "GV_NAME": r.PathValue("vm")})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	cmd := strings.TrimSpace(out)
	if h.Kind == hosts.SSH {
		// Run it from the server: the VM listens on the server's loopback.
		cmd = "ssh -t " + h.URL + " " + shellQuote(cmd)
	}
	writeJSON(w, map[string]string{"command": cmd})
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// vmScreen gives the VNC address of a VM installed from an ISO (forwarded
// through ssh for remote hosts) and opens it in the system's VNC viewer.
func (s *Server) vmScreen(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	port, pass, err := vms.VNC(ctx, h, r.PathValue("lab"), r.PathValue("vm"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	if h.Kind == hosts.SSH {
		if addr, err = h.LocalAddr(ctx, addr); err != nil {
			writeErr(w, http.StatusBadGateway, err)
			return
		}
	}
	link := "vnc://:" + pass + "@" + addr
	resp := map[string]any{"address": addr, "password": pass, "url": link}
	if r.URL.Query().Get("open") == "1" {
		if err := openURL(link); err != nil {
			resp["error"] = err.Error()
		}
	}
	writeJSON(w, resp)
}

// vmNet tells whether the machine has the VM network (lan: true) and, if
// not, how to create it.
func (s *Server) vmNet(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	if h.Kind == hosts.TCP {
		writeJSON(w, vms.NetInfo{Mode: "none", Hint: "hosts tcp:// não rodam VMs"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	ni, err := vms.Network(ctx, h)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, ni)
}

// vmTerm opens a terminal in a VM: a shell, or its logs (journalctl -f)
// with ?logs=1.
func (s *Server) vmTerm(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	if h.Kind == hosts.TCP {
		writeErr(w, http.StatusBadRequest, errors.New("hosts tcp:// não rodam VMs"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	l, err := s.findLab(ctx, h, r.PathValue("lab"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	cmd := ""
	if r.URL.Query().Get("logs") == "1" {
		cmd = "if command -v journalctl >/dev/null && sudo journalctl -n 1 -q >/dev/null 2>&1; then sudo journalctl -f -n 300 --no-pager; else sudo tail -n 300 -F /var/log/syslog /var/log/messages /var/log/cloud-init-output.log 2>/dev/null; fi"
	}
	// ctx only bounds the setup; the session itself lives on.
	var sess ttySession
	if r.URL.Query().Get("serial") == "1" {
		rw, e := vms.OpenSerial(ctx, h, l.ID, r.PathValue("vm"))
		if e == nil {
			sess = rw.(ttySession)
		}
		err = e
	} else {
		sess, err = vms.Open(ctx, h, l.ID, r.PathValue("vm"), cmd)
	}
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]string{"id": s.addTerm(sess)})
}
