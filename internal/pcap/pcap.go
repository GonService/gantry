// Package pcap reads the captures of the lab's cables (classic pcap,
// Ethernet) and describes each frame in one line, as a packet list does
// (in Wireshark's words, the same in every language of the app).
package pcap

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// Frame is one captured frame.
type Frame struct {
	Time time.Time
	Data []byte
}

// Reader reads the frames of a pcap stream (it may still be growing).
type Reader struct {
	r     *bufio.Reader
	order binary.ByteOrder
	nano  bool
}

// NewReader reads the global header.
func NewReader(r io.Reader) (*Reader, error) {
	br := bufio.NewReaderSize(r, 1<<16)
	var h [24]byte
	if _, err := io.ReadFull(br, h[:]); err != nil {
		return nil, err
	}
	pr := &Reader{r: br}
	switch {
	case binary.LittleEndian.Uint32(h[:]) == 0xA1B2C3D4:
		pr.order = binary.LittleEndian
	case binary.BigEndian.Uint32(h[:]) == 0xA1B2C3D4:
		pr.order = binary.BigEndian
	case binary.LittleEndian.Uint32(h[:]) == 0xA1B23C4D:
		pr.order, pr.nano = binary.LittleEndian, true
	case binary.BigEndian.Uint32(h[:]) == 0xA1B23C4D:
		pr.order, pr.nano = binary.BigEndian, true
	default:
		return nil, errors.New("não é um arquivo pcap")
	}
	return pr, nil
}

