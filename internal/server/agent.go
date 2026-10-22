package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gantry/internal/agent"
	"gantry/internal/agentbin"
	"gantry/internal/applog"
	"gantry/internal/hosts"
	"gantry/internal/metrics"
	"gantry/internal/vms"
)

type agentView struct {
	State        string `json:"state"` // running, missing
	Version      string `json:"version,omitempty"`
	Arch         string `json:"arch,omitempty"`
	EngineOK     bool   `json:"engineOk,omitempty"`
	MetricsSince int64  `json:"metricsSince,omitempty"`
	Started      int64  `json:"started,omitempty"`
	Outdated     bool   `json:"outdated,omitempty"`
	Bundled      bool   `json:"bundled"` // this build can install it
	Protocol     int    `json:"protocol,omitempty"`
	VMs          bool   `json:"vms,omitempty"`    // the agent samples the VMs too
	Direct       string `json:"direct,omitempty"` // paired address (TCP + TLS)
	Via          string `json:"via,omitempty"`    // tls or ssh: how it answered
	EngineVia    string `json:"engineVia,omitempty"`
}

// freshAgent in a context makes describe ask the agent again.
type freshAgent struct{}

type agentCache struct {
	at time.Time
	v  agentView
}

type collectorEntry struct {
	col    *metrics.Collector
	cancel context.CancelFunc
}

