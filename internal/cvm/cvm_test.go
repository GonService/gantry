package cvm

import (
	"encoding/base64"
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

func TestArgs(t *testing.T) {
	s := Default(12, 36<<30)
	s.Podman, s.Rosetta = false, true
	a := strings.Join(Args(s, "/Users/ana/.gantry/machines/gantry-vm", "/Users/ana", "arm64"), " ")
	for _, want := range []string{
		"--cpus 6", "--memory 8192", "virtio-blk,path=/Users/ana/.gantry/machines/gantry-vm/sistema.raw",
		"virtio-blk,path=/Users/ana/.gantry/machines/gantry-vm/dados.raw", "virtio-fs,sharedDir=/Users/ana,mountTag=home",
		"rosetta,mountTag=rosetta", "virtio-vsock,port=2375,socketURL=/Users/ana/.gantry/machines/gantry-vm/docker.sock,connect",
		"--restful-uri unix:///Users/ana/.gantry/machines/gantry-vm/rest.sock", "virtio-net,nat,mac=52:54:00:",
	} {
		if !strings.Contains(a, want) {
			t.Errorf("faltou %q em %s", want, a)
		}
	}
	if strings.Contains(a, "port=2376") {
		t.Error("socket do Podman sem Podman")
	}
	if strings.Contains(strings.Join(Args(s, "/d", "/h", "amd64"), " "), "rosetta") {
		t.Error("Rosetta num Mac Intel")
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
