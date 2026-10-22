package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"gantry/internal/hosts"
)

// SSH authentication prompts. ssh runs with SSH_ASKPASS pointing at this same
// binary; that helper posts the prompt here and waits while the UI asks the
// user (password, key passphrase, or "trust this host key?").

type prompt struct {
	ID     string `json:"id"`
	Host   string `json:"host"`
	Text   string `json:"text"`
	Kind   string `json:"kind"` // secret or confirm
	answer chan promptAnswer
}

type promptAnswer struct {
	value    string
	ok       bool
	remember bool
}

type prompts struct {
	mu      sync.Mutex
	pending []*prompt
	changed chan struct{}     // closed and replaced on every change
	cache   map[string]string // answers that led to a working connection
	trial   map[string]string // answers waiting for the connection result
}

func newPrompts() *prompts {
	return &prompts{changed: make(chan struct{}), cache: map[string]string{}, trial: map[string]string{}}
}

func (p *prompts) notify() {
	close(p.changed)
	p.changed = make(chan struct{})
}

func (p *prompts) list() ([]*prompt, chan struct{}) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := append([]*prompt{}, p.pending...)
	return out, p.changed
}

func (p *prompts) forget(host string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, m := range []map[string]string{p.cache, p.trial} {
		for k := range m {
			if strings.HasPrefix(k, host+"\x00") {
				delete(m, k)
			}
		}
	}
}

// commit keeps the answers of a connection that worked.
func (p *prompts) commit(host string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, v := range p.trial {
		if strings.HasPrefix(k, host+"\x00") {
			p.cache[k] = v
			delete(p.trial, k)
		}
	}
}

// SetAddr tells the server where it listens, so ssh's askpass helper can
// reach it. It also turns interactive ssh authentication on.
func (s *Server) SetAddr(addr string) {
	s.addr = addr
	defer s.mcpRefresh()
	exe, err := os.Executable()
	if err != nil {
		return
	}
	url := "http://" + addr + "/api/askpass"
	hosts.AskpassEnv = func(host string) []string {
		return []string{
			"SSH_ASKPASS=" + exe,
			"SSH_ASKPASS_REQUIRE=force",
			"DISPLAY=:0",
			"GANTRY_ASKPASS_URL=" + url,
			"GANTRY_ASKPASS_TOKEN=" + s.token,
			"GANTRY_ASKPASS_HOST=" + host,
		}
	}
	hosts.OnTunnelFailed = s.prompts.forget
	hosts.OnTunnelUp = s.prompts.commit
}

func promptKind(text string) string {
	t := strings.ToLower(text)
	if strings.Contains(t, "(yes/no") || strings.Contains(t, "continue connecting") {
		return "confirm"
	}
	return "secret"
}

func (s *Server) askpass(w http.ResponseWriter, r *http.Request) {
	var req struct{ Host, Prompt string }
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	p := &prompt{ID: randomID(9), Host: req.Host, Text: strings.TrimSpace(req.Prompt), answer: make(chan promptAnswer, 1)}
	p.Kind = promptKind(p.Text)
	key := p.Host + "\x00" + p.Text

	s.prompts.mu.Lock()
	if v, ok := s.prompts.cache[key]; ok && p.Kind == "secret" {
		s.prompts.mu.Unlock()
		writeJSON(w, map[string]any{"ok": true, "answer": v})
		return
	}
	s.prompts.pending = append(s.prompts.pending, p)
	s.prompts.notify()
	s.prompts.mu.Unlock()

	remove := func() {
		s.prompts.mu.Lock()
		for i, x := range s.prompts.pending {
			if x == p {
				s.prompts.pending = append(s.prompts.pending[:i], s.prompts.pending[i+1:]...)
				break
			}
		}
		s.prompts.notify()
		s.prompts.mu.Unlock()
	}

	select {
	case a := <-p.answer:
		remove()
		if a.ok && a.remember && p.Kind == "secret" {
			s.prompts.mu.Lock()
			s.prompts.trial[key] = a.value
			s.prompts.mu.Unlock()
		}
		writeJSON(w, map[string]any{"ok": a.ok, "answer": a.value})
	case <-time.After(3 * time.Minute):
		remove()
		writeJSON(w, map[string]any{"ok": false})
	case <-r.Context().Done():
		remove()
	}
}

func (s *Server) promptStream(w http.ResponseWriter, r *http.Request) {
	send, ok := startSSE(w)
	if !ok {
		return
	}
	for {
		list, changed := s.prompts.list()
		b, _ := json.Marshal(list)
		send("", string(b))
		select {
		case <-changed:
		case <-time.After(25 * time.Second):
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) answerPrompt(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Answer   string `json:"answer"`
		Cancel   bool   `json:"cancel"`
		Remember bool   `json:"remember"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	id := r.PathValue("id")
	s.prompts.mu.Lock()
	var p *prompt
	for _, x := range s.prompts.pending {
		if x.ID == id {
			p = x
		}
	}
	s.prompts.mu.Unlock()
	if p == nil {
		writeErr(w, http.StatusNotFound, errors.New("pedido de senha expirou"))
		return
	}
	a := promptAnswer{ok: !req.Cancel, value: req.Answer, remember: req.Remember}
	if p.Kind == "confirm" && a.ok {
		a.value = "yes"
	}
	select {
	case p.answer <- a:
	default:
	}
	if req.Cancel {
		// Drop any other question for the same host too.
		s.prompts.mu.Lock()
		for _, x := range s.prompts.pending {
			if x != p && x.Host == p.Host {
				select {
				case x.answer <- promptAnswer{}:
				default:
				}
			}
		}
		s.prompts.mu.Unlock()
		if h := s.host(p.Host); h != nil {
			h.CancelConnect()
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
