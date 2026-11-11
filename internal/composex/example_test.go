package composex

import (
	"os"
	"testing"
)

func TestExampleLab(t *testing.T) {
	b, err := os.ReadFile("../../examples/observabilidade/compose.yaml")
	if err != nil {
		t.Skip(err)
	}
	p, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	plan := p.Plan("docker")
	if len(plan) != 2 || plan[0].Runtime != "podman" || plan[1].Services[0] != "grafana" {
		t.Fatalf("plan %+v", plan)
	}
	for _, l := range p.CrossLinks("docker") {
		if len(l.Via) == 0 {
			t.Errorf("%s -> %s sem porta", l.From, l.To)
		}
	}
}

// The lab of the tutorial (docs/do-zero-ao-lab.md) must keep loading as written.
func TestExamplePrimeiroLab(t *testing.T) {
	b, err := os.ReadFile("../../examples/primeiro-lab/compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	p, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.VMs) != 4 || len(p.Redes) != 2 {
		t.Fatalf("esperava 4 equipamentos/VMs e 2 redes: %d, %d", len(p.VMs), len(p.Redes))
	}
	if sv := p.Services["srv"]; sv == nil || len(sv.Interfaces) != 1 || sv.Interfaces[0].Net != "lan-b" {
		t.Fatalf("srv fora da lan-b: %+v", sv)
	}
}
