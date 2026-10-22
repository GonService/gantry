package vms

import (
	"os"
	"strings"
	"testing"

	"gantry/internal/composex"
)

func TestUserDataAndEnv(t *testing.T) {
	p, err := composex.Parse([]byte(`
x-vms:
  web:
    memory: 2g
    ports: ["8081:80", "127.0.0.1:5353:53/udp"]
    packages: [nginx]
    run: ["echo ok > /var/www/html/ok.txt"]
    cloud-init: |
      timezone: America/Sao_Paulo
      packages: [curl]
`))
	if err != nil {
		t.Fatal(err)
	}
	v := p.VM("web")
	if v == nil || v.Memory != 2048 || v.Image != "debian-12" || v.CPUs != 1 {
		t.Fatalf("vm %+v", v)
	}
	ud, err := UserData(v)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"#cloud-config", "timezone: America/Sao_Paulo", "- curl", "- nginx", "__GANTRY_PUBKEY__", "hostname: web", "echo ok"} {
		if !strings.Contains(ud, want) {
			t.Errorf("user-data sem %q:\n%s", want, ud)
		}
	}
	env, err := Env("lab", v, nil)
	if err != nil {
		t.Fatal(err)
	}
	if env["GV_PORTS"] != "tcp::8081:80 udp:127.0.0.1:5353:53" || env["GV_MEM"] != "2048" {
		t.Errorf("env %v", env)
	}
	if a, b := SSHPort("lab", v), SSHPort("lab", v); a != b || a < 22200 || a >= 23000 {
		t.Errorf("ssh port %d", a)
	}
}

func TestVMErrors(t *testing.T) {
	for _, bad := range []string{
		"x-vms:\n  a:\n    ports: [\"80\"]\n",
		"x-vms:\n  a:\n    memory: lots\n",
		"x-vms:\n  a:\n    disk: big\n",
		"x-vms:\n  'a b': {}\n",
	} {
		if _, err := composex.Parse([]byte(bad)); err == nil {
			t.Errorf("want error for %q", bad)
		}
	}
}

func TestParseStatus(t *testing.T) {
	got := parseStatus("web running 42 22301 hvf tcp::8081:80\ndb stopped - 22302 - -\n")
	if len(got) != 2 || got[0].PID != 42 || got[0].Ports[0] != "tcp::8081:80" || got[1].State != "stopped" || got[1].Accel != "" {
		t.Errorf("%+v", got)
	}
}

func TestExampleNginx(t *testing.T) {
	b, err := os.ReadFile("../../examples/vm-nginx/compose.yaml")
	if err != nil {
		t.Skip(err)
	}
	p, err := composex.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	v := p.VM("web")
	if v == nil || len(p.Names) != 0 || v.Memory != 1024 || v.Ports[0].Host != "8081" {
		t.Fatalf("%+v", v)
	}
	ud, err := UserData(v)
	if err != nil || !strings.Contains(ud, "Olá do Gantry") {
		t.Fatalf("%v\n%s", err, ud)
	}
}

func TestEnvLocalImage(t *testing.T) {
	v := &composex.VM{Name: "web", Image: "nixos-lab-26.05", CPUs: 2, Memory: 2048, Disk: "10G"}
	if _, err := Env("p", v, nil); err == nil || !strings.Contains(err.Error(), "pastas de imagens") {
		t.Fatalf("sem caminho da imagem: %v", err)
	}
	v.ImageSrc = "/imgs/nixos-lab-base.qcow2"
	env, err := Env("p", v, nil)
	if err != nil {
		t.Fatal(err)
	}
	if env["GV_IMAGE_SRC"] != v.ImageSrc || env["GV_EFI"] != "1" {
		t.Errorf("env %v", env)
	}
	deb, _ := Env("p", &composex.VM{Name: "d", Image: "debian-12", CPUs: 1, Memory: 1024, Disk: "10G"}, nil)
	if _, ok := deb["GV_EFI"]; ok {
		t.Error("Debian não usa UEFI")
	}
}

func TestEnvNetworkLab(t *testing.T) {
	p, err := composex.Parse([]byte("x-vms:\n  sw1:\n    image: /i.qcow2\n    cloud-init: false\n    management: false\n    nic: e1000\n    interfaces: [l1, none]\n  sw2:\n    image: /i.qcow2\n    interfaces: [l1, {bridge: br0}]\n"))
	if err != nil {
		t.Fatal(err)
	}
	AssignPorts("/labs/x", p)
	a, _ := Env("lab", p.VM("sw1"), nil)
	b, _ := Env("lab", p.VM("sw2"), nil)
	if a["GV_NOCI"] != "1" || a["GV_NOMGMT"] != "1" || a["GV_NIC"] != "e1000" || a["GV_USERDATA"] != "" || a["GV_SERIAL"] == "" || a["GV_DOWN"] != "1" {
		t.Errorf("env %v", a)
	}
	fa, fb := strings.Fields(a["GV_IFACES"]), strings.Fields(b["GV_IFACES"])
	if len(fa) != 2 || len(fb) != 2 || !strings.HasPrefix(fa[0], "relay:") || !strings.HasPrefix(fa[1], "relay:") || !strings.HasPrefix(fb[1], "bridge:br0:") {
		t.Fatalf("%v %v", fa, fb)
	}
	if b["GV_NOCI"] != "" || b["GV_USERDATA"] == "" {
		t.Error("sw2 perdeu o cloud-init")
	}
	// The relay table: each end of l1 points to the other; the free port
	// to nothing; the bridge is not in it.
	tab := map[string][]string{}
	for _, l := range strings.Split(RelayTable("lab", p), "\n") {
		f := strings.Fields(l)
		tab[f[3]+"/"+f[4]] = f
	}
	s1, s2, free := tab["sw1/0"], tab["sw2/0"], tab["sw1/1"]
	if len(tab) != 3 || s1[2] != s2[0] || s2[2] != s1[0] || free[2] != "0" {
		t.Errorf("tabela %v", tab)
	}
	// Moving the cable to the free port does not change the VMs' hashes.
	before := a["GV_HASH"]
	sw1 := p.VM("sw1")
	sw1.Interfaces[0].Link, sw1.Interfaces[1].Link = "", "l1"
	a2, _ := Env("lab", sw1, nil)
	if a2["GV_HASH"] != before {
		t.Error("trocar o cabo mudou a definição da VM (pediria reinício)")
	}
	// No two ports on the same UDP ports.
	seen := map[string]bool{}
	for _, f := range tab {
		for _, x := range f[:2] {
			if seen[x] {
				t.Errorf("porta %s repetida", x)
			}
			seen[x] = true
		}
	}
}
