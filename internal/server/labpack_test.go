package server

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestLabExtras: the equipment goes into the project package under
// .gantrylab/, next to the project files, and a package of the package
// leaves it out.
func TestLabExtras(t *testing.T) {
	proj, extras, dest := t.TempDir(), t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(proj, "compose.yaml"), []byte("name: x\nx-vms:\n  r1:\n    image: cisco-iol\n"), 0o644)
	os.MkdirAll(filepath.Join(proj, labPackDir), 0o755)
	os.WriteFile(filepath.Join(proj, labPackDir, "velho"), []byte("não vai"), 0o644)
	os.MkdirAll(filepath.Join(extras, "nvram"), 0o755)
	os.WriteFile(filepath.Join(extras, "nvram", "r1.tar"), []byte("nvram"), 0o600)
	os.WriteFile(filepath.Join(extras, "manifest.json"), []byte(`{"format":1,"nodes":[{"name":"r1","kind":"iol","nvram":true}]}`), 0o600)
	var base, full bytes.Buffer
	if err := tarDir(&base, proj, false); err != nil {
		t.Fatal(err)
	}
	if err := withLabExtras(&base, &full, extras); err != nil {
		t.Fatal(err)
	}
	if err := untar(&full, dest); err != nil {
		t.Fatal(err)
	}
	for f, want := range map[string]string{"compose.yaml": "name: x\nx-vms:\n  r1:\n    image: cisco-iol\n", labPackDir + "/nvram/r1.tar": "nvram"} {
		if b, err := os.ReadFile(filepath.Join(dest, f)); err != nil || string(b) != want {
			t.Errorf("%s: %q %v", f, b, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, labPackDir, "velho")); err == nil {
		t.Error("the .gantrylab of the project folder went into the package")
	}
	if _, err := os.Stat(filepath.Join(dest, labPackDir, "manifest.json")); err != nil {
		t.Error(err)
	}
}
