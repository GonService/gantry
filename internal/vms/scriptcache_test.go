package vms

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gantry/internal/config"
	"gantry/internal/hosts"
)

// TestScriptCache: on a machine reached over a connection the script is
// written once and then only called (the copy is found by the hash of the
// script in its name), and written again when it goes missing.
func TestScriptCache(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	h, err := hosts.New(config.Host{Name: "x", URL: "unix:///tmp/nada.sock"})
	if err != nil {
		t.Fatal(err)
	}
	old := cacheScript
	cacheScript = func(*hosts.Host) bool { return true }
	defer func() { cacheScript = old }()
	f := filepath.Join(cache, "gantry", "vmctl-"+scriptID+".sh")
	out, err := Run(context.Background(), h, "ports-seen", map[string]string{"GV_PROJECT": "lab"})
	if err != nil || !strings.HasPrefix(out, "now ") {
		t.Fatalf("primeira chamada: %v %q", err, out)
	}
	if b, err := os.ReadFile(f); err != nil || string(b) != script {
		t.Fatalf("o script não ficou na máquina: %v", err)
	}
	// Second call: uses the copy there (changed here, it is not rewritten).
	os.WriteFile(f, []byte(script+"\n"), 0o644)
	if out, err := Run(context.Background(), h, "ports-seen", map[string]string{"GV_PROJECT": "lab"}); err != nil || !strings.HasPrefix(out, "now ") {
		t.Fatalf("segunda chamada: %v %q", err, out)
	}
	if b, _ := os.ReadFile(f); string(b) != script+"\n" {
		t.Fatal("regravou o script sem precisar")
	}
	os.Remove(f)
	if out, err := Run(context.Background(), h, "ports-seen", map[string]string{"GV_PROJECT": "lab"}); err != nil || strings.Contains(out, scriptMissing) {
		t.Fatalf("depois de apagado: %v %q", err, out)
	}
}
