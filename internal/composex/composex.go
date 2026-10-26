// Package composex reads compose files as written (not normalized by a
// compose tool), including Gantry's x-gantry extension:
//
//	x-gantry:
//	  runtime: podman          # default engine for the lab
//	services:
//	  grafana:
//	    image: grafana/grafana
//	    x-gantry:
//	      runtime: docker      # this service runs in Docker
//
// Compose tools ignore x- keys, so the file stays a valid compose file.
package composex

import (
	"fmt"
	"net"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
)

// Runtimes a lab may ask for.
var Runtimes = []string{"docker", "podman"}

func validRuntime(r string) bool {
	for _, x := range Runtimes {
		if x == r {
			return true
		}
	}
	return false
}

type Port struct {
	HostIP    string `json:"hostIp,omitempty"`
	Host      string `json:"host,omitempty"` // empty: not published
	Container string `json:"container"`
	Proto     string `json:"proto"`
}

type Service struct {
	Name        string   `json:"name"`
	Image       string   `json:"image,omitempty"`
	Build       bool     `json:"build,omitempty"`
	Runtime     string   `json:"runtime,omitempty"` // as written; empty means the lab default
	Ports       []Port   `json:"ports"`
	Networks    []string `json:"networks"`
	NetworkMode string   `json:"networkMode,omitempty"`
	DependsOn   []string `json:"dependsOn"`
	Profiles    []string `json:"profiles,omitempty"`
	// Binds are the host paths mounted into the service (./x, /x, ~/x), as written.
	Binds []string `json:"-"`
	// Interfaces are the ports of the container on the lab's cables
	// (x-interfaces: [r1-c1, none]), as eth1, eth2…; Addresses their IPv4
	// (x-addresses: [10.0.12.10/24]).
	Interfaces []VMIface `json:"interfaces,omitempty"`
	Addresses  []string  `json:"addresses,omitempty"`
	// Pos is where the node is drawn in the lab's topology (x-pos: [x, y]).
	Pos []int `json:"pos,omitempty"`
	// Icon is how the node is drawn (x-icon: router…), "" for the default.
	Icon string `json:"icon,omitempty"`
}

type Network struct {
	Key      string `json:"key"`
	Name     string `json:"name,omitempty"`
	External bool   `json:"external,omitempty"`
	Driver   string `json:"driver,omitempty"`
}

type Project struct {
	Name     string              `json:"name,omitempty"`    // top-level name:, if any
	Runtime  string              `json:"runtime,omitempty"` // lab default from x-gantry
	Names    []string            `json:"services"`          // service names in file order
	Services map[string]*Service `json:"-"`
	Networks map[string]*Network `json:"-"`
	Gantry   bool                `json:"gantry"` // the file uses x-gantry
	VMs      []*VM               `json:"vms"`    // x-vms, in file order
	// Images are the names x-imagens gives to image files (iol-l3: …bin).
	Images map[string]string `json:"images,omitempty"`
	// LAN puts every VM of the project on the project network (x-gantry: lan: true).
	LAN bool `json:"lan,omitempty"`
	// Uses lists services the project takes from outside (a Postgres that
	// already runs on the machine…), with the variables to fill for each:
	//
	//	x-gantry:
	//	  usa:
	//	    postgres: {host: PG_HOST, porta: PG_PORTA, usuario: PG_USUARIO, senha: PG_SENHA, banco: PG_BANCO}
	Uses map[string]map[string]string `json:"uses,omitempty"`
	// Taps are the services with ports on cables, as nodes of the lab
	// (Kind "ctr"): the cables code treats them like the VMs.
	Taps []*VM `json:"-"`
	// RelayID is the IOL instance number the cables' relay of the project
	// answers on (set when the project loads, for IOL nodes).
	RelayID int `json:"-"`
	// Kube marks the project as a Kubernetes cluster: the VM that runs the
	// API server and its port published on the machine (x-gantry:
	// kubernetes: {vm: k3s-server, porta: 6443}). Gantry adds the cluster
	// to the sidebar when it answers.
	Kube *KubeSpec `json:"kubernetes,omitempty"`
	// Redes are the lab networks (x-redes): segments where many ports meet,
	// like a switch, with an optional subnet and DHCP. RedeNames keeps the
	// file order.
	Redes     map[string]*Rede `json:"redes,omitempty"`
	RedeNames []string         `json:"-"`
	// Notas are the notes on the drawing of a lab (x-notas): text, a
	// table, an image, where the teacher put them in PNETLab or EVE-NG.
	Notas []*Nota `json:"notas,omitempty"`
}

// Nota is one note on the drawing: its text in a small Markdown (lines,
// **bold**, tables, # titles, - lists and images of the project folder),
// where it is and its size.
type Nota struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	Pos  []int  `json:"pos,omitempty"`
	Size []int  `json:"size,omitempty"`
	Cor  string `json:"cor,omitempty"` // amarela, azul, verde, rosa, cinza ("" : sem fundo)
}

// NotaCores are the colors a note can have.
var NotaCores = map[string]bool{"": true, "amarela": true, "azul": true, "verde": true, "rosa": true, "cinza": true}

// NotasYAML writes the x-notas block of the compose ("" when there are no
// notes).
func NotasYAML(notas []*Nota) string {
	if len(notas) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Notas do desenho do lab (texto, tabelas, imagens da pasta do projeto)\nx-notas:\n")
	for _, n := range notas {
		fmt.Fprintf(&b, "  %s:\n", n.ID)
		if len(n.Pos) == 2 {
			fmt.Fprintf(&b, "    pos: [%d, %d]\n", n.Pos[0], n.Pos[1])
		}
		if len(n.Size) == 2 {
			fmt.Fprintf(&b, "    size: [%d, %d]\n", n.Size[0], n.Size[1])
		}
		if n.Cor != "" {
			fmt.Fprintf(&b, "    cor: %s\n", n.Cor)
		}
		text := strings.TrimRight(strings.ReplaceAll(n.Text, "\r\n", "\n"), "\n ")
		if strings.TrimSpace(text) == "" {
			b.WriteString("    text: \"\"\n")
			continue
		}
		// A literal block keeps the text as typed; the indentation is
		// stated when the first line starts with spaces.
		ind := ""
		if strings.HasPrefix(text, " ") {
			ind = "2"
		}
		fmt.Fprintf(&b, "    text: |%s-\n", ind)
		for _, l := range strings.Split(text, "\n") {
			if strings.TrimSpace(l) == "" {
				b.WriteString("\n")
				continue
			}
			// $ is doubled: compose reads $NAME as a variable, in x- fields too
			b.WriteString("      " + strings.ReplaceAll(strings.TrimRight(l, " \t"), "$", "$$") + "\n")
		}
	}
	return b.String()
}

// parseNotas reads x-notas: {nota-1: {text: …, pos: [x, y], size: [w, h], cor: amarela}}.
func (p *Project) parseNotas(k string, x any) error {
	m := asMap(x)
	if m == nil && x != nil {
		return fmt.Errorf("%s: use um mapa, como {nota-1: {text: Olá, pos: [100, 80]}}", k)
	}
	for _, it := range m {
		id := str(it.Key)
		where := k + "." + id
		if !vmName.MatchString(id) {
			return fmt.Errorf("%s: nome inválido", where)
		}
		n := &Nota{ID: id}
		for _, kv := range asMap(it.Value) {
			f, v := str(kv.Key), kv.Value
			switch f {
			case "text", "texto":
				// $$ is how a compose writes a $ (it reads $NAME as a variable)
				n.Text = strings.ReplaceAll(str(v), "$$", "$")
			case "pos":
				pos, err := parsePos(where+".pos", v)
				if err != nil {
					return err
				}
				n.Pos = pos
			case "size", "tamanho":
				sz, err := parsePos(where+".size", v)
				if err != nil || sz[0] < 20 || sz[1] < 20 {
					return fmt.Errorf("%s.size: use [largura, altura], como [240, 120]", where)
				}
				n.Size = sz
			case "cor", "color":
				n.Cor = str(v)
				if !NotaCores[n.Cor] {
					return fmt.Errorf("%s.cor: use amarela, azul, verde, rosa ou cinza", where)
				}
			default:
				return fmt.Errorf("%s: campo desconhecido %q (use text, pos, size, cor)", where, f)
			}
		}
		p.Notas = append(p.Notas, n)
	}
	return nil
}

// Rede is one lab network: a switch every port on it joins.
type Rede struct {
	Name     string   `json:"name"`
	Subnet   string   `json:"subnet,omitempty"`  // 10.0.1.0/24 ("" : only the switch)
	Gateway  string   `json:"gateway,omitempty"` // handed out by DHCP and used by the VMs with no other way out
	DHCP     bool     `json:"dhcp,omitempty"`
	DHCPFrom string   `json:"dhcpFrom,omitempty"` // the pool (filled when dhcp is on)
	DHCPTo   string   `json:"dhcpTo,omitempty"`
	DNS      []string `json:"dns,omitempty"`
	Pos      []int    `json:"pos,omitempty"` // where the cloud is drawn
}

