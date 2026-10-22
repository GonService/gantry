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
