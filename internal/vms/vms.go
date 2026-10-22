// Package vms runs the QEMU virtual machines of a lab (x-vms) on the lab's
// machine: the Mac itself or a server over SSH. The work is done by an
// embedded shell script, so nothing has to be installed besides QEMU.
package vms

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"

	"gantry/internal/composex"
	"gantry/internal/hosts"
	"gantry/internal/vmscript"
)

// script is vmctl.sh, shared with the agent.
var script = vmscript.Script

// State is a VM as the machine reports it.
type State struct {
	Name    string   `json:"name"`
	State   string   `json:"state"` // running, stopped
	PID     int      `json:"pid,omitempty"`
	SSHPort int      `json:"sshPort,omitempty"`
	Accel   string   `json:"accel,omitempty"` // hvf, kvm, tcg
	Ports   []string `json:"ports"`           // proto:ip:host:guest
	VNC     int      `json:"vnc,omitempty"`   // screen port, for VMs installed from an ISO
	LAN     string   `json:"lan,omitempty"`   // address on the project network
	// Autostart: the VM starts with the machine (boot hook).
	Autostart bool `json:"autostart,omitempty"`
	// Spec is the hash of the definition the VM was started with (GV_HASH).
	Spec string `json:"-"`
	// NixSpec is the hash of the Nix files the VM has (GV_NIXHASH, from its
	// creation or the last Aplicar that worked), for a VM with nix:.
	NixSpec string `json:"-"`
	// BaseSpec is the definition without the Nix files (GV_BASEHASH).
	BaseSpec string `json:"-"`
	// Serial is the port of the interactive serial console (0 for a VM
	// started before it existed).
	Serial int `json:"serial,omitempty"`
	// NoCloudInit: no gantry user in the VM, so no Shell nor Logs over SSH.
	NoCloudInit bool `json:"noCloudInit,omitempty"`
	// Rejected is how many lines of the startup configuration the IOS of
	// a network device refused when it started (Rejected lists them).
	Rejected int `json:"rejected,omitempty"`
	// NoIdlePC: a Dynamips router whose image has no idle-PC yet.
	NoIdlePC bool `json:"noIdlePC,omitempty"`
}

// LocalImage is an image of the catalog that is not downloaded: Gantry
// builds it (images/ in the repository) and the user says, per machine,
// where its file is.
type LocalImage struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Build string `json:"build,omitempty"` // how to build it (images Gantry builds)
	// Source is where the user gets it (vendor images Gantry cannot
	// download: their license does not allow redistribution).
	Source string `json:"source,omitempty"`
}

// LocalImages are the images whose file the user points to.
var LocalImages = []LocalImage{
	{Name: "nixos-lab-26.05", Label: "NixOS 26.05 (imagem base do Gantry)", Build: "nix build ./images/nixos#packages.<system>.base-qcow2 · images/nixos/README.md"},
	{Name: "cisco-iosvl2", Label: "Cisco IOSvL2 (switch L2/L3)", Source: "Cisco Modeling Labs (CML), refplat: vios_l2-adventerprisek9-m…qcow2"},
	{Name: "cisco-iosv", Label: "Cisco IOSv (roteador)", Source: "Cisco Modeling Labs (CML), refplat: vios-adventerprisek9-m…qcow2 (um .vmdk vira qcow2 em Importar disco)"},
	{Name: "cisco-csr1000v", Label: "Cisco CSR 1000v (roteador)", Source: "Cisco Modeling Labs (CML), refplat: csr1000v-universalk9…qcow2, ou software.cisco.com com contrato"},
	{Name: "cisco-iol", Label: "Cisco IOL (roteador, IOS on Linux)", Source: "Cisco Modeling Labs 2.7+ (CML), refplat › iol-xe: x86_64_crb_linux-adventerprisek9-ms…; só em Linux x86_64"},
	{Name: "cisco-iol-l2", Label: "Cisco IOL-L2 (switch, IOS on Linux)", Source: "Cisco Modeling Labs 2.7+ (CML), refplat › ioll2-xe: x86_64_crb_linux_l2-adventerprisek9-ms…; só em Linux x86_64"},
	{Name: "cisco-c7200", Label: "Cisco 7200 (roteador, Dynamips)", Source: "a imagem do IOS do 7200 que você tem (c7200-adventerprisek9-mz…image ou .bin); o Gantry não distribui IOS"},
	{Name: "cisco-c3725", Label: "Cisco 3725 (roteador, Dynamips)", Source: "a imagem do IOS do 3725 que você tem (c3725-adventerprisek9-mz…bin)"},
	{Name: "cisco-c2691", Label: "Cisco 2691 (roteador, Dynamips)", Source: "a imagem do IOS do 2691 que você tem (c2691-adventerprisek9-mz…bin)"},
	{Name: IOLLicenseName, Label: "Licença do IOL (iourc), se a sua imagem pedir", Source: "o arquivo iourc que veio com a sua licença; o Gantry não gera licenças"},
}

// IOLLicenseName is the catalog entry of the IOL license file (not an
// image: the file IOL reads through IOURC).
const IOLLicenseName = "cisco-iourc"

