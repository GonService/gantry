package composex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

// The lab of examples/lab-mpls: the compose parses, every node has its
// configuration file, and the files the services mount are there and valid.
func TestLabMPLSExample(t *testing.T) {
	dir := "../../examples/lab-mpls"
	b, err := os.ReadFile(filepath.Join(dir, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range p.VMs {
		if v.Kind != "iol" || v.ConfigFile == "" || v.ImageName == "" {
			t.Errorf("%s: kind %q config %q", v.Name, v.Kind, v.ConfigFile)
			continue
		}
		cfg, err := os.ReadFile(filepath.Join(dir, v.ConfigFile))
		if err != nil {
			t.Errorf("%s: %v", v.Name, err)
		} else if !strings.Contains(string(cfg), "hostname "+v.Name) {
			t.Errorf("%s: %s sem hostname %s", v.Name, v.ConfigFile, v.Name)
		}
	}
	for _, f := range []string{"prometheus/prometheus.yml", "prometheus/snmp.yml", "grafana/datasources.yaml", "grafana/dashboards.yaml"} {
		var x any
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err == nil {
			err = yaml.Unmarshal(b, &x)
		}
		if err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	js, _ := filepath.Glob(filepath.Join(dir, "grafana/paineis/*.json"))
	if len(js) == 0 {
		t.Error("sem painéis do Grafana")
	}
	for _, f := range js {
		var x map[string]any
		b, _ := os.ReadFile(f)
		if err := json.Unmarshal(b, &x); err != nil || x["uid"] == nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	t.Logf("%d nós, %d serviços, redes %d, cabos de containers %d", len(p.VMs), len(p.Names), len(p.Redes), len(p.Taps))
}
