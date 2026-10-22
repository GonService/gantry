package tap

import "testing"

func TestParsePort(t *testing.T) {
	p, err := ParsePort("eth1,30010,52:54:10:aa:bb:cc,10.0.12.10/24")
	if err != nil || p.Name != "eth1" || p.Relay != 30010 || p.MAC.String() != "52:54:10:aa:bb:cc" || p.Addr.String() != "10.0.12.10/24" || p.Addr.IP.String() != "10.0.12.10" {
		t.Fatalf("%+v %v", p, err)
	}
	if p, err = ParsePort("eth2,30012,,"); err != nil || p.Addr != nil || p.MAC != nil {
		t.Fatalf("%+v %v", p, err)
	}
	for _, bad := range []string{"eth1", "eth1,x", "eth1,1,zz", "eth1,1,,10.0.0.1", "eth1,1,,fe80::1/64", ",1"} {
		if _, err := ParsePort(bad); err == nil {
			t.Errorf("aceitou %q", bad)
		}
	}
}

func TestDefaultGateway(t *testing.T) {
	route := "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n" +
		"eth0\t0011A8C0\t00000000\t0001\t0\t0\t0\t00FFFFFF\t0\t0\t0\n" +
		"eth0\t00000000\t01001BAC\t0003\t0\t0\t0\t00000000\t0\t0\t0\n"
	if gw := defaultGateway(route); gw != "172.27.0.1" {
		t.Fatal(gw)
	}
	if gw := defaultGateway("x\n"); gw != "" {
		t.Fatal(gw)
	}
}

func TestParsePortMore(t *testing.T) {
	p, err := ParsePort("eth1,30010,,10.0.1.10/24+10.0.1.11/24+10.0.9.1/30")
	if err != nil || p.Addr.String() != "10.0.1.10/24" || len(p.More) != 2 || p.More[1].String() != "10.0.9.1/30" || p.More[1].IP.String() != "10.0.9.1" {
		t.Fatal(p, err)
	}
	if _, err := ParsePort("eth1,30010,,10.0.1.10/24+x"); err == nil {
		t.Fatal("aceitou")
	}
}