type KubeSpec struct {
	VM   string `json:"vm"`
	Port int    `json:"port"`
}

// SharedKinds are the services a project can take from the machine.
var SharedKinds = []string{"postgres", "mysql", "redis", "rabbitmq", "mongo"}

// UsesLAN tells whether any VM goes on the project network.
// Nodes are the VMs and the containers on cables: what the cables join.
func (p *Project) Nodes() []*VM {
	return append(append([]*VM{}, p.VMs...), p.Taps...)
}

func (p *Project) UsesLAN() bool {
	for _, v := range p.VMs {
		if v.LAN {
			return true
		}
	}
	return false
}

// VM is a QEMU virtual machine declared in x-vms:
//
//	x-vms:
//	  web:
//	    image: debian-12      # or ubuntu-24.04, a URL, or a path to a qcow2
//	    cpus: 2
//	    memory: 2g
//	    disk: 20G
//	    ports: ["8081:80"]
//	    packages: [nginx]
//	    run: ["echo ok > /tmp/ok"]
//	    cloud-init: |         # extra cloud-config, merged with the above
//	      timezone: America/Sao_Paulo
//	    lan: true             # a second NIC on the project network (see vmctl lan)
//
// A VM with a Gantry NixOS image is configured by a NixOS module instead:
//
//	image: nixos-lab-26.05
//	nix:
//	  module: ./web.nix   # relative to the project folder
//	  params:             # read by the module as config.gantry.params
//	    httpPort: 80
type VM struct {
	Name      string   `json:"name"`
	Image     string   `json:"image"`
	CPUs      int      `json:"cpus"`
	Memory    int      `json:"memory"` // MB
	Disk      string   `json:"disk"`
	Ports     []Port   `json:"ports"`
	SSH       int      `json:"ssh,omitempty"` // host port for the VM's SSH; 0 picks one
	Packages  []string `json:"packages,omitempty"`
	Run       []string `json:"run,omitempty"`
	CloudInit string   `json:"cloudInit,omitempty"`
	// ISO boots an installer (path or URL) on a blank disk, with the screen
	// over VNC, instead of a cloud image configured by cloud-init.
	ISO string `json:"iso,omitempty"`
	// LAN adds a NIC on the machine's VM network, with a fixed IP: the VMs of
	// the project reach each other, and the containers reach them, by name.
	LAN bool `json:"lan,omitempty"`
	// Screen serves the VM's screen over VNC (tela: true), for systems
	// without cloud-init, such as disks imported from VirtualBox or VMware.
	Screen bool `json:"screen,omitempty"`
	// After lists VMs that must be up first (depois: k3s-server): they
	// start before this one, which waits until they answer over SSH.
	After []string `json:"after,omitempty"`
	// ImageSrc is where an image Gantry does not download is on the
	// machine (the settings of the host), filled in when the project loads.
	ImageSrc string `json:"-"`
	// ImageName is the name image: used when it came from x-imagens.
	ImageName string `json:"imageName,omitempty"`
	// Catalog is the catalog image a file named in image: is (cisco-iol for
	// an i86bi_…bin), "" for any other file or a catalog name.
	Catalog string `json:"-"`
	// CloudInitFile is the project file that holds the VM's extra
	// cloud-config (cloud-init: ./web.cloud-init.yaml); its text goes to
	// CloudInit when the project loads, or CloudInitErr says why not.
	CloudInitFile string `json:"cloudInitFile,omitempty"`
	CloudInitErr  string `json:"-"`
	// Nix configures a NixOS VM by a module and its parameters (nix:).
	Nix *VMNix `json:"nix,omitempty"`
	// Warnings are parts of the definition that are valid but have no
	// effect (packages with nix:…).
	Warnings []string `json:"warnings,omitempty"`
	// Interfaces are extra NICs, in order after the management one: a
	// point-to-point link to another VM (two ends with the same link name)
	// or a bridge of the machine. For network labs (switches, routers).
	Interfaces []VMIface `json:"interfaces,omitempty"`
	// NIC is the model of every NIC of the VM (virtio by default; network
	// appliances such as Cisco IOSv want e1000).
	NIC string `json:"nic,omitempty"`
	// NoManagement leaves out the NIC Gantry uses for internet, SSH and
	// published ports (management: false), so the first interface of the
	// appliance is the first link.
	NoManagement bool `json:"noManagement,omitempty"`
	// NoCloudInit: no seed, no gantry user (cloud-init: false), for images
	// that are not cloud images (network appliances).
	NoCloudInit bool `json:"noCloudInit,omitempty"`
	// Machine is the QEMU machine type on x86 (q35 by default, or pc).
	Machine string `json:"machine,omitempty"`
	// Kind is how the node runs: "" (a QEMU VM) or "iol" (Cisco IOS on
	// Linux: a Linux program, much lighter than a VM; Linux x86_64 only).
	Kind string `json:"kind,omitempty"`
	// Pos is where the node is drawn in the lab's topology (pos: [x, y]).
	Pos []int `json:"pos,omitempty"`
	// Icon is how the node is drawn in the topology (icon: router), ""
	// for the one that fits its image.
	Icon string `json:"icon,omitempty"`
	// Metrics: the VM runs the Prometheus node_exporter (metrics: true),
	// reached on a port of the host, for graphs from inside the VM.
	Metrics bool `json:"metrics,omitempty"`
	// Platform is the router model of a Dynamips node (7200, 3725, 2691).
	Platform string `json:"platform,omitempty"`
	// NPE is the processor board of a 7200 (npe: npe-200); "" is npe-400.
	NPE string `json:"npe,omitempty"`
	// Slots are the adapters of a Dynamips router, slot 1 onwards
	// (slots: [PA-FE-TX, PA-FE-TX]); empty: the default ones.
	Slots []string `json:"slots,omitempty"`
	// Config is the startup configuration of an IOL or Dynamips node
	// (config: |): written to its NVRAM before the first boot, so it boots
	// with it as saved. For a VPCS, its startup.vpc (ip, set pcname…).
	Config string `json:"config,omitempty"`
	// ConfigFile is the project file with that configuration
	// (config: equipamentos/R1.cfg), read when the project loads; ConfigErr
	// says why it could not be read.
	ConfigFile string `json:"configFile,omitempty"`
	ConfigErr  string `json:"-"`
	// IOLID is the IOL instance number on the machine (set when the
	// project loads).
	IOLID int `json:"-"`
	// IOLRelay is the instance number of the project's relay, which its
	// NETMAP points to.
	IOLRelay int `json:"-"`
	// IOLLicense is the iourc file the user gave for IOL, when the image
	// asks for one (set when the project loads).
	IOLLicense string `json:"-"`
}

// VMIface is one extra NIC of a VM.
type VMIface struct {
	Link   string `json:"link,omitempty"`   // the cable: the other end is the other interface with this name
	Bridge string `json:"bridge,omitempty"` // a bridge of the machine (br0, gantry0…)
	// Net is the lab network (x-redes) the port is on: a switch, not a cable.
	Net string `json:"rede,omitempty"`
	// IPs are the addresses the port gets (10.0.1.10/24…); DHCP asks the
	// network for one. Neither: no address, configured inside the machine.
	IPs  []string `json:"ips,omitempty"`
	DHCP bool     `json:"dhcp,omitempty"`
	// Gateway and DNS come from the network (set when the project is
	// parsed): the way out of a VM with no management NIC.
	Gateway string   `json:"-"`
	DNS     []string `json:"-"`
	// None of link, bridge and rede: a port with no cable (- none).
	// Side is which end of the link this is (0 or 1), set when the
	// project is parsed.
	Side int `json:"side,omitempty"`
	// RPort and VPort are the UDP ports of the port on 127.0.0.1 (the
	// relay's and the VM's), set when the project loads (vms.AssignPorts).
	RPort int `json:"-"`
	VPort int `json:"-"`
}

// NICModels are the QEMU devices a VM's NICs can be.
var NICModels = map[string]string{"virtio": "virtio-net-pci", "e1000": "e1000", "e1000e": "e1000e", "rtl8139": "rtl8139", "vmxnet3": "vmxnet3"}

// VMNix is the nix: of a VM: a NixOS module of the project and the
// parameters of this instance. Gantry delivers them in /etc/nixos/lab.nix
// and /etc/nixos/gantry-params.json; the image applies them with
// nixos-rebuild.
type VMNix struct {
	Module string         `json:"module"`
	Params map[string]any `json:"params"`
	// Content is the module's text, read from the project folder when the
	// project loads (the compose parser does not know the folder).
	Content string `json:"-"`
	// ReadErr says why the module could not be read.
	ReadErr string `json:"-"`
}