// Next reads the next frame.
func (p *Reader) Next() (Frame, error) {
	var h [16]byte
	if _, err := io.ReadFull(p.r, h[:]); err != nil {
		return Frame{}, err
	}
	sec, frac := p.order.Uint32(h[0:]), p.order.Uint32(h[4:])
	n := p.order.Uint32(h[8:])
	if n > 1<<18 {
		return Frame{}, fmt.Errorf("registro grande demais (%d bytes)", n)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(p.r, b); err != nil {
		return Frame{}, err
	}
	ns := int64(frac) * 1000
	if p.nano {
		ns = int64(frac)
	}
	return Frame{Time: time.Unix(int64(sec), ns), Data: b}, nil
}

var etherTypes = map[uint16]string{0x0806: "ARP", 0x86DD: "IPv6", 0x88CC: "LLDP", 0x8809: "LACP", 0x88A8: "802.1ad", 0x8847: "MPLS"}

// Describe is a one-line summary: source → destination, protocol, details.
func Describe(b []byte) string {
	if len(b) < 14 {
		return fmt.Sprintf("short frame (%d bytes)", len(b))
	}
	dst, src := net.HardwareAddr(b[0:6]), net.HardwareAddr(b[6:12])
	et := binary.BigEndian.Uint16(b[12:14])
	off := 14
	vlan := ""
	for (et == 0x8100 || et == 0x88A8) && len(b) >= off+4 {
		vlan += fmt.Sprintf("vlan %d ", binary.BigEndian.Uint16(b[off:off+2])&0x0FFF)
		et = binary.BigEndian.Uint16(b[off+2 : off+4])
		off += 4
	}
	l2 := src.String() + " → " + dst.String()
	if et <= 1500 { // 802.3 with LLC: STP, CDP, DTP, VTP…
		switch {
		case dst.String() == "01:80:c2:00:00:00":
			return vlan + l2 + "  STP " + stp(b[off:])
		case dst.String() == "01:00:0c:cc:cc:cc":
			return vlan + l2 + "  CDP/VTP/DTP"
		case dst.String() == "01:00:0c:cc:cc:cd":
			return vlan + l2 + "  PVST+"
		case dst.String() == "01:80:c2:00:00:02":
			return vlan + l2 + "  LACP"
		}
		return vlan + l2 + fmt.Sprintf("  802.3 (%d bytes)", et)
	}
	p := b[off:]
	switch et {
	case 0x0800:
		return vlan + ipv4(p, l2)
	case 0x0806:
		return vlan + arp(p, l2)
	case 0x86DD:
		return vlan + ipv6(p, l2)
	}
	if n, ok := etherTypes[et]; ok {
		return vlan + l2 + "  " + n
	}
	return vlan + l2 + fmt.Sprintf("  ethertype 0x%04x", et)
}

func stp(p []byte) string {
	// LLC (3 bytes) then the BPDU: protocol, version, type, flags, root…
	if len(p) < 3+4 {
		return ""
	}
	p = p[3:]
	if len(p) >= 4 && p[3] == 0x80 {
		return "TCN"
	}
	if len(p) < 35 {
		return ""
	}
	root := binary.BigEndian.Uint16(p[5:7])
	rmac := net.HardwareAddr(p[7:13])
	cost := binary.BigEndian.Uint32(p[13:17])
	kind := "Conf"
	if p[1] >= 2 {
		kind = "RST"
	}
	return fmt.Sprintf("%s root %d/%s cost %d", kind, root, rmac, cost)
}

func arp(p []byte, l2 string) string {
	if len(p) < 28 {
		return l2 + "  ARP"
	}
	spa, tpa := net.IP(p[14:18]), net.IP(p[24:28])
	switch binary.BigEndian.Uint16(p[6:8]) {
	case 1:
		return fmt.Sprintf("%s  ARP Who has %s? Tell %s", l2, tpa, spa)
	case 2:
		return fmt.Sprintf("%s  ARP %s is at %s", l2, spa, net.HardwareAddr(p[8:14]))
	}
	return l2 + "  ARP"
}

var ipProtos = map[byte]string{1: "ICMP", 2: "IGMP", 6: "TCP", 17: "UDP", 47: "GRE", 50: "ESP", 88: "EIGRP", 89: "OSPF", 103: "PIM", 112: "VRRP"}

func ipv4(p []byte, l2 string) string {
	if len(p) < 20 || p[0]>>4 != 4 {
		return l2 + "  IPv4 (malformed)"
	}
	ihl := int(p[0]&0x0F) * 4
	if ihl < 20 || len(p) < ihl {
		return l2 + "  IPv4 (malformed)"
	}
	src, dst := net.IP(p[12:16]), net.IP(p[16:20])
	proto := p[9]
	ttl := p[8]
	rest := p[ihl:]
	head := fmt.Sprintf("%s → %s", src, dst)
	switch proto {
	case 1:
		return head + "  " + icmp(rest) + fmt.Sprintf(" ttl %d", ttl)
	case 6:
		return head + "  " + tcp(rest)
	case 17:
		return head + "  " + udp(rest)
	case 89:
		return head + "  " + ospf(rest)
	}
	if n, ok := ipProtos[proto]; ok {
		return head + "  " + n
	}
	return head + fmt.Sprintf("  protocol %d", proto)
}

func icmp(p []byte) string {
	if len(p) < 8 {
		return "ICMP"
	}
	id, seq := binary.BigEndian.Uint16(p[4:6]), binary.BigEndian.Uint16(p[6:8])
	switch p[0] {
	case 8:
		return fmt.Sprintf("ICMP echo request id %d seq %d", id, seq)
	case 0:
		return fmt.Sprintf("ICMP echo reply id %d seq %d", id, seq)
	case 3:
		return fmt.Sprintf("ICMP destination unreachable (code %d)", p[1])
	case 11:
		return "ICMP time exceeded"
	}
	return fmt.Sprintf("ICMP type %d", p[0])
}

var tcpFlags = []struct {
	bit  byte
	name string
}{{0x02, "SYN"}, {0x10, "ACK"}, {0x01, "FIN"}, {0x04, "RST"}, {0x08, "PSH"}}

func tcp(p []byte) string {
	if len(p) < 20 {
		return "TCP"
	}
	var fl []string
	for _, f := range tcpFlags {
		if p[13]&f.bit != 0 {
			fl = append(fl, f.name)
		}
	}
	return fmt.Sprintf("TCP %d → %d [%s]", binary.BigEndian.Uint16(p[0:2]), binary.BigEndian.Uint16(p[2:4]), strings.Join(fl, ","))
}

var udpPorts = map[uint16]string{53: "DNS", 67: "DHCP", 68: "DHCP", 123: "NTP", 161: "SNMP", 162: "SNMP trap", 514: "syslog", 520: "RIP", 1985: "HSRP", 4789: "VXLAN", 3784: "BFD"}

func udp(p []byte) string {
	if len(p) < 8 {
		return "UDP"
	}
	sp, dp := binary.BigEndian.Uint16(p[0:2]), binary.BigEndian.Uint16(p[2:4])
	s := fmt.Sprintf("UDP %d → %d", sp, dp)
	if n, ok := udpPorts[dp]; ok {
		return s + " " + n
	}
	if n, ok := udpPorts[sp]; ok {
		return s + " " + n
	}
	return s
}

var ospfTypes = map[byte]string{1: "Hello", 2: "DB Description", 3: "LS Request", 4: "LS Update", 5: "LS Ack"}

func ospf(p []byte) string {
	if len(p) < 12 {
		return "OSPF"
	}
	t := ospfTypes[p[1]]
	if t == "" {
		t = fmt.Sprintf("type %d", p[1])
	}
	return fmt.Sprintf("OSPF %s router %s area %s", t, net.IP(p[4:8]), net.IP(p[8:12]))
}

func ipv6(p []byte, l2 string) string {
	if len(p) < 40 {
		return l2 + "  IPv6"
	}
	src, dst := net.IP(p[8:24]), net.IP(p[24:40])
	nh := p[6]
	s := fmt.Sprintf("%s → %s", src, dst)
	switch nh {
	case 58:
		if len(p) > 40 {
			names := map[byte]string{128: "echo request", 129: "echo reply", 133: "router solicitation", 134: "router advertisement", 135: "neighbor solicitation", 136: "neighbor advertisement"}
			if n, ok := names[p[40]]; ok {
				return s + "  ICMPv6 " + n
			}
		}
		return s + "  ICMPv6"
	case 6:
		return s + "  " + tcp(p[40:])
	case 17:
		return s + "  " + udp(p[40:])
	case 89:
		return s + "  OSPFv3"
	}
	return s + fmt.Sprintf("  IPv6 next header %d", nh)
}
