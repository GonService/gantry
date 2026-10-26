package server

import (
	"strings"
	"testing"

	"gantry/internal/composex"
)

func TestNotasRoundTrip(t *testing.T) {
	base := "name: lab\n\nx-vms:\n  R1:\n    image: vpcs\n\n# fim\nservices: {}\n"
	notas := []*composex.Nota{
		{ID: "nota-1", Text: "Relação IP\n\n| PC01 | 10.0.0.1/24 |\n|---|---|\n  recuo e $HOME", Pos: []int{10, 20}, Size: []int{300, 200}, Cor: "amarela"},
		{ID: "nota-2", Text: "", Pos: []int{1, 2}},
	}
	out := setTopBlock(base, []string{"x-notas", "x-notes"}, composex.NotasYAML(notas))
	p, err := composex.Parse(composex.Interpolate([]byte(out), map[string]string{}))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(p.Notas) != 2 || p.Notas[0].Text != notas[0].Text || p.Notas[0].Cor != "amarela" || p.Notas[0].Size[1] != 200 || p.Notas[1].Text != "" {
		t.Fatalf("%+v\n%s", p.Notas[0], out)
	}
	if !strings.Contains(out, "# fim\nservices: {}") {
		t.Fatalf("o resto do arquivo mudou:\n%s", out)
	}
	// Changing and then removing the block leaves the file as it was.
	out2 := setTopBlock(out, []string{"x-notas", "x-notes"}, composex.NotasYAML(notas[:1]))
	if strings.Contains(out2, "nota-2") || !strings.Contains(out2, "nota-1") {
		t.Fatalf("troca:\n%s", out2)
	}
	if back := setTopBlock(out2, []string{"x-notas", "x-notes"}, ""); strings.TrimRight(back, "\n") != strings.TrimRight(base, "\n") {
		t.Fatalf("remover:\n%q\n%q", back, base)
	}
}