// IsConfigFile tells whether a config: value names a file of the project
// (one word ending in .cfg, .conf, .txt, .ios or .vpc) rather than being the
// IOS commands.
func IsConfigFile(v string) bool {
	l := strings.ToLower(v)
	if v == "" || strings.ContainsAny(v, " \t\n") {
		return false
	}
	for _, ext := range []string{".cfg", ".conf", ".txt", ".ios", ".vpc"} {
		if strings.HasSuffix(l, ext) {
			return true
		}
	}
	return false
}

// IsCloudInitFile tells whether a cloud-init: value names a file of the
// project (one line ending in .yaml or .yml) rather than being the YAML.
func IsCloudInitFile(v string) bool {
	l := strings.ToLower(v)
	return v != "" && !strings.ContainsAny(v, "\n{}") && !strings.Contains(v, ": ") && (strings.HasSuffix(l, ".yaml") || strings.HasSuffix(l, ".yml"))
}

// ApplianceProfile is how a network appliance of the catalog runs: the
// model of its NICs, its memory and machine, without cloud-init and
// without the management NIC (its first port is its first cable). The
// compose can still say otherwise for each field.
type ApplianceProfile struct {
	NIC     string
	Memory  int
	Machine string
	Kind    string // "iol" for the IOS on Linux images, "dyn" for Dynamips, "vpcs" for the VPCS computer
	// Platform is the Dynamips router model (7200, 3725, 2691).
	Platform string
}

// Appliances are the catalog images that are network appliances.
var Appliances = map[string]ApplianceProfile{
	"cisco-iosvl2":   {NIC: "e1000", Memory: 1024, Machine: "pc"},
	"cisco-iosv":     {NIC: "e1000", Memory: 512, Machine: "pc"},
	"cisco-csr1000v": {NIC: "virtio", Memory: 4096, Machine: "pc"},
	"cisco-iol":      {Memory: 512, Kind: "iol"},
	"cisco-iol-l2":   {Memory: 768, Kind: "iol"},
	"cisco-c7200":    {Memory: 256, Kind: "dyn", Platform: "7200"},
	"cisco-c3725":    {Memory: 256, Kind: "dyn", Platform: "3725"},
	"cisco-c2691":    {Memory: 256, Kind: "dyn", Platform: "2691"},
	// VPCS: the simple computer of PNETLab, EVE-NG and GNS3 (ip, ping,
	// trace), one port; Gantry brings the program.
	"vpcs": {Memory: 16, Kind: "vpcs"},
}

// IsNixOSImage tells whether an image is one of the Gantry NixOS images,
// the only ones that take nix:.
func IsNixOSImage(image string) bool { return strings.HasPrefix(image, "nixos-") }

// VM returns the VM with that name.
func (p *Project) VM(name string) *VM {
	for _, v := range p.VMs {
		if v.Name == name {
			return v
		}
	}
	return nil
}

// Parse reads one or more compose files; later files override earlier ones,
// as with compose -f a -f b.
func Parse(files ...[]byte) (*Project, error) {
	p := &Project{Services: map[string]*Service{}, Networks: map[string]*Network{}}
	for i, b := range files {
		var doc yaml.MapSlice
		if err := yaml.UnmarshalWithOptions(b, &doc, yaml.UseOrderedMap()); err != nil {
			return nil, fmt.Errorf("arquivo %d: %v", i+1, cleanYAMLErr(err))
		}
		if err := p.merge(doc); err != nil {
			return nil, err
		}
	}
	uses := false
	for _, s := range p.Services {
		for _, n := range s.Networks {
			if n == "default" {
				uses = true
			}
		}
	}
	if uses && p.Networks["default"] == nil {
		p.Networks["default"] = &Network{Key: "default"}
	}
	p.Taps = nil
	for _, n := range p.Names {
		sv := p.Services[n]
		if sv == nil || len(sv.Interfaces) == 0 {
			continue
		}
		if len(sv.Addresses) > len(sv.Interfaces) {
			return nil, fmt.Errorf("services.%s.x-addresses: mais endereços (%d) que portas em x-interfaces (%d)", n, len(sv.Addresses), len(sv.Interfaces))
		}
		for _, v := range p.VMs {
			if v.Name == n {
				return nil, fmt.Errorf("o nome %s é de uma VM e de um serviço com x-interfaces; use nomes diferentes", n)
			}
		}
		for i, a := range sv.Addresses {
			if a != "" && len(sv.Interfaces[i].IPs) == 0 {
				sv.Interfaces[i].IPs = []string{a}
			}
		}
		p.Taps = append(p.Taps, &VM{Name: n, Kind: "ctr", Image: sv.Image, Interfaces: sv.Interfaces, NoCloudInit: true, NoManagement: true, Pos: sv.Pos, Icon: sv.Icon})
	}
	if err := p.linkEnds(); err != nil {
		return nil, err
	}
	return p, nil
}

// parseIfaces reads a list of ports: "a-b" (or {link: a-b}) for a cable,
// the name of a lab network (or {rede: lan-a}) for a switch, {bridge: br0}
// for a bridge of the machine, none for a free port; ip: gives addresses
// (one, a list, or dhcp).
func parseIfaces(where string, x any) ([]VMIface, error) {
	list, isList := x.([]any)
	if !isList {
		return nil, fmt.Errorf("%s: use uma lista, como [{link: sw1-sw2}, {rede: lan-a}, {bridge: br0}]", where)
	}
	var out []VMIface
	for i, it := range list {
		var f VMIface
		if s := strings.TrimSpace(str(it)); asMap(it) == nil && (s == "none" || s == "") {
			// - none: a port with no cable (still there, so the numbering
			// of the ones after it does not move)
			out = append(out, VMIface{})
			continue
		} else if asMap(it) == nil {
			f.Link = s // - sw1-sw2: the short form of {link: sw1-sw2} (or of a rede, by name)
		} else {
			im := asMap(it)
			if l, ok := get(im, "link"); ok {
				f.Link = strings.TrimSpace(interpolate(str(l)))
			}
			if b, ok := get(im, "bridge"); ok {
				f.Bridge = strings.TrimSpace(interpolate(str(b)))
			}
			for _, k := range []string{"rede", "network", "net"} {
				if n, ok := get(im, k); ok {
					f.Net = strings.TrimSpace(interpolate(str(n)))
				}
			}
			if v, ok := get(im, "ip"); ok {
				ips, dhcp, err := parseIPs(fmt.Sprintf("%s[%d].ip", where, i), v)
				if err != nil {
					return nil, err
				}
				f.IPs, f.DHCP = ips, dhcp
			}
			for _, kv := range im {
				switch str(kv.Key) {
				case "link", "bridge", "rede", "network", "net", "ip":
				default:
					return nil, fmt.Errorf("%s[%d]: campo desconhecido %q (use link, rede, bridge ou ip)", where, i, str(kv.Key))
				}
			}
		}
		n := 0
		for _, x := range []string{f.Link, f.Bridge, f.Net} {
			if x != "" {
				n++
			}
		}
		if n != 1 {
			return nil, fmt.Errorf("%s[%d]: informe um só: link (o cabo até outra máquina), rede (uma rede do lab, em x-redes) ou bridge (uma bridge da máquina)", where, i)
		}
		for _, x := range []string{f.Link, f.Bridge, f.Net} {
			if x != "" && !vmName.MatchString(x) {
				return nil, fmt.Errorf("%s[%d]: nome inválido %q", where, i, x)
			}
		}
		if f.Bridge != "" && (len(f.IPs) > 0 || f.DHCP) {
			return nil, fmt.Errorf("%s[%d]: ip: vale para link e rede; numa bridge, configure dentro da máquina", where, i)
		}
		out = append(out, f)
	}
	if len(out) > 28 {
		return nil, fmt.Errorf("%s: no máximo 28", where)
	}
	return out, nil
}

// parseIPs reads ip: of a port: dhcp, an address, or a list of them.
func parseIPs(where string, v any) ([]string, bool, error) {
	var items []any
	if l, ok := v.([]any); ok {
		items = l
	} else {
		items = []any{v}
	}
	var out []string
	for j, it := range items {
		a := strings.TrimSpace(interpolate(str(it)))
		if strings.EqualFold(a, "dhcp") {
			if len(items) != 1 {
				return nil, false, fmt.Errorf("%s: dhcp vai sozinho (ip: dhcp)", where)
			}
			return nil, true, nil
		}
		if a == "" || strings.EqualFold(a, "none") {
			continue
		}
		ip := a
		if k := strings.IndexByte(a, '/'); k >= 0 {
			ip = a[:k]
			if _, _, err := net.ParseCIDR(a); err != nil {
				return nil, false, fmt.Errorf("%s[%d]: endereço inválido %q (use 10.0.1.10/24)", where, j, a)
			}
		}
		if x := net.ParseIP(ip); x == nil || x.To4() == nil {
			return nil, false, fmt.Errorf("%s[%d]: endereço IPv4 inválido %q (use 10.0.1.10/24)", where, j, a)
		}
		out = append(out, a)
	}
	return out, false, nil
}

