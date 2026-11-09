package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"gantry/internal/applog"
	"gantry/internal/config"
	"gantry/internal/cvm"
	"gantry/internal/engine"
	"gantry/internal/sysinfo"
)

// The Container VM: a Linux VM that holds the engines of this Mac. Its
// processes and logs are read only while its page asks (no loop here).

var (
	cvmSampler cvm.Sampler
	cvmInfoMu  sync.Mutex
	cvmInfo    = map[string]struct {
		at   time.Time
		info sysinfo.Info
	}{}
)

type cvmView struct {
	Name    string        `json:"name"`
	State   string        `json:"state"`
	Spec    cvm.Spec      `json:"spec"`
	Disks   []cvm.DiskUse `json:"disks"`
	Docker  string        `json:"docker,omitempty"`
	Podman  string        `json:"podman,omitempty"`
	Dir     string        `json:"dir"`
	Script  string        `json:"scriptSum,omitempty"`
	Pending bool          `json:"pending,omitempty"` // CPU or memory changed: applies on restart
	// Watching: the Mac's file changes are going in (hot reload).
	Watching bool `json:"watching"`
}

func cvmOf(ctx context.Context, name string) cvmView {
	s, _ := cvm.Load(name)
	v := cvmView{Name: name, State: cvm.State(ctx, name), Spec: s, Disks: cvm.Disks(name), Dir: cvm.Dir(name)}
	if s.Docker {
		v.Docker = cvm.DockerSocket(name)
	}
	if s.Podman {
		v.Podman = cvm.PodmanSocket(name)
	}
	if sc := cvm.SavedScript(name); sc != "" {
		v.Script = cvm.Sum(sc)
	}
	v.Pending = cvmPending(name, s)
	v.Watching = cvm.Watching(name)
	return v
}

// cvmRunning remembers the spec the machine started with.
var cvmRunning sync.Map // name -> string (the parts that need a restart)

func restartKey(s cvm.Spec) string {
	b, _ := json.Marshal([]any{s.CPUs, s.MemoryMiB, s.Shares, s.Rosetta})
	return string(b)
}

func cvmPending(name string, s cvm.Spec) bool {
	v, ok := cvmRunning.Load(name)
	return ok && v.(string) != restartKey(s)
}

func (s *Server) cvmList(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	out := struct {
		Supported bool      `json:"supported"`
		Vfkit     string    `json:"vfkit"`
		Home      string    `json:"home"`
		Arch      string    `json:"arch"`
		Default   cvm.Spec  `json:"default"`
		Machines  []cvmView `json:"machines"`
		Image     string    `json:"image"`
	}{Supported: cvm.Supported(), Vfkit: cvm.Vfkit(), Arch: runtime.GOARCH, Image: cvm.ImageURL(runtime.GOARCH), Machines: []cvmView{}}
	out.Home, _ = os.UserHomeDir()
	info := s.localInfo(ctx)
	out.Default = cvm.Default(info.CPUs, info.MemTotal)
	for _, n := range cvm.List() {
		out.Machines = append(out.Machines, cvmOf(ctx, n))
	}
	writeJSON(w, out)
}

// localInfo is the hardware of this computer (for the defaults).
func (s *Server) localInfo(ctx context.Context) sysinfo.Info {
	i := sysinfo.Info{CPUs: runtime.NumCPU()}
	if out, err := exec.CommandContext(ctx, "sh", "-c", sysinfo.Script).Output(); err == nil {
		i = sysinfo.Parse(string(out))
	}
	return i
}

func (s *Server) cvmScript(w http.ResponseWriter, r *http.Request) {
	var sp cvm.Spec
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&sp); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	home, _ := os.UserHomeDir()
	sc, sum := cvm.Script(sp, home)
	res := map[string]string{"script": sc, "sha256": sum, "userData": cvm.UserData(sp, "<chave pública gerada na criação>", sc)}
	if err := sp.Check(); err != nil {
		res["error"] = err.Error()
	}
	writeJSON(w, res)
}