// IsLocalImage tells whether the image is one whose file the user points to.
func IsLocalImage(name string) bool {
	for _, im := range LocalImages {
		if im.Name == name {
			return true
		}
	}
	return false
}

// IsNixOS tells whether the image is a Gantry NixOS image (UEFI, nix:).
func IsNixOS(image string) bool { return strings.HasPrefix(image, "nixos-") }

// SerialPort is where the serial console of a VM is served on the machine
// (127.0.0.1 only), for the interactive Console.
func SerialPort(project string, v *composex.VM) int {
	return 17000 + (SSHPort(project, v)-22200+800)%800
}

// The cables of a network lab go through a relay on the machine: each port
// of a VM is a UDP socket of QEMU (the VM's port) that talks to a socket of
// the relay (the relay's port), and the relay forwards what arrives to the
// port at the other end of the cable. Changing a cable changes only the
// relay's table, so the VMs keep running (and the two ports see the link
// go down and up, as with a real cable).

// ifacePorts is the stable pair of UDP ports of the i-th port of a VM.
func ifacePorts(ns, vm string, i int) (relay, own int) {
	h := fnv.New32a()
	h.Write([]byte(ns + "/" + vm + "/" + strconv.Itoa(i)))
	base := 30000 + int(h.Sum32()%7000)*2
	return base, base + 1
}

// iolID is the stable IOL instance number of a node (1..767); relayID
// the one of the project's relay (768..1023). IOL takes instance numbers
// up to 1023 only: a NETMAP line with a larger one is ignored, and the
// port sends nothing. IOL talks to its peers by
// these numbers (/tmp/netio<uid>/<n>), so they must not repeat on the
// machine.
func iolID(ns, vm string) int {
	h := fnv.New32a()
	h.Write([]byte(ns + "/" + vm))
	return 1 + int(h.Sum32()%767)
}

func relayID(ns string) int {
	h := fnv.New32a()
	h.Write([]byte(ns))
	return 768 + int(h.Sum32()%256)
}

// AssignPorts gives every relay port of a project its UDP ports, without
// two of them falling on the same ones, and the IOL nodes their instance
// numbers (ns tells projects apart: the project's folder).
func AssignPorts(ns string, p *composex.Project) {
	p.RelayID = relayID(ns)
	ids := map[int]bool{}
	for _, v := range p.VMs {
		if v.Kind == "iol" {
			id := iolID(ns, v.Name)
			for ids[id] {
				id = id%767 + 1
			}
			ids[id] = true
			v.IOLID, v.IOLRelay = id, p.RelayID
		}
	}
	// VPCS: the last byte of each one's MAC (00:50:79:66:68:xx), apart.
	var pcs []string
	for _, v := range p.VMs {
		if v.Kind == "vpcs" {
			pcs = append(pcs, v.Name)
		}
	}
	sortStrings(pcs)
	for i, n := range pcs {
		p.VM(n).IOLID = i%240 + 1
	}
	taken := map[int]bool{}
	names := make([]string, 0, len(p.VMs))
	by := map[string]*composex.VM{}
	for _, v := range p.Nodes() {
		names = append(names, v.Name)
		by[v.Name] = v
	}
	sortStrings(names)
	for _, n := range names {
		v := by[n]
		for i := range v.Interfaces {
			if v.Interfaces[i].Bridge != "" {
				continue
			}
			r, o := ifacePorts(ns, n, i)
			for taken[r] || taken[o] {
				r, o = r+2, o+2
				if o >= 44000 {
					r, o = 30000, 30001
				}
			}
			taken[r], taken[o] = true, true
			v.Interfaces[i].RPort, v.Interfaces[i].VPort = r, o
		}
	}
}

// ensurePorts fills the ports of a VM whose project did not go through
// AssignPorts (a definition parsed alone).
func ensurePorts(project string, v *composex.VM) {
	if v.Kind == "iol" && v.IOLID == 0 {
		v.IOLID = iolID(project, v.Name)
	}
	if v.Kind == "vpcs" && v.IOLID == 0 {
		v.IOLID = iolID(project, v.Name)%240 + 1
	}
	for i := range v.Interfaces {
		if v.Interfaces[i].Bridge == "" && v.Interfaces[i].RPort == 0 {
			v.Interfaces[i].RPort, v.Interfaces[i].VPort = ifacePorts(project, v.Name, i)
		}
	}
}

// IfaceMAC is the stable MAC of the i-th extra interface of a VM.
func IfaceMAC(project, name string, i int) string {
	h := fnv.New32a()
	h.Write([]byte(project + "/" + name))
	x := h.Sum32()
	return fmt.Sprintf("52:54:%02x:%02x:%02x:%02x", 0x10+i, byte(x>>16), byte(x>>8), byte(x))
}

