package vms

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"

	"gantry/internal/composex"
)

// A module with what tends to break YAML: lines starting with spaces, tabs,
// quotes, colons, "#", "- ", "---", blank lines inside and at the end.
const trickyModule = `  # começa com espaço
{ config, pkgs, ... }:
let
	p = config.gantry.params;   # tab no início
  s = "texto: com dois pontos";
  q = 'aspas simples';
in
{

  - não é lista
---
  key: |
    não é bloco
  "chave" = ''
    ${p.title or "x"}
  '';
}
`

func nixVM(t *testing.T, module, extraCloudInit string) *composex.VM {
	t.Helper()
	src := `x-vms:
  web:
    image: nixos-lab-26.05
    memory: 2048
    nix:
      module: ./web.nix
      params:
        httpPort: 8080
        title: "Lab: web"
        tags: [a, b]
` + extraCloudInit
	p, err := composex.Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	v := p.VM("web")
	v.Nix.Content = module
	return v
}

// writeFiles reads write_files back from the user-data, as cloud-init does.
func writeFiles(t *testing.T, ud string) map[string]string {
	t.Helper()
	var doc struct {
		WriteFiles []struct {
			Path    string `yaml:"path"`
			Content string `yaml:"content"`
		} `yaml:"write_files"`
	}
	if err := yaml.Unmarshal([]byte(ud), &doc); err != nil {
		t.Fatalf("user-data inválido: %v\n%s", err, ud)
	}
	out := map[string]string{}
	for _, f := range doc.WriteFiles {
		if _, dup := out[f.Path]; dup {
			t.Errorf("%s duas vezes", f.Path)
		}
		out[f.Path] = f.Content
	}
	return out
}

// pyWriteFiles does the same with PyYAML, the parser cloud-init uses.
func pyWriteFiles(t *testing.T, ud string) map[string]string {
	t.Helper()
	if err := exec.Command("python3", "-c", "import yaml").Run(); err != nil {
		t.Log("sem python3 com PyYAML: o user-data é conferido só pelo goccy")
		return nil
	}
	cmd := exec.Command("python3", "-c", `import sys, json, yaml
d = yaml.safe_load(sys.stdin.read())
print(json.dumps({f["path"]: f.get("content", "") for f in d.get("write_files", [])}))`)
	cmd.Stdin = strings.NewReader(ud)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("PyYAML recusou o user-data: %v\n%s", err, ud)
	}
	m := map[string]string{}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestNixRoundTrip(t *testing.T) {
	cases := []struct {
		name, mod string
		// goccy (the YAML library of Gantry) rejects a literal block with
		// an indentation indicator that ends in blank lines; PyYAML, which
		// cloud-init uses, reads it right. Those cases go to PyYAML only.
		goccy bool
	}{
		{"difícil", trickyModule, true},
		{"sem quebra final", "{ ... }: { }", true},
		{"uma quebra", "{ ... }: { }\n", true},
		{"vazio", "", true},
		{"várias quebras no fim", "{ ... }: { }\n\n\n", false},
		{"só quebras", "\n\n", false},
		{"crlf", "{ ... }:\r\n{ }\r\n", true},
	}
	for _, c := range cases {
		v := nixVM(t, c.mod, "")
		ud, err := UserData(v)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		params, module, _ := NixFiles(v)
		if want := strings.ReplaceAll(c.mod, "\r\n", "\n"); string(module) != want {
			t.Errorf("%s: módulo entregue %q", c.name, module)
		}
		parsers := map[string]map[string]string{"PyYAML": pyWriteFiles(t, ud)}
		if c.goccy {
			parsers["goccy"] = writeFiles(t, ud)
		}
		for parser, got := range parsers {
			if got == nil {
				continue
			}
			if got[NixModulePath] != string(module) {
				t.Errorf("%s (%s): lab.nix mudou\nquero %q\nveio  %q\n%s", c.name, parser, module, got[NixModulePath], ud)
			}
			if got[NixParamsPath] != string(params) {
				t.Errorf("%s (%s): params mudaram: %q", c.name, parser, got[NixParamsPath])
			}
		}
	}
}

