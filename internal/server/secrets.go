package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"gantry/internal/composex"
	"gantry/internal/hosts"
	"gantry/internal/secrets"
)

// Project secrets kept out of the .env: the values live in the system
// keychain (see package secrets); the app remembers which variables of
// which project are there and hands them to compose when it runs.

var secretsMu sync.Mutex

func (s *Server) secretStore() secrets.Store {
	s.secOnce.Do(func() { s.sec = secrets.Open(s.configDir()) })
	return s.sec
}

func (s *Server) secretIndexFile() string {
	return filepath.Join(s.configDir(), "segredos-projetos.json")
}

// secretIndex: machine|lab -> variable names in the keychain.
func (s *Server) secretIndex() map[string][]string {
	m := map[string][]string{}
	if b, err := os.ReadFile(s.secretIndexFile()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func (s *Server) saveSecretIndex(m map[string][]string) error {
	b, _ := json.MarshalIndent(m, "", "  ")
	_ = os.MkdirAll(s.configDir(), 0o700)
	return os.WriteFile(s.secretIndexFile(), b, 0o600)
}

func secretAccount(h *hosts.Host, lab, key string) string { return h.Machine() + "/" + lab + "/" + key }

// labSecrets returns the project's variables kept in the keychain.
func (s *Server) labSecrets(h *hosts.Host, lab string) (map[string]string, error) {
	secretsMu.Lock()
	keys := s.secretIndex()[scheduleKey(h, lab)]
	secretsMu.Unlock()
	if len(keys) == 0 {
		return nil, nil
	}
	st := s.secretStore()
	out := map[string]string{}
	var missing []string
	for _, k := range keys {
		v, err := st.Get(secretAccount(h, lab, k))
		if err != nil {
			missing = append(missing, k)
			continue
		}
		out[k] = v
	}
	if len(missing) > 0 {
		return out, fmt.Errorf("faltam no %s: %s", st.Name(), strings.Join(missing, ", "))
	}
	return out, nil
}

// withSecrets adds the project's secrets to the commands run with ctx.
func (s *Server) withSecrets(ctx context.Context, h *hosts.Host, lab string) context.Context {
	env, _ := s.labSecrets(h, lab)
	return hosts.WithEnv(ctx, env)
}

var envLine = regexp.MustCompile(`^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=`)

// moveSecrets takes keys out of the project's .env into the keychain.
func (s *Server) moveSecrets(ctx context.Context, h *hosts.Host, lab, dir string, keys []string) ([]string, error) {
	envPath := path.Join(dir, ".env")
	b, err := h.ReadFile(ctx, envPath)
	if err != nil {
		return nil, fmt.Errorf("não consegui ler o .env: %v", err)
	}
	vals := composex.ParseEnv(b)
	want := map[string]bool{}
	for _, k := range keys {
		want[k] = true
	}
	st := s.secretStore()
	var moved []string
	for _, k := range keys {
		v, ok := vals[k]
		if !ok || v == "" {
			continue
		}
		if err := st.Set(secretAccount(h, lab, k), v); err != nil {
			if errors.Is(err, secrets.ErrValue) {
				continue // stays in the .env
			}
			return moved, err
		}
		moved = append(moved, k)
	}
	if len(moved) == 0 {
		return nil, nil
	}
	isMoved := map[string]bool{}
	for _, k := range moved {
		isMoved[k] = true
	}
	var out []string
	for _, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if m := envLine.FindStringSubmatch(l); m != nil && isMoved[m[1]] {
			out = append(out, "# "+m[1]+": no chaveiro do Gantry")
			continue
		}
		out = append(out, l)
	}
	if err := h.WriteFile(ctx, envPath, []byte(strings.Join(out, "\n")+"\n")); err != nil {
		return moved, err
	}
	secretsMu.Lock()
	idx := s.secretIndex()
	k := scheduleKey(h, lab)
	have := map[string]bool{}
	for _, x := range idx[k] {
		have[x] = true
	}
	for _, x := range moved {
		if !have[x] {
			idx[k] = append(idx[k], x)
		}
	}
	sort.Strings(idx[k])
	err = s.saveSecretIndex(idx)
	secretsMu.Unlock()
	return moved, err
}

// returnSecrets writes keys back into the .env and drops them from the
// keychain.
func (s *Server) returnSecrets(ctx context.Context, h *hosts.Host, lab, dir string, keys []string) error {
	env, _ := s.labSecrets(h, lab)
	envPath := path.Join(dir, ".env")
	b, err := readEnvFile(ctx, h, envPath)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	back := map[string]bool{}
	for _, k := range keys {
		v, ok := env[k]
		if !ok {
			continue
		}
		back[k] = true
		line := k + "=" + envQuote(v)
		replaced := false
		for i, l := range lines {
			if l == "# "+k+": no chaveiro do Gantry" {
				lines[i], replaced = line, true
			}
		}
		if !replaced {
			lines = append(lines, line)
		}
	}
	if err := h.WriteFile(ctx, envPath, []byte(strings.Join(lines, "\n")+"\n")); err != nil {
		return err
	}
	st := s.secretStore()
	secretsMu.Lock()
	defer secretsMu.Unlock()
	idx := s.secretIndex()
	k := scheduleKey(h, lab)
	var keep []string
	for _, x := range idx[k] {
		if back[x] {
			_ = st.Delete(secretAccount(h, lab, x))
			continue
		}
		keep = append(keep, x)
	}
	if len(keep) == 0 {
		delete(idx, k)
	} else {
		idx[k] = keep
	}
	return s.saveSecretIndex(idx)
}

func (s *Server) labSecretsView(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	l, err := s.findLab(ctx, h, r.PathValue("lab"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	secretsMu.Lock()
	stored := s.secretIndex()[scheduleKey(h, l.ID)]
	secretsMu.Unlock()
	if stored == nil {
		stored = []string{}
	}
	cands := []string{}
	if l.WorkingDir != "" {
		if b, err := h.ReadFile(ctx, path.Join(l.WorkingDir, ".env")); err == nil {
			for k, v := range composex.ParseEnv(b) {
				if v != "" && secrets.IsSecretName(k) {
					cands = append(cands, k)
				}
			}
		}
	}
	sort.Strings(cands)
	_, missErr := s.labSecrets(h, l.ID)
	resp := map[string]any{"store": s.secretStore().Name(), "stored": stored, "candidates": cands}
	if missErr != nil {
		resp["error"] = missErr.Error()
	}
	writeJSON(w, resp)
}

func (s *Server) labSecretsMove(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		Keys []string `json:"keys"`
		Back bool     `json:"back"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	l, err := s.findLab(ctx, h, r.PathValue("lab"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if l.WorkingDir == "" {
		writeErr(w, http.StatusBadRequest, errors.New("o projeto não tem pasta"))
		return
	}
	if req.Back {
		if err := s.returnSecrets(ctx, h, l.ID, l.WorkingDir, req.Keys); err != nil {
			writeErr(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
		return
	}
	moved, err := s.moveSecrets(ctx, h, l.ID, l.WorkingDir, req.Keys)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "moved": moved, "store": s.secretStore().Name()})
}

// storeSecrets puts values in the keychain for a project (a copy of one).
func (s *Server) storeSecrets(h *hosts.Host, lab string, env map[string]string) error {
	st := s.secretStore()
	secretsMu.Lock()
	defer secretsMu.Unlock()
	idx := s.secretIndex()
	k := scheduleKey(h, lab)
	have := map[string]bool{}
	for _, x := range idx[k] {
		have[x] = true
	}
	for name, v := range env {
		if err := st.Set(secretAccount(h, lab, name), v); err != nil {
			return err
		}
		if !have[name] {
			idx[k] = append(idx[k], name)
		}
	}
	sort.Strings(idx[k])
	return s.saveSecretIndex(idx)
}

// readEnvFile reads a .env to rewrite it: a missing file is empty, but any
// other error stops the caller (rewriting would lose the other variables).
func readEnvFile(ctx context.Context, h *hosts.Host, p string) ([]byte, error) {
	b, err := h.ReadFile(ctx, p)
	if err == nil {
		return b, nil
	}
	// Only "no such file" counts as empty: with the connection down, an
	// existence check would also say no.
	if errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "No such file") {
		return nil, nil
	}
	return nil, fmt.Errorf("não consegui ler %s: %v", p, err)
}

// envQuote writes a value for a compose .env as it is: single quotes keep
// everything literal ($ included); a value with a single quote goes in
// double quotes with \, " and $ escaped.
func envQuote(v string) string {
	if v != "" && !strings.ContainsAny(v, " #\"'$\\`\t") {
		return v
	}
	if !strings.Contains(v, "'") {
		return "'" + v + "'"
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", "$$")
	return `"` + r.Replace(v) + `"`
}

// setEnvVar sets one variable in the project's .env, keeping the rest.
func setEnvVar(ctx context.Context, h *hosts.Host, dir, key, val string) error {
	envPath := path.Join(dir, ".env")
	b, err := readEnvFile(ctx, h, envPath)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	set := false
	for i, ln := range lines {
		if m := envLine.FindStringSubmatch(ln); m != nil && m[1] == key {
			lines[i], set = key+"="+envQuote(val), true
		}
	}
	if !set {
		lines = append(lines, key+"="+envQuote(val))
	}
	return h.WriteFile(ctx, envPath, []byte(strings.Join(lines, "\n")+"\n"))
}
