package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// The agent samples the VMs of the machine with vmctl, like the app does.
func TestAgentVMs(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	vm := filepath.Join(state, "gantry", "vms", "web", "box")
	_ = os.MkdirAll(vm, 0o755)
	busy := exec.Command("sh", "-c", "while :; do :; done")
	if err := busy.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = busy.Process.Kill(); _ = busy.Wait() })
	_ = os.WriteFile(filepath.Join(vm, "qemu.pid"), []byte(strconv.Itoa(busy.Process.Pid)+"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(vm, "vm.env"), []byte("ssh=22222\nports=\nimage=debian-12\ncpus=1\nmem=512\naccel=kvm\nvnc=\n"), 0o644)
	_ = os.WriteFile(filepath.Join(vm, "disk.qcow2"), make([]byte, 64<<10), 0o644)

	a := New(Options{Interval: time.Hour})
	ctx := context.Background()
	a.col.Sample(ctx)
	time.Sleep(1200 * time.Millisecond)
	a.col.Sample(ctx)
	res := a.col.Query(0, "web", 10)
	for _, s := range res.Series {
		if s.Kind == "vm" && s.Name == "box" && len(s.Points) > 0 && s.Points[len(s.Points)-1].CPU > 20 {
			return
		}
	}
	t.Fatalf("a VM não apareceu nas métricas do agente: %+v", res.Series)
}
