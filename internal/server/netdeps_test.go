package server

import (
	"slices"
	"testing"

	"gantry/internal/composex"
)

func TestNetDependents(t *testing.T) {
	p, err := composex.Parse([]byte(`services:
  grafana: {image: g}
  web: {image: n, network_mode: "service:grafana"}
  log: {image: b, network_mode: "service:web"}
  prometheus:
    image: p
    x-interfaces: [{link: lan, ip: 10.0.0.2/24}]
  rotas: {image: b, network_mode: "service:prometheus"}
  outro:
    image: b
    x-interfaces: [{link: lan, ip: 10.0.0.3/24}]
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := netDependents(p, []string{"grafana"}); !slices.Equal(got, []string{"log", "web"}) {
		t.Errorf("grafana: %v", got)
	}
	if got := netDependents(p, []string{"prometheus"}); !slices.Equal(got, []string{"prometheus-cabos", "rotas"}) {
		t.Errorf("prometheus: %v", got)
	}
	if got := netDependents(p, []string{"rotas"}); len(got) != 0 {
		t.Errorf("rotas: %v", got)
	}
}