// parseRedes reads x-redes: the lab networks.
func (p *Project) parseRedes(x any) error {
	m := asMap(x)
	if m == nil && x != nil {
		return fmt.Errorf("x-redes: use um mapa, como {lan-a: {subnet: 10.0.1.0/24, dhcp: true}}")
	}
	if p.Redes == nil {
		p.Redes = map[string]*Rede{}
	}
	for _, it := range m {
		name := str(it.Key)
		where := "x-redes." + name
		if !vmName.MatchString(name) {
			return fmt.Errorf("%s: nome inválido", where)
		}
		r := &Rede{Name: name}
		dhcp := ""
		for _, kv := range asMap(it.Value) {
			k, v := str(kv.Key), kv.Value
			val := strings.TrimSpace(interpolate(str(v)))
			switch k {
			case "subnet", "rede", "cidr":
				ip, n, err := net.ParseCIDR(val)
				if err != nil || ip.To4() == nil {
					return fmt.Errorf("%s.subnet: use IPv4 com máscara, como 10.0.1.0/24", where)
				}
				r.Subnet = n.String()
			case "gateway", "gw":
				if x := net.ParseIP(val); x == nil || x.To4() == nil {
					return fmt.Errorf("%s.gateway: endereço IPv4 inválido %q", where, val)
				}
				r.Gateway = val
			case "dhcp":
				dhcp = val
			case "dns":
				var l []any
				if ll, ok := v.([]any); ok {
					l = ll
				} else {
					l = []any{v}
				}
				for _, d := range l {
					ds := strings.TrimSpace(str(d))
					if x := net.ParseIP(ds); x == nil || x.To4() == nil {
						return fmt.Errorf("%s.dns: endereço IPv4 inválido %q", where, ds)
					}
					r.DNS = append(r.DNS, ds)
				}
			case "pos", "x-pos":
				pos, err := parsePos(where+".pos", v)
				if err != nil {
					return err
				}
				r.Pos = pos
			default:
				return fmt.Errorf("%s: campo desconhecido %q (use subnet, gateway, dhcp, dns)", where, k)
			}
		}
		switch strings.ToLower(dhcp) {
		case "", "false", "no", "nao", "não", "off":
		default:
			if r.Subnet == "" {
				return fmt.Errorf("%s.dhcp: o DHCP precisa de subnet (subnet: 10.0.1.0/24)", where)
			}
			from, to, err := dhcpPool(r.Subnet, dhcp)
			if err != nil {
				return fmt.Errorf("%s.dhcp: %v", where, err)
			}
			r.DHCP, r.DHCPFrom, r.DHCPTo = true, from, to
		}
		if r.Gateway != "" && r.Subnet != "" && !inSubnet(r.Gateway, r.Subnet) {
			return fmt.Errorf("%s.gateway: %s fica fora de %s", where, r.Gateway, r.Subnet)
		}
		if _, ok := p.Redes[name]; !ok {
			p.RedeNames = append(p.RedeNames, name)
		}
		p.Redes[name] = r
	}
	return nil
}

func ip4(s string) uint32 {
	x := net.ParseIP(s).To4()
	if x == nil {
		return 0
	}
	return uint32(x[0])<<24 | uint32(x[1])<<16 | uint32(x[2])<<8 | uint32(x[3])
}

func ip4s(n uint32) string {
	return fmt.Sprintf("%d.%d.%d.%d", byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
}

func inSubnet(ip, subnet string) bool {
	_, n, err := net.ParseCIDR(subnet)
	x := net.ParseIP(strings.SplitN(ip, "/", 2)[0])
	return err == nil && x != nil && n.Contains(x)
}

// dhcpPool is the range DHCP hands out: the one given (a-b), or by default
// .100 to .199 of a /24 or larger, the upper half of a smaller one (the low
// addresses stay for the fixed ones).
func dhcpPool(subnet, spec string) (string, string, error) {
	_, n, _ := net.ParseCIDR(subnet)
	ones, _ := n.Mask.Size()
	base := ip4(n.IP.String())
	size := uint32(1) << uint(32-ones)
	if size < 4 {
		return "", "", fmt.Errorf("%s é pequena demais para DHCP", subnet)
	}
	if a, b, ok := strings.Cut(spec, "-"); ok {
		a, b = strings.TrimSpace(a), strings.TrimSpace(b)
		if !inSubnet(a, subnet) || !inSubnet(b, subnet) || ip4(a) > ip4(b) {
			return "", "", fmt.Errorf("use um intervalo dentro de %s, como %s-%s", subnet, ip4s(base+100), ip4s(base+199))
		}
		return a, b, nil
	}
	if size >= 256 {
		return ip4s(base + 100), ip4s(base + 199), nil
	}
	return ip4s(base + size/2), ip4s(base + size - 2), nil
}

// DHCPServer is the address the DHCP of a network answers from: the last
// usable one of the subnet.
func (r *Rede) DHCPServer() string {
	_, n, err := net.ParseCIDR(r.Subnet)
	if err != nil {
		return ""
	}
	ones, _ := n.Mask.Size()
	return ip4s(ip4(n.IP.String()) + uint32(1)<<uint(32-ones) - 2)
}

// Prefix is the mask length of the network ("24"), "" with no subnet.
func (r *Rede) Prefix() string {
	if i := strings.IndexByte(r.Subnet, '/'); i >= 0 {
		return r.Subnet[i+1:]
	}
	return ""
}

// linkEnds checks that each link joins exactly two interfaces and tells
// each end which side it is.
func (p *Project) linkEnds() error {
	type end struct {
		vm *VM
		i  int
	}
	ends := map[string][]end{}
	var order []string
	for _, v := range p.Nodes() {
		if p.Redes[v.Name] != nil {
			return fmt.Errorf("o nome %s é de uma rede (x-redes) e de uma máquina; use nomes diferentes", v.Name)
		}
		for i := range v.Interfaces {
			f := &v.Interfaces[i]
			if f.Link != "" && p.Redes[f.Link] != nil {
				f.Net, f.Link = f.Link, "" // - lan-a: the name of a network
			}
			if len(f.IPs) > 0 || f.DHCP {
				switch {
				case v.Kind == "iol" || v.Kind == "dyn":
					return fmt.Errorf("%s, porta %d: num roteador IOL/Dynamips o IP se configura dentro dele (ip address …); tire o ip:", v.Name, i)
				case v.Kind == "vpcs":
					return fmt.Errorf("%s, porta %d: no VPCS o IP vai na config (config: ip 10.0.0.10 10.0.0.1 24); tire o ip:", v.Name, i)
				case v.Kind == "ctr" && f.DHCP:
					return fmt.Errorf("%s, porta %d: containers levam IP fixo (ip: 10.0.1.20/24); dhcp é para VMs", v.Name, i)
				case v.Kind != "ctr" && (v.NoCloudInit || v.ISO != ""):
					return fmt.Errorf("%s, porta %d: sem cloud-init o Gantry não configura a rede da VM; tire o ip: e configure dentro dela", v.Name, i)
				}
			}
			if f.Net != "" {
				r := p.Redes[f.Net]
				if r == nil {
					return fmt.Errorf("%s, porta %d: a rede %s não está em x-redes", v.Name, i, f.Net)
				}
				f.Gateway, f.DNS = r.Gateway, r.DNS
				if f.DHCP && !r.DHCP {
					return fmt.Errorf("%s, porta %d: ip: dhcp, mas a rede %s não tem dhcp (x-redes.%s.dhcp: true)", v.Name, i, f.Net, f.Net)
				}
				for j, a := range f.IPs {
					if r.Subnet == "" {
						if !strings.Contains(a, "/") {
							return fmt.Errorf("%s, porta %d: %s precisa da máscara (a rede %s não tem subnet)", v.Name, i, a, f.Net)
						}
						continue
					}
					if !inSubnet(a, r.Subnet) {
						return fmt.Errorf("%s, porta %d: %s fica fora da rede %s (%s)", v.Name, i, a, f.Net, r.Subnet)
					}
					if !strings.Contains(a, "/") {
						f.IPs[j] = a + "/" + r.Prefix()
					}
				}
				continue
			}
			if f.Link != "" {
				if f.DHCP {
					return fmt.Errorf("%s, porta %d: ip: dhcp precisa de uma rede com DHCP (rede: …); um cabo não tem", v.Name, i)
				}
				for _, a := range f.IPs {
					if !strings.Contains(a, "/") {
						return fmt.Errorf("%s, porta %d: %s precisa da máscara, como %s/30", v.Name, i, a, a)
					}
				}
			}
			if f.Link == "" {
				continue
			}
			if _, ok := ends[f.Link]; !ok {
				order = append(order, f.Link)
			}
			ends[f.Link] = append(ends[f.Link], end{v, i})
		}
	}
	for _, l := range order {
		e := ends[l]
		if len(e) != 2 {
			var who []string
			for _, x := range e {
				who = append(who, x.vm.Name)
			}
			return fmt.Errorf("o link %s liga %d interface(s) (%s); um link é um cabo e liga exatamente duas. Para juntar mais, use bridge:", l, len(e), strings.Join(who, ", "))
		}
		for side, x := range e {
			x.vm.Interfaces[x.i].Side = side
		}
	}
	return nil
}

// parsePos reads where a node is drawn: [x, y], in the drawing's units.
func parsePos(where string, v any) ([]int, error) {
	l, ok := v.([]any)
	if !ok || len(l) != 2 {
		return nil, fmt.Errorf("%s: use [x, y], como [120, 80]", where)
	}
	out := make([]int, 2)
	for i, x := range l {
		n, err := strconv.Atoi(strings.TrimSpace(interpolate(str(x))))
		if err != nil || n < -100000 || n > 100000 {
			return nil, fmt.Errorf("%s: use [x, y] com números inteiros, como [120, 80]", where)
		}
		out[i] = n
	}
	return out, nil
}

// Cable is one link of the lab: the two ports it joins.
type Cable struct {
	Link string   `json:"link"`
	A    CableEnd `json:"a"`
	B    CableEnd `json:"b"`
}

// CableEnd is a port of a node (its index in the node's interfaces).
type CableEnd struct {
	Node string `json:"node"`
	Port int    `json:"port"`
}

// Cables lists the links of the project, in the order they first appear.
func (p *Project) Cables() []Cable {
	var out []Cable
	at := map[string]int{}
	for _, v := range p.Nodes() {
		for i, f := range v.Interfaces {
			if f.Link == "" {
				continue
			}
			e := CableEnd{Node: v.Name, Port: i}
			if k, ok := at[f.Link]; ok {
				out[k].B = e
				continue
			}
			at[f.Link] = len(out)
			out = append(out, Cable{Link: f.Link, A: e})
		}
	}
	return out
}

func cleanYAMLErr(err error) string {
	s := err.Error()
	if i := strings.Index(s, "\n"); i > 0 {
		s = s[:i]
	}
	return s
}

func get(m yaml.MapSlice, key string) (any, bool) {
	for _, it := range m {
		if k, ok := it.Key.(string); ok && k == key {
			return it.Value, true
		}
	}
	return nil, false
}

func asMap(v any) yaml.MapSlice {
	switch m := v.(type) {
	case yaml.MapSlice:
		return m
	case map[string]any:
		out := yaml.MapSlice{}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out = append(out, yaml.MapItem{Key: k, Value: m[k]})
		}
		return out
	}
	return nil
}

