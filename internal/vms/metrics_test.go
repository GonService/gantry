package vms

import (
	"strconv"
	"strings"
	"testing"

	"gantry/internal/composex"
)

func TestMetricsVM(t *testing.T) {
	p, err := composex.Parse([]byte(`x-vms:
  web:
    image: debian-12
    metrics: true
    run: ["echo ok"]
  nix:
    image: nixos-lab-26.05
    metrics: true
    nix:
      module: ./web.nix
`))
	if err != nil {
		t.Fatal(err)
	}
	web := p.VM("web")
	ud, err := UserData(web)
	if err != nil {
		t.Fatal(err)
	}
	i, j := strings.Index(ud, "prometheus-node-exporter"), strings.Index(ud, "echo ok")
	if i < 0 || j < 0 || i > j {
		t.Fatalf("the exporter must be installed before the VM's commands:\n%s", ud)
	}
	env, err := Env("lab", web, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(env["GV_PORTS"], "tcp:127.0.0.1:"+strconv.Itoa(MetricsPort("lab", web))+":9100") {
		t.Fatalf("GV_PORTS %q", env["GV_PORTS"])
	}
	nix := p.VM("nix")
	nix.Nix.Content = "{ ... }: { }"
	params, _, err := NixFiles(nix)
	if err != nil || !strings.Contains(string(params), `"metrics": true`) {
		t.Fatalf("%s %v", params, err)
	}
	if ud, _ := UserData(nix); strings.Contains(ud, "prometheus-node-exporter") {
		t.Fatal("NixOS gets the exporter from its module, not from apt")
	}
	// Without metrics: the hash of an existing VM stays the same.
	plain, _ := composex.Parse([]byte("x-vms:\n  web:\n    image: debian-12\n"))
	if e, _ := Env("lab", plain.VM("web"), nil); strings.Contains(e["GV_PORTS"], "9100") {
		t.Fatal(e["GV_PORTS"])
	}
	for _, y := range []string{"x-vms:\n  r:\n    image: cisco-iosv\n    metrics: true\n", "x-vms:\n  r:\n    image: cisco-iol\n    metrics: true\n", "x-vms:\n  r:\n    iso: x.iso\n    metrics: true\n"} {
		if _, err := composex.Parse([]byte(y)); err == nil || !strings.Contains(err.Error(), "metrics precisa de cloud-init") {
			t.Errorf("%q: %v", y, err)
		}
	}
}
