package server

import "testing"

func TestVPCSAddr(t *testing.T) {
	for cfg, want := range map[string]string{
		"set pcname PC01\nip 10.1.1.10/24 10.1.1.1\n":             "10.1.1.10/24 gw 10.1.1.1",
		"ip 10.1.1.10 255.255.255.192 10.1.1.1":                   "10.1.1.10/26 gw 10.1.1.1",
		"ip 10.1.1.10 24":                                         "10.1.1.10/24",
		"ip 10.1.1.10/24 10.1.1.1\nip dhcp":                       "DHCP",
		"ip6 2001:db8::10/64\nset pcname X":                       "",
		"# comentário\nip 192.168.0.5/24 192.168.0.1 dns 8.8.8.8": "192.168.0.5/24 gw 192.168.0.1",
	} {
		if got := vpcsAddr(cfg); got != want {
			t.Errorf("%q: %q, esperava %q", cfg, got, want)
		}
	}
}