func str(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case int, int64, uint64, float64, bool:
		return fmt.Sprint(x)
	}
	return fmt.Sprint(v)
}

func strList(v any) []string {
	var out []string
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			out = append(out, str(e))
		}
	case string:
		out = append(out, x)
	}
	return out
}

// keys returns the keys of a map, or the items of a list (both forms are
// valid for networks and depends_on).
func keys(v any) []string {
	if m := asMap(v); m != nil {
		var out []string
		for _, it := range m {
			out = append(out, str(it.Key))
		}
		return out
	}
	return strList(v)
}

func runtimeOf(v any) (string, error) {
	m := asMap(v)
	if m == nil {
		return "", nil
	}
	r, _ := get(m, "runtime")
	rt := strings.ToLower(strings.TrimSpace(str(r)))
	if rt != "" && !validRuntime(rt) {
		return "", fmt.Errorf("runtime desconhecido %q (use %s)", rt, strings.Join(Runtimes, " ou "))
	}
	return rt, nil
}

// reProjectName is what compose accepts as a project name.
var reProjectName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

func (p *Project) merge(doc yaml.MapSlice) error {
	if v, ok := get(doc, "name"); ok && str(v) != "" {
		p.Name = interpolate(str(v))
		// The compose rule; the name also becomes folders and process names
		// of the VMs, so a lab from someone else cannot point elsewhere.
		if !reProjectName.MatchString(p.Name) {
			return fmt.Errorf("name: %q não é um nome de projeto válido (letras minúsculas, números, - e _, começando com letra ou número)", p.Name)
		}
	}
	if x, ok := get(doc, "x-gantry"); ok {
		p.Gantry = true
		rt, err := runtimeOf(x)
		if err != nil {
			return fmt.Errorf("x-gantry: %v", err)
		}
		if rt != "" {
			p.Runtime = rt
		}
		if l, ok := get(asMap(x), "lan"); ok {
			p.LAN = truthy(l)
		}
		if k, ok := get(asMap(x), "kubernetes"); ok {
			ks := &KubeSpec{Port: 6443}
			if m := asMap(k); m != nil {
				if v, ok := get(m, "vm"); ok {
					ks.VM = str(v)
				}
				if v, ok := get(m, "porta"); ok {
					fmt.Sscan(interpolate(str(v)), &ks.Port)
				} else if v, ok := get(m, "port"); ok {
					fmt.Sscan(interpolate(str(v)), &ks.Port)
				}
			} else {
				ks.VM = str(k)
			}
			if ks.VM == "" {
				return fmt.Errorf("x-gantry: kubernetes: informe a VM do servidor (vm: …)")
			}
			p.Kube = ks
		}
		u, ok := get(asMap(x), "usa")
		if !ok {
			u, ok = get(asMap(x), "uses")
		}
		if ok {
			for _, it := range asMap(u) {
				kind := strings.ToLower(str(it.Key))
				known := false
				for _, k := range SharedKinds {
					known = known || k == kind
				}
				if !known {
					return fmt.Errorf("x-gantry: usa: serviço desconhecido %q (use %s)", kind, strings.Join(SharedKinds, ", "))
				}
				vars := map[string]string{}
				for _, r := range asMap(it.Value) {
					vars[strings.ToLower(str(r.Key))] = str(r.Value)
				}
				if p.Uses == nil {
					p.Uses = map[string]map[string]string{}
				}
				p.Uses[kind] = vars
			}
		}
	}
	if nets, ok := get(doc, "networks"); ok {
		for _, it := range asMap(nets) {
			key := str(it.Key)
			n := p.Networks[key]
			if n == nil {
				n = &Network{Key: key}
				p.Networks[key] = n
			}
			if m := asMap(it.Value); m != nil {
				if v, ok := get(m, "name"); ok {
					n.Name = str(v)
				}
				if v, ok := get(m, "driver"); ok {
					n.Driver = str(v)
				}
				if v, ok := get(m, "external"); ok {
					n.External = str(v) == "true" || asMap(v) != nil
				}
			}
		}
	}
	for _, k := range []string{"x-redes", "x-networks"} {
		if x, ok := get(doc, k); ok {
			if err := p.parseRedes(x); err != nil {
				return err
			}
		}
	}
	for _, k := range []string{"x-notas", "x-notes"} {
		if x, ok := get(doc, k); ok {
			p.Notas = nil // a later file replaces the notes of an earlier one
			if err := p.parseNotas(k, x); err != nil {
				return err
			}
		}
	}
	// x-imagens: names for the image files the VMs use (image: iol-l3), so
	// the file of each one is written once, in the compose itself.
	imgs := map[string]string{}
	for _, k := range []string{"x-imagens", "x-images"} {
		x, ok := get(doc, k)
		if !ok || x == nil {
			continue
		}
		m := asMap(x)
		if m == nil {
			return fmt.Errorf("%s: use nome: arquivo (iol-l3: i86bi_Linux-L3-….bin ou um caminho como /opt/iol/L3.bin)", k)
		}
		for _, it := range m {
			name, file := str(it.Key), interpolate(str(it.Value))
			if !vmName.MatchString(name) {
				return fmt.Errorf("%s: nome inválido %q", k, name)
			}
			if !IsImageFileRef(file) && !(IsImagePath(file) && ClassifyImage(file) != "") && !(IsImagePath(file) && IsImageFileRef(path.Base(file))) {
				return fmt.Errorf("%s.%s: %q não é um arquivo de imagem (um nome como i86bi_Linux-L3-….bin, um .qcow2, ou o caminho completo dele)", k, name, file)
			}
			imgs[name] = file
		}
	}
	p.Images = imgs
	if vms, ok := get(doc, "x-vms"); ok {
		for _, it := range asMap(vms) {
			v, err := parseVM(str(it.Key), asMap(it.Value), imgs)
			if err != nil {
				return err
			}
			if _, set := get(asMap(it.Value), "lan"); !set && p.LAN {
				v.LAN = true
			}
			if old := p.VM(v.Name); old != nil {
				*old = *v
			} else {
				p.VMs = append(p.VMs, v)
			}
		}
	}
	svcs, _ := get(doc, "services")
	for _, it := range asMap(svcs) {
		name := str(it.Key)
		m := asMap(it.Value)
		s := p.Services[name]
		if s == nil {
			s = &Service{Name: name, Ports: []Port{}, Networks: []string{"default"}, DependsOn: []string{}}
			p.Services[name] = s
			p.Names = append(p.Names, name)
		}
		if m == nil {
			continue
		}
		if v, ok := get(m, "image"); ok {
			s.Image = interpolate(str(v))
		}
		if _, ok := get(m, "build"); ok {
			s.Build = true
		}
		if v, ok := get(m, "x-gantry"); ok {
			p.Gantry = true
			rt, err := runtimeOf(v)
			if err != nil {
				return fmt.Errorf("serviço %s: x-gantry: %v", name, err)
			}
			s.Runtime = rt
		}
		if v, ok := get(m, "ports"); ok {
			s.Ports = parsePorts(v)
		}
		if v, ok := get(m, "x-interfaces"); ok && v != nil {
			list, err := parseIfaces("services."+name+".x-interfaces", v)
			if err != nil {
				return err
			}
			for i, f := range list {
				if f.Bridge != "" {
					return fmt.Errorf("services.%s.x-interfaces[%d]: um container liga só por cabos (link:); para uma rede da máquina use networks:", name, i)
				}
			}
			s.Interfaces = list
			p.Gantry = true
		}
		if v, ok := get(m, "x-pos"); ok && v != nil {
			pos, err := parsePos("services."+name+".x-pos", v)
			if err != nil {
				return err
			}
			s.Pos = pos
		}
		if v, ok := get(m, "x-icon"); ok && v != nil {
			ic, err := parseIcon("services."+name+".x-icon", v)
			if err != nil {
				return err
			}
			s.Icon = ic
		}
		if v, ok := get(m, "x-addresses"); ok && v != nil {
			list, isList := v.([]any)
			if !isList {
				return fmt.Errorf("services.%s.x-addresses: use uma lista, como [10.0.12.10/24]", name)
			}
			s.Addresses = nil
			for i, it := range list {
				a := strings.TrimSpace(interpolate(str(it)))
				if a != "" && a != "none" && a != "dhcp" {
					if ip, _, err := net.ParseCIDR(a); err != nil || ip.To4() == nil {
						return fmt.Errorf("services.%s.x-addresses[%d]: use IPv4 com máscara, como 10.0.12.10/24 (ou none)", name, i)
					}
				} else {
					a = ""
				}
				s.Addresses = append(s.Addresses, a)
			}
		}
		if v, ok := get(m, "network_mode"); ok {
			s.NetworkMode = str(v)
			s.Networks = []string{}
		}
		if v, ok := get(m, "networks"); ok {
			s.Networks = keys(v)
		}
		if v, ok := get(m, "depends_on"); ok {
			s.DependsOn = keys(v)
		}
		if v, ok := get(m, "profiles"); ok {
			s.Profiles = strList(v)
		}
		if v, ok := get(m, "volumes"); ok {
			s.Binds = parseBinds(v)
		}
	}
	return nil
}