// ifacesArg is GV_IFACES: one word per extra NIC, in order. A port of the
// relay is the same whatever cable it has, so changing cables does not
// change the VM's definition.
func ifacesArg(project string, v *composex.VM) string {
	ensurePorts(project, v)
	var out []string
	for i, f := range v.Interfaces {
		mac := IfaceMAC(project, v.Name, i)
		if f.Bridge != "" {
			out = append(out, "bridge:"+f.Bridge+":"+mac)
		} else {
			out = append(out, fmt.Sprintf("relay:%d:%d:%s", f.RPort, f.VPort, mac))
		}
	}
	return strings.Join(out, " ")
}

// downArg is GV_DOWN: the ports with no cable, whose link starts down.
func downArg(v *composex.VM) string {
	var out []string
	for i, f := range v.Interfaces {
		if f.Bridge == "" && f.Link == "" && f.Net == "" {
			out = append(out, strconv.Itoa(i))
		}
	}
	return strings.Join(out, " ")
}

// RelayTable is the relay's table for a project: one line per relay port,
// "relay-port vm-port peer vm index", the peer the relay port at the other
// end of the cable (0: no cable) or seg:NAME (a lab network, a switch);
// each network a line "#seg NAME subnet gateway dhcp-from dhcp-to server
// dns" ("-" for what it does not have).
func RelayTable(project string, p *composex.Project) string {
	type end struct {
		v *composex.VM
		i int
	}
	ends := map[string][]end{}
	for _, v := range p.Nodes() {
		ensurePorts(project, v)
		for i, f := range v.Interfaces {
			if f.Link != "" {
				ends[f.Link] = append(ends[f.Link], end{v, i})
			}
		}
	}
	var lines []string
	for _, v := range p.Nodes() {
		for i, f := range v.Interfaces {
			if f.Bridge != "" {
				continue
			}
			own := strconv.Itoa(f.VPort)
			if v.Kind == "iol" {
				// An IOL port: the relay talks to the IOL instance itself.
				own = fmt.Sprintf("iol:%d:%d", v.IOLID, IOLPort(i))
			} else if v.Kind == "ctr" {
				// A container's port: the relay answers whoever sends from
				// the container (its address is only known then).
				own = "ctr"
			}
			peer := "0"
			for _, e := range ends[f.Link] {
				if f.Link != "" && !(e.v == v && e.i == i) {
					peer = strconv.Itoa(e.v.Interfaces[e.i].RPort)
				}
			}
			if f.Net != "" {
				peer = "seg:" + f.Net
			}
			lines = append(lines, fmt.Sprintf("%d %s %s %s %d", f.RPort, own, peer, v.Name, i))
		}
	}
	sortStrings(lines)
	var segs []string
	for _, n := range p.RedeNames {
		r := p.Redes[n]
		dash := func(x string) string {
			if x == "" {
				return "-"
			}
			return x
		}
		from, to, srv := "-", "-", "-"
		if r.DHCP {
			from, to, srv = r.DHCPFrom, r.DHCPTo, r.DHCPServer()
		}
		segs = append(segs, fmt.Sprintf("#seg %s %s %s %s %s %s %s", n, dash(r.Subnet), dash(r.Gateway), from, to, srv, dash(strings.Join(r.DNS, ","))))
	}
	lines = append(segs, lines...)
	if rid := relayIDOf(project, p); hasIOL(p) {
		lines = append([]string{fmt.Sprintf("#rid %d", rid)}, lines...)
	}
	return strings.Join(lines, "\n")
}

func hasIOL(p *composex.Project) bool {
	for _, v := range p.VMs {
		if v.Kind == "iol" {
			return true
		}
	}
	return false
}

func relayIDOf(project string, p *composex.Project) int {
	if p.RelayID == 0 {
		p.RelayID = relayID(project)
	}
	return p.RelayID
}

// IOLPort is the byte IOL uses for its i-th Ethernet port: Ethernet<bay>/
// <unit>, four units per bay, the unit in the high nibble.
func IOLPort(i int) int { return (i%4)<<4 | i/4 }

// IOLNetmap is the NETMAP of an IOL node: each of its ports goes to the
// project's relay, which is the cable.
func IOLNetmap(project string, v *composex.VM) string {
	ensurePorts(project, v)
	rid := v.IOLRelay
	if rid == 0 {
		rid = relayID(project)
	}
	var out []string
	for i := range v.Interfaces {
		out = append(out, fmt.Sprintf("%d:%d/%d %d:%d/%d", v.IOLID, i/4, i%4, rid, i/4, i%4))
	}
	return strings.Join(out, "\n") + "\n"
}

// HasRelay tells whether a project has ports that go through the relay.
func HasRelay(p *composex.Project) bool {
	for _, v := range p.Nodes() {
		for _, f := range v.Interfaces {
			if f.Bridge == "" {
				return true
			}
		}
	}
	return false
}

// SyncRelay gives the machine's relay of the project its table (starting
// the relay when needed). The ports whose cable changed see the link go
// down and come back up.
func SyncRelay(ctx context.Context, h *hosts.Host, project string, p *composex.Project) (string, error) {
	if !HasRelay(p) {
		return "", nil
	}
	return Run(ctx, h, "relay", map[string]string{"GV_PROJECT": project, "GV_RELAY": base64.StdEncoding.EncodeToString([]byte(RelayTable(project, p) + "\n"))})
}

