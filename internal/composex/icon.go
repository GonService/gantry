package composex

import (
	"fmt"
	"strings"
)

// Icons are the ways a node can be drawn in the lab's topology.
var Icons = []string{"router", "switch", "l3switch", "firewall", "server", "pc", "container", "cloud", "appliance"}

func parseIcon(where string, v any) (string, error) {
	s := strings.ToLower(strings.TrimSpace(interpolate(str(v))))
	if s == "" {
		return "", nil
	}
	for _, ic := range Icons {
		if s == ic {
			return s, nil
		}
	}
	return "", fmt.Errorf("%s: ícone desconhecido %q (use %s)", where, s, strings.Join(Icons, ", "))
}

// IconOf is how a node is drawn: the icon it asks for, else the one of
// its kind (IOL-L2 and IOSvL2 are switches, the other Cisco images
// routers, a container a container, a VM a server).
func IconOf(v *VM) string {
	if v.Icon != "" {
		return v.Icon
	}
	img := strings.ToLower(v.Image)
	cat := v.Catalog
	if cat == "" {
		cat = img
	}
	switch {
	case v.Kind == "ctr":
		return "container"
	case v.Kind == "vpcs":
		return "pc"
	case strings.Contains(cat, "l2") || strings.Contains(img, "switch"):
		return "l3switch"
	case v.Kind == "iol" || v.Kind == "dyn" || strings.HasPrefix(cat, "cisco-"):
		return "router"
	}
	for _, fw := range []string{"asav", "fortigate", "fgt", "pfsense", "opnsense", "paloalto", "pa-vm", "vsrx", "checkpoint"} {
		if strings.Contains(img, fw) {
			return "firewall"
		}
	}
	for _, sw := range []string{"vyos", "mikrotik", "chr-", "routeros", "csr", "xrv", "veos", "nxos"} {
		if strings.Contains(img, sw) {
			return "router"
		}
	}
	if v.NoCloudInit && v.ISO != "" {
		return "appliance"
	}
	return "server"
}
