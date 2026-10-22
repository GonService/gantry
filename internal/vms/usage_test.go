package vms

import (
	"encoding/base64"
	"strings"
	"testing"

	"gantry/internal/composex"
)

func TestParseCPUTime(t *testing.T) {
	for in, want := range map[string]float64{"12.34": 12.34, "0:01.23": 1.23, "1:02.5": 62.5, "00:01:02": 62, "1-02:03:04": 93784} {
		got, ok := parseCPUTime(in)
		if !ok || got != want {
			t.Errorf("%q = %v %v, quero %v", in, got, ok, want)
		}
	}
	for _, in := range []string{"-", "", "a:b"} {
		if _, ok := parseCPUTime(in); ok {
			t.Errorf("%q deveria falhar", in)
		}
	}
}

func TestParseUsages(t *testing.T) {
	u := parseUsages("lab web 3.50 204800 1048576 1024 2\nlab db - 0 0 512 1\nlixo\n")
	if len(u) != 1 {
		t.Fatalf("%+v", u)
	}
	if u[0].Project != "lab" || u[0].Name != "web" || u[0].CPUSec != 3.5 || u[0].RSS != 200<<20 || u[0].Disk != 1<<30 || u[0].Mem != 1<<30 || u[0].CPUs != 2 {
		t.Errorf("%+v", u[0])
	}
}

func TestLANEnv(t *testing.T) {
	p, err := composex.Parse([]byte("x-gantry:\n  lan: true\nservices:\n  app:\n    image: x\nx-vms:\n  web:\n    image: debian-12\n  db:\n    image: debian-12\n  solo:\n    image: debian-12\n    lan: false\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !p.UsesLAN() || !p.VM("web").LAN || p.VM("solo").LAN {
		t.Fatalf("lan: %+v %+v", p.VM("web"), p.VM("solo"))
	}
	net := &Net{Gateway: "10.213.0.1", IPs: map[string]string{"web": "10.213.0.100", "db": "10.213.0.101"}, Services: []string{"app"}}
	env, err := Env("lab", p.VM("web"), net)
	if err != nil {
		t.Fatal(err)
	}
	if env["GV_LAN"] != "1" || env["GV_LANMAC"] != LANMAC("lab", "web") || env["GV_NETCFG"] == "" {
		t.Fatalf("env: %v", env)
	}
	nc, _ := base64.StdEncoding.DecodeString(env["GV_NETCFG"])
	if !strings.Contains(string(nc), `addresses: ["10.213.0.100/24"]`) || !strings.Contains(string(nc), "52:54:00:12:34:56") {
		t.Errorf("network-config:\n%s", nc)
	}
	ud, _ := base64.StdEncoding.DecodeString(env["GV_USERDATA"])
	for _, want := range []string{"manage_etc_hosts: false", "10.213.0.101 db # gantry-lan", "10.213.0.1 gantry-host app # gantry-lan", "127.0.1.1 web # gantry-lan"} {
		if !strings.Contains(string(ud), want) {
			t.Errorf("user-data sem %q:\n%s", want, ud)
		}
	}
	if strings.Contains(string(ud), "10.213.0.100 web") {
		t.Error("a VM não deve listar a si mesma pelo IP da LAN")
	}
	// A VM off the network gets nothing of it.
	env, _ = Env("lab", p.VM("solo"), net)
	if env["GV_LAN"] != "" || env["GV_NETCFG"] != "" {
		t.Errorf("solo: %v", env)
	}
	if LANMAC("lab", "web") == LANMAC("lab", "db") {
		t.Error("MACs iguais")
	}
	ni := parseNetInfo("mode=bridge\nbridge=gantry0\ngateway=10.213.0.1\nprefix=10.213.0\nproblem=falta x\n")
	if ni.Mode != "bridge" || ni.Gateway != "10.213.0.1" || len(ni.Problem) != 1 {
		t.Errorf("netinfo: %+v", ni)
	}
}

// TestNetworkConfigPorts: the addresses of the ports on lab networks and
// cables go to the network-config; a port with none is left alone.
func TestNetworkConfigPorts(t *testing.T) {
	p, err := composex.Parse([]byte(`
x-redes:
  lan-a: {subnet: 10.0.1.0/24, gateway: 10.0.1.1, dhcp: true, dns: [1.1.1.1]}
x-vms:
  web:
    image: debian-12
    interfaces:
      - {rede: lan-a, ip: [10.0.1.10, 10.0.1.11]}
      - lan-a
      - {rede: lan-a, ip: dhcp}
  srv:
    image: debian-12
    management: false
    interfaces: [{rede: lan-a, ip: 10.0.1.20/24}]
  plain:
    image: debian-12
    interfaces: [lan-a]
`))
	if err != nil {
		t.Fatal(err)
	}
	env, err := Env("lab", p.VM("web"), nil)
	if err != nil {
		t.Fatal(err)
	}
	nc, _ := base64.StdEncoding.DecodeString(env["GV_NETCFG"])
	for _, w := range []string{"wan:", `addresses: ["10.0.1.10/24", "10.0.1.11/24"]`, IfaceMAC("lab", "web", 0), "  p2:\n", "dhcp4-overrides: {route-metric: 200}", "nameservers: {addresses: [1.1.1.1]}"} {
		if !strings.Contains(string(nc), w) {
			t.Errorf("web sem %q:\n%s", w, nc)
		}
	}
	if strings.Contains(string(nc), "p1:") || strings.Contains(string(nc), "routes:") || strings.Contains(string(nc), "set-name") {
		t.Errorf("web:\n%s", nc)
	}
	env, _ = Env("lab", p.VM("srv"), nil)
	nc, _ = base64.StdEncoding.DecodeString(env["GV_NETCFG"])
	if strings.Contains(string(nc), "wan:") || !strings.Contains(string(nc), "routes: [{to: default, via: 10.0.1.1}]") {
		t.Errorf("srv:\n%s", nc)
	}
	if env, _ = Env("lab", p.VM("plain"), nil); env["GV_NETCFG"] != "" {
		t.Errorf("plain: %q", env["GV_NETCFG"])
	}
	rt := RelayTable("lab", p)
	if !strings.Contains(rt, "#seg lan-a 10.0.1.0/24 10.0.1.1 10.0.1.100 10.0.1.199 10.0.1.254 1.1.1.1\n") || !strings.Contains(rt, " seg:lan-a web 1") {
		t.Errorf("relay:\n%s", rt)
	}
	if downArg(p.VM("plain")) != "" {
		t.Error("uma porta numa rede não fica sem cabo")
	}
}