// agentState asks the agent on the host how it is, caching for a while so
// the host list stays quick.
func (s *Server) agentState(ctx context.Context, h *hosts.Host, fresh bool) *agentView {
	if h.Agent == nil {
		return nil
	}
	s.agentMu.Lock()
	c, ok := s.agentInfo[h]
	s.agentMu.Unlock()
	if ok && !fresh && time.Since(c.at) < 30*time.Second {
		v := c.v
		return &v
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	v := agentView{State: "missing", Bundled: len(agentbin.Arches()) > 0}
	if info, err := h.Agent.Info(ctx); err == nil {
		v.State, v.Version, v.Arch = "running", info.Version, info.Arch
		v.EngineOK, v.MetricsSince, v.Started = info.EngineOK, info.MetricsSince, info.Started
		// Only a newer protocol asks for an update: a new app version
		// with the same agent features does not.
		v.Outdated = info.Protocol < agent.Protocol && v.Bundled
		v.Protocol, v.VMs = info.Protocol, info.VMs
		h.SetAgentExec(info.Protocol >= 3)
	} else {
		h.SetAgentExec(false)
		v.Via, v.EngineVia = h.Via()
	}
	v.Direct = h.Direct()
	s.agentMu.Lock()
	s.agentInfo[h] = agentCache{at: time.Now(), v: v}
	s.agentMu.Unlock()
	return &v
}

func (s *Server) forgetAgentState(h *hosts.Host) {
	s.agentMu.Lock()
	delete(s.agentInfo, h)
	s.agentMu.Unlock()
}

// startJob runs fn in the background; the UI follows its output.
const agentUnit = `[Unit]
Description=Gantry agent (métricas e arquivos dos labs)

[Service]
ExecStart=%h/.local/bin/gantry-agent
Restart=on-failure
RestartSec=5
# The labs' programs (IOL, QEMU, the cables) started through the agent go
# on running when it restarts or updates.
KillMode=process

[Install]
WantedBy=default.target
`

func (s *Server) agentOp(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	if !h.Remote() {
		writeErr(w, http.StatusBadRequest, errors.New("o agente só é usado em hosts ssh:// e wsl://"))
		return
	}
	op := r.PathValue("op")
	// Options: direct connection (TCP + mutual TLS) and its port.
	var opt struct {
		Direct *bool `json:"direct"`
		Port   int   `json:"port"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&opt)
	port := opt.Port
	if port <= 0 || port > 65535 {
		port = agent.DefaultTLSPort
	}
	// By default an install keeps what the machine had: paired stays paired.
	direct := h.Direct() != "" || op == "install"
	if opt.Direct != nil {
		direct = *opt.Direct
	}
	if h.Kind == hosts.WSL {
		// In WSL the agent is the way to the engine: it stays, paired.
		if op == "remove" || (op == "direct" && !direct) {
			writeErr(w, http.StatusBadRequest, errors.New("no WSL o app fala com o engine pelo agente; ele não pode ser removido nem desligado"))
			return
		}
		direct = true
	}
	var fn func(ctx context.Context, out io.Writer) error
	switch op {
	case "install", "update":
		fn = func(ctx context.Context, out io.Writer) error { return s.installAgent(ctx, h, out, direct, port) }
	case "remove":
		fn = func(ctx context.Context, out io.Writer) error { return s.removeAgent(ctx, h, out) }
	case "direct":
		fn = func(ctx context.Context, out io.Writer) error { return s.setAgentDirect(ctx, h, out, direct, port) }
	default:
		writeErr(w, http.StatusNotFound, errors.New("operação desconhecida"))
		return
	}
	id, err := s.startJob("agent:"+h.Name, "agent-"+op, func(ctx context.Context, out io.Writer) error {
		defer s.forgetAgentState(h)
		err := fn(ctx, out)
		if err != nil {
			fmt.Fprintf(out, "\n\x1b[31merro:\x1b[0m %v\n", err)
		}
		return err
	})
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, map[string]string{"id": id})
}

func step(out io.Writer, msg string) { fmt.Fprintf(out, "\x1b[1;32m==>\x1b[0m %s\n", msg) }

func (s *Server) installAgent(ctx context.Context, h *hosts.Host, out io.Writer, direct bool, port int) error {
	ctx = hosts.WithoutAgent(ctx)
	step(out, "verificando o servidor")
	res, err := h.Run(ctx, "", []string{"sh", "-c", `uname -s; uname -m; echo "$HOME"; command -v systemctl >/dev/null 2>&1 && systemctl --user show-environment >/dev/null 2>&1 && echo systemd || echo nosystemd`}, nil)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSpace(res), "\n")
	if len(lines) < 4 {
		return fmt.Errorf("resposta inesperada do servidor: %q", res)
	}
	sys, mach, home, init := strings.TrimSpace(lines[0]), lines[1], strings.TrimSpace(lines[2]), strings.TrimSpace(lines[3])
	if sys != "Linux" {
		return fmt.Errorf("o agente só roda em Linux (o servidor é %s)", sys)
	}
	arch, ok := agentbin.LinuxArch(mach)
	if !ok {
		return fmt.Errorf("arquitetura sem agente: %s", strings.TrimSpace(mach))
	}
	fmt.Fprintf(out, "    Linux %s, home %s, %s\n", arch, home, map[string]string{"systemd": "systemd do usuário disponível", "nosystemd": "sem systemd do usuário"}[init])
	bin, err := agentbin.Binary(arch)
	if err != nil {
		return err
	}

	step(out, fmt.Sprintf("enviando o gantry-agent %s (%.1f MB)", Version, float64(len(bin))/(1<<20)))
	binDir := home + "/.local/bin"
	if _, err := h.Run(ctx, "", []string{"mkdir", "-p", binDir, home + "/.config/systemd/user", home + "/.local/state/gantry"}, nil); err != nil {
		return err
	}
	tmp := binDir + "/gantry-agent.new"
	if err := h.WriteFile(ctx, tmp, bin); err != nil {
		return err
	}
	ver, err := h.Run(ctx, "", []string{"sh", "-c", `chmod 755 "$1" && "$1" -version`, "sh", tmp}, nil)
	if err != nil {
		return fmt.Errorf("o binário enviado não executou: %v", err)
	}
	if _, err := h.Run(ctx, "", []string{"mv", "-f", tmp, binDir + "/gantry-agent"}, nil); err != nil {
		return err
	}
	fmt.Fprintf(out, "    %s/gantry-agent %s\n", binDir, strings.TrimSpace(ver))
	if direct {
		step(out, fmt.Sprintf("pareando a conexão direta (TCP %d, TLS mútuo)", port))
		if err := s.pairDirect(ctx, h, binDir+"/gantry-agent", port, out); err != nil {
			fmt.Fprintf(out, "    \x1b[33maviso:\x1b[0m %v. O app continua usando SSH.\n", err)
		}
	} else if h.Direct() != "" {
		_, _ = h.Run(ctx, "", []string{binDir + "/gantry-agent", "unpair"}, nil)
		s.forgetDirect(h)
	}

	if init == "systemd" {
		step(out, "ativando o serviço do usuário (systemctl --user)")
		if err := h.WriteFile(ctx, home+"/.config/systemd/user/gantry-agent.service", []byte(agentUnit)); err != nil {
			return err
		}
		o, err := h.Run(ctx, "", []string{"sh", "-c", `systemctl --user daemon-reload && systemctl --user enable gantry-agent.service 2>&1 && systemctl --user restart gantry-agent.service`}, nil)
		if strings.TrimSpace(o) != "" {
			fmt.Fprintln(out, indent(o))
		}
		if err != nil {
			return err
		}
		step(out, "mantendo o agente ativo sem sessão aberta (loginctl enable-linger)")
		if o, err := h.Run(ctx, "", []string{"sh", "-c", `loginctl enable-linger "$(id -un)" 2>&1 && loginctl show-user "$(id -un)" -p Linger`}, nil); err != nil {
			fmt.Fprintf(out, "    \x1b[33maviso:\x1b[0m não consegui ativar o linger (%s).\n    O agente para quando você sair de todas as sessões. No servidor: sudo loginctl enable-linger $USER\n", strings.TrimSpace(o))
		} else {
			fmt.Fprintln(out, indent(o))
		}
	} else {
		step(out, "iniciando sem systemd (nohup)")
		if _, err := h.Run(ctx, "", []string{"sh", "-c", `pkill -x gantry-agent 2>/dev/null; sleep 0.5; nohup "$HOME/.local/bin/gantry-agent" >"$HOME/.local/state/gantry/agent.log" 2>&1 </dev/null & echo started`}, nil); err != nil {
			return err
		}
		fmt.Fprintln(out, "    \x1b[33maviso:\x1b[0m sem systemd do usuário, o agente não volta sozinho depois de reiniciar o servidor.")
	}

	step(out, "conferindo a conexão com o agente")
	var last error
	for i := 0; i < 20; i++ {
		ictx, cancel := context.WithTimeout(ctx, 2*time.Second)
		info, err := h.Agent.Info(ictx)
		cancel()
		if err == nil {
			eng := "engine não encontrado"
			if info.EngineOK {
				eng = "engine em " + info.Engine
			}
			fmt.Fprintf(out, "    agente %s rodando (pid %d), %s\n", info.Version, info.PID, eng)
			if h.Direct() != "" {
				s.reportDirect(ctx, h, out)
			}
			fmt.Fprintf(out, "\n\x1b[1;32mPronto.\x1b[0m O histórico de métricas (containers e VMs) começa agora.\n")
			return nil
		}
		last = err
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("o agente foi instalado, mas não respondeu: %v", last)
}

func (s *Server) removeAgent(ctx context.Context, h *hosts.Host, out io.Writer) error {
	ctx = hosts.WithoutAgent(ctx)
	step(out, "parando e removendo o agente")
	o, err := h.Run(ctx, "", []string{"sh", "-c", `
if command -v systemctl >/dev/null 2>&1 && systemctl --user show-environment >/dev/null 2>&1; then
  systemctl --user disable --now gantry-agent.service 2>&1 || true
fi
pkill -x gantry-agent 2>/dev/null || true
rm -f "$HOME/.config/systemd/user/gantry-agent.service" "$HOME/.local/bin/gantry-agent" "$HOME/.local/bin/gantry-agent.new"
# Only the agent's own files: the VMs of the labs live in the same folder.
st="${XDG_STATE_HOME:-$HOME/.local/state}/gantry"
rm -rf "$st/tls" "$st/metrics.gob" "$st/agent.log" "$st/agent.sock"
command -v systemctl >/dev/null 2>&1 && systemctl --user daemon-reload 2>/dev/null || true
echo ok`}, nil)
	if strings.TrimSpace(o) != "" {
		fmt.Fprintln(out, indent(o))
	}
	if err != nil {
		return err
	}
	s.forgetDirect(h)
	fmt.Fprintln(out, "\n\x1b[1;32mPronto.\x1b[0m O agente e o histórico de métricas foram apagados do servidor. As VMs dos projetos continuam lá.")
	return nil
}

func indent(s string) string {
	s = strings.TrimRight(s, "\n")
	return "    " + strings.ReplaceAll(s, "\n", "\n    ")
}

// metrics returns the history for the host or one lab: from the agent when
// there is one, otherwise from a collector the app runs while it is open.
func (s *Server) metrics(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	q := r.URL.Query()
	rng, _ := strconv.Atoi(q.Get("range"))
	if rng <= 0 || rng > 7*86400 {
		rng = 3600
	}
	since := time.Now().Unix() - int64(rng)
	lab := q.Get("lab")
	const maxPoints = 240
	id := q.Get("id")
	// VMs are sampled by the app on the machine, whatever the containers' source.
	agentVMs := false
	reply := func(source string, res metrics.Result) {
		if (id == "" || strings.HasPrefix(id, "vm:")) && !agentVMs {
			if vc := s.vmCollector(h); vc != nil {
				res.Series = append(res.Series, vc.Query(since, lab, maxPoints).Series...)
			}
		}
		if id != "" {
			keep := res.Series[:0]
			for _, x := range res.Series {
				if x.ID == id || x.Name == id {
					keep = append(keep, x)
				}
			}
			res.Series = keep
		}
		if res.Series == nil {
			res.Series = []metrics.Series{}
		}
		writeJSON(w, map[string]any{"source": source, "result": res})
	}
	if q.Get("source") == "prometheus" {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		rr, step := rangeStep(r)
		res, err := s.promLab(ctx, h, lab, rr, step)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		reply("prometheus", res)
		return
	}
	if av := s.agentState(r.Context(), h, false); av != nil && av.State == "running" {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		res, err := h.Agent.Metrics(ctx, since, lab, maxPoints)
		if err == nil {
			agentVMs = av.VMs
			reply("agent", res)
			return
		}
		s.forgetAgentState(h)
	}
	reply("app", s.collector(h).Query(since, lab, maxPoints))
}

func (s *Server) collector(h *hosts.Host) *metrics.Collector {
	s.agentMu.Lock()
	defer s.agentMu.Unlock()
	if e, ok := s.collectors[h]; ok {
		return e.col
	}
	col := metrics.New(h.Engine, 10*time.Second, 6*time.Hour)
	s.collectors[h] = s.runCollector(col, func() { delete(s.collectors, h) })
	return col
}

// vmCollector samples the VMs of the host's machine while the app is open.
// Hosts of the same machine share it; tcp:// hosts have no VMs.
func (s *Server) vmCollector(h *hosts.Host) *metrics.Collector {
	if h.Kind == hosts.TCP {
		return nil
	}
	key := h.Machine()
	s.agentMu.Lock()
	defer s.agentMu.Unlock()
	if e, ok := s.vmCollectors[key]; ok {
		return e.col
	}
	col := metrics.NewVMs(func(ctx context.Context) ([]metrics.VMSample, error) {
		mh := s.machineHost(key)
		if mh == nil {
			return nil, errors.New("máquina removida")
		}
		us, err := vms.Usages(ctx, mh, "")
		if err != nil {
			return nil, err
		}
		out := make([]metrics.VMSample, 0, len(us))
		for _, u := range us {
			out = append(out, metrics.VMSample{Lab: u.Project, Name: u.Name, CPUSec: u.CPUSec, Mem: u.RSS, Disk: u.Disk})
		}
		return out, nil
	}, 10*time.Second, 6*time.Hour)
	s.vmCollectors[key] = s.runCollector(col, func() { delete(s.vmCollectors, key) })
	return col
}

// machineHost is a connected host of the machine that can run commands.
func (s *Server) machineHost(key string) *hosts.Host {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var idle *hosts.Host
	for _, x := range s.hosts {
		if x.Kind == hosts.TCP || x.Machine() != key {
			continue
		}
		if x.Wanted() {
			return x
		}
		if idle == nil {
			idle = x
		}
	}
	return idle
}

// runCollector starts col and stops it when nobody has looked for a while;
// forget runs with agentMu held.
func (s *Server) runCollector(col *metrics.Collector, forget func()) collectorEntry {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { defer applog.Recover("server/agent.go:414"); col.Run(ctx) }()
	go func() {
		defer applog.Recover("server/agent.go:415")
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if col.Idle() > 15*time.Minute {
					s.agentMu.Lock()
					forget()
					s.agentMu.Unlock()
					cancel()
					return
				}
			}
		}
	}()
	return collectorEntry{col: col, cancel: cancel}
}

// watchLab streams an event whenever the lab's compose files change, using
// the agent's inotify. Without the agent it answers 404 and the UI polls.
func (s *Server) watchLab(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	av := s.agentState(r.Context(), h, false)
	if av == nil || av.State != "running" {
		writeErr(w, http.StatusNotFound, errors.New("sem agente neste host"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	l, err := s.findLab(ctx, h, r.PathValue("lab"))
	var p string
	if err == nil {
		p, err = s.labFile(ctx, h, l)
	}
	cancel()
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	paths := append([]string{p}, l.Files...)
	send, ok := startSSE(w)
	if !ok {
		return
	}
	send("ready", "{}")
	changes := make(chan string, 8)
	errc := make(chan error, 1)
	go func() {
		defer applog.Recover("server/agent.go:463")
		errc <- h.Agent.Watch(r.Context(), paths, func(p string) {
			select {
			case changes <- p:
			default:
			}
		})
	}()
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case p := <-changes:
			send("change", strconv.Quote(p))
		case <-errc:
			send("end", "{}")
			return
		case <-ping.C:
			send("ping", "{}")
		}
	}
}
