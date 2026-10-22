package composex

import (
	"strings"
	"testing"
)

func TestServiceInterfaces(t *testing.T) {
	p, err := Parse([]byte(`services:
  cli:
    image: alpine
    x-interfaces: [r1-cli, none]
    x-addresses: [10.0.12.10/24]
x-vms:
  r1:
    image: cisco-iosv
    interfaces: [r1-cli]
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Taps) != 1 || p.Taps[0].Kind != "ctr" || len(p.Taps[0].Interfaces) != 2 || p.Taps[0].Interfaces[0].Side != 1 && p.VMs[0].Interfaces[0].Side != 1 {
		t.Fatalf("%+v", p.Taps)
	}
	if n := p.Nodes(); len(n) != 2 {
		t.Fatal(n)
	}
	if s := p.Services["cli"]; len(s.Addresses) != 1 || s.Addresses[0] != "10.0.12.10/24" {
		t.Fatal(s.Addresses)
	}
	for y, want := range map[string]string{
		"services:\n  a:\n    image: x\n    x-interfaces: [{bridge: br0}]\n":                                                "só por cabos",
		"services:\n  a:\n    image: x\n    x-interfaces: [ab]\n    x-addresses: [10.0.0.1]\n":                              "máscara",
		"services:\n  a:\n    image: x\n    x-interfaces: [ab]\n    x-addresses: [1.1.1.1/24, 2.2.2.2/24]\n":                "mais endereços",
		"services:\n  a:\n    image: x\n    x-interfaces: [ab]\n":                                                           "exatamente duas",
		"services:\n  a:\n    image: x\n    x-interfaces: [ab]\nx-vms:\n  a:\n    image: debian-12\n    interfaces: [ab]\n": "VM e de um serviço",
	} {
		if _, err := Parse([]byte(y)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v (queria %q)", y, err, want)
		}
	}
}