func (s *Server) cvmCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Spec     cvm.Spec `json:"spec"`
		SHA256   string   `json:"sha256"`
		Recreate bool     `json:"recreate"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := req.Spec.Check(); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	label := "Criar a Container VM " + req.Spec.Name
	if req.Recreate {
		label = "Recriar a Container VM " + req.Spec.Name
	}
	jid, err := s.startJob("cvm:"+req.Spec.Name, label, func(ctx context.Context, out io.Writer) error {
		j := jobOf(out)
		prog := func(done, total int64) {
			if j == nil {
				return
			}
			if total < 0 {
				j.setProgress("")
			} else if total > 0 {
				j.setProgress(fmt.Sprintf("Baixando a imagem: %s de %s (%d%%)", humanBytes(done), humanBytes(total), done*100/total))
			} else {
				j.setProgress("Baixando a imagem: " + humanBytes(done))
			}
		}
		create := cvm.Create
		if req.Recreate {
			create = cvm.Recreate
		}
		err := create(ctx, req.Spec, req.SHA256, out, prog)
		if err == nil {
			s.cvmStarted(req.Spec.Name)
		}
		config.ForgetDetected()
		s.reload()
		return err
	})
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, map[string]string{"id": jid})
}

// cvmStarted runs what follows a start: the engines appear, the ssh-agent
// goes in.
func (s *Server) cvmStarted(name string) {
	sp, _ := cvm.Load(name)
	cvmRunning.Store(name, restartKey(sp))
	_ = cvm.WriteShims(name)
	go func() {
		defer applog.Recover("server/cvm.go:sync")
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if err := cvm.SyncGuest(ctx, name); err != nil {
			slog.Warn("Container VM: ajustes depois de ligar", "máquina", name, "erro", err)
		}
		if sp.SSHAgent {
			cvm.ForwardAgent(name)
		}
		if err := cvm.StartWatch(name); err != nil {
			slog.Info("Container VM: sem avisos de alteração", "máquina", name, "erro", err)
		}
		if err := cvm.SyncFinder(ctx, name); err != nil {
			slog.Warn("Container VM: volumes no Finder", "máquina", name, "erro", err)
		}
	}()
}

func (s *Server) cvmPower(w http.ResponseWriter, r *http.Request) {
	name, act := r.PathValue("name"), r.PathValue("acao")
	if !cvm.Exists(name) {
		writeErr(w, http.StatusNotFound, errors.New("máquina não encontrada"))
		return
	}
	label := map[string]string{"ligar": "Ligar ", "desligar": "Desligar ", "reiniciar": "Reiniciar ", "apagar": "Apagar "}[act]
	if label == "" {
		writeErr(w, http.StatusNotFound, errors.New("ação desconhecida"))
		return
	}
	jid, err := s.startJob("cvm:"+name, label+name, func(ctx context.Context, out io.Writer) error {
		defer func() { config.ForgetDetected(); s.reload() }()
		if act != "ligar" {
			cvm.StopAgent(name)
			cvm.StopWatch(name)
			if err := cvm.Stop(ctx, name, out); err != nil {
				return err
			}
			cvmRunning.Delete(name)
			cvmSampler.Forget(name)
			fmt.Fprintln(out, "Desligada.")
		}
		switch act {
		case "apagar":
			if err := cvm.Delete(ctx, name, out); err != nil {
				return err
			}
			fmt.Fprintln(out, "Máquina e discos apagados.")
			return nil
		case "ligar", "reiniciar":
			cvm.DailySnapshot(ctx, name)
			if err := cvm.Start(ctx, name, out); err != nil {
				return err
			}
			s.cvmStarted(name)
			fmt.Fprintln(out, "Ligada.")
		}
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, map[string]string{"id": jid})
}

// cvmEdit changes CPU, memory, the data disk and the start options.
func (s *Server) cvmEdit(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	cur, err := cvm.Load(name)
	if err != nil {
		writeErr(w, http.StatusNotFound, errors.New("máquina não encontrada"))
		return
	}
	var req struct {
		CPUs        *int         `json:"cpus"`
		MemoryMiB   *int         `json:"memoryMiB"`
		DataGiB     *int         `json:"dataGiB"`
		AutoStart   *bool        `json:"autoStart"`
		IdleStopMin *int         `json:"idleStopMin"`
		Shares      *[]cvm.Share `json:"shares"`
		Rosetta     *bool        `json:"rosetta"`
		SSHAgent    *bool        `json:"sshAgent"`
		SnapDaily   *bool        `json:"snapDaily"`
		SnapKeep    *int         `json:"snapKeep"`
		Watch       *bool        `json:"watch"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	n := cur
	if req.CPUs != nil {
		n.CPUs = *req.CPUs
	}
	if req.MemoryMiB != nil {
		n.MemoryMiB = *req.MemoryMiB
	}
	if req.AutoStart != nil {
		n.AutoStart = *req.AutoStart
	}
	if req.IdleStopMin != nil {
		n.IdleStopMin = *req.IdleStopMin
	}
	if req.Shares != nil {
		n.Shares = *req.Shares
	}
	if req.Rosetta != nil {
		n.Rosetta = *req.Rosetta
	}
	if req.SSHAgent != nil {
		n.SSHAgent = *req.SSHAgent
		if !n.SSHAgent {
			cvm.StopAgent(name)
		} else if cvm.State(r.Context(), name) == cvm.StateRunning {
			cvm.ForwardAgent(name)
		}
	}
	if req.SnapDaily != nil {
		n.SnapDaily = *req.SnapDaily
	}
	if req.SnapKeep != nil && *req.SnapKeep > 0 {
		n.SnapKeep = *req.SnapKeep
	}
	if req.Watch != nil {
		n.Watch = *req.Watch
	}
	if req.DataGiB != nil && *req.DataGiB != cur.DataGiB {
		if *req.DataGiB < cur.DataGiB {
			writeErr(w, http.StatusBadRequest, errors.New("o disco de dados só aumenta"))
			return
		}
		if err := cvm.Grow(name, *req.DataGiB); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		n.DataGiB = *req.DataGiB
	}
	if err := n.Check(); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := cvm.Save(n); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if req.Watch != nil {
		if n.Watch && cvm.State(r.Context(), name) == cvm.StateRunning {
			if err := cvm.StartWatch(name); err != nil {
				writeErr(w, http.StatusBadGateway, err)
				return
			}
		} else if !n.Watch {
			cvm.StopWatch(name)
		}
	}
	writeJSON(w, cvmOf(r.Context(), name))
}