// RelayMap is the table the machine's relay has now ("" when none).
func RelayMap(ctx context.Context, h *hosts.Host, project string) string {
	out, _ := Run(ctx, h, "relay-map", map[string]string{"GV_PROJECT": project})
	return strings.TrimSpace(out)
}

// VNCPort is where the screen of a VM booted from an ISO is served.
func VNCPort(project string, v *composex.VM) int {
	return 15900 + (SSHPort(project, v)-22200+800)%800
}

// SSHPort is the VM's SSH port on the host: the one in the file, or a
// stable one derived from the lab and VM names.
func SSHPort(project string, v *composex.VM) int {
	if v.SSH > 0 {
		return v.SSH
	}
	h := fnv.New32a()
	h.Write([]byte(project + "/" + v.Name))
	return 22200 + int(h.Sum32()%800)
}

// MetricsPort is the port of the host where the node_exporter of a VM with
// metrics: true answers (127.0.0.1 only).
func MetricsPort(project string, v *composex.VM) int {
	h := fnv.New32a()
	h.Write([]byte(project + "/" + v.Name + "/metrics"))
	return 19100 + int(h.Sum32()%800)
}

// nodeExporterScript installs and starts the node_exporter in a VM: the
// distribution's package, or the release from GitHub when there is none.
const nodeExporterScript = `set -e
if command -v apt-get >/dev/null 2>&1; then
  DEBIAN_FRONTEND=noninteractive apt-get install -y prometheus-node-exporter || true
elif command -v dnf >/dev/null 2>&1; then
  (dnf install -y epel-release && dnf install -y golang-github-prometheus-node-exporter && systemctl enable --now node_exporter) || true
fi
if ! (ss -ltn 2>/dev/null || netstat -ltn 2>/dev/null) | grep -q ':9100 '; then
  arch=$(uname -m); case "$arch" in x86_64) arch=amd64 ;; aarch64) arch=arm64 ;; esac
  v=1.8.2
  curl -fsSL "https://github.com/prometheus/node_exporter/releases/download/v$v/node_exporter-$v.linux-$arch.tar.gz" | tar xz -C /tmp
  install -m 755 "/tmp/node_exporter-$v.linux-$arch/node_exporter" /usr/local/bin/node_exporter
  printf '[Unit]\nDescription=node_exporter\n[Service]\nExecStart=/usr/local/bin/node_exporter\nRestart=always\n[Install]\nWantedBy=multi-user.target\n' >/etc/systemd/system/node_exporter.service
  systemctl daemon-reload && systemctl enable --now node_exporter
fi
`

// Net is the project network a VM joins (lan: true): its gateway (the
// machine) and the address of each VM and service name of the project.
type Net struct {
	Gateway  string            // the machine on the VM network
	IPs      map[string]string // VM name -> its address
	Services []string          // container services, reached through the gateway
}

// hostsLines are the /etc/hosts entries a VM of the project gets.
func (n *Net) hostsLines(self string) []string {
	var out []string
	out = append(out, "127.0.1.1 "+self)
	names := make([]string, 0, len(n.IPs))
	for name := range n.IPs {
		names = append(names, name)
	}
	sortStrings(names)
	for _, name := range names {
		if name != self {
			out = append(out, n.IPs[name]+" "+name)
		}
	}
	if n.Gateway != "" {
		svcs := append([]string{"gantry-host"}, n.Services...)
		out = append(out, n.Gateway+" "+strings.Join(svcs, " "))
	}
	return out
}

// LANMAC is the stable MAC of the VM's NIC on the project network.
func LANMAC(project, name string) string {
	h := fnv.New32a()
	h.Write([]byte(project + "/" + name))
	x := h.Sum32()
	return fmt.Sprintf("52:54:01:%02x:%02x:%02x", byte(x>>16), byte(x>>8), byte(x))
}

// NetworkConfig is the cloud-init network-config v2: DHCP on the user-mode
// NIC (internet, SSH, published ports), the fixed address on the LAN (ip,
// when the VM is on it) and the addresses of the ports on cables and lab
// networks (fixed or DHCP; a port with none is left for inside the VM).
// "" when there is nothing to configure.
func NetworkConfig(project string, v *composex.VM, ip string) string {
	var ports strings.Builder
	route := v.NoManagement // the way out is a network's gateway
	for i, f := range v.Interfaces {
		if f.Bridge != "" || (len(f.IPs) == 0 && !f.DHCP) {
			continue
		}
		fmt.Fprintf(&ports, "  p%d:\n    match: {macaddress: \"%s\"}\n", i, IfaceMAC(project, v.Name, i))
		if f.DHCP {
			ports.WriteString("    dhcp4: true\n")
			if !v.NoManagement {
				// the management NIC stays the way out
				ports.WriteString("    dhcp4-overrides: {route-metric: 200}\n")
			}
		} else {
			ports.WriteString("    dhcp4: false\n")
			q := make([]string, len(f.IPs))
			for k, a := range f.IPs {
				q[k] = strconv.Quote(a)
			}
			fmt.Fprintf(&ports, "    addresses: [%s]\n", strings.Join(q, ", "))
			if route && f.Gateway != "" {
				fmt.Fprintf(&ports, "    routes: [{to: default, via: %s}]\n", f.Gateway)
				route = false
			}
			if len(f.DNS) > 0 {
				fmt.Fprintf(&ports, "    nameservers: {addresses: [%s]}\n", strings.Join(f.DNS, ", "))
			}
		}
	}
	if ip == "" && ports.Len() == 0 {
		return ""
	}
	out := "version: 2\nethernets:\n"
	if !v.NoManagement {
		out += "  wan:\n    match: {macaddress: \"52:54:00:12:34:56\"}\n"
		if ip != "" {
			out += "    set-name: eth0\n"
		}
		out += "    dhcp4: true\n"
	}
	if ip != "" {
		out += fmt.Sprintf("  lan:\n    match: {macaddress: \"%s\"}\n    set-name: lan0\n    dhcp4: false\n    addresses: [\"%s/24\"]\n", LANMAC(project, v.Name), ip)
	}
	return out + ports.String()
}

