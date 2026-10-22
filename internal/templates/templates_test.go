package templates

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gantry/internal/composex"
)

// eaten finds $NAME or ${…} left after interpolation (it would have been
// replaced by the .env). $4 (awk) and $(…) survive and are fine.
var eaten = regexp.MustCompile(`\$[A-Za-z_{]`)

func TestBuiltinsParse(t *testing.T) {
	bs := Builtin()
	if len(bs) < 10 {
		t.Fatalf("só %d modelos", len(bs))
	}
	for _, b := range bs {
		if b.Name == "" || b.Category == "" || b.Description == "" {
			t.Errorf("%s sem cabeçalho: %+v", b.ID, b)
		}
		env := map[string]string{}
		for _, v := range composex.Vars([]byte(b.YAML)) {
			env[v.Name] = "x"
			if v.Default != "" {
				env[v.Name] = v.Default
			}
		}
		p, err := composex.Parse(composex.Interpolate([]byte(b.YAML), env))
		if err != nil {
			t.Errorf("%s: %v", b.ID, err)
			continue
		}
		if len(p.Names) == 0 && len(p.VMs) == 0 {
			t.Errorf("%s vazio", b.ID)
		}
		images := map[string]bool{"debian-12": true, "debian-13": true, "ubuntu-24.04": true, "ubuntu-22.04": true, "rocky-9": true, "alma-9": true, "nixos-lab-26.05": true, "cisco-iosvl2": true, "cisco-iosv": true, "cisco-csr1000v": true, "cisco-iol": true, "cisco-iol-l2": true, "cisco-c7200": true, "cisco-c3725": true, "cisco-c2691": true, "": true}
		for _, vm := range p.VMs {
			if !images[vm.Image] && vm.Image != "x" { // "x": an image the user gives (a variable with no default)
				t.Errorf("%s: VM %s com imagem desconhecida %q", b.ID, vm.Name, vm.Image)
			}
			// The first boot runs these with sh: a $var left here was eaten by
			// the interpolation, which would break the script silently.
			for _, c := range vm.Run {
				if eaten.MatchString(c) {
					t.Errorf("%s: VM %s tem uma variável de shell que a interpolação apaga: %q", b.ID, vm.Name, c)
				}
			}
		}
	}
}

