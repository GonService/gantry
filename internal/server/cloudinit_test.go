package server

import (
	"strings"
	"testing"

	"gantry/internal/composex"
)

const ciCompose = `# projeto
x-vms:
  web:
    image: debian-12
    packages: [nginx]
    cloud-init: |
      timezone: America/Sao_Paulo
      write_files:
        - path: /etc/motd
          content: oi

    ports: ["8080:80"]
  db:
    image: debian-12
services:
  app:
    image: nginx:1.27
`

func TestSetBlockField(t *testing.T) {
	out, ok := setBlockField(ciCompose, "x-vms", "web", "cloud-init", "./web.cloud-init.yaml")
	if !ok {
		t.Fatal("não achou a VM")
	}
	want := `    packages: [nginx]
    cloud-init: ./web.cloud-init.yaml
    ports: ["8080:80"]
  db:`
	if !strings.Contains(out, want) || strings.Contains(out, "timezone") || !strings.HasPrefix(out, "# projeto\n") {
		t.Fatalf("bloco trocado errado:\n%s", out)
	}
	out, _ = setBlockField(out, "x-vms", "db", "cloud-init", "./db.cloud-init.yaml")
	if !strings.Contains(out, "  db:\n    image: debian-12\n    cloud-init: ./db.cloud-init.yaml\nservices:") {
		t.Fatalf("campo novo no lugar errado:\n%s", out)
	}
	out, _ = setBlockField(out, "x-vms", "web", "cloud-init", "")
	if strings.Contains(out, "web.cloud-init") || !strings.Contains(out, "packages: [nginx]\n    ports:") {
		t.Fatalf("remoção:\n%s", out)
	}
}

func TestCloudInitEditAndPreview(t *testing.T) {
	list, err := cloudInitPreview(ciCompose, "", nil)
	if err != nil || len(list) != 2 {
		t.Fatalf("%v %v", list, err)
	}
	web := list[1]
	if list[0].Name == "web" {
		web = list[0]
	}
	if !strings.Contains(web.Extra, "timezone") || !strings.Contains(web.UserData, "#cloud-config") || !strings.Contains(web.UserData, "nginx") || !strings.Contains(web.UserData, "<gantry-ssh-key>") {
		t.Fatalf("prévia %+v", web)
	}
	// Moving to a file: the compose points to it, the preview reads it.
	compose, _ := setBlockField(ciCompose, "x-vms", "web", "cloud-init", "./web.cloud-init.yaml")
	files := map[string]string{"web.cloud-init.yaml": "timezone: UTC\nruncmd:\n  - echo pronto\n"}
	list, err = cloudInitPreview(compose, "", files)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range list {
		if x.Name == "web" && (x.File != "./web.cloud-init.yaml" || !strings.Contains(x.UserData, "timezone: UTC") || !strings.Contains(x.UserData, "echo pronto")) {
			t.Errorf("arquivo não lido: %+v", x)
		}
	}
	// A file that is not in the project is an error of that VM only.
	list, _ = cloudInitPreview(compose, "", nil)
	for _, x := range list {
		if x.Name == "web" && !strings.Contains(x.Error, "web.cloud-init.yaml") {
			t.Errorf("sem erro do arquivo: %+v", x)
		}
		if x.Name == "db" && x.Error != "" {
			t.Errorf("erro vazou para db: %+v", x)
		}
	}
}

func TestCablesFlow(t *testing.T) {
	src := "x-vms:\n  sw1:\n    image: x\n    interfaces:\n      - link: a\n      - none\n    nic: e1000\n"
	out, _ := setBlockField(src, "x-vms", "sw1", "interfaces", ifacesFlow([]composex.VMIface{{Link: "b"}, {}, {Bridge: "br0"}}))
	if out != "x-vms:\n  sw1:\n    image: x\n    interfaces: [b, none, {bridge: br0}]\n    nic: e1000\n" {
		t.Fatalf("%q", out)
	}
	out, _ = setBlockField(out, "x-vms", "sw1", "interfaces", ifacesFlow(nil))
	if strings.Contains(out, "interfaces") {
		t.Errorf("%q", out)
	}
}
