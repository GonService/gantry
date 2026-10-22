package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gantry/internal/applog"
	"gantry/internal/config"
	"gantry/internal/hosts"
	"gantry/internal/metrics"
	"gantry/internal/obs"
)

type obsCache struct {
	at  time.Time
	eps map[string]obs.Endpoint
}

// obsEndpoints merges the configured endpoints with the ones found among
// the containers of the host's machine. The config wins.
func (s *Server) obsEndpoints(ctx context.Context, h *hosts.Host, fresh bool) map[string]obs.Endpoint {
	s.agentMu.Lock()
	c, ok := s.obsFound[h]
	s.agentMu.Unlock()
	found := c.eps
	if !ok || fresh || time.Since(c.at) > 30*time.Second {
		found = map[string]obs.Endpoint{}
		for _, e := range s.machineEngines(ctx, h) {
			cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			cs, err := e.Engine.Containers(cctx)
			cancel()
			if err != nil {
				continue
			}
			for k, v := range obs.Detect(cs) {
				if _, ok := found[k]; !ok {
					found[k] = v
				}
			}
		}
		s.agentMu.Lock()
		s.obsFound[h] = obsCache{at: time.Now(), eps: found}
		s.agentMu.Unlock()
	}
	out := map[string]obs.Endpoint{}
	for k, v := range found {
		out[k] = v
	}
	cfg := s.cfg.ObsFor(h.Name)
	for k, u := range map[string]string{"prometheus": cfg.Prometheus, "loki": cfg.Loki, "grafana": cfg.Grafana} {
		if u != "" {
			out[k] = obs.Endpoint{URL: u, Source: "config"}
		}
	}
	return out
}

func (s *Server) obsClient(ctx context.Context, h *hosts.Host, kind string) (*obs.Client, obs.Endpoint, error) {
	ep, ok := s.obsEndpoints(ctx, h, false)[kind]
	if !ok {
		return nil, ep, fmt.Errorf("nenhum %s encontrado para %s; gere um projeto com ele ou informe o endereço em Observabilidade", kind, h.Name)
	}
	token := ""
	if kind == "grafana" {
		token = s.cfg.ObsFor(h.Name).GrafanaToken
	}
	c, err := obs.NewClient(ep.URL, token, func(ctx context.Context, addr string) (net.Conn, error) {
		return h.DialTCP(ctx, addr)
	})
	return c, ep, err
}

type obsStatus struct {
	obs.Endpoint
	OK      bool   `json:"ok"`
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
}

// obsInfo reports each service and whether it answers.
func (s *Server) obsInfo(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	eps := s.obsEndpoints(ctx, h, r.URL.Query().Get("fresh") == "1")
	out := map[string]*obsStatus{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for kind, ep := range eps {
		wg.Add(1)
		go func() {
			defer applog.Recover("server/obs.go:102")
			defer wg.Done()
			st := &obsStatus{Endpoint: ep}
			c, _, err := s.obsClient(ctx, h, kind)
			if err == nil {
				cctx, ccancel := context.WithTimeout(ctx, 5*time.Second)
				switch kind {
				case "prometheus":
					st.Version, err = c.PromVersion(cctx)
				case "loki":
					err = c.LokiReady(cctx)
				case "grafana":
					st.Version, err = c.GrafanaVersion(cctx)
				}
				ccancel()
			}
			st.OK = err == nil
			if err != nil {
				st.Error = err.Error()
			}
			mu.Lock()
			out[kind] = st
			mu.Unlock()
		}()
	}
	wg.Wait()
	cfg := s.cfg.ObsFor(h.Name)
	cfg.GrafanaToken = strings.Repeat("•", min(len(cfg.GrafanaToken), 8))
	writeJSON(w, map[string]any{"services": out, "config": cfg})
}

func (s *Server) obsSet(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var o config.Obs
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&o); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	old := s.cfg.ObsFor(h.Name)
	if strings.Trim(o.GrafanaToken, "•") == "" && o.GrafanaToken != "" {
		o.GrafanaToken = old.GrafanaToken // the masked value came back unchanged
	}
	for _, u := range []string{o.Prometheus, o.Loki, o.Grafana} {
		if u == "" {
			continue
		}
		if _, err := obs.NewClient(u, "", nil); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	}
	o.Prometheus, o.Loki, o.Grafana = strings.TrimSpace(o.Prometheus), strings.TrimSpace(o.Loki), strings.TrimSpace(o.Grafana)
	if err := s.cfg.SetObs(h.Name, o); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// promQuery runs any PromQL over a time range, for the query box.
func (s *Server) promQuery(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeErr(w, http.StatusBadRequest, errors.New("informe a consulta"))
		return
	}
	rng, step := rangeStep(r)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	c, _, err := s.obsClient(ctx, h, "prometheus")
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	end := time.Now()
	res, err := c.QueryRange(ctx, q, end.Add(-rng), end, step)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if len(res) > 20 {
		res = res[:20]
	}
	writeJSON(w, map[string]any{"series": res, "step": int(step / time.Second)})
}

func rangeStep(r *http.Request) (time.Duration, time.Duration) {
	sec, _ := strconv.Atoi(r.URL.Query().Get("range"))
	if sec <= 0 || sec > 30*86400 {
		sec = 3600
	}
	rng := time.Duration(sec) * time.Second
	step := rng / 240
	if step < 15*time.Second {
		step = 15 * time.Second
	}
	return rng, step.Round(time.Second)
}

