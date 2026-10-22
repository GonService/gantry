package composex

import (
	"strings"
	"testing"
)

func TestClassifyImage(t *testing.T) {
	for f, want := range map[string]string{
		"i86bi_LinuxL2-AdvEnterpriseK9-M_152_May_2018.bin":                        "cisco-iol-l2",
		"i86bi_LinuxL3-AdvEnterpriseK9-M2_157_3_May_2018.bin":                     "cisco-iol",
		"i86bi-linux-l3-adventerprisek9-15.5.2T.bin":                              "cisco-iol",
		"i86bi-linux-l2-adventerprisek9-15.1a.bin":                                "cisco-iol-l2",
		"x86_64_crb_linux-adventerprisek9-ms.iol":                                 "cisco-iol",
		"x86_64_crb_linux_l2-adventerprisek9-ms.iol":                              "cisco-iol-l2",
		"/opt/unetlab/addons/iol/bin/L2-ADVENTERPRISEK9-M-15.2-IRON-20151103.bin": "cisco-iol-l2",
		"c7200-adventerprisek9-mz.152-4.S6.image":                                 "cisco-c7200",
		"c3725-adventerprisek9-mz.124-15.T14.bin":                                 "cisco-c3725",
		"c2691-adventerprisek9-mz.124-15.T14.bin":                                 "cisco-c2691",
		"vios_l2-adventerprisek9-m.SSA.high_iron_20200929.qcow2":                  "cisco-iosvl2",
		"viosl2-adventerprisek9-m-15.2.2020":                                      "cisco-iosvl2",
		"vios-adventerprisek9-m.spa.159-3.m6.qcow2":                               "cisco-iosv",
		"csr1000v-universalk9.17.03.06-serial.qcow2":                              "cisco-csr1000v",
		"iourc":                              "cisco-iourc",
		"debian-12-genericcloud-amd64.qcow2": "",
		"fortinet-FGT-v7.2.qcow2":            "",
	} {
		if got := ClassifyImage(f); got != want {
			t.Errorf("%s: %q, want %q", f, got, want)
		}
	}
	for s, want := range map[string]bool{"x.qcow2": true, "i86bi_LinuxL2-x.bin": true, "debian-12": false, "/a/b.qcow2": false, "https://x/y.qcow2": false, "cisco-iol": false} {
		if IsImageFileRef(s) != want {
			t.Errorf("IsImageFileRef(%s) != %v", s, want)
		}
	}
}

func TestIconOf(t *testing.T) {
	p, err := Parse([]byte(`
x-vms:
  r1: {image: cisco-iol, interfaces: [none]}
  sw: {image: i86bi_Linux-L2-AdvEnterpriseK9-M_152_May_2018.bin, interfaces: [none]}
  fw: {image: /srv/asav-9.20.qcow2}
  web: {image: debian-12}
  pc1: {image: debian-12, icon: pc}
services:
  c:
    image: alpine
    x-interfaces: [none]
    x-icon: Cloud
`))
	if err != nil {
		t.Fatal(err)
	}
	for n, want := range map[string]string{"r1": "router", "sw": "l3switch", "fw": "firewall", "web": "server", "pc1": "pc"} {
		if got := IconOf(p.VM(n)); got != want {
			t.Errorf("%s: %s, want %s", n, got, want)
		}
	}
	if got := IconOf(p.Taps[0]); got != "cloud" {
		t.Error(got)
	}
	if _, err := Parse([]byte("x-vms:\n  a: {icon: robo}\n")); err == nil || !strings.Contains(err.Error(), "ícone desconhecido") {
		t.Error(err)
	}
}

func TestImagesSection(t *testing.T) {
	p, err := Parse([]byte(`x-imagens:
  iol-l3: i86bi_Linux-L3-AdvEnterpriseK9-M2_157_3_May_2018.bin
  iol-l2: /opt/unetlab/addons/iol/bin/i86bi_LinuxL2-AdvEnterpriseK9-M_152_May_2018.bin
x-vms:
  R1: {image: iol-l3}
  SW1: {image: iol-l2}
`))
	if err != nil {
		t.Fatal(err)
	}
	r1, sw1 := p.VM("R1"), p.VM("SW1")
	if r1.Image != "i86bi_Linux-L3-AdvEnterpriseK9-M2_157_3_May_2018.bin" || r1.Catalog != "cisco-iol" || r1.Kind != "iol" || r1.ImageName != "iol-l3" || r1.ImageSrc != "" {
		t.Fatalf("R1: %+v", r1)
	}
	if sw1.Catalog != "cisco-iol-l2" || sw1.Kind != "iol" || sw1.ImageSrc != "/opt/unetlab/addons/iol/bin/i86bi_LinuxL2-AdvEnterpriseK9-M_152_May_2018.bin" {
		t.Fatalf("SW1: %+v", sw1)
	}
	if _, err := Parse([]byte("x-imagens:\n  x: nginx\n")); err == nil {
		t.Fatal("aceitou algo que não é imagem")
	}
}