func TestNixParams(t *testing.T) {
	v := nixVM(t, "{ ... }: { }\n", "")
	params, _, err := NixFiles(v)
	if err != nil {
		t.Fatal(err)
	}
	var p map[string]any
	if err := json.Unmarshal(params, &p); err != nil {
		t.Fatal(err)
	}
	if p["hostname"] != "web" || p["httpPort"] != float64(8080) || p["title"] != "Lab: web" || len(p["tags"].([]any)) != 2 {
		t.Errorf("params %v", p)
	}
	if !strings.HasPrefix(string(params), "{\n  \"") || !strings.HasSuffix(string(params), "}\n") {
		t.Errorf("JSON sem indentação: %q", params)
	}
	v.Nix.Params["hostname"] = "web-01"
	params, _, _ = NixFiles(v)
	if !strings.Contains(string(params), `"hostname": "web-01"`) {
		t.Errorf("hostname do usuário perdido: %s", params)
	}
}

func TestNixMergeWriteFiles(t *testing.T) {
	v := nixVM(t, "{ ... }: { }\n", `    cloud-init:
      timezone: America/Sao_Paulo
      write_files:
        - path: /etc/motd
          content: |
            olá
`)
	ud, err := UserData(v)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(ud, "write_files:") != 1 {
		t.Fatalf("write_files duplicado:\n%s", ud)
	}
	got := writeFiles(t, ud)
	if got["/etc/motd"] != "olá\n" || got[NixModulePath] == "" || got[NixParamsPath] == "" || len(got) != 3 {
		t.Errorf("arquivos %v", got)
	}
	if !strings.Contains(ud, "timezone: America/Sao_Paulo") {
		t.Error("o resto do cloud-init do usuário sumiu")
	}
}

func TestNixHashAndEnv(t *testing.T) {
	v := nixVM(t, "{ ... }: { }\n", "")
	v.ImageSrc = "/imgs/base.qcow2"
	env, err := Env("p", v, nil)
	if err != nil {
		t.Fatal(err)
	}
	params, module, _ := NixFiles(v)
	if env["GV_NIXHASH"] != NixHash(params, module) || len(env["GV_NIXHASH"]) != 64 {
		t.Fatalf("GV_NIXHASH %q", env["GV_NIXHASH"])
	}
	// Only the module changes: GV_HASH and GV_NIXHASH change, GV_BASEHASH
	// does not (the VM is updated in place).
	old := env
	v.Nix.Content = "{ ... }: { services.nginx.enable = true; }\n"
	env, _ = Env("p", v, nil)
	if env["GV_HASH"] == old["GV_HASH"] || env["GV_NIXHASH"] == old["GV_NIXHASH"] {
		t.Fatal("mudar o módulo não mudou os hashes")
	}
	if env["GV_BASEHASH"] != old["GV_BASEHASH"] {
		t.Error("mudar só o módulo mudou o resto da definição")
	}
	// A param changes the Nix hash too.
	v.Nix.Params["title"] = "outro"
	if env2, _ := Env("p", v, nil); env2["GV_NIXHASH"] == env["GV_NIXHASH"] || env2["GV_BASEHASH"] != old["GV_BASEHASH"] {
		t.Error("params fora do hash Nix")
	}
	// CPU changes: the rest of the definition changed.
	v.CPUs = 4
	if env3, _ := Env("p", v, nil); env3["GV_BASEHASH"] == old["GV_BASEHASH"] {
		t.Error("uma mudança de CPU passou por mudança só de Nix")
	}
	// The module that could not be read is an error, with the field.
	v.Nix.ReadErr = "./web.nix não existe na pasta do projeto"
	if _, err := Env("p", v, nil); err == nil || !strings.Contains(err.Error(), "nix.module") {
		t.Errorf("erro %v", err)
	}
}

func TestNixVMWithoutNixKeepsHash(t *testing.T) {
	v := &composex.VM{Name: "d", Image: "debian-12", CPUs: 1, Memory: 1024, Disk: "10G", Packages: []string{"nginx"}}
	env, err := Env("p", v, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := env["GV_NIXHASH"]; ok || len(env["GV_HASH"]) != 16 || env["GV_BASEHASH"] != env["GV_HASH"] {
		t.Errorf("env %v", env)
	}
}
