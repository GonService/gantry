package composex

import (
	"reflect"
	"strings"
	"testing"
)

const graphLab = `services:
  cli:
    image: alpine
    x-interfaces: [sw1-cli]
    x-pos: [40, 300]
x-vms:
  r1:
    image: cisco-iosv
    interfaces: [r1-sw1, none]
    pos: [200, 60]
  sw1:
    image: cisco-iosvl2
    interfaces: [r1-sw1, sw1-cli, none]
`

func TestGraphNodesCablesPositions(t *testing.T) {
	p, err := Parse([]byte(graphLab))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	pos := map[string][]int{}
	for _, n := range p.Nodes() {
		names = append(names, n.Name)
		pos[n.Name] = n.Pos
	}
	if !reflect.DeepEqual(names, []string{"r1", "sw1", "cli"}) {
		t.Fatal(names)
	}
	if !reflect.DeepEqual(pos["r1"], []int{200, 60}) || !reflect.DeepEqual(pos["cli"], []int{40, 300}) || pos["sw1"] != nil {
		t.Fatal(pos)
	}
	want := []Cable{
		{Link: "r1-sw1", A: CableEnd{"r1", 0}, B: CableEnd{"sw1", 0}},
		{Link: "sw1-cli", A: CableEnd{"sw1", 1}, B: CableEnd{"cli", 0}},
	}
	if got := p.Cables(); !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
}

func TestPosErrors(t *testing.T) {
	for y, want := range map[string]string{
		"x-vms:\n  a:\n    image: debian-12\n    pos: [1]\n":                      "[x, y]",
		"x-vms:\n  a:\n    image: debian-12\n    pos: [a, 2]\n":                   "números inteiros",
		"services:\n  a:\n    image: x\n    x-interfaces: [none]\n    x-pos: 3\n": "[x, y]",
	} {
		if _, err := Parse([]byte(y)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v (queria %q)", y, err, want)
		}
	}
}
