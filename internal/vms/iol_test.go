package vms

import (
	"encoding/base64"
	"strconv"
	"strings"
	"testing"

	"gantry/internal/composex"
)

func TestIOLProject(t *testing.T) {
	p, err := composex.Parse([]byte(`x-vms:
  r1:
    image: cisco-iol
    interfaces: [a, none, none, none, b]
  sw1:
    image: cisco-iol-l2
    interfaces: [a, b]
  vm:
    image: debian-12
    interfaces: [none]
`))
	if err != nil {
		t.Fatal(err)
	}
	AssignPorts("/labs/x", p)
	by := map[string]*composex.VM{}
	for _, v := range p.VMs {
		by[v.Name] = v
	}
	r1, sw1 := by["r1"], by["sw1"]
	if r1.Kind != "iol" || !r1.NoCloudInit || !r1.NoManagement || r1.Memory != 512 || sw1.Memory != 768 {
		t.Fatalf("perfil: %+v", r1)
	}
	if r1.IOLID < 1 || r1.IOLID > 767 || sw1.IOLID > 767 || r1.IOLID == sw1.IOLID || p.RelayID < 768 || p.RelayID > 1023 { // IOL takes ids up to 1023
		t.Fatalf("ids: r1=%d sw1=%d relay=%d", r1.IOLID, sw1.IOLID, p.RelayID)
	}
	if by["vm"].IOLID != 0 {
		t.Fatal("VM comum com id IOL")
	}
	// Port 4 is Ethernet1/0.
	if IOLPort(4) != 0x01 || IOLPort(1) != 0x10 {
		t.Fatalf("IOLPort: %x %x", IOLPort(4), IOLPort(1))
	}
	nm := IOLNetmap("/labs/x", r1)
	lines := strings.Split(strings.TrimSpace(nm), "\n")
	if len(lines) != 5 || !strings.HasSuffix(lines[4], " "+itoa(p.RelayID)+":1/0") || !strings.HasPrefix(lines[4], itoa(r1.IOLID)+":1/0 ") {
		t.Fatalf("NETMAP: %q", nm)
	}
	tab := RelayTable("/labs/x", p)
	if !strings.HasPrefix(tab, "#rid "+itoa(p.RelayID)+"\n") || !strings.Contains(tab, " iol:"+itoa(r1.IOLID)+":1 ") {
		t.Fatalf("relay: %s", tab)
	}
	r1.ImageSrc = "/opt/iol/r.bin"
	env, err := Env("/labs/x", r1, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := base64.StdEncoding.DecodeString(env["GV_NETMAP"])
	if env["GV_KIND"] != "iol" || env["GV_IOLPORTS"] != "5" || string(got) != nm {
		t.Fatalf("env: %v", env)
	}
}

func TestIOLErrors(t *testing.T) {
	for _, y := range []string{
		"x-vms:\n  r1:\n    image: cisco-iol\n    interfaces: [bridge: br0]\n",
		"x-vms:\n  r1:\n    image: cisco-iol\n    lan: true\n",
	} {
		if _, err := composex.Parse([]byte(y)); err == nil {
			t.Errorf("aceitou: %s", y)
		}
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

func TestIOLConfig(t *testing.T) {
	p, err := composex.Parse([]byte("x-vms:\n  R1:\n    image: cisco-iol\n    config: |\n      hostname R1\n      router ospf 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	v := p.VM("R1")
	v.ImageSrc = "/opt/iol/r.bin"
	env, err := Env("lab", v, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := base64.StdEncoding.DecodeString(env["GV_CONFIG"])
	if string(got) != "hostname R1\nrouter ospf 1\n" {
		t.Fatalf("%q", got)
	}
	// The configuration does not change the hash (it only goes in once).
	v.Config = "hostname X\n"
	env2, _ := Env("lab", v, nil)
	if env2["GV_HASH"] != env["GV_HASH"] {
		t.Fatal("hash mudou")
	}
	if _, err := composex.Parse([]byte("x-vms:\n  V:\n    image: debian-12\n    config: |\n      x\n")); err == nil {
		t.Fatal("config numa VM comum")
	}
}
