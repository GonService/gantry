package applog

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRecoverAndSafe(t *testing.T) {
	dir := t.TempDir()
	if _, err := Setup(dir, false); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		// Done só depois de Recover ter escrito o panic no log.
		defer wg.Done()
		defer Recover("teste")
		var m map[string]int
		m["x"] = 1 // panics
	}()
	wg.Wait()
	if p := Panics(); len(p) == 0 || p[len(p)-1].Where != "teste" || !strings.Contains(p[len(p)-1].Stack, "applog_test.go") {
		t.Fatalf("%+v", p)
	}
	err := Safe("operação", func() error { panic("bum") })
	if err == nil || !strings.Contains(err.Error(), "bum") {
		t.Fatal(err)
	}
	if err := Safe("ok", func() error { return errors.New("normal") }); err.Error() != "normal" {
		t.Fatal(err)
	}
	slog.Info("conectando", "url", "http://127.0.0.1:7777/?t=abcdef123", "token", "segredo", "env", "DB_PASSWORD=naofica")
	raw, _ := os.ReadFile(Path())
	if strings.Contains(string(raw), "abcdef123") || strings.Contains(string(raw), "segredo") || strings.Contains(string(raw), "naofica") {
		t.Fatalf("o arquivo de log tem segredos:\n%s", raw)
	}
	tail := Tail(1 << 20)
	if !strings.Contains(tail, "onde=teste") || strings.Contains(tail, "abcdef123") || strings.Contains(tail, "segredo") {
		t.Fatalf("log:\n%s", tail)
	}
}

func TestRotation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "gantry.log")
	r, err := openRotating(p)
	if err != nil {
		t.Fatal(err)
	}
	line := []byte(strings.Repeat("x", 1<<20) + "\n")
	for i := 0; i < 23; i++ {
		if _, err := r.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range []string{"", ".1", ".2", ".3"} {
		if _, err := os.Stat(p + s); err != nil {
			t.Fatalf("falta %s: %v", s, err)
		}
	}
	if _, err := os.Stat(p + ".4"); err == nil {
		t.Fatal("guardou arquivos demais")
	}
	if st, _ := os.Stat(p); st.Size() > maxSize {
		t.Fatalf("tamanho %d", st.Size())
	}
}

func TestMask(t *testing.T) {
	in := "Authorization: Bearer abc.def password=hunter2 senha: \"x y\" -----BEGIN OPENSSH PRIVATE KEY-----\nAAA\n-----END OPENSSH PRIVATE KEY-----" +
		` {"password":"js1","db_token": "js2"} AWS_SECRET_ACCESS_KEY=env1 GITHUB_TOKEN=env2 --password cli1 postgres://u:url1@h/db mysql -psecret`
	out := Mask(in)
	for _, bad := range []string{"abc.def", "hunter2", "x y", "AAA", "js1", "js2", "env1", "env2", "cli1", "url1"} {
		if strings.Contains(out, bad) {
			t.Fatalf("%q ficou em %q", bad, out)
		}
	}
}

func TestMaskAddrs(t *testing.T) {
	in := "ssh: connect to host 10.1.2.3 port 22; ssh://ana@lab.local:2222 falhou; https://k8s.example.com:6443/api; fe80::1:2:3:4"
	out := MaskAddrs(in)
	for _, bad := range []string{"10.1.2.3", "ana@", "lab.local", "k8s.example.com", "fe80"} {
		if strings.Contains(out, bad) {
			t.Fatalf("%q ficou em %q", bad, out)
		}
	}
}
