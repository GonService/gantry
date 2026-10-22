// Package applog is the log of the app: a file that rotates by size, the
// panics of background work (caught, written down, and the app goes on),
// and what a diagnostic needs from it.
package applog

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sync"
	"time"
)

const (
	maxSize  = 5 << 20 // a file rotates past this
	keepOlds = 3       // gantry.log.1 … .3
)

// Panic is one panic caught in background work.
type Panic struct {
	When  time.Time `json:"when"`
	Where string    `json:"where"`
	Value string    `json:"value"`
	Stack string    `json:"stack"`
}

var (
	mu     sync.Mutex
	file   *rotating
	panics []Panic
)

// Setup sends the log (slog and the standard log) to dir/gantry.log, and
// also to stderr when echo is set (the browser version, run from a
// terminal). Without a call, the log goes to stderr only.
func Setup(dir string, echo bool) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	r, err := openRotating(filepath.Join(dir, "gantry.log"))
	if err != nil {
		return "", err
	}
	mu.Lock()
	file = r
	mu.Unlock()
	var w io.Writer = r
	if echo {
		w = io.MultiWriter(r, os.Stderr)
	}
	w = maskWriter{w}
	slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})))
	log.SetFlags(0) // slog stamps the time
	return r.path, nil
}

// Path is the current log file ("" without Setup).
func Path() string {
	mu.Lock()
	defer mu.Unlock()
	if file == nil {
		return ""
	}
	return file.path
}

// Recover is deferred first in a goroutine: a panic there is written down
// and the app goes on, instead of closing the window.
//
//	go func() { defer applog.Recover("watch"); … }()
func Recover(where string) {
	if v := recover(); v != nil {
		record(where, v)
	}
}

// Safe runs fn and turns a panic into an error (for work whose caller waits
// for an answer, like an operation shown to the user).
func Safe(where string, fn func() error) (err error) {
	defer func() {
		if v := recover(); v != nil {
			record(where, v)
			err = fmt.Errorf("erro interno do Gantry em %s: %v (os detalhes estão no log)", where, v)
		}
	}()
	return fn()
}

// Record writes down a panic already recovered by the caller.
func Record(where string, v any) { record(where, v) }

func record(where string, v any) {
	p := Panic{When: time.Now(), Where: where, Value: fmt.Sprint(v), Stack: string(debug.Stack())}
	mu.Lock()
	panics = append(panics, p)
	if len(panics) > 20 {
		panics = panics[len(panics)-20:]
	}
	mu.Unlock()
	slog.Error("panic", "onde", where, "valor", p.Value, "pilha", p.Stack)
}

// Panics are the panics caught since the app opened, newest last.
func Panics() []Panic {
	mu.Lock()
	defer mu.Unlock()
	return append([]Panic(nil), panics...)
}

// Tail is the end of the log (current and previous file), with secrets
// masked.
func Tail(max int) string {
	mu.Lock()
	r := file
	mu.Unlock()
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var buf []byte
	for _, p := range []string{r.path + ".1", r.path} {
		if b, err := os.ReadFile(p); err == nil {
			buf = append(buf, b...)
		}
	}
	if len(buf) > max {
		buf = buf[len(buf)-max:]
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		}
	}
	return Mask(string(buf))
}

