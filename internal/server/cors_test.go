package server

import (
	"net/http"
	"net/http/httptest"
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
