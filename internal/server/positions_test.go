package server

import (
	"reflect"
	"strings"
	"testing"

	"gantry/internal/composex"
)

func TestSetPositionsRoundTrip(t *testing.T) {
	src := `services:
  cli:
    image: alpine
    x-interfaces: [sw1-cli]   # cabo para o switch
x-vms:
  r1:
    image: cisco-iosv
    interfaces: [r1-sw1]
    pos: [1, 2]
  sw1:
    image: cisco-iosvl2
    interfaces: [r1-sw1, sw1-cli]
`
	texts := map[string]string{"a.yaml": src}
	if err := setPositions(texts, []string{"a.yaml"}, map[string][]int{"r1": {200, 60}, "sw1": {420, 60}, "cli": {300, 260}}); err != nil {
		t.Fatal(err)
	}
	out := texts["a.yaml"]
	if !strings.Contains(out, "# cabo para o switch") {
		t.Fatal("comment lost:\n" + out)
	}
	p, err := composex.Parse([]byte(out))
	if err != nil {
		t.Fatal(err, "\n", out)
	}
	got := map[string][]int{}
	for _, n := range p.Nodes() {
		got[n.Name] = n.Pos
	}
	if !reflect.DeepEqual(got, map[string][]int{"r1": {200, 60}, "sw1": {420, 60}, "cli": {300, 260}}) {
		t.Fatalf("%v\n%s", got, out)
	}
	if len(p.Cables()) != 2 {
		t.Fatal(p.Cables())
	}
	// nil takes the position out; an unknown node is an error.
	if err := setPositions(texts, []string{"a.yaml"}, map[string][]int{"r1": nil}); err != nil || strings.Contains(texts["a.yaml"], "pos: [200, 60]") {
		t.Fatal(err, texts["a.yaml"])
	}
	if err := setPositions(texts, []string{"a.yaml"}, map[string][]int{"zz": {1, 1}}); err == nil {
		t.Fatal("unknown node accepted")
	}
}