func (s *Server) cvmProcs(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if cvm.State(r.Context(), name) != cvm.StateRunning {
		writeErr(w, http.StatusConflict, errors.New("a máquina está desligada"))
		return
	}
	snap, err := cvmSampler.Sample(r.Context(), name)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, snap)
}

// cvmSys is the hardware block of the machine's page (cached a few seconds).
func (s *Server) cvmSys(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if cvm.State(r.Context(), name) != cvm.StateRunning {
		writeJSON(w, sysinfo.Info{})
		return
	}
	cvmInfoMu.Lock()
	c, ok := cvmInfo[name]
	cvmInfoMu.Unlock()
	if ok && time.Since(c.at) < 4*time.Second {
		writeJSON(w, c.info)
		return
	}
	out, err := cvm.Run(r.Context(), name, sysinfo.Script)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	info := sysinfo.Parse(out)
	cvmInfoMu.Lock()
	cvmInfo[name] = struct {
		at   time.Time
		info sysinfo.Info
	}{time.Now(), info}
	cvmInfoMu.Unlock()
	writeJSON(w, info)
}

func (s *Server) cvmLogs(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	text, err := cvm.Logs(r.Context(), r.PathValue("name"), r.URL.Query().Get("fonte"), n)
	if err != nil && text == "" {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	// Logs are data: they go as they are, never through the translation.
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"text": text})
}

