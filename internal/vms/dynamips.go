package vms

import (
	"fmt"
	"strings"

	"gantry/internal/composex"
)

// Dynamips runs the IOS of the classic routers (7200, 3725, 2691) as a
// program on the machine, like IOL; each Ethernet port is a UDP NIO whose
// other end is the cables' relay (the same as a QEMU VM's port).

// dynPorts are the router's Ethernet ports, in the order of interfaces:
// (the adapters of slots:, or the default ones).
func dynPorts(v *composex.VM) []composex.DynPort {
	ports, err := composex.DynLayout(v.Platform, v.Slots)
	if err != nil {
		ports, _ = composex.DynLayout(v.Platform, nil)
	}
	return ports
}

// DynPortNames are the short names of the router's ports (f0/0, f1/0…),
// one per port of interfaces:.
func DynPortNames(v *composex.VM) []string {
	ports := dynPorts(v)
	out := make([]string, 0, len(v.Interfaces))
	for i := range v.Interfaces {
		if i < len(ports) {
			out = append(out, ports[i].Short())
		}
	}
	return out
}

// DynArgs are the adapters and NIOs of a Dynamips node: each port sends to
// its relay port and listens on its own. With slots: every adapter named
// goes in (as on the original router); without, only the slots in use.
func DynArgs(project string, v *composex.VM) string {
	ensurePorts(project, v)
	ports := dynPorts(v)
	var out []string
	seen := map[int]bool{}
	card := func(p composex.DynPort) {
		if !seen[p.Slot] {
			seen[p.Slot] = true
			out = append(out, fmt.Sprintf("-p %d:%s", p.Slot, p.Adapter))
		}
	}
	if v.Slots != nil {
		for _, p := range ports {
			card(p)
		}
	}
	for i, f := range v.Interfaces {
		if i >= len(ports) {
			break
		}
		p := ports[i]
		card(p)
		out = append(out, fmt.Sprintf("-s %d:%d:udp:%d:127.0.0.1:%d", p.Slot, p.Port, f.VPort, f.RPort))
	}
	return strings.Join(out, " ")
}
