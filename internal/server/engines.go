package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"gantry/internal/engine"
	"gantry/internal/hosts"
)

// machineEngines lists the engines on the same machine as h, h's own first:
// over SSH the other sockets on the server, locally the other local hosts.
func (s *Server) machineEngines(ctx context.Context, h *hosts.Host) []hosts.EngineRef {
	if h.Kind != hosts.Unix {
		return h.Engines(ctx)
	}
	out := h.Engines(ctx)
	seen := map[string]bool{out[0].Runtime: true}
	s.mu.RLock()
	list := append([]*hosts.Host(nil), s.hosts...)
	s.mu.RUnlock()
	for _, x := range list {
		if x == h || x.Kind != hosts.Unix {
			continue
		}
		rt := x.Runtime()
		if rt == "" {
			vctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			if v, err := x.Engine.Version(vctx); err == nil {
				rt = v.Runtime()
				x.SetRuntime(rt)
			}
			cancel()
		}
		if rt == "" || seen[rt] {
			continue
		}
		seen[rt] = true
		out = append(out, hosts.EngineRef{Runtime: rt, Socket: x.Socket(), Engine: x.Engine, Host: x})
	}
	return out
}

func (s *Server) engineFor(ctx context.Context, h *hosts.Host, rt string) (hosts.EngineRef, error) {
	if rt == "" || rt == h.Runtime() {
		return hosts.EngineRef{Runtime: h.Runtime(), Socket: h.Socket(), Engine: h.Engine, Host: h}, nil
	}
	for _, e := range s.machineEngines(ctx, h) {
		if e.Runtime == rt {
			return e, nil
		}
	}
	if h.Kind == hosts.TCP {
		return hosts.EngineRef{}, fmt.Errorf("hosts tcp:// só alcançam um engine; este projeto pede %s", rt)
	}
	return hosts.EngineRef{}, fmt.Errorf("não encontrei o %s nesta máquina (%s)", rt, h.Name)
}

// reqEngine is the engine a container route targets (?rt=podman picks a
// sibling engine, for containers of mixed labs).
func (s *Server) reqEngine(w http.ResponseWriter, r *http.Request, h *hosts.Host) (*engine.Client, bool) {
	e, err := s.engineFor(r.Context(), h, r.URL.Query().Get("rt"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return nil, false
	}
	return e.Engine, true
}

// listEngines shows the engines on the host's machine, so the other ones
// of a server (Podman next to Docker, containerd through the agent) can be
// added as hosts of their own.
func (s *Server) listEngines(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	type view struct {
		Runtime string `json:"runtime"`
		Socket  string `json:"socket"`
		Main    bool   `json:"main"`
		Host    string `json:"host,omitempty"` // Gantry host already showing it
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	h.RefreshSockets(ctx)
	s.mu.RLock()
	list := append([]*hosts.Host(nil), s.hosts...)
	s.mu.RUnlock()
	out := []view{}
	for i, e := range s.machineEngines(ctx, h) {
		v := view{Runtime: e.Runtime, Socket: e.Socket, Main: i == 0}
		if e.Host != nil && e.Host != h {
			v.Host = e.Host.Name
		}
		for _, x := range list {
			if x != h && x.Kind == h.Kind && x.URL == h.URL && x.ConfigSocket() == e.Socket {
				v.Host = x.Name
			}
		}
		out = append(out, v)
	}
	writeJSON(w, out)
}