func TestStore(t *testing.T) {
	p := filepath.Join(t.TempDir(), "templates.json")
	s := Open(p)
	m, err := s.Save(Template{Name: "Meu", YAML: "services: {}"})
	if err != nil || m.ID == "" {
		t.Fatal(m, err)
	}
	m.Name = "Meu 2"
	if _, err := s.Save(m); err != nil {
		t.Fatal(err)
	}
	if got, ok := Open(p).Get(m.ID); !ok || got.Name != "Meu 2" {
		t.Errorf("%+v", got)
	}
	if _, err := s.Save(Template{ID: "gantry-nginx", Name: "x"}); err != ErrBuiltin {
		t.Error("alterou um modelo embutido")
	}
	if err := s.Delete(m.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get(m.ID); ok {
		t.Error("não apagou")
	}
}

func TestLabObservabilidade(t *testing.T) {
	var lab Template
	for _, b := range Builtin() {
		if b.ID == "gantry-lab-observabilidade" {
			lab = b
		}
	}
	if lab.Name == "" {
		t.Fatal("modelo do lab não encontrado")
	}
	for _, f := range []string{"prometheus.yml", "grafana/provisioning/datasources/prometheus.yml", "grafana/provisioning/dashboards/gantry.yml",
		"grafana/dashboards/maquinas.json", "grafana/dashboards/prometheus.json", "grafana/dashboards/nginx.json",
		"grafana/dashboards/traefik.json", "traefik/dinamico.yml", "alertas.yml"} {
		if lab.Files[f] == "" {
			t.Errorf("faltou %s", f)
		}
	}
	env := map[string]string{"GRAFANA_SENHA": "x"}
	p, err := composex.Parse(composex.Interpolate([]byte(lab.YAML), env))
	if err != nil {
		t.Fatal(err)
	}
	if p.Runtime != "podman" || len(p.Names) != 5 || len(p.VMs) != 1 || len(p.VMs[0].Ports) != 3 {
		t.Errorf("%+v %+v", p, p.VMs)
	}
	// The VM ports can change in the .env; Prometheus gets them from there.
	if !strings.Contains(lab.Files["prometheus.yml"], "${PORTA_NODE_VM}") {
		t.Error("o prometheus.yml deveria usar ${PORTA_NODE_VM}")
	}
	for _, v := range composex.Vars([]byte(lab.YAML)) {
		if v.Name == "PORTA_HTTP" && v.Default != "80" {
			t.Errorf("PORTA_HTTP = %q", v.Default)
		}
	}
}

// Built-in templates pin their images: a version, not latest. The few that
// follow a moving tag take it from a variable the user can set.
func TestPinnedImages(t *testing.T) {
	floating := regexp.MustCompile(`(?m)^\s*image:\s*["']?([^\s"'$]+?)(?::(latest|main|master))?["']?\s*$`)
	for _, tp := range Builtin() {
		for _, src := range append([]string{tp.YAML}, mapValues(tp.Files)...) {
			for _, m := range floating.FindAllStringSubmatch(src, -1) {
				img := m[1]
				if m[2] != "" || (!strings.Contains(img, ":") && !strings.HasPrefix(img, "debian-") && !strings.HasPrefix(img, "ubuntu-") && !strings.HasPrefix(img, "rocky-") && !strings.HasPrefix(img, "nixos-") && !strings.HasPrefix(img, "cisco-")) {
					t.Errorf("%s: imagem sem versão fixa: %s", tp.ID, m[0])
				}
			}
		}
	}
}

func mapValues(m map[string]string) []string {
	var out []string
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

// The NixOS template brings its module and parses with nix:.
func TestNixOSTemplate(t *testing.T) {
	var tp Template
	for _, x := range Builtin() {
		if x.ID == "gantry-vm-nixos-web" {
			tp = x
		}
	}
	if tp.Files["web.nix"] == "" {
		t.Fatalf("modelo sem web.nix: %+v", tp)
	}
	names := map[string]bool{}
	for _, v := range composex.Vars([]byte(tp.YAML)) {
		names[v.Name] = true
	}
	if !names["HTTP_PORT"] || !names["TITLE"] {
		t.Errorf("variáveis %v", names)
	}
	p, err := composex.Parse(composex.Interpolate([]byte(tp.YAML), map[string]string{"HTTP_PORT": "8081", "TITLE": "Meu: lab"}))
	if err != nil {
		t.Fatal(err)
	}
	v := p.VM("web")
	if v == nil || v.Nix == nil || v.Nix.Module != "./web.nix" || v.Nix.Params["title"] != "Meu: lab" || v.Ports[0].Container != "8081" {
		t.Errorf("%+v %+v", v, v.Nix)
	}
	if n, _ := v.Nix.Params["httpPort"].(uint64); n != 8081 {
		t.Errorf("httpPort %#v", v.Nix.Params["httpPort"])
	}
}

// The network labs: appliances wired by their cables, with the profile of
// each image (no cloud-init, no management NIC, its NIC model).
func TestNetworkLabTemplates(t *testing.T) {
	want := map[string]int{"gantry-lab-switches-cisco": 3, "gantry-lab-roteadores-cisco": 3, "gantry-lab-campus-cisco": 3, "gantry-lab-iol-cisco": 4}
	for _, tp := range Builtin() {
		n, ok := want[tp.ID]
		if !ok {
			continue
		}
		delete(want, tp.ID)
		p, err := composex.Parse([]byte(tp.YAML))
		if err != nil {
			t.Fatalf("%s: %v", tp.ID, err)
		}
		if len(p.VMs) != n {
			t.Errorf("%s: %d VMs", tp.ID, len(p.VMs))
		}
		for _, v := range p.VMs {
			prof := composex.Appliances[v.Image]
			if !v.NoCloudInit || !v.NoManagement || v.NIC != prof.NIC || v.Memory != prof.Memory || v.Kind != prof.Kind || len(v.Interfaces) < 3 {
				t.Errorf("%s/%s: %+v", tp.ID, v.Name, v)
			}
		}
	}
	if len(want) > 0 {
		t.Errorf("faltam %v", want)
	}
}