func (s *Server) cvmSignal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PID    int    `json:"pid"`
		Signal string `json:"signal"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := cvm.Signal(r.Context(), r.PathValue("name"), req.PID, req.Signal); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) cvmSavedScript(w http.ResponseWriter, r *http.Request) {
	sc := cvm.SavedScript(r.PathValue("name"))
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"script": sc, "sha256": cvm.Sum(sc)})
}

// cvmAutoStart starts, when the app opens, the machines set to.
func (s *Server) cvmAutoStart() {
	if !cvm.Supported() {
		return
	}
	// The commands of ~/.gantry/bin (docker, podman, nerdctl in the machine)
	// for the app too, after the Mac's own.
	if !strings.Contains(os.Getenv("PATH"), cvm.BinDir()) {
		_ = os.Setenv("PATH", os.Getenv("PATH")+string(os.PathListSeparator)+cvm.BinDir())
	}
	for _, name := range cvm.List() {
		sp, err := cvm.Load(name)
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		st := cvm.State(ctx, name)
		if st == cvm.StateRunning {
			s.cvmStarted(name)
		} else if sp.AutoStart && st == cvm.StateStopped {
			cvm.DailySnapshot(ctx, name)
			if err := cvm.Start(ctx, name, io.Discard); err == nil {
				s.cvmStarted(name)
				config.ForgetDetected()
				s.reload()
			} else {
				slog.Warn("Container VM não ligou", "máquina", name, "erro", err)
			}
		}
		cancel()
	}
}

// The Engines tab: contexts, connection, PATH, the docker.sock link.

func (s *Server) cvmIntegration(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !cvm.Exists(name) {
		writeErr(w, http.StatusNotFound, errors.New("máquina não encontrada"))
		return
	}
	if r.Method == http.MethodPut {
		var want cvm.Wanted
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&want); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if err := cvm.Apply(r.Context(), name, want); err != nil {
			writeErr(w, http.StatusBadGateway, err)
			return
		}
		config.ForgetDetected()
		s.reload()
	}
	writeJSON(w, cvm.Inspect(name))
}

func (s *Server) cvmLink(w http.ResponseWriter, r *http.Request) {
	var req struct {
		On bool `json:"on"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if err := cvm.LinkSocket(ctx, r.PathValue("name"), req.On); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, cvm.Inspect(r.PathValue("name")))
}

// cvmTest pings each engine of the machine through its socket on the Mac.
func (s *Server) cvmTest(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	sp, err := cvm.Load(name)
	if err != nil {
		writeErr(w, http.StatusNotFound, errors.New("máquina não encontrada"))
		return
	}
	type res struct {
		Name string `json:"name"`
		OK   bool   `json:"ok"`
		Ms   int64  `json:"ms"`
		Info string `json:"info"`
	}
	out := []res{}
	ping := func(label, sock string) {
		c := engine.New(func(ctx context.Context) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		})
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		t0 := time.Now()
		err := c.Ping(ctx)
		x := res{Name: label, OK: err == nil, Ms: time.Since(t0).Milliseconds()}
		if err != nil {
			x.Info = err.Error()
		} else {
			x.Info = sock
		}
		out = append(out, x)
	}
	if sp.Docker {
		ping("Docker", cvm.DockerSocket(name))
	}
	if sp.Podman {
		ping("Podman", cvm.PodmanSocket(name))
	}
	if sp.Containerd {
		t0 := time.Now()
		o, err := cvm.Run(r.Context(), name, "sudo nerdctl version --format '{{.Client.Version}}'")
		x := res{Name: "nerdctl", OK: err == nil, Ms: time.Since(t0).Milliseconds(), Info: strings.TrimSpace(o)}
		if err != nil {
			x.Info = err.Error()
		}
		out = append(out, x)
	}
	writeJSON(w, out)
}