// UserData builds the cloud-config: a gantry user with the machine's VM key
// (filled in by the script), the packages and commands, then the VM's own
// cloud-init merged on top.
func UserData(v *composex.VM) (string, error) {
	return userData(v, nil, true)
}

// userData builds the cloud-config; withNix adds the files of nix: (left
// out for the part of the definition hash that is not the Nix config).
func userData(v *composex.VM, net *Net, withNix bool) (string, error) {
	cfg := map[string]any{}
	if v.CloudInitFile != "" && v.CloudInitErr != "" {
		return "", fmt.Errorf("x-vms.%s.cloud-init: %s", v.Name, v.CloudInitErr)
	}
	if strings.TrimSpace(v.CloudInit) != "" {
		if err := yaml.Unmarshal([]byte(v.CloudInit), &cfg); err != nil {
			return "", fmt.Errorf("x-vms.%s.cloud-init: %v", v.Name, err)
		}
		if cfg == nil {
			cfg = map[string]any{}
		}
	}
	gantry := map[string]any{
		"name": "gantry", "shell": "/bin/bash", "sudo": "ALL=(ALL) NOPASSWD:ALL",
		"lock_passwd": true, "ssh_authorized_keys": []string{"__GANTRY_PUBKEY__"},
	}
	users, _ := cfg["users"].([]any)
	if len(users) == 0 {
		users = []any{"default"}
	}
	cfg["users"] = append(users, gantry)
	if _, ok := cfg["hostname"]; !ok {
		cfg["hostname"] = v.Name
	}
	if len(v.Packages) > 0 {
		pk, _ := cfg["packages"].([]any)
		for _, p := range v.Packages {
			pk = append(pk, p)
		}
		cfg["packages"] = pk
		if _, ok := cfg["package_update"]; !ok {
			cfg["package_update"] = true
		}
	}
	if net != nil {
		// The names of the project, on every boot (bootcmd), in /etc/hosts.
		cfg["manage_etc_hosts"] = false
		bc, _ := cfg["bootcmd"].([]any)
		bc = append(bc, "sed -i '/# gantry-lan$/d' /etc/hosts")
		for _, l := range net.hostsLines(v.Name) {
			bc = append(bc, "echo '"+l+" # gantry-lan' >> /etc/hosts")
		}
		cfg["bootcmd"] = bc
	}
	if v.Metrics && v.Nix == nil {
		// The exporter first: the VM's own commands may take long.
		rc, _ := cfg["runcmd"].([]any)
		cfg["runcmd"] = append([]any{[]any{"sh", "-c", nodeExporterScript}}, rc...)
	}
	if len(v.Run) > 0 {
		rc, _ := cfg["runcmd"].([]any)
		for _, c := range v.Run {
			rc = append(rc, c)
		}
		cfg["runcmd"] = rc
	}
	var files [][]byte
	if withNix && v.Nix != nil {
		params, module, err := NixFiles(v)
		if err != nil {
			return "", err
		}
		files = nixWriteFiles(cfg, params, module)
	}
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return "", err
	}
	doc := string(b)
	if files != nil {
		if doc, err = literalBlocks(doc, files); err != nil {
			return "", err
		}
	}
	return "#cloud-config\n" + doc, nil
}

func portsArg(project string, v *composex.VM) string {
	var out []string
	for _, p := range v.Ports {
		out = append(out, p.Proto+":"+p.HostIP+":"+p.Host+":"+p.Container)
	}
	if v.Metrics {
		out = append(out, fmt.Sprintf("tcp:127.0.0.1:%d:9100", MetricsPort(project, v)))
	}
	return strings.Join(out, " ")
}

