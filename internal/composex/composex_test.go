package composex

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

const obs = `
x-gantry:
  runtime: podman
x-common: &common
  restart: unless-stopped
services:
  prometheus:
    <<: *common
    image: prom/prometheus:${PROM_TAG:-v2.53.0}
    ports: ["9090:9090"]
    networks: [obs]
  loki:
    image: grafana/loki
    ports:
      - target: 3100
        published: 3100
        host_ip: 127.0.0.1
    networks:
      obs: {}
  grafana:
    image: grafana/grafana
    x-gantry:
      runtime: docker
    ports: ["127.0.0.1:3000:3000/tcp", "8125:8125/udp"]
    depends_on:
      prometheus:
        condition: service_started
      loki: {}
networks:
  obs:
    driver: bridge
`

func TestParseAndPlan(t *testing.T) {
	p, err := Parse([]byte(obs))
	if err != nil {
		t.Fatal(err)
	}
	if !p.Gantry || p.Runtime != "podman" {
		t.Fatalf("lab runtime: %+v", p)
	}
	if got := p.Names; !reflect.DeepEqual(got, []string{"prometheus", "loki", "grafana"}) {
		t.Fatalf("names %v", got)
	}
	if img := p.Services["prometheus"].Image; img != "prom/prometheus:v2.53.0" {
		t.Errorf("interpolated image %q", img)
	}
	g := p.Services["grafana"]
	if g.Runtime != "docker" || !reflect.DeepEqual(g.DependsOn, []string{"prometheus", "loki"}) {
		t.Errorf("grafana %+v", g)
	}
	if !reflect.DeepEqual(g.Ports, []Port{{HostIP: "127.0.0.1", Host: "3000", Container: "3000", Proto: "tcp"}, {Host: "8125", Container: "8125", Proto: "udp"}}) {
		t.Errorf("grafana ports %+v", g.Ports)
	}
	if l := p.Services["loki"]; l.Ports[0] != (Port{HostIP: "127.0.0.1", Host: "3100", Container: "3100", Proto: "tcp"}) || l.Networks[0] != "obs" {
		t.Errorf("loki %+v", l)
	}
	if !reflect.DeepEqual(g.Networks, []string{"default"}) || p.Networks["default"] == nil || p.Networks["obs"].Driver != "bridge" {
		t.Errorf("networks %+v %+v", g.Networks, p.Networks)
	}
	plan := p.Plan("docker")
	want := []Group{{Runtime: "podman", Services: []string{"prometheus", "loki"}}, {Runtime: "docker", Services: []string{"grafana"}}}
	if !reflect.DeepEqual(plan, want) {
		t.Errorf("plan %+v", plan)
	}
	if !p.Mixed("docker") || !p.Mixed("podman") {
		t.Error("should be mixed")
	}
	links := p.CrossLinks("docker")
	if len(links) != 2 || links[0].To != "prometheus" || len(links[0].Via) != 1 || links[0].Via[0].Host != "9090" {
		t.Errorf("links %+v", links)
	}
}

