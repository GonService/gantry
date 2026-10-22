package server

// The graphs from inside the VMs with metrics: true: the node_exporter of
// each one, read through the port the host forwards to it.

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"gantry/internal/hosts"
	"gantry/internal/vmmetrics"
	"gantry/internal/vms"
)

var (
	vmReadingsMu sync.Mutex
	vmReadings   = map[string]vmmetrics.Reading{} // host/lab/vm -> last reading
)

type vmMetricsItem struct {
	VM    string           `json:"vm"`
	Port  int              `json:"port"`
	Point *vmmetrics.Point `json:"point,omitempty"`
	Error string           `json:"error,omitempty"`
}

// scrapeVM reads the node_exporter on a port of the host.
func scrapeVM(ctx context.Context, h *hosts.Host, port int) (string, error) {
	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/metrics"
	return h.Run(ctx, "", []string{"sh", "-c", `curl -fsS --max-time 5 "$1" 2>/dev/null || wget -qO- -T 5 "$1"`, "sh", url}, nil)
}

func (s *Server) labVMMetrics(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	l, err := s.findLab(ctx, h, r.PathValue("lab"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	out := []vmMetricsItem{}
	if len(l.Files) == 0 {
		writeJSON(w, map[string]any{"vms": out})
		return
	}
	p, _, err := s.loadProject(ctx, h, l.Files)
	if err != nil {
		writeJSON(w, map[string]any{"vms": out})
		return
	}
	running := map[string]bool{}
	for _, v := range l.VMs {
		running[v.Name] = v.State == "running"
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, v := range p.VMs {
		if !v.Metrics {
			continue
		}
		it := vmMetricsItem{VM: v.Name, Port: vms.MetricsPort(l.ID, v)}
		if !running[v.Name] {
			it.Error = "parada"
			out = append(out, it)
			continue
		}
		wg.Add(1)
		go func(it vmMetricsItem) {
			defer wg.Done()
			body, err := scrapeVM(ctx, h, it.Port)
			if err != nil || !strings.Contains(body, "node_") {
				it.Error = "o node_exporter ainda não respondeu (o cloud-init instala no primeiro boot)"
			} else {
				cur := vmmetrics.Read(vmmetrics.Parse(strings.NewReader(body)), time.Now())
				key := h.Name + "/" + l.ID + "/" + it.VM
				vmReadingsMu.Lock()
				prev, ok := vmReadings[key]
				vmReadings[key] = cur
				vmReadingsMu.Unlock()
				var pp *vmmetrics.Reading
				if ok && cur.At.Sub(prev.At) < 5*time.Minute {
					pp = &prev
				}
				pt := vmmetrics.PointOf(pp, cur)
				it.Point = &pt
			}
			mu.Lock()
			out = append(out, it)
			mu.Unlock()
		}(it)
	}
	wg.Wait()
	writeJSON(w, map[string]any{"vms": out})
}
