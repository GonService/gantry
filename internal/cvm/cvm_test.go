package cvm

import (
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScriptFollowsTheChoices(t *testing.T) {
	s := Default(12, 36<<30)
	s.Rosetta = true
	s.Podman = false
	s.Extra = "apt-get install -y htop\r\n"
	sc, sum := Script(s, "/Users/ana")
	for _, want := range []string{
		"#!/bin/bash", "mkfs.ext4 -q -L gantry-dados /dev/vdb", "home /Users/ana virtiofs",
		"/opt/rosetta/rosetta:CF", "download.docker.com", "9DC858229FC7DD38854AE2D88D81803C0EBFCD88",
		"vsock docker 2375 UNIX-CONNECT:/run/docker.sock", "vsock ssh 2222 TCP:127.0.0.1:22",
		"apt-get install -y htop", "passo \"Pronto\"",
	} {
		if !strings.Contains(sc, want) {
			t.Errorf("o script não tem %q", want)
		}
	}
	if strings.Contains(sc, "podman") {
		t.Error("Podman desmarcado, mas está no script")
	}
	if strings.Contains(sc, "\r") {
		t.Error("o script ficou com \\r")
	}
	if len(sum) != 64 || Sum(sc) != sum {
		t.Errorf("sha256 errado: %s", sum)
	}
	if _, sum2 := Script(s, "/Users/ana"); sum2 != sum {
		t.Error("o mesmo spec deu outro script")
	}
	s.QEMU = true
	if _, sum3 := Script(s, "/Users/ana"); sum3 == sum {
		t.Error("mudar uma escolha não mudou o script")
	}
}

func TestUserDataCarriesTheScript(t *testing.T) {
	s := Default(8, 16<<30)
	sc, _ := Script(s, "/Users/ana")
	ud := UserData(s, "ssh-ed25519 AAAA gantry@x\n", sc)
	if !strings.HasPrefix(ud, "#cloud-config\n") || !strings.Contains(ud, "- ssh-ed25519 AAAA gantry@x\n") {
		t.Fatalf("user-data:\n%s", ud)
	}
	i := strings.Index(ud, "content: ")
	b64 := strings.TrimSpace(strings.SplitN(ud[i+9:], "\n", 2)[0])
	got, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || string(got) != sc {
		t.Error("o script no user-data não é o revisado")
	}
}

func TestLeanSystem(t *testing.T) {
	s := Default(12, 36<<30)
	sc, _ := Script(s, "/Users/ana")
	for _, want := range []string{
		"vsock mem 2379 OPEN:/proc/meminfo,rdonly", "gantry-vsock-mem.service",
		"apt-daily.timer", "SystemMaxUse=64M", "OnCalendar=hourly", "touch /etc/cloud/cloud-init.disabled",
	} {
		if !strings.Contains(sc, want) {
			t.Errorf("o script não tem %q", want)
		}
	}
	if !strings.Contains(syncScript(s), "VSOCK-LISTEN:2379") {
		t.Error("syncScript sem a ponte da memória")
	}
	if mac("a") != mac("a") || mac("a") == mac("b") {
		t.Error("MAC deve ser estável e diferente por máquina")
	}
}

func TestCheck(t *testing.T) {
	s := Default(4, 8<<30)
	if err := s.Check(); err != nil {
		t.Fatal(err)
	}
	if s.MemoryMiB != 4096 || s.CPUs != 2 {
		t.Errorf("padrão num Mac pequeno: %d CPU %d MiB", s.CPUs, s.MemoryMiB)
	}
	for _, bad := range []func(*Spec){
		func(x *Spec) { x.Name = "Gantry VM" },
		func(x *Spec) { x.Docker, x.Podman = false, false },
		func(x *Spec) { x.DataGiB = 2 },
	} {
		c := s
		bad(&c)
		if c.Check() == nil {
			t.Errorf("aceitou %+v", c)
		}
	}
}

const sample1 = `==PS
    1 root                       9000 12000 /sbin/init
  612 root                       8000 120000 /usr/bin/dockerd -H fd://
 2210 root                       7000 210000 dynamips -T 2001 c7200
 2302 root                       6000 160000 /iol/i86bi_Linux-L2 1
   12 root                       9000     0 [kworker/0:1]
==STAT
1 (systemd) S 0 1 1 0 -1 4194560 1 1 0 0 10 5 0 0 20 0 1 0 1 1 1
612 (dockerd) S 1 612 612 0 -1 4194560 1 1 0 0 100 50 0 0 20 0 14 0 1 1 1
2210 (dynamips c7200) R 1 2210 2210 0 -1 4194560 1 1 0 0 1000 200 0 0 20 0 3 0 1 1 1
2302 (i86bi_Linux-L2) S 1 2302 2302 0 -1 4194560 1 1 0 0 300 100 0 0 20 0 2 0 1 1 1
==CG
/proc/1/cgroup:0::/init.scope
/proc/612/cgroup:0::/system.slice/docker.service
/proc/2210/cgroup:0::/machine.slice/libpod-7f3a7f3a7f3a7f3a7f3a7f3a.scope/container
/proc/2302/cgroup:0::/system.slice/docker-abcdefabcdefabcdef.scope
==EXE
lrwxrwxrwx 1 root root 0 Nov  8 10:00 /proc/2302/exe -> /opt/rosetta/rosetta
==CPU
cpu  1000 0 500 8000 0 0 0 0 0 0
cpu0 500 0 250 4000 0 0 0 0 0 0
cpu1 500 0 250 4000 0 0 0 0 0 0
==MEM
MemTotal:        8000000 kB
MemFree:         3000000 kB
MemAvailable:    5000000 kB
Buffers:          100000 kB
Cached:          1000000 kB
SwapTotal:       1000000 kB
SwapFree:        1000000 kB
==LOAD
0.42 0.38 0.30 2/300 999
==DISK
 254       0 vda 100 0 1000 0 50 0 2000 0 0 0 0
 254       1 vda1 100 0 1000 0 50 0 2000 0 0 0 0
 254      16 vdb 10 0 100 0 5 0 200 0 0 0 0
==NET
Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo:  999 1 0 0 0 0 0 0  999 1 0 0 0 0 0 0
  enp0s1: 10000 10 0 0 0 0 0 0 20000 20 0 0 0 0 0 0
`

func TestSampleRates(t *testing.T) {
	t0 := time.Unix(1000, 0)
	a := parseRaw(sample1, t0)
	s2 := strings.NewReplacer(
		"2210 (dynamips c7200) R 1 2210 2210 0 -1 4194560 1 1 0 0 1000 200", "2210 (dynamips c7200) R 1 2210 2210 0 -1 4194560 1 1 0 0 1150 250",
		"cpu  1000 0 500 8000", "cpu  1200 0 600 8600",
		"cpu0 500 0 250 4000", "cpu0 650 0 300 4300",
		"enp0s1: 10000 10 0 0 0 0 0 0 20000", "enp0s1: 30000 10 0 0 0 0 0 0 60000",
	).Replace(sample1)
	b := parseRaw(s2, t0.Add(2*time.Second))
	snap := build(a, b, map[string]string{"abcdefabcdefabcdef": "SW1 · lab"})
	if snap.First || len(snap.Cores) != 2 {
		t.Fatalf("snapshot: %+v", snap)
	}
	if snap.CPU != 33.3 { // (200+100) of 900
		t.Errorf("CPU total %.1f", snap.CPU)
	}
	if snap.NetIn != 10000 || snap.NetOut != 20000 {
		t.Errorf("rede %v %v", snap.NetIn, snap.NetOut)
	}
	if snap.MemUsed != 3000000*1024 || snap.Load != "0.42 0.38 0.30" {
		t.Errorf("memória/carga: %d %q", snap.MemUsed, snap.Load)
	}
	byPID := map[int]Proc{}
	for _, p := range snap.Procs {
		byPID[p.PID] = p
	}
	if _, ok := byPID[12]; ok {
		t.Error("thread do kernel na lista")
	}
	d := byPID[2210]
	if d.CPU != 100 || d.Kind != "container" || d.State != "R" || d.Cmd != "dynamips -T 2001 c7200" {
		t.Errorf("dynamips: %+v", d)
	}
	if sw := byPID[2302]; !sw.Rosetta || sw.Origin != "SW1 · lab" {
		t.Errorf("iol: %+v", sw)
	}
	if e := byPID[612]; e.Kind != "docker" || e.RSS != 120000*1024 {
		t.Errorf("dockerd: %+v", e)
	}
	if snap.Procs[0].PID != 2210 {
		t.Errorf("ordem: %d primeiro", snap.Procs[0].PID)
	}
}

func TestParseNames(t *testing.T) {
	n := parseNames("abcdefabcdefabcdef web-1 loja\n1234567890abcd db <no value>\n\n")
	if n["abcdefabcdefabcdef"] != "web-1 · loja" || n["1234567890abcd"] != "db" {
		t.Errorf("%v", n)
	}
}

func TestSharesAndContainerd(t *testing.T) {
	s := Default(12, 36<<30)
	s.Containerd = true
	s.Shares = []Share{{Path: "/Volumes/Dados/datasets", ReadOnly: true}}
	if err := s.Check(); err != nil {
		t.Fatal(err)
	}
	sc, _ := Script(s, "/Users/ana")
	for _, want := range []string{"share0 /Volumes/Dados/datasets virtiofs ro,nofail", "nerdctl-full-2.4.2-linux-$arch.tar.gz", nerdctlSums["arm64"], "sha256sum -c -", "rm -f /usr/local/lib/systemd/system/containerd.service", "makestep 1 -1"} {
		if !strings.Contains(sc, want) {
			t.Errorf("o script não tem %q", want)
		}
	}
	if !strings.Contains(syncScript(s), "share0 /Volumes/Dados/datasets virtiofs ro") {
		t.Error("syncScript sem a pasta")
	}
	bad := s
	bad.Shares = []Share{{Path: "relativo"}}
	if bad.Check() == nil {
		t.Error("aceitou caminho relativo")
	}
}

func TestIntegrationFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("DOCKER_CONFIG", "")
	s := Default(8, 16<<30)
	s.Containerd = true
	if err := Save(s); err != nil {
		t.Fatal(err)
	}
	yes := true
	if err := Apply(context.Background(), s.Name, Wanted{DockerContext: &yes, Path: &yes, Use: &yes}); err != nil {
		t.Fatal(err)
	}
	in := Inspect(s.Name)
	if in.DockerContext != "gantry" || !in.Path || in.CurrentCtx != "gantry" {
		t.Errorf("integração: %+v", in)
	}
	for _, bin := range []string{"docker", "podman", "nerdctl", "gantry-vm"} {
		b, err := os.ReadFile(filepath.Join(home, ".gantry", "bin", bin))
		if err != nil {
			t.Fatalf("faltou %s: %v", bin, err)
		}
		if !strings.Contains(string(b), "gantry@gantry-vm") {
			t.Errorf("%s:\n%s", bin, b)
		}
	}
	rc, _ := os.ReadFile(filepath.Join(home, ".zshrc"))
	if strings.Count(string(rc), rcStart) != 1 {
		t.Errorf("zshrc:\n%s", rc)
	}
	no := false
	if err := Apply(context.Background(), s.Name, Wanted{DockerContext: &no, Path: &no}); err != nil {
		t.Fatal(err)
	}
	in = Inspect(s.Name)
	if in.DockerContext != "" || in.Path {
		t.Errorf("não desfez: %+v", in)
	}
	if rc, _ := os.ReadFile(filepath.Join(home, ".zshrc")); strings.Contains(string(rc), "gantry") {
		t.Errorf("zshrc ficou com o bloco:\n%s", rc)
	}
}

