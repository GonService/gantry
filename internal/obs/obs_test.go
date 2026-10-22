package obs

import (
	"testing"

	"gantry/internal/engine"
)

func TestDetect(t *testing.T) {
	mk := func(name, image string, priv, pub int, ip string) engine.Container {
		c := engine.Container{Names: []string{"/" + name}, Image: image, State: "running", Labels: map[string]string{"com.docker.compose.project": "obs"}}
		c.Ports = []engine.Port{{IP: ip, PrivatePort: priv, PublicPort: pub, Type: "tcp"}}
		return c
	}
	got := Detect([]engine.Container{
		mk("node", "prom/node-exporter", 9100, 9100, ""),
		mk("prom", "docker.io/prom/prometheus:v2.53.0", 9090, 19090, "0.0.0.0"),
		mk("loki", "grafana/loki:3.1.0", 3100, 3100, "127.0.0.1"),
		mk("graf", "grafana/grafana-oss@sha256:abc", 3000, 13000, "::"),
	})
	if got["prometheus"].URL != "http://127.0.0.1:19090" || got["prometheus"].Container != "prom" {
		t.Errorf("prometheus %+v", got["prometheus"])
	}
	if got["loki"].URL != "http://127.0.0.1:3100" || got["grafana"].URL != "http://127.0.0.1:13000" {
		t.Errorf("%+v", got)
	}
	if _, ok := got["node"]; ok || len(got) != 3 {
		t.Errorf("extra: %+v", got)
	}
	sub := mk("graf", "grafana/grafana:11.1.0", 3000, 3000, "")
	sub.Labels[LabelPath] = "/grafana/"
	if u := Detect([]engine.Container{sub})["grafana"].URL; u != "http://127.0.0.1:3000/grafana" {
		t.Errorf("sub path: %s", u)
	}
}

func TestContainerSelector(t *testing.T) {
	s, err := ContainerSelector([]string{"job", "container"}, "obs-web-1", "obs", "web")
	if err != nil || s != `{container=~"/?obs-web-1"}` {
		t.Errorf("%s %v", s, err)
	}
	s, _ = ContainerSelector([]string{"compose_project", "compose_service"}, "x", "obs", "web")
	if s != `{compose_project="obs", compose_service="web"}` {
		t.Error(s)
	}
	if _, err := ContainerSelector([]string{"job"}, "x", "", ""); err == nil {
		t.Error("want error")
	}
}
