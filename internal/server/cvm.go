package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"time"

	"gantry/internal/applog"
	"gantry/internal/config"
	"gantry/internal/cvm"
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
	return v
}

// cvmRunning remembers the CPU and memory the machine started with.
var cvmRunning sync.Map // name -> [2]int

func cvmPending(name string, s cvm.Spec) bool {
	v, ok := cvmRunning.Load(name)
	if !ok {
		return false
	}
	r := v.([2]int)
	return r[0] != s.CPUs || r[1] != s.MemoryMiB
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
		Spec   cvm.Spec `json:"spec"`
		SHA256 string   `json:"sha256"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := req.Spec.Check(); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	jid, err := s.startJob("cvm:"+req.Spec.Name, "Criar a Container VM "+req.Spec.Name, func(ctx context.Context, out io.Writer) error {
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
		err := cvm.Create(ctx, req.Spec, req.SHA256, out, prog)
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
	cvmRunning.Store(name, [2]int{sp.CPUs, sp.MemoryMiB})
	if sp.SSHAgent {
		go func() {
			defer applog.Recover("server/cvm.go:agent")
			time.Sleep(3 * time.Second)
			cvm.ForwardAgent(name)
		}()
	}
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
		CPUs        *int  `json:"cpus"`
		MemoryMiB   *int  `json:"memoryMiB"`
		DataGiB     *int  `json:"dataGiB"`
		AutoStart   *bool `json:"autoStart"`
		IdleStopMin *int  `json:"idleStopMin"`
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