func TestWatchPieces(t *testing.T) {
	for p, skip := range map[string]bool{
		"/Users/ana/Projects/web/src/App.tsx":        false,
		"/Users/ana/Library/Caches/x":                true,
		"/Users/ana/Projects/web/.git/index":         true,
		"/Users/ana/.gantry/machines/vm/console.log": true,
		"/Users/ana/Projects/.DS_Store":              true,
	} {
		if skipPath(p) != skip {
			t.Errorf("skipPath(%s) = %v", p, !skip)
		}
	}
	if q := shQuote("it's"); q != `'it'\''s'` {
		t.Errorf("shQuote: %s", q)
	}
	// The touch loop parses path<TAB>mtime, paths with spaces included.
	dir := t.TempDir()
	f := filepath.Join(dir, "a b.txt")
	if err := os.WriteFile(f, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", touchLoop)
	cmd.Stdin = strings.NewReader(f + "\t1700000000.250000000\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	fi, _ := os.Stat(f)
	if fi.ModTime().Unix() != 1700000000 {
		t.Errorf("mtime: %v", fi.ModTime())
	}
	if _, err := watchPaths(nil, nil); err == nil {
		t.Error("watchPaths sem caminhos deveria falhar")
	}
}

func TestFinderScript(t *testing.T) {
	s := Default(8, 16<<30)
	on := FinderScript(s, true)
	for _, want := range []string{"nfs-kernel-server", "/srv/gantry/gantry-vm-docker", "/srv/gantry/gantry-vm-podman", "mount --bind /var/lib/docker/volumes /srv/gantry/gantry-vm-docker", "(ro,sync,no_subtree_check,insecure,all_squash,anonuid=0,anongid=0,fsid=1)", "fsid=2", "exportfs -ra", "ip=$("} {
		if !strings.Contains(on, want) {
			t.Errorf("faltou %q:\n%s", want, on)
		}
	}
	s.FinderRW = true
	if !strings.Contains(FinderScript(s, true), "(rw,") {
		t.Error("escrita pedida e export ro")
	}
	off := FinderScript(s, false)
	if !strings.Contains(off, "rm -f /etc/exports.d/gantry.exports") || strings.Contains(off, "apt-get") {
		t.Errorf("off:\n%s", off)
	}
	for _, sc := range []string{on, off} {
		if out, err := exec.Command("bash", "-n", "-c", sc).CombinedOutput(); err != nil {
			t.Errorf("sintaxe: %v %s", err, out)
		}
	}
}

// The script runs with set -u: the vsock helper must not need its 4th
// argument (the meminfo bridge has no unit to wait for).
func TestVsockHelperOptionalUnit(t *testing.T) {
	sc, _ := Script(Default(8, 16<<30), "/Users/ana")
	if !strings.Contains(sc, "After=network.target ${4:-}") {
		t.Fatal("vsock() usa $4 sem valor padrão; com set -u o script para")
	}
}

// Each system gets its own script, and a custom one points the bridges at
// the sockets the user gave.
func TestSystems(t *testing.T) {
	s := Default(8, 16<<30)
	if s.System() != OSDebian || (Spec{}).System() != OSDebian {
		t.Fatal("o padrão (e as máquinas de antes) é o Debian")
	}
	s.OS = OSAlpine
	sc, _ := Script(s, "/Users/ana")
	for _, want := range []string{"#!/bin/sh", "apk add docker", "vsock ssh 2222 TCP:127.0.0.1:22 sshd", "#!/sbin/openrc-run", "OPEN:/proc/meminfo", "/etc/sudoers.d/gantry", "s/^gantry:![^:]*:/gantry:*:/"} {
		if !strings.Contains(sc, want) {
			t.Errorf("alpine sem %q", want)
		}
	}
	if strings.Contains(sc, "apt-get") || strings.Contains(sc, "systemctl") {
		t.Error("o script do alpine usa apt ou systemd")
	}
	if ud := UserData(s, "k", sc); !strings.Contains(ud, "groups: [wheel]") || !strings.Contains(ud, "shell: /bin/sh") {
		t.Errorf("user-data do alpine: %s", ud)
	}
	s.QEMU = true
	if s.Check() == nil {
		t.Error("alpine com QEMU devia ser recusado")
	}
	s.QEMU = false

	s.OS = OSCustom
	if s.Check() == nil {
		t.Error("sistema próprio sem imagem devia ser recusado")
	}
	s.Custom = &Custom{Image: "https://exemplo/meu.qcow2", DockerSock: "/var/run/docker.sock"}
	if err := s.Check(); err != nil {
		t.Fatal(err)
	}
	sc, _ = Script(s, "/Users/ana")
	for _, want := range []string{"UNIX-CONNECT:/var/run/docker.sock", "UNIX-CONNECT:/run/podman/podman.sock", "INIT=systemd", "INIT=openrc"} {
		if !strings.Contains(sc, want) {
			t.Errorf("custom sem %q", want)
		}
	}
	s.Custom.Sum = "xyz"
	if s.Check() == nil {
		t.Error("aceitou uma soma que não é hexadecimal")
	}
	s.Custom = &Custom{Image: "ftp://x"}
	if s.Check() == nil {
		t.Error("aceitou uma imagem que não é https nem caminho")
	}
}

// The Podman connection: listed from podman's JSON, and "use as default"
// adds it when missing and makes it the default.
func TestPodmanConnection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	bin := t.TempDir()
	logf := filepath.Join(bin, "log")
	script := `#!/bin/sh
echo "$*" >>` + logf + `
case "$*" in
  "system connection list --format json")
    if grep -q "connection default" ` + logf + `; then
      echo '[{"Name":"gantry","URI":"unix:///x","Default":true}]'
    else
      echo '[{"Name":"outra","URI":"ssh://x","Default":true}]'
    fi ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "podman"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	s := Default(4, 8<<30)
	if err := Save(s); err != nil {
		t.Fatal(err)
	}
	in := Inspect(s.Name)
	if !in.PodmanCLI || in.PodmanConn || in.PodmanDefault != "outra" || in.PodmanConnN != "gantry" {
		t.Fatalf("antes: %+v", in)
	}
	yes := true
	if err := Apply(context.Background(), s.Name, Wanted{PodmanUse: &yes}); err != nil {
		t.Fatal(err)
	}
	log, _ := os.ReadFile(logf)
	if !strings.Contains(string(log), "system connection add gantry unix://") || !strings.Contains(string(log), "system connection default gantry") {
		t.Fatalf("comandos: %s", log)
	}
	if in := Inspect(s.Name); in.PodmanDefault != "gantry" || !in.PodmanConn {
		t.Fatalf("depois: %+v", in)
	}
}
