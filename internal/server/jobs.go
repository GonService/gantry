package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"gantry/internal/applog"
)

// errJobRunning is returned when the same key already has a job running.
var errJobRunning = errors.New("já existe uma operação em andamento para isso")

// startJob runs fn in the background, collecting its output for
// /jobs/{id}/stream. Jobs with the same key do not run at the same time
// (one operation per project, one agent install per host…), but different
// keys do.
func (s *Server) startJob(key, label string, fn func(ctx context.Context, out io.Writer) error) (string, error) {
	s.jobMu.Lock()
	for _, j := range s.jobs {
		j.mu.Lock()
		busy := !j.done && j.key == key
		j.mu.Unlock()
		if busy {
			s.jobMu.Unlock()
			return "", errJobRunning
		}
	}
	jid := randomID(9)
	// Generous: big images and disks over SSH take long; jobs can be canceled.
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	j := &job{label: label, key: key, started: time.Now(), cancel: cancel}
	s.jobs[jid] = j
	s.jobMu.Unlock()
	go func() {
		defer applog.Recover("server/jobs.go:36")
		defer cancel()
		slog.Info("operação", "id", jid, "o que", label)
		err := applog.Safe("operação "+label, func() error { return fn(ctx, crlf{j}) })
		if err != nil {
			slog.Warn("operação falhou", "id", jid, "o que", label, "duração", time.Since(j.started).Round(time.Millisecond), "erro", err)
		} else {
			slog.Info("operação concluída", "id", jid, "o que", label, "duração", time.Since(j.started).Round(time.Millisecond))
		}
		if err == nil && ctx.Err() == context.Canceled {
			err = errors.New("cancelado")
		}
		j.mu.Lock()
		j.done = true
		if err != nil {
			j.err = err.Error()
			if ctx.Err() == context.Canceled {
				j.err = "cancelado"
			}
		}
		j.mu.Unlock()
		time.AfterFunc(10*time.Minute, func() {
			s.jobMu.Lock()
			delete(s.jobs, jid)
			s.jobMu.Unlock()
		})
	}()
	return jid, nil
}

func (j *job) setProgress(p string) {
	j.mu.Lock()
	j.progress = p
	j.mu.Unlock()
}

// jobOf returns the job behind a writer handed to a job function.
func jobOf(w io.Writer) *job {
	if c, ok := w.(crlf); ok {
		return c.w
	}
	return nil
}

type jobView struct {
	ID       string `json:"id"`
	Key      string `json:"key"`
	Label    string `json:"label"`
	Started  int64  `json:"started"`
	Done     bool   `json:"done"`
	Error    string `json:"error,omitempty"`
	Progress string `json:"progress,omitempty"`
}

// listJobs lets the UI find operations still running after a reload.
func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	s.jobMu.Lock()
	out := []jobView{}
	for id, j := range s.jobs {
		j.mu.Lock()
		out = append(out, jobView{ID: id, Key: j.key, Label: j.label, Started: j.started.Unix(), Done: j.done, Error: j.err, Progress: j.progress})
		j.mu.Unlock()
	}
	s.jobMu.Unlock()
	sort.Slice(out, func(a, b int) bool { return out[a].Started > out[b].Started })
	writeJSON(w, out)
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	s.jobMu.Lock()
	j := s.jobs[r.PathValue("id")]
	s.jobMu.Unlock()
	if j == nil {
		writeErr(w, http.StatusNotFound, errors.New("operação não encontrada"))
		return
	}
	j.mu.Lock()
	done := j.done
	j.mu.Unlock()
	if !done && j.cancel != nil {
		_, _ = crlf{j}.Write([]byte("\n\x1b[33mcancelando…\x1b[0m\n"))
		j.cancel()
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// jobBusy says whether a job with this key is running.
func (s *Server) jobBusy(key string) bool {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	for _, j := range s.jobs {
		j.mu.Lock()
		busy := !j.done && j.key == key
		j.mu.Unlock()
		if busy {
			return true
		}
	}
	return false
}
