package unl

import (
	"encoding/base64"
	"strings"
	"testing"

	"gantry/internal/composex"
)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func sample(extra string) []byte {
	r1 := "hostname R1\ninterface FastEthernet2/0\n ip address 10.0.12.1 255.255.255.252\nend\n"
	return []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<lab name="Teste OSPF" version="1">
  <topology>
    <nodes>
      <node id="1" type="dynamips" template="c7200" image="c7200-adventerprisek9-mz.152-4.S6.image" name="R1" left="100" top="50" ram="512" idlepc="0x61da0e54" dynamips_options="-P 7200 -t npe-200" slot1="PA-FE-TX" slot2="PA-FE-TX" slot3="" config_data="` + b64(r1) + `">
        <interface id="0" name="fa0/0" type="ethernet" network_id="1"/>
        <interface id="32" name="fa2/0" type="ethernet" network_id="2"/>
      </node>
      <node id="2" type="iol" template="iol" image="i86bi_Linux-L2-Adventerprisek9-ms.SSA.high_iron_20190423.bin" name="SW 01" left="300" top="50" ethernet="1" ram="1024">
        <interface id="16" name="e0/1" type="ethernet" network_id="1"/>
        <interface id="48" name="e0/3" type="ethernet" network_id="3"/>
        <interface id="1" name="e1/0" type="ethernet" network_id="4"/>
      </node>
      <node id="3" type="vpcs" template="vpcs" image="" name="PC01" left="500" top="50" config_data="` + b64("set pcname PC01\nip 192.168.1.10 192.168.1.1 24\n") + `">
        <interface id="0" name="eth0" type="ethernet" network_id="3"/>
      </node>
      <node id="4" type="iol" template="iol" image="i86bi_linux-adventerprisek9-ms.154-1.T.bin" name="R2" left="100" top="200" ethernet="1">
        <interface id="2" name="e2/0" type="ethernet" network_id="2"/>
        <interface id="0" name="e0/0" type="ethernet" network_id="5"/>
      </node>
      ` + extra + `
    </nodes>
    <networks>
      <network id="1" type="bridge" name="Net-R1"/>
      <network id="2" type="bridge" name="Net-R1R2"/>
      <network id="3" type="bridge" name="Net-PC"/>
      <network id="4" type="bridge" name="Net-solta"/>
      <network id="5" type="pnet0" name="Internet"/>
    </networks>
  </topology>
  <objects>
    <textobjects>
      <textobject id="1" name="txt 1" type="text"><data>` + b64("%3Cp%3ERela%C3%A7%C3%A3o%20IP%3C%2Fp%3E%3Ctable%3E%3Ctr%3E%3Ctd%3EPC01%3C%2Ftd%3E%3Ctd%3E192.168.1.10%3C%2Ftd%3E%3C%2Ftr%3E%3C%2Ftable%3E") + `</data></textobject>
      <textobject id="2" name="txt 2" type="text"><data>` + b64("%3Cdiv%20id%3D%22customText2%22%20class%3D%22customShape%22%20style%3D%22position%3A%20absolute%3B%20top%3A%20251.994px%3B%20left%3A%2061.9744px%3B%20width%3A%20310px%3B%20height%3A%20439.858px%3B%22%3E%3Cp%3EFoto%3C%2Fp%3E%3Cfigure%3E%3Cimg%20class%3D%22ck_image%22%20src%3D%22data%3Aimage%2Fpng%3Bbase64%2CiVBORw0KGgoAAAANSUhEUg%3D%3D%22%3E%3C%2Ffigure%3E%3C%2Fdiv%3E") + `</data></textobject>
    </textobjects>
  </objects>
  <workbooks><workbook id="Roteiro" type="html"><content><page>` + b64("%3Ch2%3EEtapa%201%20%u2014%20Corrigir%3C/h2%3E%3Cp%3EAche%20as%20falhas.%3C/p%3E") + `</page></content></workbook></workbooks>
</lab>`)
}

func TestConvert(t *testing.T) {
	b := sample("")
	l, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	r := Convert(l, b, "teste.unl")
	if len(r.Blocking) > 0 {
		t.Fatalf("bloqueios: %v", r.Blocking)
	}
	p, err := composex.Parse([]byte(r.Compose))
	if err != nil {
		t.Fatalf("%v\n%s", err, r.Compose)
	}
	if p.Name != "teste-ospf" {
		t.Errorf("nome %q", p.Name)
	}
	r1, sw, pc, r2 := p.VM("R1"), p.VM("SW-01"), p.VM("PC01"), p.VM("R2")
	if r1 == nil || sw == nil || pc == nil || r2 == nil {
		t.Fatalf("nós: %s", r.Compose)
	}
	// R1: the 7200 with its adapters and NPE; fa2/0 is the third port, as
	// there; its 512 MB become the 256 the 7200 runs with (noted)
	if r1.Kind != "dyn" || strings.Join(r1.Slots, ",") != "PA-FE-TX,PA-FE-TX" || len(r1.Interfaces) != 3 ||
		r1.Interfaces[0].Link != "r1-sw-01" || r1.Interfaces[2].Link != "r1-r2" || r1.ConfigFile != "equipamentos/R1.cfg" || r1.Memory != 256 || r1.NPE != "npe-200" {
		t.Fatalf("R1: %+v", r1)
	}
	// the IOL switch keeps its 4 ports; e1/0 (no cable on the other side)
	// stays without one; its memory is the original's
	if sw.Kind != "iol" || len(sw.Interfaces) != 5 || sw.Interfaces[1].Link != "r1-sw-01" || sw.Interfaces[3].Link != "pc01-sw-01" || sw.Interfaces[4].Link != "" || sw.Memory != 1024 {
		t.Fatalf("SW: %+v", sw.Interfaces)
	}
	if pc.Kind != "vpcs" || pc.ConfigFile != "equipamentos/PC01.vpc" || pc.Interfaces[0].Link != "pc01-sw-01" {
		t.Fatalf("PC: %+v", pc)
	}
	// R2: e2/0 is Gantry's port 8; the cloud port is left without a cable
	if len(r2.Interfaces) != 9 || r2.Interfaces[8].Link != "r1-r2" || r2.Interfaces[0].Link != "" {
		t.Fatalf("R2: %+v", r2.Interfaces)
	}
	if !strings.Contains(r.Files["equipamentos/R1.cfg"], "interface FastEthernet2/0") {
		t.Fatalf("a config mudou: %q", r.Files["equipamentos/R1.cfg"])
	}
	if r.Files["original/teste.unl"] != string(b) {
		t.Fatal("sem o original")
	}
	rep, rd := r.Files["IMPORTACAO.md"], r.Files["README.md"]
	for _, w := range []string{"0x61da0e54", "o original pede 512 MB", "com a npe-200", "Internet", "c7200-adventerprisek9-mz.152-4.S6.image", "fa2/0 → r1-r2"} {
		if !strings.Contains(rep, w) {
			t.Errorf("relatório sem %q:\n%s", w, rep)
		}
	}
	for _, w := range []string{"Relação IP", "| PC01 | 192.168.1.10 |\n|---|---|", "### Etapa 1 — Corrigir", "Ache as falhas."} {
		if !strings.Contains(rd, w) {
			t.Errorf("README sem %q:\n%s", w, rd)
		}
	}
}

// TestConvertBlocks: equipment Gantry does not run stops the import
// instead of being replaced by something else.
func TestConvertBlocks(t *testing.T) {
	b := sample(`<node id="9" type="qemu" template="fortinet" image="fortinet-FGT-7.0" name="FW"><interface id="0" name="port1" type="ethernet" network_id="1"/></node>`)
	l, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	r := Convert(l, b, "x.unl")
	if len(r.Blocking) != 1 || !strings.Contains(r.Blocking[0], "FW: qemu (fortinet)") {
		t.Fatalf("%v", r.Blocking)
	}
	if _, err := Parse([]byte("<lab/>")); err == nil {
		t.Fatal("lab vazio aceito")
	}
}

// TestRemarks: a switch running a router image and a configuration of an
// interface the device lacks are pointed out, not changed.
func TestRemarks(t *testing.T) {
	sw := &Node{Name: "SW04", Image: "i86bi_linux-adventerprisek9-ms.154-1.T.bin"}
	if r := remarks(sw, "iol", []string{"Ethernet0/0"}); len(r) != 1 || !strings.Contains(r[0], "IOL L3, um roteador; sem configuração") {
		t.Fatalf("%q", r)
	}
	l2 := &Node{Name: "SW01", Image: "i86bi_Linux-L2-Adventerprisek9-ms.SSA.high_iron_20190423.bin"}
	if r := remarks(l2, "iol", nil); len(r) != 0 {
		t.Fatalf("L2: %q", r)
	}
	r1 := &Node{Name: "R1", Config: "interface FastEthernet0/0\n no shutdown\ninterface FastEthernet4/0\ninterface Loopback0\n"}
	if r := remarks(r1, "dyn", []string{"FastEthernet0/0", "FastEthernet1/0"}); len(r) != 1 || !strings.Contains(r[0], "FastEthernet4/0") {
		t.Fatalf("R1: %q", r)
	}
}

// TestConvertInjection: a .unl is an untrusted file. Line breaks in its
// lab name or in an image name must not add lines (a services: block with a
// privileged container) to the compose; the original keeps a safe name.
func TestConvertInjection(t *testing.T) {
	evil := "x&#10;services:&#10;  pwn:&#10;    image: alpine&#10;    privileged: true&#10;    volumes: [&quot;/:/host&quot;]&#10;#"
	b := []byte(strings.Replace(string(sample("")), `<lab name="Teste OSPF"`, `<lab name="`+evil+`"`, 1))
	b = []byte(strings.Replace(string(b), `image="c7200-adventerprisek9-mz.152-4.S6.image"`, `image="c7200.image&#10;services: {pwn: {image: alpine}}"`, 1))
	l, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(l.Name, "\n") {
		t.Fatal("o teste devia ter uma quebra de linha no nome")
	}
	r := Convert(l, b, "../../x\\..\\lab.unl")
	p, err := composex.Parse([]byte(r.Compose))
	if err != nil {
		t.Fatalf("%v\n%s", err, r.Compose)
	}
	if len(p.Services) != 0 {
		t.Fatalf("serviço injetado:\n%s", r.Compose)
	}
	for f := range r.Files {
		if strings.Contains(f, "..") || strings.Count(f, "/") > 1 {
			t.Fatalf("arquivo fora da pasta: %q", f)
		}
	}
}

func TestConvertNotas(t *testing.T) {
	b := sample("")
	l, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	r := Convert(l, b, "teste.unl")
	p, err := composex.Parse([]byte(r.Compose))
	if err != nil {
		t.Fatalf("%v\n%s", err, r.Compose)
	}
	if len(p.Notas) != 2 {
		t.Fatalf("notas: %+v\n%s", p.Notas, r.Compose)
	}
	if !strings.Contains(p.Notas[0].Text, "| PC01 | 192.168.1.10 |") || !strings.Contains(p.Notas[0].Text, "|---|---|") {
		t.Errorf("tabela: %q", p.Notas[0].Text)
	}
	n := p.Notas[1]
	if !strings.Contains(n.Text, "Foto") || !strings.Contains(n.Text, "![](notas/nota-2-1.png)") || len(n.Size) != 2 || n.Size[0] != 310 || n.Size[1] != 440 {
		t.Errorf("nota com imagem: %+v", n)
	}
	if len(n.Pos) != 2 {
		t.Errorf("posição: %v", n.Pos)
	}
	if img := r.Files["notas/nota-2-1.png"]; !strings.HasPrefix(img, "\x89PNG") {
		t.Errorf("imagem: %q", img)
	}
}
