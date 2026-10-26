package sysinfo

import (
	"os/exec"
	"runtime"
	"testing"
)

func TestScriptHere(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip()
	}
	out, err := exec.Command("sh", "-c", Script).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	i := Parse(string(out))
	if i.CPUs < 1 || i.MemTotal <= 0 || i.MemUsed <= 0 || len(i.Disks) == 0 || i.Disks[0].Total <= 0 || !i.HasCPU || i.Uptime <= 0 {
		t.Errorf("%+v\n%s", i, out)
	}
}

func TestParseMac(t *testing.T) {
	i := Parse("os=macOS 15.1\ncpus=10\nmem_total=17179869184\nmem_used=8589934592\ncpu_pct=12.5\nfs=100 40 /System/Volumes/Data\nfs=500 10 /Volumes/Meu Disco\n")
	if i.OS != "macOS 15.1" || i.CPUs != 10 || i.CPUPct != 12.5 || len(i.Disks) != 2 || i.Disks[0].Used != 40 || i.Disks[1].Path != "/Volumes/Meu Disco" {
		t.Errorf("%+v", i)
	}
}
