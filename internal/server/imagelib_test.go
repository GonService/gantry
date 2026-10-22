package server

import (
	"testing"

	"gantry/internal/composex"
)

func TestClassifyFound(t *testing.T) {
	f := classifyFound([]string{
		"/home/m/gantry/imagens/i86bi_LinuxL2-AdvEnterpriseK9-M_152_May_2018.bin",
		"/home/m/gantry/imagens/i86bi_LinuxL3-AdvEnterpriseK9-M2_157_3_May_2018.bin",
		"/home/m/gantry/imagens/iourc",
		"/opt/unetlab/addons/qemu/vios-adventerprisek9-m.spa.159-3.m6/virtioa.qcow2",
		"/opt/unetlab/addons/qemu/fortinet-FGT-v7.2/virtioa.qcow2",
		"/opt/unetlab/addons/iol/bin/L3-ADVENTERPRISEK9-M-15.5-2T.bin",
		"",
	})
	if len(f) != 6 {
		t.Fatal(f)
	}
	if pickCatalog(f, "cisco-iol-l2") != "/home/m/gantry/imagens/i86bi_LinuxL2-AdvEnterpriseK9-M_152_May_2018.bin" || pickCatalog(f, "cisco-iourc") != "/home/m/gantry/imagens/iourc" {
		t.Error(f)
	}
	if pickCatalog(f, "cisco-iosv") != "/opt/unetlab/addons/qemu/vios-adventerprisek9-m.spa.159-3.m6/virtioa.qcow2" {
		t.Error("EVE-NG", f)
	}
	if x := pickName(f, "fortinet-FGT-v7.2"); x == nil || x.Catalog != "" {
		t.Error("o nome de uma imagem do EVE-NG é a pasta", f)
	}
	// The last IOL by name wins (here the one in the EVE-NG folder).
	if pickCatalog(f, "cisco-iol") != "/opt/unetlab/addons/iol/bin/L3-ADVENTERPRISEK9-M-15.5-2T.bin" {
		t.Error(pickCatalog(f, "cisco-iol"))
	}
	// A VM can name a file of the folders; what it is comes from its name.
	p, err := composex.Parse([]byte("x-vms:\n  r1:\n    image: i86bi_LinuxL2-AdvEnterpriseK9-M_152_May_2018.bin\n    interfaces: [none]\n"))
	if err != nil || p.VM("r1").Kind != "iol" || p.VM("r1").Catalog != "cisco-iol-l2" {
		t.Fatal(err, p.VM("r1"))
	}
}