// Snapshots of the data disk.

func (s *Server) cvmSnaps(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !cvm.Exists(name) {
		writeErr(w, http.StatusNotFound, errors.New("máquina não encontrada"))
		return
	}
	if r.Method == http.MethodPost {
		var req struct {
			Reason string `json:"reason"`
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&req)
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		if _, err := cvm.TakeSnapshot(ctx, name, req.Reason, false, nil); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, cvm.Snapshots(name))
}

func (s *Server) cvmSnapDelete(w http.ResponseWriter, r *http.Request) {
	if err := cvm.DeleteSnapshot(r.PathValue("name"), r.PathValue("id")); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, cvm.Snapshots(r.PathValue("name")))
}

func (s *Server) cvmSnapRestore(w http.ResponseWriter, r *http.Request) {
	name, id := r.PathValue("name"), r.PathValue("id")
	jid, err := s.startJob("cvm:"+name, "Restaurar o snapshot "+id+" de "+name, func(ctx context.Context, out io.Writer) error {
		defer func() { config.ForgetDetected(); s.reload() }()
		wasOn := cvm.State(ctx, name) == cvm.StateRunning
		cvm.StopAgent(name)
		cvm.StopWatch(name)
		if err := cvm.Stop(ctx, name, out); err != nil {
			return err
		}
		if err := cvm.RestoreSnapshot(ctx, name, id, out); err != nil {
			return err
		}
		if wasOn {
			if err := cvm.Start(ctx, name, out); err != nil {
				return err
			}
			s.cvmStarted(name)
		}
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, map[string]string{"id": jid})
}

// Platforms and credentials.

func (s *Server) cvmPlatforms(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if cvm.State(r.Context(), name) != cvm.StateRunning {
		writeErr(w, http.StatusConflict, errors.New("a máquina está desligada"))
		return
	}
	ps, err := cvm.Platforms(r.Context(), name)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, ps)
}

func (s *Server) cvmQEMU(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	jid, err := s.startJob("cvm:"+name, "QEMU na Container VM "+name, func(ctx context.Context, out io.Writer) error {
		fmt.Fprintf(out, "$ %s\n\n", cvm.QEMUScript)
		o, err := cvm.Run(ctx, name, cvm.QEMUScript)
		_, _ = out.Write([]byte(o))
		if err == nil {
			sp, _ := cvm.Load(name)
			sp.QEMU = true
			_ = cvm.Save(sp)
		}
		return err
	})
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, map[string]string{"id": jid})
}

func (s *Server) cvmCreds(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	writeJSON(w, cvm.Credentials(ctx, r.PathValue("name")))
}

// cvmTerm opens a shell in the machine for the dock.
func (s *Server) cvmTerm(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	cmd := ""
	if src := r.URL.Query().Get("logs"); src != "" {
		units := map[string]string{"sistema": "", "docker": " -u docker.service -u containerd.service", "podman": " -u podman.service -u podman.socket"}
		u, ok := units[src]
		if !ok {
			writeErr(w, http.StatusBadRequest, errors.New("log desconhecido"))
			return
		}
		cmd = "sudo journalctl -f -n 300 --no-pager" + u
	}
	t, err := cvm.Shell(ctx, r.PathValue("name"), cmd)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]string{"id": s.addTerm(t)})
}

