package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"gantry/internal/applog"
	"gantry/internal/config"
	"gantry/internal/engine"
	"gantry/internal/i18n"
	"gantry/internal/local"
)

// job is a long-running local command (install, start a VM) whose output the
// UI follows live.
type job struct {
	mu      sync.Mutex
	out     []byte
	done    bool
	err     string
	label   string
	key     string
	started time.Time
	cancel  context.CancelFunc
	// progress is the latest one-line status (image download), shown by the
	// UI above the log.
	progress string
}

func (j *job) Write(p []byte) (int, error) {
	j.mu.Lock()
	j.out = append(j.out, p...)
	j.mu.Unlock()
	return len(p), nil
}

func (j *job) snapshot(from int) ([]byte, bool, string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if from > len(j.out) {
		from = len(j.out)
	}
	return append([]byte(nil), j.out[from:]...), j.done, j.err
}

func (s *Server) localStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	st := local.Inspect(ctx)
	// A socket file can outlive its engine; ask it before calling it running.
	var wg sync.WaitGroup
	for i := range st.Engines {
		e := &st.Engines[i]
		if e.State != "running" || e.Socket == "" {
			continue
		}
		wg.Add(1)
		go func() {
			defer applog.Recover("server/local.go:65")
			defer wg.Done()
			sock := e.Socket
			c := engine.New(func(ctx context.Context) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sock)
			})
			pctx, pcancel := context.WithTimeout(ctx, 3*time.Second)
			defer pcancel()
			if err := c.Ping(pctx); err != nil {
				e.State = "unresponsive"
				e.Detail = "o socket existe, mas o engine não respondeu"
			}
		}()
	}
	wg.Wait()
	writeJSON(w, st)
}

func (s *Server) localAction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("action")
	cmdline := local.Command(id)
	if cmdline == "" {
		writeErr(w, http.StatusNotFound, errors.New("ação desconhecida"))
		return
	}
	jid, err := s.startJob("local", id, func(ctx context.Context, out io.Writer) error {
		_, _ = out.Write([]byte("$ " + cmdline + "\n\n"))
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", cmdline)
		cmd.Env = append(os.Environ(), "HOMEBREW_NO_AUTO_UPDATE=1", "HOMEBREW_NO_ENV_HINTS=1", "NONINTERACTIVE=1")
		cmd.Stdout = out
		cmd.Stderr = out
		err := cmd.Run()
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

// crlf turns bare newlines into CRLF so the output reads well in xterm.
type crlf struct{ w *job }

func (c crlf) Write(p []byte) (int, error) {
	n := len(p)
	if i18n.English() {
		p = []byte(i18n.T(string(p)))
	}
	out := make([]byte, 0, len(p)+8)
	for i, b := range p {
		if b == '\n' && (i == 0 || p[i-1] != '\r') {
			out = append(out, '\r')
		}
		out = append(out, b)
	}
	_, _ = c.w.Write(out)
	return n, nil
}

func (s *Server) jobStream(w http.ResponseWriter, r *http.Request) {
	s.jobMu.Lock()
	j := s.jobs[r.PathValue("id")]
	s.jobMu.Unlock()
	if j == nil {
		writeErr(w, http.StatusNotFound, errors.New("operação não encontrada"))
		return
	}
	send, ok := startSSE(w)
	if !ok {
		return
	}
	pos := 0
	lastProg := ""
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	for {
		chunk, done, errMsg := j.snapshot(pos)
		j.mu.Lock()
		prog := j.progress
		j.mu.Unlock()
		if prog != lastProg {
			lastProg = prog
			send("progress", prog)
		}
		if len(chunk) > 0 {
			pos += len(chunk)
			send("", base64.StdEncoding.EncodeToString(chunk))
		}
		if done {
			b, _ := json.Marshal(map[string]any{"ok": errMsg == "", "error": errMsg})
			send("end", string(b))
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-t.C:
		}
	}
}
