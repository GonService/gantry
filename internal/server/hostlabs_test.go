package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"gantry/internal/config"
	"gantry/internal/hosts"
	"gantry/internal/labs"
)

// TestAdoptHostLabs: projects with VMs made by another Gantry on the machine
// show up here, with the compose files from the machine's own Gantry.
func TestAdoptHostLabs(t *testing.T) {
	state, home, work := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("HOME", home)
	root := filepath.Join(state, "gantry", "vms")
	for _, vm := range []string{"multicast/PE1", "multicast/PE2", "sem-arquivo/R1", "conhecido/R1"} {
		os.MkdirAll(filepath.Join(root, vm), 0o755)
		os.WriteFile(filepath.Join(root, vm, "vm.env"), []byte("ssh=\n"), 0o644)
	}
	compose := filepath.Join(work, "Área de trabalho", "LAB3", "compose.yaml")
	os.MkdirAll(filepath.Dir(compose), 0o755)
	os.WriteFile(compose, []byte("name: multicast\n"), 0o644)
	// The machine's Gantry knows "multicast" here (local) and on another
	// machine, where the file is not on this one.
	other := map[string]map[string]labs.Known{
		"FGon":  {"multicast": {Dir: "/home/outro/LAB_4-B", Files: []string{"/home/outro/LAB_4-B/compose.yaml"}}},
		"Local": {"multicast": {Dir: filepath.Dir(compose), Files: []string{compose}, VMs: []string{"PE1", "PE2", "Host1"}, Used: 42}},
	}
	b, _ := json.Marshal(other)
	os.MkdirAll(filepath.Join(home, ".config", "gantry"), 0o755)
	os.WriteFile(filepath.Join(home, ".config", "gantry", "state.json"), b, 0o644)

	s := &Server{store: labs.OpenStore(filepath.Join(t.TempDir(), "state.json"))}
	s.store.SetLab("Nilton", "conhecido", "/srv/c", []string{"/srv/c/compose.yaml"}, nil, []string{"R1"})
	h, err := hosts.New(config.Host{Name: "Nilton", URL: "unix:///tmp/nada.sock"})
	if err != nil {
		t.Fatal(err)
	}
	s.adoptHostLabs(context.Background(), h)
	k := s.store.Known("Nilton")
	m, ok := k["multicast"]
	if !ok {
		t.Fatalf("multicast não apareceu: %+v", k)
	}
	if m.Dir != filepath.Dir(compose) || !slices.Equal(m.Files, []string{compose}) || m.Used != 42 {
		t.Errorf("multicast: %+v", m)
	}
	if !slices.Equal(m.VMs, []string{"PE1", "PE2", "Host1"}) {
		t.Errorf("VMs: %v", m.VMs)
	}
	if _, ok := k["sem-arquivo"]; ok {
		t.Error("um projeto sem compose conhecido entrou")
	}
	if k["conhecido"].Dir != "/srv/c" {
		t.Errorf("mexeu no projeto que já era conhecido: %+v", k["conhecido"])
	}
	// project-set wrote the files: the machine says it, no state.json needed.
	os.Remove(filepath.Join(home, ".config", "gantry", "state.json"))
	os.WriteFile(filepath.Join(root, "sem-arquivo", "compose.files"), []byte(compose+"\n"), 0o644)
	hostLabsSeen.Lock()
	delete(hostLabsSeen.at, "Nilton")
	hostLabsSeen.Unlock()
	s.adoptHostLabs(context.Background(), h)
	if got := s.store.Known("Nilton")["sem-arquivo"]; !slices.Equal(got.Files, []string{compose}) || !slices.Equal(got.VMs, []string{"R1"}) {
		t.Errorf("sem-arquivo com compose.files: %+v", got)
	}
}
