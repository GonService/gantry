package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gantry/internal/hosts"
	"gantry/internal/labs"
	"gantry/internal/vms"
)

// More for the VMs of a project: snapshots, disk size, clones, starting
// with the machine and disks from other hypervisors.

var snapRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

func (s *Server) vmContext(w http.ResponseWriter, r *http.Request, h *hosts.Host) (labs.Lab, string, bool) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	l, err := s.findLab(ctx, h, r.PathValue("lab"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return l, "", false
	}
	name := r.PathValue("vm")
	for _, v := range l.VMs {
		if v.Name == name {
			return l, name, true
		}
	}
	writeErr(w, http.StatusNotFound, errors.New("VM não encontrada no projeto"))
	return l, "", false
}

func (s *Server) vmSnapshots(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	l, name, ok := s.vmContext(w, r, h)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	list, err := vms.Snapshots(ctx, h, l.ID, name)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, list)
}

// vmJob runs a vmctl command for one VM as a background job.
func (s *Server) vmJob(w http.ResponseWriter, h *hosts.Host, l labs.Lab, label string, fn func(ctx context.Context, out io.Writer) error) {
	id, err := s.startJob("lab:"+h.Machine()+"/"+l.ID, label, func(ctx context.Context, out io.Writer) error {
		return fn(hosts.WithLive(ctx, out), out)
	})
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, map[string]string{"id": id})
}

func vmEnv(project, name string, extra ...string) map[string]string {
	m := map[string]string{"GV_PROJECT": project, "GV_NAME": name}
	for i := 0; i+1 < len(extra); i += 2 {
		m[extra[i]] = extra[i+1]
	}
	return m
}

func (s *Server) vmSnapshotCreate(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req)
	if req.Name == "" {
		req.Name = time.Now().Format("2006-01-02_15-04-05")
	}
	if !snapRe.MatchString(req.Name) {
		writeErr(w, http.StatusBadRequest, errors.New("nome de snapshot: letras, números, ponto, - e _"))
		return
	}
	if req.Name == "gantry-backup" {
		writeErr(w, http.StatusBadRequest, errors.New("gantry-backup é usado pelos backups; escolha outro nome"))
		return
	}
	l, name, ok := s.vmContext(w, r, h)
	if !ok {
		return
	}
	s.vmJob(w, h, l, "Snapshot de "+name, func(ctx context.Context, out io.Writer) error {
		_, err := vms.Run(ctx, h, "snap-create", vmEnv(l.ID, name, "GV_SNAP", req.Name))
		return err
	})
}

func (s *Server) vmSnapshotRestore(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	snap := r.PathValue("snap")
	if !snapRe.MatchString(snap) {
		writeErr(w, http.StatusBadRequest, errors.New("snapshot inválido"))
		return
	}
	l, name, ok := s.vmContext(w, r, h)
	if !ok {
		return
	}
	s.vmJob(w, h, l, "Voltar "+name+" a "+snap, func(ctx context.Context, out io.Writer) error {
		_, err := vms.Run(ctx, h, "snap-restore", vmEnv(l.ID, name, "GV_SNAP", snap))
		return err
	})
}

func (s *Server) vmSnapshotDelete(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	snap := r.PathValue("snap")
	if !snapRe.MatchString(snap) {
		writeErr(w, http.StatusBadRequest, errors.New("snapshot inválido"))
		return
	}
	l, name, ok := s.vmContext(w, r, h)
	if !ok {
		return
	}
	if s.jobBusy("lab:" + h.Machine() + "/" + l.ID) {
		writeErr(w, http.StatusConflict, errJobRunning)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	out, err := vms.Run(ctx, h, "snap-delete", vmEnv(l.ID, name, "GV_SNAP", snap))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "output": out})
}

var sizeRe = regexp.MustCompile(`^[1-9][0-9]{0,5}[GMT]$`)

