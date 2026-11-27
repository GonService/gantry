package vms

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"gantry/internal/config"
	"gantry/internal/hosts"
)

// TestProjects: the projects with VMs on the machine, with the compose files
// project-set wrote, and the state.json of a Gantry of the machine.
func TestProjects(t *testing.T) {
	state, home := t.TempDir(), t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("HOME", home)
	root := filepath.Join(state, "gantry", "vms")
	for _, d := range []string{"lab-a/R1", "lab-a/R2", "lab-b/PC", "vazio"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	for _, f := range []string{"lab-a/R1/vm.env", "lab-a/R2/vm.env", "lab-b/PC/vm.env"} {
		os.WriteFile(filepath.Join(root, f), []byte("ssh=\n"), 0o644)
	}
	os.WriteFile(filepath.Join(root, "vazio", "relay.map"), nil, 0o644) // VMs removed: not a project
	os.MkdirAll(filepath.Join(home, ".config", "gantry"), 0o755)
	os.WriteFile(filepath.Join(home, ".config", "gantry", "state.json"), []byte("{}"), 0o644)

	h, err := hosts.New(config.Host{Name: "x", URL: "unix:///tmp/nada.sock"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	files := []string{"/home/ana/Área de trabalho/lab a/compose.yaml", "/home/ana/Área de trabalho/lab a/extra.yaml"}
	if err := SetProjectFiles(ctx, h, "lab-a", files); err != nil {
		t.Fatal(err)
	}
	ps, states, err := Projects(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 2 || ps[0].Name != "lab-a" || ps[1].Name != "lab-b" {
		t.Fatalf("projetos: %+v", ps)
	}
	if !slices.Equal(ps[0].VMs, []string{"R1", "R2"}) || !slices.Equal(ps[0].Files, files) {
		t.Errorf("lab-a: %+v", ps[0])
	}
	if !slices.Equal(ps[1].VMs, []string{"PC"}) || len(ps[1].Files) != 0 {
		t.Errorf("lab-b: %+v", ps[1])
	}
	if len(states) != 1 || states[0] != filepath.Join(home, ".config", "gantry", "state.json") {
		t.Errorf("state.json: %v", states)
	}
}
