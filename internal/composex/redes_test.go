package composex

import (
	"reflect"
	"strings"
	"testing"
)

func TestRedes(t *testing.T) {
	p, err := Parse([]byte(`
x-redes:
  lan-a: {subnet: 10.0.1.0/24, gateway: 10.0.1.1, dhcp: true, dns: 1.1.1.1, pos: [300, 40]}
  lan-b: {subnet: 10.0.2.0/28, dhcp: 10.0.2.5-10.0.2.9}
  sw: {}
x-vms:
  r1:
    image: cisco-iol
    interfaces: [lan-a, r1-web]
  web:
    image: debian-12
    interfaces:
      - {rede: lan-a, ip: [10.0.1.10, 10.0.1.11/24]}
      - {link: r1-web, ip: 192.168.0.2/30}
      - {rede: lan-b, ip: dhcp}
      - sw
services:
  cli:
    image: alpine
    x-interfaces: [{rede: lan-a, ip: 10.0.1.20}, lan-b]
`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.RedeNames, []string{"lan-a", "lan-b", "sw"}) {
		t.Fatal(p.RedeNames)
	}
	a, b := p.Redes["lan-a"], p.Redes["lan-b"]
	if !a.DHCP || a.DHCPFrom != "10.0.1.100" || a.DHCPTo != "10.0.1.199" || a.Gateway != "10.0.1.1" || a.DHCPServer() != "10.0.1.254" || !reflect.DeepEqual(a.Pos, []int{300, 40}) || a.DNS[0] != "1.1.1.1" {
		t.Errorf("%+v", a)
	}
	if b.DHCPFrom != "10.0.2.5" || b.DHCPTo != "10.0.2.9" || b.DHCPServer() != "10.0.2.14" || p.Redes["sw"].DHCP {
		t.Errorf("%+v", b)
	}
	r1, web := p.VM("r1"), p.VM("web")
	if r1.Interfaces[0].Net != "lan-a" || r1.Interfaces[0].Link != "" || r1.Interfaces[1].Link != "r1-web" {
		t.Errorf("%+v", r1.Interfaces)
	}
	w := web.Interfaces
	if w[0].Net != "lan-a" || !reflect.DeepEqual(w[0].IPs, []string{"10.0.1.10/24", "10.0.1.11/24"}) || w[1].IPs[0] != "192.168.0.2/30" || !w[2].DHCP || w[3].Net != "sw" {
		t.Errorf("%+v", w)
	}
	if c := p.Taps[0]; c.Interfaces[0].IPs[0] != "10.0.1.20/24" || c.Interfaces[1].Net != "lan-b" {
		t.Errorf("%+v", c.Interfaces)
	}
	for src, want := range map[string]string{
		"x-vms:\n  a:\n    interfaces: [{rede: x}]\n":                                                                           "não está em x-redes",
		"x-redes:\n  l: {subnet: 10.0.1.0/24}\nx-vms:\n  a:\n    interfaces: [{rede: l, ip: dhcp}]\n":                           "não tem dhcp",
		"x-redes:\n  l: {subnet: 10.0.1.0/24}\nx-vms:\n  a:\n    interfaces: [{rede: l, ip: 10.0.2.1}]\n":                       "fora da rede",
		"x-redes:\n  l: {dhcp: true}\n":                                                                                         "precisa de subnet",
		"x-redes:\n  l: {subnet: 10.0.1.0/24, gateway: 10.0.9.1}\n":                                                             "fora de",
		"x-redes:\n  l: {subnet: 10.0.1.0/24}\nx-vms:\n  a:\n    image: cisco-iol\n    interfaces: [{rede: l, ip: 10.0.1.2}]\n": "dentro dele",
		"x-vms:\n  a:\n    interfaces: [{link: a-b, ip: 10.0.0.1}]\n  b:\n    interfaces: [a-b]\n":                              "máscara",
		"x-vms:\n  a:\n    interfaces: [{link: a-b, rede: x}]\n":                                                                "um só",
		"x-vms:\n  a:\n    interfaces: [{bridge: br0, ip: 10.0.0.1/24}]\n":                                                      "numa bridge",
		"x-redes:\n  l: {subnet: 10.0.1.0/24}\nx-vms:\n  a:\n    interfaces: [{rede: l, ip: [dhcp, 10.0.1.2]}]\n":               "sozinho",
	} {
		if _, err := Parse([]byte(src)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v (want %q)", src, err, want)
		}
	}
}