func TestPlainCompose(t *testing.T) {
	p, err := Parse([]byte("services:\n  web:\n    image: nginx\n    ports: [\"80\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Gantry || p.Mixed("docker") {
		t.Error("plain compose is not mixed")
	}
	if p.Services["web"].Ports[0] != (Port{Container: "80", Proto: "tcp"}) {
		t.Errorf("port %+v", p.Services["web"].Ports)
	}
}

func TestOverrideAndErrors(t *testing.T) {
	p, err := Parse([]byte("services:\n  a:\n    image: x\n"), []byte("x-gantry: {runtime: podman}\nservices:\n  a:\n    image: y\n  b:\n    image: z\n"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Services["a"].Image != "y" || p.RuntimeOf("b", "docker") != "podman" || len(p.Names) != 2 {
		t.Errorf("override %+v", p)
	}
	if _, err := Parse([]byte("services:\n  a:\n    x-gantry: {runtime: lxc}\n")); err == nil {
		t.Error("want error for unknown runtime")
	}
	if _, err := Parse([]byte("services: [\n")); err == nil {
		t.Error("want yaml error")
	}
}

func TestParsePort(t *testing.T) {
	cases := map[string]Port{
		"8080:80":             {Host: "8080", Container: "80", Proto: "tcp"},
		"53:53/udp":           {Host: "53", Container: "53", Proto: "udp"},
		"[::1]:8080:80":       {HostIP: "::1", Host: "8080", Container: "80", Proto: "tcp"},
		"9000-9001:9000-9001": {Host: "9000-9001", Container: "9000-9001", Proto: "tcp"},
	}
	for in, want := range cases {
		if got := ParsePort(in); got != want {
			t.Errorf("%s: %+v", in, got)
		}
	}
}

func TestInterpolateAndVars(t *testing.T) {
	src := []byte("a: ${A}\nb: ${B:-dois}\nc: ${C:?falta}\nd: $D\ne: $$HOME\nf: ${B-x}\ng: ${A:+sim}\n")
	got := string(Interpolate(src, map[string]string{"A": "1", "C": "3", "D": "4"}))
	want := "a: 1\nb: dois\nc: 3\nd: 4\ne: $$HOME\nf: x\ng: sim\n"
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	vs := Vars(src)
	if len(vs) != 4 || vs[1].Name != "B" || vs[1].Default != "dois" || !vs[2].Required || vs[2].Message != "falta" {
		t.Errorf("%+v", vs)
	}
	env := ParseEnv([]byte("# c\nA=1\nexport B=\"dois tres\"\n\nC='x'\n"))
	if env["A"] != "1" || env["B"] != "dois tres" || env["C"] != "x" {
		t.Errorf("%v", env)
	}
}

func TestBinds(t *testing.T) {
	p, err := Parse([]byte("services:\n  a:\n    image: x\n    volumes:\n      - ./conf/a.yml:/etc/a.yml:ro\n      - dados:/data\n      - type: bind\n        source: /srv/x\n        target: /x\n      - type: volume\n        source: v\n        target: /v\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(p.Services["a"].Binds); got != "[./conf/a.yml /srv/x]" {
		t.Errorf("binds = %s", got)
	}
}

func TestUses(t *testing.T) {
	p, err := Parse([]byte("x-gantry:\n  usa:\n    postgres: {host: PG_HOST, senha: PG_SENHA}\nservices:\n  a:\n    image: x\n"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Uses["postgres"]["host"] != "PG_HOST" || p.Uses["postgres"]["senha"] != "PG_SENHA" {
		t.Errorf("usa: %+v", p.Uses)
	}
	if _, err := Parse([]byte("x-gantry:\n  usa:\n    oracle: {}\nservices:\n  a:\n    image: x\n")); err == nil {
		t.Error("serviço desconhecido devia dar erro")
	}
}

func TestKubeAndAfter(t *testing.T) {
	p, err := Parse([]byte("x-gantry:\n  kubernetes: {vm: srv, porta: \"${P:-7443}\"}\nx-vms:\n  srv:\n    image: debian-12\n  ag:\n    depois: srv\n    image: debian-12\n"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Kube == nil || p.Kube.VM != "srv" || p.Kube.Port != 7443 {
		t.Fatalf("kube: %+v", p.Kube)
	}
	if v := p.VM("ag"); v == nil || len(v.After) != 1 || v.After[0] != "srv" {
		t.Fatalf("depois: %+v", v)
	}
}

func TestParseVMNix(t *testing.T) {
	p, err := Parse([]byte(`x-vms:
  web:
    image: nixos-lab-26.05
    memory: 2048
    packages: [nginx]
    run: ["echo oi"]
    nix:
      module: ./web.nix
      params:
        httpPort: 80
        title: Lab web
        nested: {a: 1, b: [x, y]}
`))
	if err != nil {
		t.Fatal(err)
	}
	v := p.VM("web")
	if v.Nix == nil || v.Nix.Module != "./web.nix" {
		t.Fatalf("nix %+v", v.Nix)
	}
	if v.Nix.Params["hostname"] != "web" || v.Nix.Params["title"] != "Lab web" {
		t.Errorf("params %v", v.Nix.Params)
	}
	if n, ok := v.Nix.Params["nested"].(map[string]any); !ok || len(n["b"].([]any)) != 2 {
		t.Errorf("params aninhados %#v", v.Nix.Params["nested"])
	}
	if len(v.Packages) != 0 || len(v.Run) != 0 || len(v.Warnings) != 1 || !strings.Contains(v.Warnings[0], "ignorados") {
		t.Errorf("packages/run com nix: %v %v %v", v.Packages, v.Run, v.Warnings)
	}
	p, _ = Parse([]byte("x-vms:\n  w:\n    image: nixos-lab-26.05\n    nix:\n      module: ./w.nix\n      params: {hostname: w-01}\n"))
	if w := p.VM("w"); w.Nix.Params["hostname"] != "w-01" || len(w.Warnings) != 1 || !strings.Contains(w.Warnings[0], "2048") {
		t.Errorf("hostname do usuário ou aviso de memória: %v %v", w.Nix.Params, w.Warnings)
	}
	for src, want := range map[string]string{
		"x-vms:\n  w:\n    image: debian-12\n    nix:\n      module: ./w.nix\n":                           "só vale com uma imagem NixOS",
		"x-vms:\n  w:\n    nix:\n      module: ./w.nix\n":                                                 "só vale com uma imagem NixOS",
		"x-vms:\n  w:\n    image: nixos-lab-26.05\n    nix:\n      params: {a: 1}\n":                      "nix.module",
		"x-vms:\n  w:\n    image: nixos-lab-26.05\n    nix:\n      module: ./w.nix\n      pacotes: [x]\n": "campo desconhecido",
	} {
		if _, err := Parse([]byte(src)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: erro %v, quero %q", src, err, want)
		}
	}
}

func TestCloudInitFile(t *testing.T) {
	for v, want := range map[string]bool{"./web.cloud-init.yaml": true, "ci/base.YML": true, "timezone: UTC": false, "a.yaml\nb: 1": false, "{a: b.yaml}": false, "": false} {
		if IsCloudInitFile(v) != want {
			t.Errorf("%q: %v", v, !want)
		}
	}
	p, err := Parse([]byte("x-vms:\n  w:\n    cloud-init: ./w.cloud-init.yaml\n"))
	if err != nil || p.VM("w").CloudInitFile != "./w.cloud-init.yaml" || p.VM("w").CloudInit != "" {
		t.Fatalf("%+v %v", p.VM("w"), err)
	}
}

func TestVMInterfaces(t *testing.T) {
	p, err := Parse([]byte(`x-vms:
  sw1:
    image: ./ios.qcow2
    cloud-init: false
    management: false
    nic: E1000
    machine: pc
    interfaces: [sw1-sw2, none, {bridge: br0}]
  sw2:
    image: ./ios.qcow2
    gerencia: false
    interfaces:
      - link: sw1-sw2
`))
	if err != nil {
		t.Fatal(err)
	}
	a, b := p.VM("sw1"), p.VM("sw2")
	if !a.NoCloudInit || !a.NoManagement || a.NIC != "e1000" || a.Machine != "pc" || len(a.Interfaces) != 3 {
		t.Fatalf("%+v", a)
	}
	if a.Interfaces[0].Link != "sw1-sw2" || a.Interfaces[0].Side != 0 || b.Interfaces[0].Side != 1 {
		t.Errorf("lados %+v %+v", a.Interfaces, b.Interfaces)
	}
	if !reflect.DeepEqual(a.Interfaces[1], VMIface{}) || a.Interfaces[2].Bridge != "br0" || !b.NoManagement || b.NoCloudInit {
		t.Errorf("%+v %+v", a.Interfaces, b)
	}
	for src, want := range map[string]string{
		"x-vms:\n  a:\n    interfaces: [x]\n":                                               "liga 1 interface",
		"x-vms:\n  a:\n    interfaces: [x, x]\n  b:\n    interfaces: [x]\n":                 "liga 3 interface",
		"x-vms:\n  a:\n    nic: ne2k\n":                                                     "modelo conhecido",
		"x-vms:\n  a:\n    management: false\n    ports: [\"80:80\"]\n":                     "placa de gerência",
		"x-vms:\n  a:\n    interfaces: [{link: x, bridge: y}]\n  b:\n    interfaces: [x]\n": "link (o cabo",
		"x-vms:\n  a:\n    interfaces: {x: 1}\n":                                            "use uma lista",
	} {
		if _, err := Parse([]byte(src)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v (quero %q)", src, err, want)
		}
	}
	p, _ = Parse([]byte("x-vms:\n  a:\n    cloud-init: true\n"))
	if p.VM("a").NoCloudInit {
		t.Error("cloud-init: true desligou o cloud-init")
	}
}

func TestApplianceProfile(t *testing.T) {
	p, err := Parse([]byte("x-vms:\n  r1:\n    image: cisco-csr1000v\n  r2:\n    image: cisco-iosv\n    memory: 1g\n    nic: virtio\n    management: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	r1, r2 := p.VM("r1"), p.VM("r2")
	if r1.Memory != 4096 || r1.NIC != "virtio" || !r1.NoCloudInit || !r1.NoManagement || r1.Machine != "pc" {
		t.Errorf("perfil do CSR: %+v", r1)
	}
	if r2.Memory != 1024 || r2.NIC != "virtio" || r2.NoManagement || !r2.NoCloudInit {
		t.Errorf("o compose devia mandar: %+v", r2)
	}
}

func TestIOLConfigFile(t *testing.T) {
	p, err := Parse([]byte(`x-vms:
  R1:
    image: i86bi_Linux-L3-AdvEnterpriseK9-M2_157_3_May_2018.bin
    config: equipamentos/R1.cfg
  R2:
    image: i86bi_Linux-L3-AdvEnterpriseK9-M2_157_3_May_2018.bin
    config: |
      hostname R2
      end
`))
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range p.VMs {
		switch v.Name {
		case "R1":
			if v.ConfigFile != "equipamentos/R1.cfg" || v.Config != "" {
				t.Errorf("R1: file %q config %q", v.ConfigFile, v.Config)
			}
		case "R2":
			if v.ConfigFile != "" || v.Config == "" {
				t.Errorf("R2: file %q config %q", v.ConfigFile, v.Config)
			}
		}
	}
	for v, want := range map[string]bool{"R1.cfg": true, "a/b.conf": true, "end": false, "hostname R1.cfg": false, "x.txt\n": false} {
		if IsConfigFile(v) != want {
			t.Errorf("IsConfigFile(%q) = %v", v, !want)
		}
	}
}
