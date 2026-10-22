package vms

import (
	"strings"
	"testing"

	"gantry/internal/composex"
)

func TestDynamipsPorts(t *testing.T) {
	for _, c := range []struct {
		plat string
		i    int
		want string
	}{{"7200", 0, "f0/0"}, {"7200", 1, "f1/0"}, {"7200", 2, "f1/1"}, {"7200", 3, "f2/0"}, {"7200", 12, "f6/1"},
		{"3725", 0, "f0/0"}, {"3725", 1, "f0/1"}, {"3725", 2, "f1/0"}, {"3725", 17, "f1/15"}, {"3725", 18, "f2/0"}, {"2691", 5, "f1/3"}} {
		ports, _ := composex.DynLayout(c.plat, nil)
		if got := ports[c.i].Short(); got != c.want {
			t.Errorf("%s %d: %s, queria %s", c.plat, c.i, got, c.want)
		}
	}
}

func TestDynamipsEnv(t *testing.T) {
	p, err := composex.Parse([]byte(`x-vms:
  r1:
    image: cisco-c7200
    interfaces: [r1-r2, none, r1-iol]
  r2:
    image: cisco-c3725
    interfaces: [r1-r2]
  i1:
    image: cisco-iol
    interfaces: [r1-iol]
`))
	if err != nil {
		t.Fatal(err)
	}
	AssignPorts("/lab", p)
	r1 := p.VM("r1")
	if r1.Kind != "dyn" || r1.Platform != "7200" || r1.Memory != 256 || !r1.NoManagement || !r1.NoCloudInit {
		t.Fatalf("%+v", r1)
	}
	r1.ImageSrc = "/imagens/c7200.image"
	env, err := Env("lab", r1, nil)
	if err != nil {
		t.Fatal(err)
	}
	f := r1.Interfaces
	want := "-p 0:C7200-IO-FE -s 0:0:udp:" + itoa(f[0].VPort) + ":127.0.0.1:" + itoa(f[0].RPort) +
		" -p 1:PA-2FE-TX -s 1:0:udp:" + itoa(f[1].VPort) + ":127.0.0.1:" + itoa(f[1].RPort) +
		" -s 1:1:udp:" + itoa(f[2].VPort) + ":127.0.0.1:" + itoa(f[2].RPort)
	if env["GV_KIND"] != "dyn" || env["GV_DYNPLAT"] != "7200" || env["GV_DYNARGS"] != want {
		t.Fatalf("%v\nqueria %s", env, want)
	}
	// The relay sends a Dynamips port's frames to its UDP NIO, as a VM's;
	// the cable to the IOL node goes to the IOL instance.
	tab := RelayTable("lab", p)
	if !strings.Contains(tab, itoa(f[0].RPort)+" "+itoa(f[0].VPort)+" "+itoa(p.VM("r2").Interfaces[0].RPort)+" r1 0") {
		t.Fatalf("relay: %s", tab)
	}
	if _, err := composex.Parse([]byte("x-vms:\n  r:\n    image: cisco-c2691\n    interfaces: [" + strings.Repeat("none, ", 18) + "none]\n")); err == nil || !strings.Contains(err.Error(), "no máximo 18") {
		t.Fatalf("too many ports: %v", err)
	}
	if _, err := composex.Parse([]byte("x-vms:\n  r:\n    image: cisco-c7200\n    interfaces: [{bridge: br0}]\n")); err == nil || !strings.Contains(err.Error(), "Dynamips liga só por cabos") {
		t.Fatalf("bridge: %v", err)
	}
}

// TestDynamipsSlots: the adapters of slots: (as in a PNETLab lab) name the
// ports as the original router does, and all of them go in.
func TestDynamipsSlots(t *testing.T) {
	p, err := composex.Parse([]byte(`x-vms:
  R1:
    image: cisco-c7200
    slots: [PA-FE-TX, PA-FE-TX, PA-FE-TX]
    interfaces: [none, none, none, r1-r5]
  R5:
    image: cisco-iol
    interfaces: [r1-r5]
`))
	if err != nil {
		t.Fatal(err)
	}
	AssignPorts("/lab", p)
	r1 := p.VM("R1")
	if got := strings.Join(DynPortNames(r1), " "); got != "f0/0 f1/0 f2/0 f3/0" {
		t.Fatalf("portas: %s", got)
	}
	args := DynArgs("lab", r1)
	for _, w := range []string{"-p 0:C7200-IO-FE", "-p 1:PA-FE-TX", "-p 2:PA-FE-TX", "-p 3:PA-FE-TX", "-s 3:0:udp:"} {
		if !strings.Contains(args, w) {
			t.Fatalf("sem %q: %s", w, args)
		}
	}
	// an empty slot keeps the numbering; Ethernet and Gigabit adapters
	ports, err := composex.DynLayout("7200", []string{"", "PA-4E", "PA-GE"})
	if err != nil || len(ports) != 6 || ports[1].Name != "Ethernet2/0" || ports[5].Name != "GigabitEthernet3/0" {
		t.Fatalf("%v %+v", err, ports)
	}
	for _, bad := range []string{"slots: [NM-1FE-TX]", "slots: [a, b, c, d, e, f, g]"} {
		if _, err := composex.Parse([]byte("x-vms:\n  r:\n    image: cisco-c7200\n    " + bad + "\n")); err == nil {
			t.Errorf("%s: aceito", bad)
		}
	}
	if _, err := composex.Parse([]byte("x-vms:\n  r:\n    image: cisco-c7200\n    slots: [PA-FE-TX]\n    interfaces: [a, b, c]\n")); err == nil || !strings.Contains(err.Error(), "no máximo 2") {
		t.Fatalf("portas demais: %v", err)
	}
	if _, err := composex.Parse([]byte("x-vms:\n  r:\n    image: cisco-iol\n    slots: [PA-FE-TX]\n")); err == nil {
		t.Fatal("slots num IOL")
	}
}
