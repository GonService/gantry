package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIOLConfigFromFile(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	compose := "x-vms:\n  R1:\n    image: i86bi_Linux-L3-AdvEnterpriseK9-M2_157_3_May_2018.bin\n    config: equipamentos/R1.cfg\n" +
		"  R2:\n    image: i86bi_Linux-L3-AdvEnterpriseK9-M2_157_3_May_2018.bin\n    config: equipamentos/R2.cfg\n"
	os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(compose), 0o644)
	os.MkdirAll(filepath.Join(dir, "equipamentos"), 0o755)
	os.WriteFile(filepath.Join(dir, "equipamentos", "R1.cfg"), []byte("hostname R1\nend\n"), 0o644)
	h := e.srv.host("fake")
	p, _, err := e.srv.loadProject(context.Background(), h, []string{filepath.Join(dir, "compose.yaml")})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range p.VMs {
		switch v.Name {
		case "R1":
			if v.Config != "hostname R1\nend\n" || v.ConfigErr != "" {
				t.Errorf("R1: %q %q", v.Config, v.ConfigErr)
			}
		case "R2":
			if !strings.Contains(v.ConfigErr, "não existe") {
				t.Errorf("R2 sem arquivo: %q", v.ConfigErr)
			}
		}
	}
}
