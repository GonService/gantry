package server

// The access of assistants (gantry mcp): a token of its own, written with
// the address of the API to mcp.json while the access is on; it reads a
// fixed set of endpoints and, only with writing on, calls a few actions.
// Secrets and Helm values never go out through it.

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"

	"gantry/internal/config"
	"gantry/internal/mcp"
)

type mcpCtxKey struct{}

// fromMCP tells whether a request came with the assistants' token.
func fromMCP(r *http.Request) bool {
	v, _ := r.Context().Value(mcpCtxKey{}).(bool)
	return v
}

func (s *Server) mcpDir() string { return filepath.Dir(s.cfg.Path()) }

// mcpRefresh writes or removes mcp.json as the settings say.
func (s *Server) mcpRefresh() {
	m := s.cfg.MCPSettings()
	s.mcpMu.Lock()
	defer s.mcpMu.Unlock()
	if !m.On || s.addr == "" || s.cfg.Path() == "" {
		s.mcpToken = ""
		_ = os.Remove(mcp.InfoFile(s.mcpDir()))
		return
	}
	if s.mcpToken == "" {
		s.mcpToken = randomID(24)
	}
	_ = mcp.WriteInfo(s.mcpDir(), mcp.Info{URL: "http://" + s.addr, Token: s.mcpToken, Write: m.Write})
}

func (s *Server) validMCP(t string) bool {
	s.mcpMu.Lock()
	tok := s.mcpToken
	s.mcpMu.Unlock()
	return t != "" && tok != "" && subtle.ConstantTimeCompare([]byte(t), []byte(tok)) == 1
}

var (
	mcpRead = []*regexp.Regexp{
		regexp.MustCompile(`^/api/hosts$`),
		regexp.MustCompile(`^/api/hosts/[^/]+/labs$`),
		regexp.MustCompile(`^/api/hosts/[^/]+/labs/[^/]+/(drift|topology|vm-metricas|estados)$`),
		regexp.MustCompile(`^/api/hosts/[^/]+/containers/[^/]+/logs$`),
		regexp.MustCompile(`^/api/clusters$`),
		regexp.MustCompile(`^/api/clusters/[^/]+/(visao|helm|busca)$`),
		regexp.MustCompile(`^/api/clusters/[^/]+/helm/[^/]+/[^/]+$`),
		regexp.MustCompile(`^/api/clusters/[^/]+/r/[^/]+$`),
		regexp.MustCompile(`^/api/clusters/[^/]+/r/[^/]+/[^/]+/[^/]+/detalhe$`),
		regexp.MustCompile(`^/api/clusters/[^/]+/pods/[^/]+/[^/]+/logs$`),
		regexp.MustCompile(`^/api/clusters/[^/]+/varios/[^/]+/[^/]+/[^/]+/pods$`),
	}
	mcpWrite = []*regexp.Regexp{
		regexp.MustCompile(`^/api/hosts/[^/]+/labs/[^/]+/(up|down|stop|restart)$`),
		regexp.MustCompile(`^/api/hosts/[^/]+/containers/[^/]+/(restart|start|stop)$`),
		regexp.MustCompile(`^/api/clusters/[^/]+/r/(deployments|statefulsets|daemonsets)/[^/]+/[^/]+/(escalar|reiniciar)$`),
	}
	mcpSecret = regexp.MustCompile(`^/api/clusters/[^/]+/r/secrets(/|$)`)
)

// mcpAllowed: what the assistants' token may call.
func (s *Server) mcpAllowed(r *http.Request) bool {
	p := r.URL.Path
	if mcpSecret.MatchString(p) {
		return false
	}
	list := mcpRead
	switch r.Method {
	case http.MethodGet, http.MethodHead:
	case http.MethodPost:
		if !s.cfg.MCPSettings().Write {
			return false
		}
		list = mcpWrite
	default:
		return false
	}
	for _, re := range list {
		if re.MatchString(p) {
			return true
		}
	}
	return false
}

func withMCP(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), mcpCtxKey{}, true))
}

func (s *Server) getMCP(w http.ResponseWriter, r *http.Request) {
	m := s.cfg.MCPSettings()
	exe, _ := os.Executable()
	writeJSON(w, map[string]any{"on": m.On, "write": m.Write, "command": exe, "file": mcp.InfoFile(s.mcpDir())})
}

func (s *Server) setMCP(w http.ResponseWriter, r *http.Request) {
	var req config.MCP
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if !req.On {
		req.Write = false
	}
	if err := s.cfg.SetMCP(req); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.mcpRefresh()
	s.getMCP(w, r)
}

// mcpRemove takes mcp.json away (the app closing).
func (s *Server) mcpRemove() {
	if s.cfg.Path() != "" {
		_ = os.Remove(mcp.InfoFile(s.mcpDir()))
	}
}
