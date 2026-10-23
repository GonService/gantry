package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"gantry/internal/config"
	"gantry/internal/labs"
)

func TestDesktopOriginAuth(t *testing.T) {
	no := false
	cfg := &config.Config{Autodetect: &no}
	s := New(cfg, labs.OpenStore(t.TempDir()+"/s.json"), fstest.MapFS{}, true)
	s.AllowOrigin("wails://wails")
	h := s.Handler()

	do := func(method, url, origin, auth string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, url, nil)
		r.Host = "127.0.0.1:7777"
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if auth != "" {
			r.Header.Set("Authorization", "Bearer "+auth)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	if w := do(http.MethodOptions, "/api/hosts", "wails://wails", ""); w.Code != http.StatusNoContent || w.Header().Get("Access-Control-Allow-Origin") != "wails://wails" {
		t.Fatalf("preflight: %d %v", w.Code, w.Header())
	}
	if w := do(http.MethodGet, "/api/hosts", "wails://wails", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("sem token deveria negar: %d", w.Code)
	}
	if w := do(http.MethodGet, "/api/hosts", "wails://wails", s.Token()); w.Code != http.StatusOK {
		t.Fatalf("com token: %d %s", w.Code, w.Body)
	}
	if w := do(http.MethodGet, "/api/hosts?t="+s.Token(), "wails://wails", ""); w.Code != http.StatusOK {
		t.Fatalf("token na query: %d", w.Code)
	}
	if w := do(http.MethodPost, "/api/hosts", "http://evil.example", s.Token()); w.Code != http.StatusForbidden {
		t.Fatalf("origem estranha deveria ser barrada: %d", w.Code)
	}
	if w := do(http.MethodGet, "/api/hosts", "http://evil.example", s.Token()); w.Code != http.StatusUnauthorized || w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("origem estranha não pode usar o header: %d", w.Code)
	}
}

// On the network with HTTPS, the app's own pages (https://host) use the
// API; the same host over http is another origin.
func TestHTTPSOriginAndAccess(t *testing.T) {
	no := false
	cfg := &config.Config{Autodetect: &no}
	s := New(cfg, labs.OpenStore(t.TempDir()+"/s.json"), fstest.MapFS{}, false)
	s.SetAddr("127.0.0.1:1")
	s.SetAccess(Access{Exposed: true, TLS: true, Fingerprint: "AB:CD", Link: "https://192.0.2.10:7777"})
	h := s.Handler()
	do := func(method, origin string, tls bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/acesso", nil)
		r.Host = "192.0.2.10:7777"
		if tls {
			r = httptest.NewRequest(method, "https://192.0.2.10:7777/api/acesso", nil)
		}
		r.Header.Set("Origin", origin)
		r.Header.Set("Authorization", "Bearer "+s.Token())
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := do(http.MethodGet, "https://192.0.2.10:7777", true); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"tls":true`) {
		t.Fatalf("a própria página em HTTPS: %d %s", w.Code, w.Body)
	}
	if w := do(http.MethodGet, "http://192.0.2.10:7777", true); w.Code != http.StatusUnauthorized {
		t.Fatalf("http no lugar de https é outra origem: %d", w.Code)
	}
	if l := s.LoginLink(); !strings.HasPrefix(l, "https://192.0.2.10:7777/?c=") {
		t.Fatalf("link: %s", l)
	}
}