// Env is what the script needs to create or start a VM. net is the project
// network, for VMs with lan: true (nil otherwise).
func Env(project string, v *composex.VM, net *Net) (map[string]string, error) {
	if !v.LAN {
		net = nil
	}
	if IsLocalImage(v.Image) && v.ISO == "" && v.ImageSrc == "" {
		return nil, fmt.Errorf("VM %s: a imagem %s não está nesta máquina; ponha o arquivo numa das pastas de imagens (página da máquina › Configurações › Imagens) ou informe onde ele está", v.Name, v.Image)
	}
	if composex.IsImageFileRef(v.Image) && v.ImageSrc == "" {
		return nil, fmt.Errorf("VM %s: o arquivo %s não está nas pastas de imagens desta máquina (página da máquina › Configurações › Imagens)", v.Name, v.Image)
	}
	ud := ""
	if !v.NoCloudInit {
		var err error
		if ud, err = userData(v, net, true); err != nil {
			return nil, err
		}
	}
	base := ud
	var err error
	nixHash := ""
	if v.Nix != nil {
		// The Nix files count apart (GV_NIXHASH): a change only there is
		// applied inside the running VM, without recreating it.
		if base, err = userData(v, net, false); err != nil {
			return nil, err
		}
		if nixHash, err = NixHashOf(v); err != nil {
			return nil, err
		}
	}
	env := map[string]string{
		"GV_PROJECT": project, "GV_NAME": v.Name, "GV_IMAGE": v.Image,
		"GV_CPUS": strconv.Itoa(v.CPUs), "GV_MEM": strconv.Itoa(v.Memory), "GV_DISK": v.Disk,
		"GV_SSH": strconv.Itoa(SSHPort(project, v)), "GV_PORTS": portsArg(project, v),
		"GV_USERDATA": base64.StdEncoding.EncodeToString([]byte(ud)),
	}
	if v.ImageSrc != "" {
		env["GV_IMAGE_SRC"] = v.ImageSrc
		if composex.IsImageFileRef(v.Image) && v.Kind == "" {
			env["GV_IMAGE"] = v.ImageSrc // a disk of the folders: used as a path
		}
	}
	if IsNixOS(v.Image) {
		env["GV_EFI"] = "1" // the NixOS images boot only with UEFI
	}
	if v.ISO != "" {
		env["GV_ISO"] = v.ISO
		env["GV_VNC"] = strconv.Itoa(VNCPort(project, v))
	} else if v.Screen {
		env["GV_VNC"] = strconv.Itoa(VNCPort(project, v))
	}
	lanIP := ""
	if v.LAN {
		env["GV_LAN"] = "1"
		env["GV_LANMAC"] = LANMAC(project, v.Name)
		if net != nil && net.IPs[v.Name] != "" {
			lanIP = net.IPs[v.Name]
			env["GV_LANIP"] = lanIP
		}
	}
	lan := ""
	if lanIP != "" || (!v.NoCloudInit && v.ISO == "" && v.Kind == "") {
		lan = NetworkConfig(project, v, lanIP)
	}
	if lan != "" {
		env["GV_NETCFG"] = base64.StdEncoding.EncodeToString([]byte(lan))
	}
	env["GV_SERIAL"] = strconv.Itoa(SerialPort(project, v))
	if v.NIC != "" {
		env["GV_NIC"] = composex.NICModels[v.NIC]
	}
	if v.NoManagement {
		env["GV_NOMGMT"] = "1"
	}
	if v.NoCloudInit {
		env["GV_NOCI"] = "1"
	}
	if v.Machine != "" {
		env["GV_MACHINE"] = v.Machine
	}
	if v.Kind == "iol" {
		// IOS on Linux: the program runs on the machine, with its ports in
		// the NETMAP pointing to the cables' relay.
		ensurePorts(project, v)
		env["GV_KIND"] = "iol"
		env["GV_IOLID"] = strconv.Itoa(v.IOLID)
		env["GV_IOLPORTS"] = strconv.Itoa(len(v.Interfaces))
		env["GV_NETMAP"] = base64.StdEncoding.EncodeToString([]byte(IOLNetmap(project, v)))
		if v.IOLLicense != "" {
			env["GV_IOURC"] = v.IOLLicense
		}
		if v.ConfigFile != "" && v.ConfigErr != "" {
			return nil, fmt.Errorf("x-vms.%s.config: %s", v.Name, v.ConfigErr)
		}
		if v.Config != "" {
			// Typed on the first boot only: not in the hash.
			env["GV_CONFIG"] = base64.StdEncoding.EncodeToString([]byte(v.Config))
		}
	}
	if v.Kind == "vpcs" {
		// VPCS: the computer program on the machine, its one port a UDP
		// NIO to the cables' relay (-s its own, -c the relay's).
		ensurePorts(project, v)
		rp, lp := ifacePorts(project, v.Name, 0)
		if len(v.Interfaces) > 0 {
			lp, rp = v.Interfaces[0].VPort, v.Interfaces[0].RPort
		}
		env["GV_KIND"] = "vpcs"
		env["GV_VPCSARGS"] = fmt.Sprintf("-m %d -s %d -c %d -t 127.0.0.1", v.IOLID, lp, rp)
		if v.ConfigFile != "" && v.ConfigErr != "" {
			return nil, fmt.Errorf("x-vms.%s.config: %s", v.Name, v.ConfigErr)
		}
		if v.Config != "" {
			env["GV_CONFIG"] = base64.StdEncoding.EncodeToString([]byte(v.Config))
		}
	}
	if v.Kind == "dyn" {
		// Dynamips: the router program on the machine, its ports UDP NIOs
		// to the cables' relay.
		env["GV_KIND"] = "dyn"
		env["GV_DYNPLAT"] = v.Platform
		if v.NPE != "" {
			env["GV_DYNNPE"] = v.NPE
		}
		env["GV_DYNARGS"] = DynArgs(project, v)
		env["GV_DYNPORTS"] = strings.Join(DynPortNames(v), " ")
		if v.ConfigFile != "" && v.ConfigErr != "" {
			return nil, fmt.Errorf("x-vms.%s.config: %s", v.Name, v.ConfigErr)
		}
		if v.Config != "" {
			// Into the NVRAM on the first boot only: not in the hash.
			env["GV_CONFIG"] = base64.StdEncoding.EncodeToString([]byte(v.Config))
		}
	}
	if ifs := ifacesArg(project, v); ifs != "" {
		env["GV_IFACES"] = ifs
		if d := downArg(v); d != "" {
			env["GV_DOWN"] = d // not in the hash: cables change live
		}
	}
	parts := []string{v.Image, env["GV_CPUS"], env["GV_MEM"], env["GV_SSH"], env["GV_PORTS"], base, v.ISO, lan}
	// The network-lab options count only when used, so the VMs that do not
	// use them keep the hash they had.
	if extra := strings.Join([]string{env["GV_IFACES"], env["GV_NIC"], env["GV_NOMGMT"], env["GV_NOCI"], env["GV_MACHINE"]}, "|"); extra != "||||" {
		parts = append(parts, "lab:"+extra)
	}
	env["GV_HASH"] = specHash(parts, nixHash)
	// GV_BASEHASH is the definition without the Nix files: when only those
	// changed, the VM is updated in place (Aplicar), without a restart.
	env["GV_BASEHASH"] = specHash(parts, "")
	if nixHash != "" {
		env["GV_NIXHASH"] = nixHash
	}
	return env, nil
}

