package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func sh(t *testing.T, script string, args ...string) (string, error) {
	t.Helper()
	out, err := exec.Command("sh", append([]string{"-c", script, "sh"}, args...)...).CombinedOutput()
	return string(out), err
}

func TestTreeScripts(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services: {}\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, ".git", "objects"), 0o755)
	os.MkdirAll(filepath.Join(dir, "node_modules", "x"), 0o755)
	outside := t.TempDir()
	os.Symlink(outside, filepath.Join(dir, "fora"))

	op := func(args ...string) string {
		out, err := sh(t, treeOpScript, append([]string{dir}, args...)...)
		if err == nil {
			return "ok"
		}
		if i := strings.LastIndex(out, "gantry-arvore:"); i >= 0 {
			return out[i+len("gantry-arvore:") : i+len("gantry-arvore:")+1]
		}
		return out + err.Error()
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"arquivo", "nginx/conf.d/default.conf"}, "ok"},
		{[]string{"arquivo", "nginx/conf.d/default.conf"}, "5"},
		{[]string{"pasta", "dados/pg"}, "ok"},
		{[]string{"pasta", "dados"}, "5"},
		{[]string{"arquivo", "fora/x.conf"}, "4"},
		{[]string{"renomear", "dados/pg", "dados/postgres"}, "ok"},
		{[]string{"renomear", "nada", "outro"}, "6"},
		{[]string{"renomear", "nginx/conf.d/default.conf", "fora/x"}, "4"},
		{[]string{"excluir", "dados"}, "ok"},
		{[]string{"excluir", "dados"}, "6"},
		{[]string{"excluir", "fora"}, "ok"}, // the link goes, what it points to stays
	} {
		if got := op(c.args...); got != c.want {
			t.Errorf("%v: %s, queria %s", c.args, got, c.want)
		}
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("excluir o link apagou a pasta de fora")
	}
	if _, err := os.Stat(filepath.Join(dir, "nginx/conf.d/default.conf")); err != nil {
		t.Fatal(err)
	}

	out, err := sh(t, treeScript, dir)
	if err != nil {
		t.Fatal(out, err)
	}
	items, cut := parseTree(out)
	var got []string
	for _, it := range items {
		s := it.Path
		if it.Dir {
			s += "/"
		}
		got = append(got, s)
	}
	if want := "nginx/ nginx/conf.d/ nginx/conf.d/default.conf compose.yaml"; strings.Join(got, " ") != want || cut {
		t.Errorf("árvore: %q (cortada %v)", strings.Join(got, " "), cut)
	}
	if out, _ := sh(t, treeScript, filepath.Join(dir, "nada")); !strings.Contains(out, "gantry-arvore:3") {
		t.Errorf("pasta que não existe: %q", out)
	}

	os.WriteFile(filepath.Join(dir, "grande.log"), make([]byte, 5<<20), 0o644)
	for name, want := range map[string]string{"compose.yaml": "", "nginx/conf.d/default.conf": "", "nginx": "6", "grande.log": "7", "nada.yml": "6"} {
		out, err := sh(t, fileCheckScript, dir, name)
		got := ""
		if err != nil {
			got = out[strings.LastIndex(out, ":")+1:][:1]
		}
		if got != want {
			t.Errorf("fileCheck %s: %q, queria %q", name, got, want)
		}
	}
}

func TestSortTree(t *testing.T) {
	items := []treeItem{{Path: "b.yml"}, {Path: "a", Dir: true}, {Path: "a/z.conf"}, {Path: "a/sub", Dir: true}, {Path: "a/sub/x"}, {Path: "C.txt"}, {Path: ".env"}}
	sortTree(items)
	var got []string
	for _, it := range items {
		got = append(got, it.Path)
	}
	if strings.Join(got, " ") != "a a/sub a/sub/x a/z.conf .env b.yml C.txt" {
		t.Errorf("ordem: %v", got)
	}
}

func TestMissingBinds(t *testing.T) {
	binds := map[string][]string{
		"web":  {"./nginx/conf.d", "./nginx/nginx.conf", "./site", "~/x", "/etc/hosts"},
		"prom": {"./prometheus.yml", "/srv/p/rules", "./data"},
	}
	have := map[string]bool{"site": true}
	got := missingBinds([]string{"prom", "web"}, func(n string) []string { return binds[n] }, "/srv/p", have)
	var s []string
	for _, m := range got {
		k := "f"
		if m.Dir {
			k = "d"
		}
		s = append(s, k+":"+m.Path+":"+strings.Join(m.Services, ","))
	}
	if want := "d:data:prom d:nginx/conf.d:web f:nginx/nginx.conf:web f:prometheus.yml:prom d:rules:prom"; strings.Join(s, " ") != want {
		t.Errorf("faltando: %s", strings.Join(s, " "))
	}
	if got := missingBinds([]string{"web"}, func(n string) []string { return binds[n] }, "", nil); len(got) != 3 {
		t.Errorf("sem pasta (Criar projeto): %v", got)
	}
	for p, want := range map[string]bool{".env": true, ".htpasswd": true, "conf.d": false, "data": false, "x.yaml": true, "v1.2": false} {
		if bindIsFile(p) != want {
			t.Errorf("bindIsFile(%s) = %v", p, !want)
		}
	}
}

func TestCleanRel(t *testing.T) {
	for p, ok := range map[string]bool{"a/b.conf": true, "./a": true, "../x": false, "/etc/x": false, "": false, ".": false, "a/../../x": false, "a\nb": false} {
		if _, err := cleanRel(p); (err == nil) != ok {
			t.Errorf("cleanRel(%q): %v", p, err)
		}
	}
}
