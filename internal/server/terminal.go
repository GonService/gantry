package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"gantry/internal/applog"
	"gantry/internal/hosts"
)

// ttySession is what a terminal talks to: an exec in a container or an
// SSH session in a VM.
type ttySession interface {
	io.ReadWriteCloser
	Resize(ctx context.Context, cols, rows int) error
}

// term is a shell inside a container or a VM. Output goes to the browser
// over SSE, keystrokes come back as POSTs.
type term struct {
	id      string
	ex      ttySession
	started atomic.Bool
	once    sync.Once
	s       *Server
}

func (t *term) close() {
	t.once.Do(func() {
		_ = t.ex.Close()
		t.s.termMu.Lock()
		delete(t.s.terms, t.id)
		t.s.termMu.Unlock()
	})
}

var shellCmd = []string{"/bin/sh", "-c", "if command -v bash >/dev/null 2>&1; then exec bash; else exec sh; fi"}

func (s *Server) newTerm(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	eng, ok := s.reqEngine(w, r, h)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	ex, err := eng.ExecTTY(ctx, r.PathValue("id"), shellCmd)
	if err != nil {
		writeErr(w, engineStatus(err), err)
		return
	}
	writeJSON(w, map[string]string{"id": s.addTerm(ex)})
}

// addTerm registers a session; it closes if nobody attaches in 30 s.
func (s *Server) addTerm(ex ttySession) string {
	t := &term{id: randomID(12), ex: ex, s: s}
	s.termMu.Lock()
	s.terms[t.id] = t
	s.termMu.Unlock()
	time.AfterFunc(30*time.Second, func() {
		if !t.started.Load() {
			t.close()
		}
	})
	return t.id
}

func (s *Server) getTerm(id string) *term {
	s.termMu.Lock()
	defer s.termMu.Unlock()
	return s.terms[id]
}

func (s *Server) termStream(w http.ResponseWriter, r *http.Request) {
	t := s.getTerm(r.PathValue("id"))
	if t == nil || !t.started.CompareAndSwap(false, true) {
		writeErr(w, http.StatusNotFound, errors.New("sessão encerrada"))
		return
	}
	defer t.close()
	send, ok := startSSE(w)
	if !ok {
		return
	}
	go func() {
		defer applog.Recover("server/terminal.go:91")
		<-r.Context().Done()
		t.close()
	}()
	buf := make([]byte, 32<<10)
	for {
		n, err := t.ex.Read(buf)
		if n > 0 {
			send("", base64.StdEncoding.EncodeToString(buf[:n]))
		}
		if err != nil {
			if r.Context().Err() == nil {
				send("exit", "{}")
			}
			return
		}
	}
}

func (s *Server) termInput(w http.ResponseWriter, r *http.Request) {
	t := s.getTerm(r.PathValue("id"))
	if t == nil {
		writeErr(w, http.StatusNotFound, errors.New("sessão encerrada"))
		return
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if _, err := t.ex.Write(b); err != nil {
		t.close()
		writeErr(w, http.StatusGone, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) termResize(w http.ResponseWriter, r *http.Request) {
	t := s.getTerm(r.PathValue("id"))
	if t == nil {
		writeErr(w, http.StatusNotFound, errors.New("sessão encerrada"))
		return
	}
	var sz struct{ Cols, Rows int }
	if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&sz); err != nil || sz.Cols <= 0 || sz.Rows <= 0 {
		writeErr(w, http.StatusBadRequest, errors.New("tamanho inválido"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := t.ex.Resize(ctx, sz.Cols, sz.Rows); err != nil {
		writeErr(w, engineStatus(err), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) termClose(w http.ResponseWriter, r *http.Request) {
	if t := s.getTerm(r.PathValue("id")); t != nil {
		t.close()
	}
	w.WriteHeader(http.StatusNoContent)
}