// cvmIdleLoop stops the machines that asked to, after their minutes with no
// container running. One look a minute at the engines' sockets.
func (s *Server) cvmIdleLoop(ctx context.Context) {
	idle := map[string]time.Time{}
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		for _, name := range cvm.List() {
			sp, err := cvm.Load(name)
			if err != nil || sp.IdleStopMin <= 0 || cvm.State(ctx, name) != cvm.StateRunning {
				delete(idle, name)
				continue
			}
			if cvmBusy(ctx, name, sp) {
				delete(idle, name)
				continue
			}
			if idle[name].IsZero() {
				idle[name] = time.Now()
				continue
			}
			if time.Since(idle[name]) >= time.Duration(sp.IdleStopMin)*time.Minute {
				slog.Info("Container VM parada por falta de uso", "máquina", name, "minutos", sp.IdleStopMin)
				cvm.StopAgent(name)
				cvm.StopWatch(name)
				_ = cvm.Stop(ctx, name, io.Discard)
				cvmRunning.Delete(name)
				delete(idle, name)
				config.ForgetDetected()
				s.reload()
			}
		}
	}
}

// cvmBusy tells whether any container runs in the machine (or it cannot
// tell: then it counts as busy).
func cvmBusy(ctx context.Context, name string, sp cvm.Spec) bool {
	socks := []string{}
	if sp.Docker {
		socks = append(socks, cvm.DockerSocket(name))
	}
	if sp.Podman {
		socks = append(socks, cvm.PodmanSocket(name))
	}
	for _, sock := range socks {
		c := engine.New(func(ctx context.Context) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		})
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		list, err := c.Containers(cctx)
		cancel()
		if err != nil {
			return true
		}
		for _, x := range list {
			if x.State == "running" {
				return true
			}
		}
	}
	if sp.Containerd {
		out, err := cvm.Run(ctx, name, "sudo nerdctl ps -q | grep -c . || true")
		if err != nil || strings.TrimSpace(out) != "0" {
			return true
		}
	}
	return false
}

// Volumes in the Finder.

type cvmVolume struct {
	Engine     string `json:"engine"`
	Name       string `json:"name"`
	Mountpoint string `json:"mountpoint"`
	Project    string `json:"project,omitempty"`
}

func (s *Server) cvmVolumes(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	sp, err := cvm.Load(name)
	if err != nil {
		writeErr(w, http.StatusNotFound, errors.New("máquina não encontrada"))
		return
	}
	out := struct {
		Finder   bool              `json:"finder"`
		FinderRW bool              `json:"finderRW"`
		Mounts   []cvm.FinderMount `json:"mounts"`
		Volumes  []cvmVolume       `json:"volumes"`
	}{Finder: sp.Finder, FinderRW: sp.FinderRW, Mounts: cvm.FinderMounts(name), Volumes: []cvmVolume{}}
	if out.Mounts == nil {
		out.Mounts = []cvm.FinderMount{}
	}
	if cvm.State(r.Context(), name) == cvm.StateRunning {
		for _, e := range []struct {
			on   bool
			eng  string
			sock string
		}{{sp.Docker, "docker", cvm.DockerSocket(name)}, {sp.Podman, "podman", cvm.PodmanSocket(name)}} {
			if !e.on {
				continue
			}
			sock := e.sock
			c := engine.New(func(ctx context.Context) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sock)
			})
			ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
			vs, err := c.Volumes(ctx)
			cancel()
			if err != nil {
				continue
			}
			for _, v := range vs {
				out.Volumes = append(out.Volumes, cvmVolume{Engine: e.eng, Name: v.Name, Mountpoint: v.Mountpoint, Project: v.Labels["com.docker.compose.project"]})
			}
		}
	}
	writeJSON(w, out)
}

func (s *Server) cvmFinder(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req struct {
		On bool `json:"on"`
		RW bool `json:"rw"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	label := "Volumes de " + name + " no Finder"
	jid, err := s.startJob("cvm:"+name, label, func(ctx context.Context, out io.Writer) error {
		return cvm.SetFinder(ctx, name, req.On, req.RW, out)
	})
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, map[string]string{"id": jid})
}

func (s *Server) cvmOpenVolume(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Engine     string `json:"engine"`
		Volume     string `json:"volume"`
		Mountpoint string `json:"mountpoint"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	if err := cvm.OpenVolume(ctx, r.PathValue("name"), req.Engine, req.Volume, req.Mountpoint); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
