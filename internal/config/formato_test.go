package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareNewFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gantry")
	p, err := Prepare(dir, "1.0.0")
	if err != nil || p.Found != Format || p.Migrated || p.Copy != "" {
		t.Fatalf("%+v %v", p, err)
	}
	var fi formatInfo
	b, _ := os.ReadFile(filepath.Join(dir, formatFile))
	if json.Unmarshal(b, &fi) != nil || fi.Version != Format || fi.App != "1.0.0" {
		t.Fatalf("%s", b)
	}
}

func TestPrepareMigratesAfterCopy(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"hosts":[]}`), 0o600)
	os.MkdirAll(filepath.Join(dir, "clusters"), 0o700)
	os.WriteFile(filepath.Join(dir, "clusters", "a.yaml"), []byte("kube"), 0o600)
	os.WriteFile(filepath.Join(dir, "gantry.log"), []byte("log"), 0o600)
	os.WriteFile(filepath.Join(dir, formatFile), []byte(`{"versao":1}`), 0o600)

	saved := migrations
	defer func() { migrations = saved }()
	// Pretend this build writes format 3.
	var ran []string
	migrations = []func(string) error{
		func(d string) error {
			ran = append(ran, "1→2")
			return os.WriteFile(filepath.Join(d, "config.json"), []byte(`{"v":2}`), 0o600)
		},
		func(d string) error { ran = append(ran, "2→3"); return nil },
	}
	restore := setFormat(3)
	defer restore()

	p, err := Prepare(dir, "2.0.0")
	if err != nil || !p.Migrated || p.Found != 1 || strings.Join(ran, ",") != "1→2,2→3" {
		t.Fatalf("%+v %v %v", p, err, ran)
	}
	// The copy has the old files (not the log).
	if b, _ := os.ReadFile(filepath.Join(p.Copy, "config.json")); string(b) != `{"hosts":[]}` {
		t.Fatalf("cópia: %s", b)
	}
	if b, _ := os.ReadFile(filepath.Join(p.Copy, "clusters", "a.yaml")); string(b) != "kube" {
		t.Fatalf("cópia do kubeconfig: %s", b)
	}
	if _, err := os.Stat(filepath.Join(p.Copy, "gantry.log")); err == nil {
		t.Fatal("o log não vai para a cópia")
	}
	// A failing migration stops and says where the copy is.
	os.WriteFile(filepath.Join(dir, formatFile), []byte(`{"versao":2}`), 0o600)
	migrations[1] = func(string) error { return errors.New("quebrou") }
	if _, err := Prepare(dir, "2.0.0"); err == nil || !strings.Contains(err.Error(), "copias") {
		t.Fatalf("erro: %v", err)
	}
}

func TestPrepareNewerFolderUntouched(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{}`), 0o600)
	os.WriteFile(filepath.Join(dir, formatFile), []byte(`{"versao":99,"app":"9.9.9"}`), 0o600)
	p, err := Prepare(dir, "1.0.0")
	if err != nil || !p.Newer {
		t.Fatalf("%+v %v", p, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, formatFile)); !strings.Contains(string(b), "99") {
		t.Fatalf("mexeu no formato: %s", b)
	}
}

func TestDailyCopiesKeepSeven(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{}`), 0o600)
	for _, d := range []string{"20260101", "20260102", "20260103", "20260104", "20260105", "20260106", "20260107", "20260108"} {
		os.MkdirAll(filepath.Join(dir, copiesDir, "diaria-"+d), 0o700)
	}
	if _, err := Prepare(dir, "1.0.0"); err != nil {
		t.Fatal(err)
	}
	cs := Copies(dir)
	if len(cs) != keepDaily || strings.Contains(strings.Join(cs, ","), "20260101") || strings.Contains(strings.Join(cs, ","), "20260102") {
		t.Fatalf("%v", cs)
	}
}
