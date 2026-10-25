package tools

import (
	"os/exec"
	"strings"
	"testing"
)

func TestParseLinuxServer(t *testing.T) {
	out := "os=Linux\narch=x86_64\npm=apt-get\ndocker=/usr/bin/docker\npodman=\nqemu-system-x86_64=\nqemu-img=/usr/bin/qemu-img\nqemu-system=\ndocker-compose-plugin=1\nkvm-exists=1\ncurl=/usr/bin/curl\npython3=/usr/bin/python3\n"
	r := Parse(out, false)
	get := func(id string) Tool {
		for _, x := range r.Tools {
			if x.ID == id {
				return x
			}
		}
		t.Fatalf("falta %s em %+v", id, r.Tools)
		return Tool{}
	}
	if e := get("engine"); !e.Have || e.Found != "docker" {
		t.Fatalf("engine %+v", e)
	}
	if c := get("compose"); !c.Have || c.Found != "docker compose" {
		t.Fatalf("compose %+v", c)
	}
	q := get("qemu")
	if q.Have || q.Install != "sudo apt install -y qemu-system qemu-utils" || q.Brew {
		t.Fatalf("qemu %+v", q)
	}
	if k := get("kvm"); k.Have || !strings.Contains(k.Install, "usermod -aG kvm") {
		t.Fatalf("kvm %+v", k)
	}
	if iso := get("iso"); iso.Have || iso.Install != "sudo apt install -y genisoimage" {
		t.Fatalf("iso %+v", iso)
	}
	for _, x := range r.Tools {
		if x.ID == "ssh" || x.ID == "helm" || x.ID == "dialogo" {
			t.Fatalf("%s só aparece neste computador", x.ID)
		}
	}
	// qemu, iso, kvm missing (essential/recommended)
	if r.Missing != 3 {
		t.Fatalf("faltando %d: %+v", r.Missing, r.Tools)
	}
}

func TestParseMacLocal(t *testing.T) {
	out := "os=Darwin\narch=arm64\npm=brew\npodman=/opt/homebrew/bin/podman\ndocker-compose=/opt/homebrew/bin/docker-compose\nqemu-system=\nqemu-img=\ncurl=/usr/bin/curl\nssh=/usr/bin/ssh\ngit=/usr/bin/git\nhdiutil=/usr/bin/hdiutil\n"
	r := Parse(out, true)
	for _, x := range r.Tools {
		switch x.ID {
		case "qemu":
			if x.Have || x.Install != "brew install qemu" || !x.Brew {
				t.Fatalf("qemu %+v", x)
			}
		case "helm":
			if x.Install != "brew install helm" || !x.Brew {
				t.Fatalf("helm %+v", x)
			}
		case "iso", "kvm", "dialogo":
			t.Fatalf("%s não se aplica ao Mac", x.ID)
		}
	}
}

// The script runs on a real shell and every line parses.
func TestScriptRuns(t *testing.T) {
	out, err := exec.Command("sh", "-c", Script).Output()
	if err != nil {
		t.Fatal(err)
	}
	r := Parse(string(out), true)
	if r.OS == "" || r.Arch == "" || len(r.Tools) < 6 {
		t.Fatalf("%+v\n%s", r, out)
	}
}

func TestDynamipsFedora(t *testing.T) {
	out := "os=Linux\narch=x86_64\npm=dnf\npodman=/usr/bin/podman\npython3=/usr/bin/python3\ndynamips=\n"
	r := Parse(out, false)
	for _, x := range r.Tools {
		if x.ID == "dynamips" {
			if x.Have || x.Install != "sudo dnf install -y dynamips" {
				t.Fatalf("dynamips %+v", x)
			}
			return
		}
	}
	t.Fatalf("falta dynamips em %+v", r.Tools)
}

func TestParseLib32(t *testing.T) {
	find := func(r Report) *Tool {
		for i := range r.Tools {
			if r.Tools[i].ID == "lib32" {
				return &r.Tools[i]
			}
		}
		return nil
	}
	r := Parse("os=Linux\narch=x86_64\npm=dnf\nlib32=\n", false)
	l := find(r)
	if l == nil || l.Have || !strings.Contains(l.Install, "glibc.i686") {
		t.Fatalf("lib32 faltando no Fedora: %+v", l)
	}
	r = Parse("os=Linux\narch=x86_64\npm=apt-get\nlib32=1\n", false)
	if l := find(r); l == nil || !l.Have || l.Install != "" {
		t.Fatalf("lib32 presente: %+v", l)
	}
	if find(Parse("os=Linux\narch=aarch64\npm=dnf\n", false)) != nil || find(Parse("os=Darwin\narch=arm64\npm=brew\n", true)) != nil {
		t.Fatal("lib32 só vale para Linux x86_64")
	}
}
