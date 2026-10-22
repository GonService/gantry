// Package tap puts the ports of a container on the cables of a lab: each
// port is a TAP interface (eth1, eth2…) inside the container, and its frames
// go to the project's relay over UDP, like the ports of the VMs. It runs as
// "gantry-agent tap" in a sidecar that shares the container's network.
package tap

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// Port is one TAP port: its name, the relay port it talks to, its MAC and
// (optionally) its IPv4 address with the mask.
type Port struct {
	Name  string
	Relay int
	MAC   net.HardwareAddr
	Addr  *net.IPNet
	// More are the other addresses of the port (as aliases eth1:1…).
	More []*net.IPNet
}

// ParsePort reads "eth1,30010,52:54:00:aa:bb:cc,10.0.12.10/24" (the MAC
// and the address may be empty; more addresses go joined by +).
func ParsePort(s string) (Port, error) {
	f := strings.Split(s, ",")
	if len(f) < 2 {
		return Port{}, fmt.Errorf("porta %q: use nome,porta-do-relay[,mac[,ip/máscara]]", s)
	}
	p := Port{Name: f[0]}
	if p.Name == "" || len(p.Name) > 15 {
		return Port{}, fmt.Errorf("porta %q: nome inválido", s)
	}
	n, err := strconv.Atoi(f[1])
	if err != nil || n <= 0 || n > 65535 {
		return Port{}, fmt.Errorf("porta %q: porta do relay inválida", s)
	}
	p.Relay = n
	if len(f) > 2 && f[2] != "" {
		if p.MAC, err = net.ParseMAC(f[2]); err != nil {
			return Port{}, fmt.Errorf("porta %q: MAC inválido", s)
		}
	}
	if len(f) > 3 && f[3] != "" {
		for i, a := range strings.Split(f[3], "+") {
			ip, nw, err := net.ParseCIDR(a)
			if err != nil || ip.To4() == nil {
				return Port{}, fmt.Errorf("porta %q: endereço inválido (use 10.0.0.2/24)", s)
			}
			nw.IP = ip.To4()
			if i == 0 {
				p.Addr = nw
			} else if i < 10 {
				p.More = append(p.More, nw)
			}
		}
	}
	return p, nil
}

// RelayHost finds where the relay listens, seen from the container: the
// GANTRY_RELAY variable, host.containers.internal (Podman) or the default
// gateway (the engine's bridge, Docker).
func RelayHost(given string) (string, error) {
	if given != "" && given != "auto" {
		return given, nil
	}
	if v := os.Getenv("GANTRY_RELAY"); v != "" {
		return v, nil
	}
	for _, n := range []string{"host.containers.internal", "host.docker.internal"} {
		if ips, err := net.LookupHost(n); err == nil && len(ips) > 0 {
			return ips[0], nil
		}
	}
	b, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return "", err
	}
	if gw := defaultGateway(string(b)); gw != "" {
		return gw, nil
	}
	return "", errors.New("não achei o relay: sem rota padrão no container (use --relay)")
}

// defaultGateway reads /proc/net/route (hex, little endian) for 0.0.0.0/0.
func defaultGateway(route string) string {
	for _, l := range strings.Split(route, "\n")[1:] {
		f := strings.Fields(l)
		if len(f) < 3 || f[1] != "00000000" {
			continue
		}
		v, err := strconv.ParseUint(f[2], 16, 32)
		if err != nil || v == 0 {
			continue
		}
		return net.IPv4(byte(v), byte(v>>8), byte(v>>16), byte(v>>24)).String()
	}
	return ""
}