// vmResize grows a VM's disk (stopping and starting it again) and writes the
// new size in the compose (or its .env variable), so a new VM gets it too.
func (s *Server) vmResize(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		Size string `json:"size"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req)
	req.Size = strings.ToUpper(strings.TrimSpace(req.Size))
	if !sizeRe.MatchString(req.Size) {
		writeErr(w, http.StatusBadRequest, errors.New("tamanho: um número com G, M ou T (ex.: 40G)"))
		return
	}
	l, name, ok := s.vmContext(w, r, h)
	if !ok {
		return
	}
	running := false
	for _, v := range l.VMs {
		if v.Name == name {
			running = v.State == "running"
		}
	}
	s.vmJob(w, h, l, "Disco de "+name, func(ctx context.Context, out io.Writer) error {
		if running {
			if _, err := vms.Run(ctx, h, "stop", vmEnv(l.ID, name)); err != nil {
				return err
			}
		}
		_, err := vms.Run(ctx, h, "resize", vmEnv(l.ID, name, "GV_SIZE", req.Size))
		if err == nil {
			if e := s.setVMField(ctx, h, l, name, "disk", req.Size); e != nil {
				fmt.Fprintf(out, "aviso: o disco aumentou, mas não consegui gravar o tamanho no compose: %v\n", e)
			}
		}
		if running {
			if _, e := vms.Run(ctx, h, "start", vmEnv(l.ID, name)); e != nil && err == nil {
				err = e
			}
		}
		return err
	})
}

// setVMField writes field: value in the VM's block of the compose; when the
// value there is ${VAR...}, the .env gets VAR instead.
func (s *Server) setVMField(ctx context.Context, h *hosts.Host, l labs.Lab, vm, field, value string) error {
	for _, f := range l.Files {
		b, err := h.ReadFile(ctx, f)
		if err != nil {
			continue
		}
		lines := strings.Split(string(b), "\n")
		start, end := findBlock(lines, "x-vms", vm)
		if start < 0 {
			continue
		}
		if i := fieldLine(lines, start, end, field); i >= 0 {
			if m := tagVarRe.FindStringSubmatch(":" + fieldValue(lines[i])); m != nil && strings.HasPrefix(fieldValue(lines[i]), "${") {
				return setEnvVar(ctx, h, l.WorkingDir, m[1], value)
			}
			lines[i] = strings.Repeat(" ", indentOf(lines[i])) + field + ": " + value
		} else {
			ins := strings.Repeat(" ", childIndent(lines, start, end)) + field + ": " + value
			lines = append(lines[:start+1], append([]string{ins}, lines[start+1:]...)...)
		}
		return h.WriteFile(ctx, f, []byte(strings.Join(lines, "\n")))
	}
	return errors.New("a VM não está nos arquivos do projeto")
}

// vmClone copies a VM (disk included) under a new name in the same
// project and brings it up; the copy gets its own SSH port, without the
// original's published ports.
func (s *Server) vmClone(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req)
	if !labName.MatchString(req.Name) {
		writeErr(w, http.StatusBadRequest, errors.New("nome da cópia: letras minúsculas, números, - ou _"))
		return
	}
	l, name, ok := s.vmContext(w, r, h)
	if !ok {
		return
	}
	for _, v := range l.VMs {
		if v.Name == req.Name {
			writeErr(w, http.StatusConflict, errors.New("já existe uma VM com esse nome no projeto"))
			return
		}
	}
	running := false
	for _, v := range l.VMs {
		if v.Name == name {
			running = v.State == "running"
		}
	}
	s.vmJob(w, h, l, "Clonar "+name, func(ctx context.Context, out io.Writer) error {
		// The compose first: if the file cannot take the copy, nothing else
		// is done.
		var file string
		var orig, edited string
		for _, f := range l.Files {
			b, err := h.ReadFile(ctx, f)
			if err != nil {
				continue
			}
			if e, ok := cloneBlock(string(b), "x-vms", name, req.Name, "ports", "ssh", "vnc"); ok {
				file, orig, edited = f, string(b), e
				break
			}
		}
		if file == "" {
			return errors.New("a VM não está nos arquivos do projeto")
		}
		if running {
			if _, err := vms.Run(ctx, h, "stop", vmEnv(l.ID, name)); err != nil {
				return err
			}
		}
		_, err := vms.Run(ctx, h, "clone", vmEnv(l.ID, name, "GV_TO", req.Name))
		if running {
			_, _ = vms.Run(ctx, h, "start", vmEnv(l.ID, name))
		}
		if err != nil {
			return err
		}
		if err := h.WriteFile(ctx, file, []byte(edited)); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s: %s ganhou a VM %s\n", path.Base(file), path.Base(path.Dir(file)), req.Name)
		p, _, err := s.loadProject(ctx, h, l.Files)
		if err != nil {
			_ = h.WriteFile(ctx, file, []byte(orig))
			return fmt.Errorf("o compose ficou inválido com a cópia (%v); voltei o arquivo", err)
		}
		if p.VM(req.Name) == nil {
			return errors.New("a cópia não apareceu no compose")
		}
		s.store.SetLab(h.Name, l.ID, l.WorkingDir, l.Files, l.Runtimes, vmNames(p))
		o, err := s.vmsUp(ctx, h, l.ID, p)
		fmt.Fprint(out, o)
		return err
	})
}

// vmAutostart marks a VM to start with the machine and makes sure the
// machine has the boot hook that starts them.
func (s *Server) vmAutostart(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		On bool `json:"on"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req)
	l, name, ok := s.vmContext(w, r, h)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	on := "0"
	if req.On {
		on = "1"
	}
	out, err := vms.Run(ctx, h, "autostart-set", vmEnv(l.ID, name, "GV_ON", on))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	hook := ""
	if req.On {
		if hook, err = installVMBootHook(ctx, h); err != nil {
			writeErr(w, http.StatusBadGateway, fmt.Errorf("a VM foi marcada, mas não consegui preparar a inicialização da máquina: %v", err))
			return
		}
	}
	writeJSON(w, map[string]any{"ok": true, "output": strings.TrimSpace(out), "hook": hook})
}