// parseBinds keeps the host side of bind mounts: "./a:/b:ro" in the short
// syntax, or {type: bind, source: ./a} in the long one.
func parseBinds(v any) []string {
	var out []string
	list, _ := v.([]any)
	for _, it := range list {
		src := ""
		if m := asMap(it); m != nil {
			t, _ := get(m, "type")
			if sv, ok := get(m, "source"); ok && (str(t) == "bind" || str(t) == "") {
				src = str(sv)
			}
		} else {
			src = str(it)
			if i := strings.Index(src, ":"); i > 0 {
				src = src[:i]
			}
		}
		if strings.HasPrefix(src, ".") || strings.HasPrefix(src, "/") || strings.HasPrefix(src, "~") {
			out = append(out, src)
		}
	}
	return out
}

var varRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::?[-?])?([^}]*)\}`)

// interpolate shows ${VAR:-default} as its default; other variables stay.
func interpolate(s string) string {
	return varRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := varRe.FindStringSubmatch(m)
		if strings.Contains(m, "-") && sub[2] != "" {
			return sub[2]
		}
		return m
	})
}

func parsePorts(v any) []Port {
	out := []Port{}
	list, _ := v.([]any)
	for _, e := range list {
		if m := asMap(e); m != nil {
			p := Port{Proto: "tcp"}
			if x, ok := get(m, "target"); ok {
				p.Container = str(x)
			}
			if x, ok := get(m, "published"); ok {
				p.Host = interpolate(str(x))
			}
			if x, ok := get(m, "host_ip"); ok {
				p.HostIP = str(x)
			}
			if x, ok := get(m, "protocol"); ok && str(x) != "" {
				p.Proto = str(x)
			}
			out = append(out, p)
			continue
		}
		out = append(out, ParsePort(interpolate(str(e))))
	}
	return out
}

// ParsePort reads the short syntax: [ip:][host:]container[/proto].
func ParsePort(s string) Port {
	p := Port{Proto: "tcp"}
	if i := strings.LastIndex(s, "/"); i > 0 {
		p.Proto, s = s[i+1:], s[:i]
	}
	if strings.HasPrefix(s, "[") { // [::1]:8080:80
		if j := strings.Index(s, "]:"); j > 0 {
			p.HostIP, s = s[1:j], s[j+2:]
		}
	}
	parts := strings.Split(s, ":")
	switch len(parts) {
	case 1:
		p.Container = parts[0]
	case 2:
		p.Host, p.Container = parts[0], parts[1]
	default:
		n := len(parts)
		p.HostIP = strings.Join(parts[:n-2], ":")
		p.Host, p.Container = parts[n-2], parts[n-1]
	}
	return p
}

// RuntimeOf is the engine a service runs in.
func (p *Project) RuntimeOf(svc, hostDefault string) string {
	if s := p.Services[svc]; s != nil && s.Runtime != "" {
		return s.Runtime
	}
	if p.Runtime != "" {
		return p.Runtime
	}
	return hostDefault
}

// Group is the services one engine runs.
type Group struct {
	Runtime  string   `json:"runtime"`
	Services []string `json:"services"`
}

// Plan splits the services by engine, ordered so that a group whose
// services others depend on comes first.
func (p *Project) Plan(hostDefault string) []Group {
	by := map[string][]string{}
	var order []string
	for _, n := range p.Names {
		rt := p.RuntimeOf(n, hostDefault)
		if _, ok := by[rt]; !ok {
			order = append(order, rt)
		}
		by[rt] = append(by[rt], n)
	}
	// needs[a][b]: group a has a service that depends on one in group b.
	needs := map[string]map[string]bool{}
	for _, n := range p.Names {
		a := p.RuntimeOf(n, hostDefault)
		for _, d := range p.Services[n].DependsOn {
			if _, ok := p.Services[d]; !ok {
				continue
			}
			if b := p.RuntimeOf(d, hostDefault); b != a {
				if needs[a] == nil {
					needs[a] = map[string]bool{}
				}
				needs[a][b] = true
			}
		}
	}
	var out []Group
	done := map[string]bool{}
	for len(out) < len(order) {
		picked := ""
		for _, rt := range order {
			if done[rt] {
				continue
			}
			ready := true
			for b := range needs[rt] {
				if !done[b] {
					ready = false
				}
			}
			if ready {
				picked = rt
				break
			}
		}
		if picked == "" { // a cycle between engines: keep file order
			for _, rt := range order {
				if !done[rt] {
					picked = rt
					break
				}
			}
		}
		done[picked] = true
		out = append(out, Group{Runtime: picked, Services: by[picked]})
	}
	return out
}

// Mixed reports whether the lab needs another engine than the host default.
func (p *Project) Mixed(hostDefault string) bool {
	g := p.Plan(hostDefault)
	return len(g) > 1 || (len(g) == 1 && g[0].Runtime != hostDefault)
}

// Link is a depends_on between services in different engines. They do not
// share a network, so the dependency only works through a published port.
type Link struct {
	From        string `json:"from"`
	To          string `json:"to"`
	FromRuntime string `json:"fromRuntime"`
	ToRuntime   string `json:"toRuntime"`
	Via         []Port `json:"via"` // ports of To published on the host
}

func (p *Project) CrossLinks(hostDefault string) []Link {
	var out []Link
	for _, n := range p.Names {
		a := p.RuntimeOf(n, hostDefault)
		for _, d := range p.Services[n].DependsOn {
			t := p.Services[d]
			if t == nil {
				continue
			}
			if b := p.RuntimeOf(d, hostDefault); b != a {
				l := Link{From: n, To: d, FromRuntime: a, ToRuntime: b, Via: []Port{}}
				for _, pt := range t.Ports {
					if pt.Host != "" {
						l.Via = append(l.Via, pt)
					}
				}
				out = append(out, l)
			}
		}
	}
	return out
}

var badName = regexp.MustCompile(`[^a-z0-9_-]+`)

// ProjectName is how compose names a project after its folder.
func ProjectName(dir string) string {
	n := strings.ToLower(dir)
	if i := strings.LastIndex(n, "/"); i >= 0 {
		n = n[i+1:]
	}
	n = badName.ReplaceAllString(n, "")
	return strings.TrimLeft(n, "_-")
}

// FileNames are the compose files looked up in a folder, in order.
var FileNames = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

var vmName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

func parseVM(name string, m yaml.MapSlice, imgs map[string]string) (*VM, error) {
	if !vmName.MatchString(name) {
		return nil, fmt.Errorf("x-vms: nome inválido %q (use letras, números, ponto, hífen ou _)", name)
	}
	v := &VM{Name: name, Image: "debian-12", CPUs: 1, Memory: 1024, Disk: "10G", Ports: []Port{}}
	if x, ok := get(m, "image"); ok && str(x) != "" {
		v.Image = interpolate(str(x))
		if f, ok := imgs[v.Image]; ok {
			v.ImageName, v.Image = v.Image, f
		}
	}
	if x, ok := get(m, "cpus"); ok {
		n, err := strconv.Atoi(str(x))
		if err != nil || n < 1 || n > 64 {
			return nil, fmt.Errorf("x-vms.%s.cpus inválido: %v", name, x)
		}
		v.CPUs = n
	}
	if x, ok := get(m, "memory"); ok {
		mb, err := ParseMB(str(x))
		if err != nil {
			return nil, fmt.Errorf("x-vms.%s.memory: %v", name, err)
		}
		v.Memory = mb
	}
	if x, ok := get(m, "disk"); ok && str(x) != "" {
		d := strings.ToUpper(strings.TrimSpace(str(x)))
		if !regexp.MustCompile(`^[0-9]+[KMGT]?$`).MatchString(d) {
			return nil, fmt.Errorf("x-vms.%s.disk inválido: %v (use algo como 20G)", name, x)
		}
		v.Disk = d
	}
	if x, ok := get(m, "ports"); ok {
		v.Ports = parsePorts(x)
		for _, p := range v.Ports {
			if p.Host == "" || p.Container == "" {
				return nil, fmt.Errorf("x-vms.%s.ports: informe porta do host e da VM, como \"8081:80\"", name)
			}
			if p.Proto != "tcp" && p.Proto != "udp" {
				return nil, fmt.Errorf("x-vms.%s.ports: protocolo %q sem suporte", name, p.Proto)
			}
		}
	}
	if x, ok := get(m, "ssh"); ok {
		n, err := strconv.Atoi(str(x))
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("x-vms.%s.ssh: porta inválida %v", name, x)
		}
		v.SSH = n
	}
	if x, ok := get(m, "packages"); ok {
		v.Packages = strList(x)
	}
	if x, ok := get(m, "run"); ok {
		v.Run = strList(x)
	}
	if x, ok := get(m, "iso"); ok && str(x) != "" {
		v.ISO = interpolate(str(x))
		if strings.ContainsAny(v.ISO, "'\"`$\\\r\n") {
			return nil, fmt.Errorf("x-vms.%s.iso: o caminho ou a URL tem caracteres que não são aceitos (aspas, $, \\ ou quebra de linha)", name)
		}
		if _, has := get(m, "image"); !has {
			v.Image = "" // blank disk: the installer fills it
		}
	}
	if x, ok := get(m, "metrics"); ok {
		v.Metrics = truthy(x)
	}
	if x, ok := get(m, "lan"); ok {
		v.LAN = truthy(x)
	}
	if x, ok := get(m, "tela"); ok {
		v.Screen = truthy(x)
	}
	if x, ok := get(m, "depois"); ok {
		v.After = strList(x)
	} else if x, ok := get(m, "after"); ok {
		v.After = strList(x)
	}
	if x, ok := get(m, "cloud-init"); ok {
		if b, isBool := x.(bool); isBool || strings.TrimSpace(str(x)) == "false" {
			v.NoCloudInit = isBool && !b || !isBool
		} else if mm := asMap(x); mm != nil {
			b, _ := yaml.Marshal(mm)
			v.CloudInit = string(b)
		} else if f := strings.TrimSpace(str(x)); IsCloudInitFile(f) {
			// cloud-init: ./web.cloud-init.yaml — a file of the project,
			// read when the project loads.
			v.CloudInitFile = f
		} else {
			v.CloudInit = str(x)
		}
	}
	if x, ok := get(m, "nic"); ok && str(x) != "" {
		n := strings.ToLower(strings.TrimSpace(str(x)))
		if _, ok := NICModels[n]; !ok {
			return nil, fmt.Errorf("x-vms.%s.nic: %q não é um modelo conhecido (use virtio, e1000, e1000e, rtl8139 ou vmxnet3)", name, str(x))
		}
		v.NIC = n
	}
	for _, k := range []string{"management", "gerencia", "gerência"} {
		if x, ok := get(m, k); ok && !truthy(x) {
			v.NoManagement = true
		}
	}
	if x, ok := get(m, "machine"); ok && str(x) != "" {
		mt := strings.TrimSpace(str(x))
		if mt != "q35" && mt != "pc" {
			return nil, fmt.Errorf("x-vms.%s.machine: use q35 ou pc", name)
		}
		v.Machine = mt
	}
	if x, ok := get(m, "pos"); ok && x != nil {
		pos, err := parsePos("x-vms."+name+".pos", x)
		if err != nil {
			return nil, err
		}
		v.Pos = pos
	}
	if x, ok := get(m, "icon"); ok && x != nil {
		ic, err := parseIcon("x-vms."+name+".icon", x)
		if err != nil {
			return nil, err
		}
		v.Icon = ic
	}
	if x, ok := get(m, "interfaces"); ok && x != nil {
		list, err := parseIfaces("x-vms."+name+".interfaces", x)
		if err != nil {
			return nil, err
		}
		v.Interfaces = list
	}
	cat := v.Image
	if IsImageFileRef(v.Image) || (IsImagePath(v.Image) && ClassifyImage(v.Image) != "") {
		// A file of the image folders (or given by its path): what it is
		// comes from its name.
		cat = ClassifyImage(v.Image)
		v.Catalog = cat
	}
	if IsImagePath(v.Image) && v.Catalog != "" {
		v.ImageSrc = v.Image // the file itself, wherever it is on the machine
	}
	if prof, ok := Appliances[cat]; ok {
		set := func(k ...string) bool {
			for _, x := range k {
				if _, ok := get(m, x); ok {
					return true
				}
			}
			return false
		}
		v.Kind, v.Platform = prof.Kind, prof.Platform
		if !set("nic") {
			v.NIC = prof.NIC
		}
		if !set("memory") {
			v.Memory = prof.Memory
		}
		if !set("machine") {
			v.Machine = prof.Machine
		}
		if !set("cloud-init") {
			v.NoCloudInit = true
		}
		if !set("management", "gerencia", "gerência") {
			v.NoManagement = true
		}
	}
	if v.Kind == "iol" || v.Kind == "dyn" || v.Kind == "vpcs" {
		// IOL, Dynamips and VPCS are not VMs: no disk, no cloud-init, no
		// management NIC; their ports go through the cables' relay.
		what := map[string]string{"iol": "IOL", "dyn": "Dynamips", "vpcs": "VPCS"}[v.Kind]
		v.NoCloudInit, v.NoManagement, v.NIC, v.Machine = true, true, "", ""
		for i, f := range v.Interfaces {
			if f.Bridge != "" {
				return nil, fmt.Errorf("x-vms.%s.interfaces[%d]: %s liga só por cabos (link:), não em bridge", name, i, what)
			}
		}
		if v.ISO != "" || v.Nix != nil || v.Screen || v.LAN {
			return nil, fmt.Errorf("x-vms.%s: %s não usa iso, nix, tela nem lan", name, what)
		}
		if v.Kind == "vpcs" && len(v.Interfaces) > 1 {
			return nil, fmt.Errorf("x-vms.%s: o VPCS tem uma porta só (tem %d em interfaces)", name, len(v.Interfaces))
		}
		if x, ok := get(m, "slots"); ok {
			if v.Kind != "dyn" {
				return nil, fmt.Errorf("x-vms.%s.slots: as placas por slot valem para roteadores Dynamips (7200, 3725, 2691)", name)
			}
			v.Slots = strList(x)
			if v.Slots == nil {
				v.Slots = []string{}
			}
		}
		if x, ok := get(m, "npe"); ok {
			v.NPE = strings.ToLower(strings.TrimSpace(str(x)))
			if v.Kind != "dyn" || v.Platform != "7200" {
				return nil, fmt.Errorf("x-vms.%s.npe: a placa NPE vale só para o 7200 do Dynamips", name)
			}
			if !DynNPEs[v.NPE] {
				return nil, fmt.Errorf("x-vms.%s.npe: %q não é uma NPE do 7200 que o Dynamips roda (use npe-100, npe-150, npe-175, npe-200, npe-225, npe-300 ou npe-400)", name, v.NPE)
			}
		}
		if v.Kind == "dyn" && v.Platform == "7200" && v.Memory > DynMaxRAM7200 {
			// Dynamips puts the 7200's RAM from address 0; past 256 MB it
			// covers the boot ROM (or the NPE's other devices) and the
			// router dies at once ("No memory map for code execution").
			return nil, fmt.Errorf("x-vms.%s.memory: o 7200 do Dynamips roda com até %d MB (com mais, a RAM cobre a ROM de boot e ele não inicia); use memory: %dM", name, DynMaxRAM7200, DynMaxRAM7200)
		}
		if v.Kind == "dyn" {
			ports, err := DynLayout(v.Platform, v.Slots)
			if err != nil {
				return nil, fmt.Errorf("x-vms.%s.slots: %v", name, err)
			}
			if len(v.Interfaces) > len(ports) {
				return nil, fmt.Errorf("x-vms.%s: o c%s do Dynamips tem no máximo %d portas Ethernet (tem %d em interfaces)", name, v.Platform, len(ports), len(v.Interfaces))
			}
		}
	}
	if v.NoManagement && len(v.Ports) > 0 {
		return nil, fmt.Errorf("x-vms.%s: ports precisa da placa de gerência (management: false tira ela)", name)
	}
	if v.Metrics && (v.NoCloudInit || v.NoManagement || v.ISO != "") {
		return nil, fmt.Errorf("x-vms.%s: metrics precisa de cloud-init e da placa de gerência (equipamentos de rede e VMs de ISO não têm)", name)
	}
	if x, ok := get(m, "nix"); ok {
		nm := asMap(x)
		if nm == nil {
			return nil, fmt.Errorf("x-vms.%s.nix: informe module (o arquivo .nix do projeto) e, se quiser, params", name)
		}
		n := &VMNix{Params: map[string]any{}}
		if mod, ok := get(nm, "module"); ok {
			n.Module = strings.TrimSpace(interpolate(str(mod)))
		}
		if n.Module == "" {
			return nil, fmt.Errorf("x-vms.%s.nix.module: informe o arquivo do módulo NixOS, como ./%s.nix", name, name)
		}
		if pr, ok := get(nm, "params"); ok && pr != nil {
			pm := asMap(pr)
			if pm == nil {
				return nil, fmt.Errorf("x-vms.%s.nix.params: use chave: valor", name)
			}
			for _, it := range pm {
				n.Params[str(it.Key)] = plainValue(it.Value)
			}
		}
		for _, it := range nm {
			if k := str(it.Key); k != "module" && k != "params" {
				return nil, fmt.Errorf("x-vms.%s.nix: campo desconhecido %q (use module e params)", name, k)
			}
		}
		if _, ok := n.Params["hostname"]; !ok {
			n.Params["hostname"] = name
		}
		if !IsNixOSImage(v.Image) {
			return nil, fmt.Errorf("x-vms.%s.nix: só vale com uma imagem NixOS do Gantry (image: nixos-lab-26.05), não com %s", name, v.Image)
		}
		if v.NoCloudInit || v.NoManagement {
			return nil, fmt.Errorf("x-vms.%s.nix: precisa do cloud-init e da placa de gerência (o Gantry entrega a configuração por eles)", name)
		}
		v.Nix = n
		if len(v.Packages) > 0 || len(v.Run) > 0 {
			v.Warnings = append(v.Warnings, fmt.Sprintf("x-vms.%s: com nix:, packages e run são ignorados; os pacotes e comandos vão no módulo %s", name, n.Module))
			v.Packages, v.Run = nil, nil
		}
		if v.Memory < 2048 {
			v.Warnings = append(v.Warnings, fmt.Sprintf("x-vms.%s: com %d MB o nixos-rebuild pode faltar memória; use pelo menos 2048", name, v.Memory))
		}
	}
	if x, ok := get(m, "config"); ok && x != nil {
		if v.Kind != "iol" && v.Kind != "dyn" && v.Kind != "vpcs" {
			return nil, fmt.Errorf("x-vms.%s.config: a configuração inicial vale para nós IOL, Dynamips e VPCS (image: um i86bi_….bin, cisco-iol, cisco-c7200 ou vpcs)", name)
		}
		c, isStr := x.(string)
		if !isStr {
			return nil, fmt.Errorf("x-vms.%s.config: use um bloco de texto (config: |) com os comandos do IOS, ou um arquivo do projeto (config: equipamentos/%s.cfg)", name, name)
		}
		if f := strings.TrimSpace(c); IsConfigFile(f) {
			// config: equipamentos/R1.cfg — a file of the project, read
			// when the project loads.
			v.ConfigFile = f
		} else {
			v.Config = c
		}
	}
	return v, nil
}