var secretRe = []*regexp.Regexp{
	// Authorization: Bearer xyz
	regexp.MustCompile(`(?i)(bearer\s+)[^\s"']+`),
	// "password": "x" (JSON) and password=x, senha: x, token=x, --password x
	regexp.MustCompile(`(?i)("(?:[a-z0-9_.-]*(?:token|senha|password|passwd|secret|apikey|api_key|authorization|credential)[a-z0-9_.-]*)"\s*:\s*)"(?:[^"\\]|\\.)*"`),
	regexp.MustCompile(`(?i)((?:^|[\s,;{(&?])-{0,2}[a-z0-9_.-]*(?:token|senha|password|passwd|secret|apikey|api_key|authorization|credential)[a-z0-9_.-]*\s*[=:]\s*)("[^"]*"|'[^']*'|[^\s,;&"']+)`),
	regexp.MustCompile(`(?i)((?:^|\s)--?(?:password|senha|token|secret|passphrase)\s+)\S+`),
	// ENV style names: AWS_SECRET_ACCESS_KEY=…, GITHUB_TOKEN=…, X_PRIVATE_KEY=…
	regexp.MustCompile(`\b([A-Z0-9_]*(?:KEY|SECRET|TOKEN|PASSWORD|PASSWD|SENHA)[A-Z0-9_]*\s*[=:]\s*)("[^"]*"|'[^']*'|\S+)`),
	// scheme://user:password@host
	regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^\s:/@]+:)[^\s@/]+(@)`),
	// ?t=… (the app's own token)
	regexp.MustCompile(`([?&](?:t|__gantry|c)=)[^&\s"]+`),
}

var keyRe = regexp.MustCompile(`(-----BEGIN [A-Z ]*PRIVATE KEY-----)[\s\S]*?(-----END [A-Z ]*PRIVATE KEY-----)`)

// Mask hides what looks like a secret (tokens, passwords, keys).
func Mask(s string) string {
	s = keyRe.ReplaceAllString(s, "$1 *** $2")
	for i, re := range secretRe {
		if i == 5 { // user:password@
			s = re.ReplaceAllString(s, "${1}***${2}")
			continue
		}
		s = re.ReplaceAllString(s, "${1}***")
	}
	return s
}

// maskWriter masks each record before it reaches the file (slog writes a
// whole record per call).
type maskWriter struct{ w io.Writer }

func (m maskWriter) Write(p []byte) (int, error) {
	if _, err := io.WriteString(m.w, Mask(string(p))); err != nil {
		return 0, err
	}
	return len(p), nil
}

var addrRe = []*regexp.Regexp{
	regexp.MustCompile(`\b[a-zA-Z][a-zA-Z0-9+.-]*://[^\s"'<>]+`),                                                // URLs
	regexp.MustCompile(`\b[A-Za-z0-9._-]+@[A-Za-z0-9.-]+\b`),                                                    // user@host
	regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}(?::\d+)?\b`),                                                  // IPv4
	regexp.MustCompile(`\b[0-9a-fA-F]{1,4}(?::[0-9a-fA-F]{1,4})*::(?:[0-9a-fA-F]{1,4}(?::[0-9a-fA-F]{1,4})*)?`), // IPv6 with ::
	regexp.MustCompile(`\b(?:[0-9a-fA-F]{1,4}:){3,7}[0-9a-fA-F]{1,4}\b`),                                        // IPv6
	regexp.MustCompile(`(?i)\bhost(?:name)?[= ]"?[A-Za-z0-9.-]+\.[A-Za-z]{2,}"?`),                               // host=example.com
}

// MaskAddrs hides addresses (URLs, user@host, IPs) besides the secrets, for
// text that leaves the computer.
func MaskAddrs(s string) string {
	s = Mask(s)
	for _, re := range addrRe {
		s = re.ReplaceAllString(s, "‹endereço›")
	}
	return s
}

// rotating is a log file that moves to .1 (and .1 to .2…) past maxSize.
type rotating struct {
	mu   sync.Mutex
	path string
	f    *os.File
	size int64
}

func openRotating(path string) (*rotating, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &rotating{path: path, f: f, size: st.Size()}, nil
}

func (r *rotating) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size+int64(len(p)) > maxSize && r.size > 0 {
		if err := r.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *rotating) rotate() error {
	r.f.Close()
	for i := keepOlds - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", r.path, i), fmt.Sprintf("%s.%d", r.path, i+1))
	}
	if err := os.Rename(r.path, r.path+".1"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	r.f, r.size = f, 0
	return nil
}