// specHash is the hash of a VM's definition (GV_HASH): the parts that need
// the VM recreated or restarted and, for a VM with nix:, the hash of its
// Nix files. A VM without nix: keeps the hash it always had.
func specHash(parts []string, nixHash string) string {
	if nixHash != "" {
		parts = append(parts, "nix:"+nixHash)
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:8])
}

// Run executes a vmctl command on the host's machine.
func Run(ctx context.Context, h *hosts.Host, cmd string, env map[string]string) (string, error) {
	if h.Kind == hosts.TCP {
		return "", errors.New("hosts tcp:// não rodam VMs; use ssh:// ou um host local")
	}
	argv := []string{"env"}
	for _, k := range sortedKeys(env) {
		argv = append(argv, k+"="+env[k])
	}
	argv = append(argv, "sh", "-c", script, "vmctl", cmd)
	run := h.Run
	switch cmd {
	case "up", "start", "stop", "rm":
		run = h.RunLive
	}
	out, err := run(ctx, "", argv, nil)
	if err != nil {
		msg := strings.TrimSpace(err.Error())
		msg = strings.TrimPrefix(msg, "erro: ")
		return out, errors.New(msg)
	}
	return out, nil
}

func sortStrings(out []string) {
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Status lists the VMs created for a lab on the host.
func Status(ctx context.Context, h *hosts.Host, project string) ([]State, error) {
	out, err := Run(ctx, h, "status", map[string]string{"GV_PROJECT": project})
	if err != nil {
		return nil, err
	}
	return parseStatus(out), nil
}

func parseStatus(out string) []State {
	var res []State
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) < 6 {
			continue
		}
		s := State{Name: f[0], State: f[1], Accel: f[4], Ports: []string{}}
		s.PID, _ = strconv.Atoi(f[2])
		s.SSHPort, _ = strconv.Atoi(f[3])
		if f[4] == "-" {
			s.Accel = ""
		}
		if f[5] != "-" {
			s.Ports = strings.Split(f[5], ",")
		}
		if len(f) > 6 && f[6] != "-" {
			s.VNC, _ = strconv.Atoi(f[6])
		}
		if len(f) > 7 && f[7] != "-" {
			s.LAN = f[7]
		}
		s.Autostart = len(f) > 8 && f[8] == "1"
		if len(f) > 9 && f[9] != "-" {
			s.Spec = f[9]
		}
		if len(f) > 10 && f[10] != "-" {
			s.NixSpec = f[10]
		}
		if len(f) > 11 && f[11] != "-" {
			s.BaseSpec = f[11]
		}
		for _, kv := range f[min(len(f), 12):] {
			k, v, _ := strings.Cut(kv, "=")
			switch k {
			case "serial":
				s.Serial, _ = strconv.Atoi(v)
			case "ci":
				s.NoCloudInit = v == "0"
			case "rej":
				s.Rejected, _ = strconv.Atoi(v)
			case "idle":
				s.NoIdlePC = v == "0"
			}
		}
		res = append(res, s)
	}
	return res
}

