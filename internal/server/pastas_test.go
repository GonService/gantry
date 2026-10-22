package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHostFolders: the in-app folder picker lists a folder (hidden ones
// too), shows the nearest existing parent of a path not created yet, and
// creates folders.
func TestHostFolders(t *testing.T) {
	e := newEnv(t)
	base := filepath.Join(e.dir, "pastas")
	os.MkdirAll(filepath.Join(base, "labs"), 0o755)
	os.MkdirAll(filepath.Join(base, ".oculta"), 0o755)
	os.WriteFile(filepath.Join(base, "arquivo.txt"), nil, 0o644)
	var l folderList
	if code := e.call("GET", "/hosts/fake/pastas?path="+base, nil, &l); code != 200 {
		t.Fatalf("status %d", code)
	}
	real, _ := filepath.EvalSymlinks(base)
	if l.Path != real || strings.Join(l.Dirs, ",") != ".oculta,labs" || !l.Writable || l.Parent != filepath.Dir(real) {
		t.Fatalf("%+v", l)
	}
	want := filepath.Join(base, "labs", "novo", "fundo")
	if code := e.call("GET", "/hosts/fake/pastas?path="+want, nil, &l); code != 200 || l.Path != filepath.Join(real, "labs") || l.Asked != want {
		t.Fatalf("%d %+v", code, l)
	}
	if code := e.call("POST", "/hosts/fake/pastas", map[string]string{"path": want}, &l); code != 200 || l.Path != filepath.Join(real, "labs", "novo", "fundo") {
		t.Fatalf("mkdir %d %+v", code, l)
	}
	if st, err := os.Stat(want); err != nil || !st.IsDir() {
		t.Fatal("pasta não criada")
	}
	if code := e.call("GET", "/hosts/fake/pastas?path=~", nil, &l); code != 200 || l.Path == "" || l.Home == "" {
		t.Fatalf("home %d %+v", code, l)
	}
}
