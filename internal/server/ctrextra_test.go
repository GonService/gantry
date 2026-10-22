package server

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gantry/internal/engine"
)

func TestParseListing(t *testing.T) {
	out := "regular file|12|1700000000|b.txt\ndirectory|4096|1700000001|etc\nsymbolic link|7|1700000002|lib\nregular file|1|1|nome|com|barra\n"
	got := parseListing(out)
	if len(got) != 4 || got[0].Name != "etc" || got[0].Type != "dir" {
		t.Fatalf("pastas primeiro: %+v", got)
	}
	if got[3].Name != "nome|com|barra" || got[1].Type != "file" || got[2].Type != "link" {
		t.Fatalf("%+v", got)
	}
	sh := parseListing("directory|-1|0|bin\nregular file|-1|0|x\n")
	if sh[0].Type != "dir" || sh[0].Size != -1 || sh[1].Type != "file" {
		t.Fatalf("sem stat: %+v", sh)
	}
}

func TestLineFilter(t *testing.T) {
	var b bytes.Buffer
	f := newLineFilter(&b, "ERRO")
	f.Write([]byte("ok 1\nerro gra"))
	f.Write([]byte("ve\nok 2\nOutro Erro\n"))
	if b.String() != "erro grave\nOutro Erro\n" {
		t.Fatalf("%q", b.String())
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"512m": 512 << 20, "2g": 2 << 30, "1.5G": 3 << 29, "1024": 1024, "256MiB": 256 << 20} {
		if v, err := parseSize(in); err != nil || v != want {
			t.Errorf("%s: %d %v", in, v, err)
		}
	}
	if _, err := parseSize("muito"); err == nil {
		t.Error("aceitou lixo")
	}
}

func TestArchiveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "conf.ini")
	os.WriteFile(src, []byte("a=1\n"), 0o640)
	st, _ := os.Stat(src)
	var buf bytes.Buffer
	if err := tarPath(&buf, src, "conf.ini", st); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "baixado.ini")
	n, kind, err := saveArchive(bytes.NewReader(buf.Bytes()), dest)
	if err != nil || kind != "file" || n != 4 {
		t.Fatalf("%d %s %v", n, kind, err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "a=1\n" {
		t.Fatalf("%q", b)
	}
	// A folder stays a tar.
	sub := filepath.Join(dir, "pasta")
	os.MkdirAll(filepath.Join(sub, "x"), 0o755)
	os.WriteFile(filepath.Join(sub, "x", "y.txt"), []byte("y"), 0o644)
	st, _ = os.Stat(sub)
	buf.Reset()
	if err := tarPath(&buf, sub, "pasta", st); err != nil {
		t.Fatal(err)
	}
	dest = filepath.Join(dir, "pasta.tar")
	if _, kind, err := saveArchive(bytes.NewReader(buf.Bytes()), dest); err != nil || kind != "tar" {
		t.Fatalf("%s %v", kind, err)
	}
	f, _ := os.Open(dest)
	defer f.Close()
	tr := tar.NewReader(f)
	var names []string
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		names = append(names, h.Name)
	}
	if len(names) != 3 || names[2] != "pasta/x/y.txt" {
		t.Fatalf("%v", names)
	}
}

func TestRegistryNames(t *testing.T) {
	for in, want := range map[string]string{"postgres:16": "docker.io", "bitnami/redis": "docker.io", "ghcr.io/a/b:1": "ghcr.io", "localhost/x": "localhost", "reg.local:5000/app": "reg.local:5000"} {
		if got := engine.RegistryOf(in); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
	if normRegistry("https://index.docker.io/v1/") != "docker.io" || normRegistry(" https://ghcr.io/ ") != "ghcr.io" {
		t.Error("normRegistry")
	}
}

func TestVersionNewer(t *testing.T) {
	cases := []struct {
		l, c string
		want bool
	}{{"v0.24.0", "0.23.0", true}, {"0.23.0", "0.23.0", false}, {"0.23.1", "0.23", true}, {"0.22.9", "0.23.0", false}, {"1.0.0", "dev", false}, {"v0.24.0-rc1", "0.23.5", true}}
	for _, c := range cases {
		if got := versionNewer(c.l, c.c); got != c.want {
			t.Errorf("%s vs %s: %v", c.l, c.c, got)
		}
	}
}

func TestYAMLDirectChildren(t *testing.T) {
	src := "x-vms:\n    web:\n        image: a\n        ports:\n            - 80:80\n        userdata: |\n            ssh: keep\n    ports:\n        image: b\n"
	lines := strings.Split(src, "\n")
	s, e := findBlock(lines, "x-vms", "ports")
	if s != 7 || e != 9 {
		t.Fatalf("VM chamada ports: %d %d", s, e)
	}
	s, e = findBlock(lines, "x-vms", "web")
	if childIndent(lines, s, e) != 8 {
		t.Fatal("indentação dos campos")
	}
	out, _ := cloneBlock(src, "x-vms", "web", "web2", "ports", "ssh")
	if !strings.Contains(out, "web2:\n        image: a\n        userdata: |\n            ssh: keep\n") {
		t.Fatalf("%s", out)
	}
}

// The tap holds releases of several apps: the newest of Gantry is picked,
// skipping other apps and pre-releases.
func TestCheckLatestFromTap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"tag_name":"reader-v2.0.0","html_url":"r"},{"tag_name":"gantry-v9.1.0-rc1","prerelease":true},{"tag_name":"gantry-v9.0.0","html_url":"https://x/g9","body":"notas"},{"tag_name":"gantry-v8.0.0"}]`)
	}))
	defer srv.Close()
	t.Setenv("GANTRY_UPDATE_URL", srv.URL)
	old := Version
	Version = "8.5.0"
	defer func() { Version = old }()
	v := checkLatest(context.Background())
	if v.Latest != "9.0.0" || !v.Newer || v.URL != "https://x/g9" || v.Notes != "notas" || v.Error != "" {
		t.Fatalf("%+v", v)
	}
	// A single release (the private repository) still works.
	one := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"tag_name":"v9.2.0","html_url":"https://x/p"}`)
	}))
	defer one.Close()
	t.Setenv("GANTRY_UPDATE_URL", one.URL)
	if v := checkLatest(context.Background()); v.Latest != "9.2.0" {
		t.Fatalf("%+v", v)
	}
	none := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"tag_name":"reader-v2.0.0"}]`)
	}))
	defer none.Close()
	t.Setenv("GANTRY_UPDATE_URL", none.URL)
	if v := checkLatest(context.Background()); v.Error == "" || v.Newer {
		t.Fatalf("%+v", v)
	}
}