// installVMBootHook keeps a copy of vmctl on the machine and registers
// "vmctl autostart" to run when it starts: a LaunchAgent on macOS, a
// systemd user unit (with linger) on Linux, or an @reboot crontab line.
func installVMBootHook(ctx context.Context, h *hosts.Host) (string, error) {
	home, err := h.Run(ctx, "", []string{"sh", "-c", `printf '%s\n%s\n%s' "$HOME" "${XDG_STATE_HOME:-$HOME/.local/state}" "$(uname -s)"`}, nil)
	if err != nil {
		return "", err
	}
	parts := strings.Split(strings.TrimSpace(home), "\n")
	if len(parts) < 3 {
		return "", errors.New("resposta inesperada da máquina")
	}
	homeDir, state, sys := parts[0], parts[1], parts[2]
	script := state + "/gantry/vmctl.sh"
	if err := h.WriteFile(ctx, script, []byte(vms.Script())); err != nil {
		return "", err
	}
	if sys == "Darwin" {
		plist := homeDir + "/Library/LaunchAgents/com.gonservice.gantry-vms.plist"
		body := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.gonservice.gantry-vms</string>
  <key>ProgramArguments</key><array><string>/bin/sh</string><string>` + html.EscapeString(script) + `</string><string>autostart</string></array>
  <key>EnvironmentVariables</key><dict><key>PATH</key><string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string></dict>
  <key>RunAtLoad</key><true/>
  <key>StandardErrorPath</key><string>` + html.EscapeString(state) + `/gantry/vms-autostart.log</string>
</dict></plist>
`
		if err := h.WriteFile(ctx, plist, []byte(body)); err != nil {
			return "", err
		}
		return "as VMs marcadas iniciam quando você entra no macOS (LaunchAgent)", nil
	}
	unit := `[Unit]
Description=Gantry: VMs que iniciam com a máquina
After=network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/bin/sh "` + strings.ReplaceAll(script, `"`, `\"`) + `" autostart

[Install]
WantedBy=default.target
`
	o, err := h.Run(ctx, "", []string{"sh", "-c", `if command -v systemctl >/dev/null 2>&1 && systemctl --user show-environment >/dev/null 2>&1; then
  mkdir -p "$HOME/.config/systemd/user" && printf '%s' "$1" >"$HOME/.config/systemd/user/gantry-vms.service" &&
  systemctl --user daemon-reload && systemctl --user enable gantry-vms.service >/dev/null 2>&1 &&
  (loginctl enable-linger "$(id -un)" >/dev/null 2>&1 || true) && echo systemd
elif command -v crontab >/dev/null 2>&1; then
  (crontab -l 2>/dev/null | grep -v 'gantry/vmctl.sh.* autostart'; echo "@reboot /bin/sh '$2' autostart") | crontab - && echo cron
else echo none; fi`, "sh", unit, script}, nil)
	if err != nil {
		return "", err
	}
	switch strings.TrimSpace(o) {
	case "systemd":
		return "as VMs marcadas iniciam com a máquina (systemd do usuário, com linger)", nil
	case "cron":
		return "as VMs marcadas iniciam com a máquina (crontab @reboot)", nil
	}
	return "", errors.New("esta máquina não tem systemd do usuário nem crontab")
}

var diskName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// importDisk converts a disk from another hypervisor (OVA, VMDK, VDI,
// VHD/VHDX, raw) picked on this computer into a qcow2 on the host, ready
// to be the image of a VM. Remote hosts get the file streamed first.
func (s *Server) importDisk(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		Path string `json:"path"` // on this computer
		Name string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if !filepath.IsAbs(req.Path) {
		writeErr(w, http.StatusBadRequest, errors.New("escolha o arquivo do disco"))
		return
	}
	if !diskName.MatchString(req.Name) {
		writeErr(w, http.StatusBadRequest, errors.New("nome do disco: letras minúsculas, números, ponto, - ou _"))
		return
	}
	f, err := openLocal(req.Path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	f.Close()
	dctx, dcancel := context.WithTimeout(r.Context(), 30*time.Second)
	dest, err := h.Run(dctx, "", []string{"sh", "-c", `printf '%s' "${XDG_DATA_HOME:-$HOME/.local/share}/gantry/discos"`}, nil)
	dcancel()
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	target := dest + "/" + req.Name + ".qcow2"
	ectx, ecancel := context.WithTimeout(r.Context(), 30*time.Second)
	_, exists := h.Run(ectx, "", []string{"test", "!", "-e", target}, nil)
	ecancel()
	if exists != nil {
		writeErr(w, http.StatusConflict, fmt.Errorf("já existe um disco %s: VMs podem usá-lo como base; escolha outro nome", req.Name))
		return
	}
	id, err := s.startJob("disco:"+h.Machine()+"/"+req.Name, "Importar disco "+req.Name, func(ctx context.Context, out io.Writer) error {
		live := hosts.WithLive(ctx, out)
		ext := strings.ToLower(filepath.Ext(req.Path))
		src := req.Path
		if h.Remote() {
			src = dest + "/envio-" + req.Name + ext
			f, err := openLocal(req.Path)
			if err != nil {
				return err
			}
			st, _ := f.Stat()
			fmt.Fprintf(out, "==> enviando %s (%s) para %s\n", filepath.Base(req.Path), humanBytes(st.Size()), h.Name)
			err = h.RunStdin(ctx, "mkdir -p "+hosts.Quote(dest)+" && cat > "+hosts.Quote(src), &progressReader{r: f, total: st.Size(), j: jobOf(out)})
			f.Close()
			if err != nil {
				return err
			}
			defer h.Run(context.Background(), "", []string{"rm", "-f", src}, nil)
		}
		fmt.Fprintf(out, "==> convertendo para qcow2\n")
		if _, err := vms.Run(live, h, "convert", map[string]string{"GV_SRC": src, "GV_DST": target}); err != nil {
			return err
		}
		fmt.Fprintf(out, "\nUse como imagem da VM: %s\nSistemas sem cloud-init (a maioria dos discos importados) precisam de tela: true para você entrar pela tela (VNC).\n", target)
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, map[string]string{"id": id, "path": target})
}

// progressReader reports how much of an upload went.
type progressReader struct {
	r     io.Reader
	total int64
	n     int64
	last  time.Time
	j     *job
	verb  string // "Enviando" when empty
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.n += int64(n)
	if p.j != nil && (time.Since(p.last) > 300*time.Millisecond || err != nil) {
		p.last = time.Now()
		pct := 0
		if p.total > 0 {
			pct = int(p.n * 100 / p.total)
		}
		verb := p.verb
		if verb == "" {
			verb = "Enviando"
		}
		if p.total > 0 {
			p.j.setProgress(fmt.Sprintf("%s: %s de %s (%d%%)", verb, humanBytes(p.n), humanBytes(p.total), pct))
		} else {
			p.j.setProgress(fmt.Sprintf("%s: %s", verb, humanBytes(p.n)))
		}
		if err != nil {
			p.j.setProgress("")
		}
	}
	return n, err
}

// vmIdlePC finds the idle-PC of a Dynamips node's image (through its
// console, with the router at the prompt) and keeps it for the image; the
// node restarts to use it when asked.
func (s *Server) vmIdlePC(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		Restart bool `json:"restart"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req)
	l, name, ok := s.vmContext(w, r, h)
	if !ok {
		return
	}
	restart := ""
	if req.Restart {
		restart = "1"
	}
	s.vmJob(w, h, l, "Idle-PC de "+name, func(ctx context.Context, out io.Writer) error {
		_, err := vms.Run(ctx, h, "idle-pc", vmEnv(l.ID, name, "GV_RESTART", restart))
		return err
	})
}