// promLab returns the CPU and memory of a lab's containers from Prometheus
// (cAdvisor metrics), in the same shape as the built-in collector.
func (s *Server) promLab(ctx context.Context, h *hosts.Host, lab string, rng, step time.Duration) (metrics.Result, error) {
	c, _, err := s.obsClient(ctx, h, "prometheus")
	if err != nil {
		return metrics.Result{}, err
	}
	end := time.Now()
	start := end.Add(-rng)
	sel := `container_label_com_docker_compose_project="` + lab + `"`
	if lab == "_avulsos" {
		sel = `name!="", container_label_com_docker_compose_project=""`
	}
	window := "1m"
	if step > time.Minute {
		window = strconv.Itoa(int(step/time.Second)*2) + "s"
	}
	cpuQ := `sum by (name) (rate(container_cpu_usage_seconds_total{` + sel + `}[` + window + `])) * 100`
	memQ := `sum by (name) (container_memory_working_set_bytes{` + sel + `})`
	cpu, err := c.QueryRange(ctx, cpuQ, start, end, step)
	if err != nil {
		return metrics.Result{}, err
	}
	mem, err := c.QueryRange(ctx, memQ, start, end, step)
	if err != nil {
		return metrics.Result{}, err
	}
	if len(cpu) == 0 && len(mem) == 0 {
		return metrics.Result{}, errors.New("o Prometheus não tem métricas de containers deste projeto. Ele precisa coletar do cAdvisor (veja examples/monitoramento)")
	}
	by := map[string]map[int64]*metrics.Point{}
	add := func(list []obs.PromSeries, set func(p *metrics.Point, v float64)) {
		for _, s := range list {
			name := s.Labels["name"]
			if by[name] == nil {
				by[name] = map[int64]*metrics.Point{}
			}
			for _, pt := range s.Points {
				t := int64(pt[0])
				p := by[name][t]
				if p == nil {
					p = &metrics.Point{T: t}
					by[name][t] = p
				}
				set(p, pt[1])
			}
		}
	}
	add(cpu, func(p *metrics.Point, v float64) { p.CPU = float32(v) })
	add(mem, func(p *metrics.Point, v float64) { p.Mem = uint64(v) })
	res := metrics.Result{Since: start.Unix(), Interval: int(step / time.Second), Step: int(step / time.Second), Series: []metrics.Series{}}
	for name, pts := range by {
		ser := metrics.Series{ID: name, Name: name, Lab: lab}
		for _, p := range pts {
			ser.Points = append(ser.Points, *p)
		}
		sort.Slice(ser.Points, func(i, j int) bool { return ser.Points[i].T < ser.Points[j].T })
		res.Series = append(res.Series, ser)
	}
	sort.Slice(res.Series, func(i, j int) bool { return res.Series[i].Name < res.Series[j].Name })
	return res, nil
}

// lokiLogs returns log history of a container from Loki.
func (s *Server) lokiLogs(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	q := r.URL.Query()
	sec, _ := strconv.Atoi(q.Get("range"))
	if sec <= 0 || sec > 30*86400 {
		sec = 3600
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	c, _, err := s.obsClient(ctx, h, "loki")
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	end := time.Now()
	start := end.Add(-time.Duration(sec) * time.Second)
	sel := q.Get("query")
	if sel == "" {
		labels, err := c.LokiLabels(ctx, start)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err)
			return
		}
		sel, err = obs.ContainerSelector(labels, q.Get("container"), q.Get("lab"), q.Get("service"))
		if err != nil {
			writeErr(w, http.StatusNotFound, err)
			return
		}
	}
	lines, err := c.LokiQuery(ctx, sel, start, end, 2000)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	if lines == nil {
		lines = []obs.LogLine{}
	}
	writeJSON(w, map[string]any{"query": sel, "lines": lines})
}

func (s *Server) grafanaDashboards(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	c, _, err := s.obsClient(ctx, h, "grafana")
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	ds, err := c.Dashboards(ctx)
	if err != nil {
		writeErr(w, http.StatusBadGateway, fmt.Errorf("Grafana: %v (se ele pede login, informe um token de service account)", err))
		return
	}
	writeJSON(w, ds)
}

// obsOpen opens a service page in the default browser. Services of SSH
// hosts get a local port forwarded to them first.
func (s *Server) obsOpen(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		Kind string `json:"kind"`
		Path string `json:"path"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	ep, ok := s.obsEndpoints(ctx, h, false)[req.Kind]
	if !ok {
		writeErr(w, http.StatusNotFound, errors.New("serviço não encontrado"))
		return
	}
	u, err := urlParse(ep.URL)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	addr := u.Host
	if u.Port() == "" {
		addr += map[bool]string{true: ":443", false: ":80"}[u.Scheme == "https"]
	}
	local, err := h.LocalAddr(ctx, addr)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	u.Host = local
	if strings.HasPrefix(req.Path, "/") {
		u.Path = strings.TrimRight(u.Path, "/") + req.Path
	}
	link := u.String()
	if err := openURL(link); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]string{"url": link})
}

func openURL(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	return cmd.Start()
}

func urlParse(s string) (*url.URL, error) {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("endereço inválido: %s", s)
	}
	return u, nil
}
