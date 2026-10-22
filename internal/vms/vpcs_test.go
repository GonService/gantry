package vms

import (
	"encoding/base64"
	"strings"
	"testing"

	"gantry/internal/composex"
)

func TestVPCSEnv(t *testing.T) {
	p, err := composex.Parse([]byte(`x-vms:
  PC02:
    image: vpcs
    interfaces: [pc02]
    config: |
      set pcname PC02
      ip 192.168.20.100 192.168.20.1 24
  PC01:
    image: vpcs
    interfaces: [pc01]
  SW:
    image: cisco-iol-l2
    interfaces: [pc01, pc02]
`))
	if err != nil {
		t.Fatal(err)
	}
	AssignPorts("/lab", p)
	pc2 := p.VM("PC02")
	if pc2.Kind != "vpcs" || !pc2.NoManagement || !pc2.NoCloudInit || composex.IconOf(pc2) != "pc" {
		t.Fatalf("%+v", pc2)
	}
	env, err := Env("lab", pc2, nil)
	if err != nil {
		t.Fatal(err)
	}
	f := pc2.Interfaces[0]
	if env["GV_KIND"] != "vpcs" || env["GV_VPCSARGS"] != "-m 2 -s "+itoa(f.VPort)+" -c "+itoa(f.RPort)+" -t 127.0.0.1" {
		t.Fatalf("%v", env)
	}
	if c, _ := base64.StdEncoding.DecodeString(env["GV_CONFIG"]); !strings.Contains(string(c), "ip 192.168.20.100") {
		t.Fatalf("config %q", c)
	}
	if p.VM("PC01").IOLID != 1 {
		t.Fatalf("mac do PC01: %d", p.VM("PC01").IOLID)
	}
	for _, bad := range []string{"interfaces: [a, b]", "interfaces: [{link: a, ip: 10.0.0.1/24}]"} {
		if _, err := composex.Parse([]byte("x-vms:\n  pc:\n    image: vpcs\n    " + bad + "\n  x:\n    image: vpcs\n    interfaces: [a]\n")); err == nil {
			t.Errorf("%s: aceito", bad)
		}
	}
}