// RejectedLine is a line of the startup configuration IOS refused.
type RejectedLine struct {
	Line    string `json:"line"`
	Message string `json:"message"`
}

// Rejected lists the lines of the startup configuration the IOS of a
// network device refused when it last started.
func Rejected(ctx context.Context, h *hosts.Host, project, name string) ([]RejectedLine, error) {
	out, err := Run(ctx, h, "rejected", map[string]string{"GV_PROJECT": project, "GV_NAME": name})
	if err != nil {
		return nil, err
	}
	res := []RejectedLine{}
	for _, l := range strings.Split(out, "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		line, msg, _ := strings.Cut(l, "\t")
		res = append(res, RejectedLine{Line: line, Message: msg})
	}
	return res, nil
}

// Console returns the serial console output from a byte offset.
func Console(ctx context.Context, h *hosts.Host, project, name string, from int64) (string, error) {
	return Run(ctx, h, "console", map[string]string{"GV_PROJECT": project, "GV_NAME": name, "GV_FROM": strconv.FormatInt(from, 10)})
}

// VNC returns the screen port and password of a VM installed from an ISO.
func VNC(ctx context.Context, h *hosts.Host, project, name string) (int, string, error) {
	out, err := Run(ctx, h, "vnc", map[string]string{"GV_PROJECT": project, "GV_NAME": name})
	if err != nil {
		return 0, "", err
	}
	f := strings.Fields(out)
	if len(f) < 2 {
		return 0, "", errors.New("esta VM não tem tela (só VMs instaladas por ISO)")
	}
	port, _ := strconv.Atoi(f[0])
	return port, f[1], nil
}

// Usage is one sample of a running VM, seen from the machine: the QEMU
// process's CPU time and memory, and the space the disk takes.
// Usage is the use of one VM's resources (see vmscript.Usage).
type Usage = vmscript.Usage

// Usages samples the running VMs of the machine (of one project when it is
// not empty) in a single command.
func Usages(ctx context.Context, h *hosts.Host, project string) ([]Usage, error) {
	out, err := Run(ctx, h, "stats", map[string]string{"GV_PROJECT": project})
	if err != nil {
		return nil, err
	}
	return parseUsages(out), nil
}

func parseUsages(out string) []Usage { return vmscript.ParseUsages(out) }

func parseCPUTime(s string) (float64, bool) { return vmscript.ParseCPUTime(s) }

// NetInfo is the VM network of a machine, as vmctl netinfo reports it.
type NetInfo struct {
	Mode    string   `json:"mode"` // vmnet (macOS), bridge (Linux, WSL2) or none
	Gateway string   `json:"gateway,omitempty"`
	Prefix  string   `json:"prefix,omitempty"` // first three octets of the /24
	Hint    string   `json:"hint,omitempty"`   // how to enable it, when missing
	Problem []string `json:"problems,omitempty"`
}

// Network reads the machine's VM network.
func Network(ctx context.Context, h *hosts.Host) (NetInfo, error) {
	out, err := Run(ctx, h, "netinfo", nil)
	if err != nil {
		return NetInfo{}, err
	}
	return parseNetInfo(out), nil
}

func parseNetInfo(out string) NetInfo {
	var n NetInfo
	for _, l := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(l), "=")
		if !ok {
			continue
		}
		switch k {
		case "mode":
			n.Mode = v
		case "gateway":
			n.Gateway = v
		case "prefix":
			n.Prefix = v
		case "hint":
			n.Hint = v
		case "problem":
			n.Problem = append(n.Problem, v)
		}
	}
	if n.Mode == "" {
		n.Mode = "none"
	}
	return n
}

// LANIP reserves (or reads) the VM's address on the machine's VM network.
func LANIP(ctx context.Context, h *hosts.Host, project, name string) (string, error) {
	out, err := Run(ctx, h, "lan-ip", map[string]string{"GV_PROJECT": project, "GV_NAME": name})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// Snapshot is a saved state of a VM's disk (and memory, when taken with the
// VM running).
type Snapshot struct {
	Name   string `json:"name"`
	Memory bool   `json:"memory"`
	Date   string `json:"date"`
}

// Snapshots lists the snapshots of a VM.
func Snapshots(ctx context.Context, h *hosts.Host, project, name string) ([]Snapshot, error) {
	out, err := Run(ctx, h, "snap-list", map[string]string{"GV_PROJECT": project, "GV_NAME": name})
	if err != nil {
		return nil, err
	}
	return parseSnapshots(out), nil
}

func parseSnapshots(out string) []Snapshot {
	res := []Snapshot{}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) < 4 {
			continue
		}
		res = append(res, Snapshot{Name: f[0], Memory: f[1] == "1", Date: f[2] + " " + f[3]})
	}
	return res
}

// ScriptPath is where the boot hook finds vmctl on the machine.
const ScriptPath = "${XDG_STATE_HOME:-$HOME/.local/state}/gantry/vmctl.sh"

// Script is vmctl itself, for the boot hook to keep a copy on the machine.
func Script() string { return script }
