package server

import (
	"strings"
	"testing"

	"gantry/internal/composex"
)

func TestRedesEdit(t *testing.T) {
	src := "x-vms:\n  a:\n    image: debian-12\n    interfaces: [none]\n\nx-redes:\n  lan:   {subnet: 10.0.1.0/24, dns: [1.1.1.1, 8.8.8.8]}  # a do escritório\n  wan:\n    subnet: 10.9.0.0/24\n\nservices:\n  web:\n    image: nginx\n"
	texts := map[string]string{"f": src}
	files := []string{"f"}
	if err := setPositions(texts, files, map[string][]int{"lan": {10, 20}, "wan": {30, 40}, "a": {1, 2}}); err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"  lan: {subnet: 10.0.1.0/24, dns: [1.1.1.1, 8.8.8.8], pos: [10, 20]} # a do escritório\n", "  wan:\n    subnet: 10.9.0.0/24\n    pos: [30, 40]\n", "    pos: [1, 2]\n"} {
		if !strings.Contains(texts["f"], w) {
			t.Errorf("sem %q:\n%s", w, texts["f"])
		}
	}
	putRede(texts, files, "dmz", redeFlow(&composex.Rede{Subnet: "10.0.5.0/24", DHCP: true, DHCPFrom: "10.0.5.10", DHCPTo: "10.0.5.20"}))
	putRede(texts, files, "wan", "")
	if err := setIfaces(texts, files, "a", []composex.VMIface{{Net: "dmz", IPs: []string{"10.0.5.2/24", "10.0.5.3/24"}}, {Net: "lan", DHCP: true}, {Link: "a-b", IPs: []string{"1.1.1.1/30"}}, {Link: "x"}}); err != nil {
		t.Fatal(err)
	}
	out := texts["f"]
	for _, w := range []string{"  dmz: {subnet: 10.0.5.0/24, dhcp: 10.0.5.10-10.0.5.20}\n\nservices:", "interfaces: [{rede: dmz, ip: [10.0.5.2/24, 10.0.5.3/24]}, {rede: lan, ip: dhcp}, {link: a-b, ip: 1.1.1.1/30}, x]"} {
		if !strings.Contains(out, w) {
			t.Errorf("sem %q:\n%s", w, out)
		}
	}
	if strings.Contains(out, "wan") {
		t.Errorf("wan ficou:\n%s", out)
	}
	// A file with no x-redes gets the section at the end.
	texts = map[string]string{"g": "services:\n  web:\n    image: nginx\n"}
	putRede(texts, []string{"g"}, "lan", "{}")
	if texts["g"] != "services:\n  web:\n    image: nginx\n\nx-redes:\n  lan: {}\n" {
		t.Errorf("%q", texts["g"])
	}
	if p, err := composex.Parse([]byte(texts["g"])); err != nil || p.Redes["lan"] == nil {
		t.Fatal(err)
	}
}

// An entry on one line keeps being one line: pos and the ports go inside
// its braces (dragging a node used to write pos: under it, breaking the
// file).
func TestFlowEntryEdit(t *testing.T) {
	src := "name: t\nx-vms:\n  SW1: {image: cisco-iol-l2, interfaces: [sw1-sw2, none]}\n  SW2: {image: cisco-iol-l2, interfaces: [sw1-sw2, none]}\n"
	texts := map[string]string{"f": src}
	if err := setPositions(texts, []string{"f"}, map[string][]int{"SW1": {48, 44}, "SW2": {300, 50}}); err != nil {
		t.Fatal(err)
	}
	if err := setIfaces(texts, []string{"f"}, "SW2", []composex.VMIface{{Link: "sw1-sw2"}, {Net: "lan"}}); err != nil {
		t.Fatal(err)
	}
	want := "name: t\nx-vms:\n  SW1: {image: cisco-iol-l2, interfaces: [sw1-sw2, none], pos: [48, 44]}\n  SW2: {image: cisco-iol-l2, interfaces: [sw1-sw2, {rede: lan}], pos: [300, 50]}\n"
	if texts["f"] != want {
		t.Fatalf("%q", texts["f"])
	}
	if _, err := composex.Parse([]byte(texts["f"] + "x-redes:\n  lan: {}\n")); err != nil {
		t.Fatal(err)
	}
}