// plainValue turns a YAML value into what JSON writes the same way (maps
// keep their keys, numbers and booleans stay typed, text is interpolated).
func plainValue(x any) any {
	switch t := x.(type) {
	case yaml.MapSlice:
		out := map[string]any{}
		for _, it := range t {
			out[str(it.Key)] = plainValue(it.Value)
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for k, v := range t {
			out[k] = plainValue(v)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, v := range t {
			out[i] = plainValue(v)
		}
		return out
	case string:
		return interpolate(t)
	}
	return x
}

func truthy(v any) bool {
	switch strings.ToLower(strings.TrimSpace(str(v))) {
	case "true", "yes", "sim", "1", "on":
		return true
	}
	return false
}

// ParseMB reads a memory size: 1024 (MB), 512m, 2g, 2GB.
func ParseMB(s string) (int, error) {
	t := strings.ToLower(strings.TrimSpace(s))
	t = strings.TrimSuffix(t, "b")
	mul := 1
	switch {
	case strings.HasSuffix(t, "g"):
		mul, t = 1024, strings.TrimSuffix(t, "g")
	case strings.HasSuffix(t, "m"):
		t = strings.TrimSuffix(t, "m")
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil || f <= 0 {
		return 0, fmt.Errorf("tamanho inválido %q (use 1024, 512m ou 2g)", s)
	}
	mb := int(f * float64(mul))
	if mb < 128 {
		return 0, fmt.Errorf("memória pequena demais: %d MB", mb)
	}
	return mb, nil
}

var fullVarRe = regexp.MustCompile(`\$\$|\$\{([A-Za-z_][A-Za-z0-9_]*)(?:(:?[-?+])([^}]*))?\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// Interpolate replaces ${VAR}, ${VAR:-default}, ${VAR-default}, ${VAR:?msg}
// and $VAR the way compose does, with the values in env (the lab's .env).
// Unset variables without a default become empty. $$ stays a literal $.
func Interpolate(b []byte, env map[string]string) []byte {
	return fullVarRe.ReplaceAllFunc(b, func(m []byte) []byte {
		if string(m) == "$$" {
			return m
		}
		sub := fullVarRe.FindSubmatch(m)
		name, op, arg := string(sub[1]), string(sub[2]), string(sub[3])
		if name == "" {
			name = string(sub[4])
		}
		v, set := env[name]
		switch op {
		case ":-":
			if v == "" {
				return []byte(arg)
			}
		case "-":
			if !set {
				return []byte(arg)
			}
		case ":+":
			if v != "" {
				return []byte(arg)
			}
			return nil
		case "+":
			if set {
				return []byte(arg)
			}
			return nil
		}
		return []byte(v)
	})
}

// Var is a variable a compose file uses.
type Var struct {
	Name     string `json:"name"`
	Default  string `json:"default,omitempty"`
	Required bool   `json:"required,omitempty"` // ${VAR:?message}
	Message  string `json:"message,omitempty"`
}

// Vars lists the variables used in a compose file, in order of appearance.
func Vars(b []byte) []Var {
	var out []Var
	seen := map[string]int{}
	for _, sub := range fullVarRe.FindAllSubmatch(b, -1) {
		name, op, arg := string(sub[1]), string(sub[2]), string(sub[3])
		if name == "" {
			name = string(sub[4])
		}
		if name == "" {
			continue
		}
		i, ok := seen[name]
		if !ok {
			i = len(out)
			seen[name] = i
			out = append(out, Var{Name: name})
		}
		switch op {
		case ":-", "-":
			if out[i].Default == "" {
				out[i].Default = arg
			}
		case ":?", "?":
			out[i].Required, out[i].Message = true, arg
		}
	}
	return out
}

// ParseEnv reads a .env file.
func ParseEnv(b []byte) map[string]string {
	env := map[string]string{}
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(strings.TrimSuffix(l, "\r"))
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		l = strings.TrimPrefix(l, "export ")
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		env[strings.TrimSpace(k)] = v
	}
	return env
}
