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
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"gantry/internal/applog"
	"gantry/internal/config"
	"gantry/internal/hosts"
	"gantry/internal/local"
)

var started = time.Now()

// recoverPanics answers 500 to a request whose handler panicked, and
// writes the panic in the log (the window stays open).
func recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if v == http.ErrAbortHandler {
				panic(v)
			}
			applog.Record(r.Method+" "+r.URL.Path, v)
			func() {
				defer func() { _ = recover() }() // the answer may already be under way
				writeErr(w, http.StatusInternalServerError, fmt.Errorf("erro interno do Gantry: %v (os detalhes estão no log)", v))
			}()
		}()
		next.ServeHTTP(w, r)
	})
}

// diagnostic is a text to send along with a problem report: versions,
// machines and their state, engines of this computer, the panics caught
// and the end of the log. Addresses, users and secrets stay out.
func (s *Server) diagnostic(ctx context.Context) string {
	var b strings.Builder
	line := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	line("# Diagnóstico do Gantry")
	line("")
	line("- Versão: %s", Version)
	line("- Sistema: %s/%s, Go %s", runtime.GOOS, runtime.GOARCH, runtime.Version())
	line("- Gerado em: %s (aberto há %s)", time.Now().Format(time.RFC3339), time.Since(started).Round(time.Second))
	p := config.LastPrepared()
	line("- Formato da configuração: %d (encontrado %d)%s", p.Format, p.Found, map[bool]string{true: ", de um Gantry mais novo", false: ""}[p.Newer])
	line("- Cópias da configuração: %s", strings.Join(config.Copies(s.configDir()), ", "))
	line("")

	line("## Máquinas")
	s.mu.RLock()
	list := append([]*hosts.Host(nil), s.hosts...)
	s.mu.RUnlock()
	views := make([]hostView, len(list))
	var wg sync.WaitGroup
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for i, h := range list {
		wg.Add(1)
		go func() {
			defer applog.Recover("diagnóstico")
			defer wg.Done()
			views[i] = s.describe(dctx, h)
		}()
	}
	wg.Wait()
	for i, v := range views {
		st := "ok"
		switch {
		case v.Idle:
			st = "não aberta"
		case v.Connecting:
			st = "conectando"
		case !v.OK:
			st = "erro: " + firstLineOf(v.Error)
		}
		agent := ""
		if v.Agent != nil {
			agent = ", com agente"
		}
		line("- máquina %d: %s, %s %s%s — %s", i+1, v.Kind, v.Runtime, v.Version, agent, st)
	}
	if len(views) == 0 {
		line("- nenhuma")
	}
	line("")

	line("## Clusters")
	modes := map[string]int{}
	for _, c := range s.clusters() {
		modes[c.Source+"/"+c.Mode]++
	}
	keys := make([]string, 0, len(modes))
	for k := range modes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		line("- %s: %d", k, modes[k])
	}
	if len(keys) == 0 {
		line("- nenhum")
	}
	line("")

	line("## Engines deste computador")
	lctx, lcancel := context.WithTimeout(ctx, 10*time.Second)
	st := local.Inspect(lctx)
	lcancel()
	for _, e := range st.Engines {
		line("- %s: %s %s", e.Name, e.State, firstLineOf(e.Detail))
	}
	if len(st.Engines) == 0 {
		line("- nenhum")
	}
	line("")

	line("## Erros internos capturados")
	ps := applog.Panics()
	if len(ps) == 0 {
		line("- nenhum")
	}
	for _, p := range ps {
		line("### %s em %s: %s", p.When.Format("15:04:05"), p.Where, p.Value)
		line("```")
		line("%s", strings.TrimSpace(p.Stack))
		line("```")
	}
	line("")

	line("## Fim do log")
	line("```")
	line("%s", strings.TrimSpace(applog.Tail(200<<10)))
	line("```")
	out := applog.MaskAddrs(b.String())
	if home, err := os.UserHomeDir(); err == nil && len(home) > 1 {
		out = strings.ReplaceAll(out, home, "~")
	}
	return out
}

func firstLineOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

func (s *Server) getDiagnostic(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, s.diagnostic(r.Context()))
}

func (s *Server) saveDiagnostic(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dest string `json:"dest"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&req)
	if !filepath.IsAbs(req.Dest) {
		writeErr(w, http.StatusBadRequest, errors.New("escolha onde salvar"))
		return
	}
	text := s.diagnostic(r.Context())
	if err := os.WriteFile(req.Dest, []byte(text), 0o600); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	slog.Info("diagnóstico salvo", "arquivo", req.Dest)
	writeJSON(w, map[string]any{"path": req.Dest, "bytes": len(text)})
}

// openLogFolder shows the log folder in the file manager.
func (s *Server) openLogFolder(w http.ResponseWriter, r *http.Request) {
	p := applog.Path()
	if p == "" {
		writeErr(w, http.StatusNotFound, errors.New("o log não está num arquivo (versão de terminal)"))
		return
	}
	if err := openPath(filepath.Dir(p)); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]string{"path": p})
}

func openPath(p string) error {
	if runtime.GOOS == "windows" {
		return exec.Command("explorer", p).Start()
	}
	return openURL(p)
}
