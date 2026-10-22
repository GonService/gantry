package pcap

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"
)

func eth(et uint16, payload []byte) []byte {
	b := append([]byte{0x52, 0x54, 0, 0, 0, 2, 0x52, 0x54, 0, 0, 0, 1}, byte(et>>8), byte(et))
	return append(b, payload...)
}

func TestDescribe(t *testing.T) {
	icmp, _ := hex.DecodeString("4500005400004000400100000a0900010a0900020800000000180001")
	arp, _ := hex.DecodeString("0001080006040001525400000001" + "0a090001" + "000000000000" + "0a090002")
	ospf, _ := hex.DecodeString("45c0004c0000000001590000" + "0a000001e0000005" + "0201002c" + "01010101" + "00000000")
	tcp, _ := hex.DecodeString("450000280000400040060000c0a80001c0a80002" + "d43100160000000100000000" + "5012ffff00000000")
	vlan := append([]byte{0x00, 0x0a, 0x08, 0x06}, arp...)
	for _, c := range []struct {
		frame []byte
		want  string
	}{
		{eth(0x0800, icmp), "10.9.0.1 → 10.9.0.2  ICMP echo request id 24 seq 1 ttl 64"},
		{eth(0x0806, arp), "ARP Who has 10.9.0.2? Tell 10.9.0.1"},
		{eth(0x0800, ospf), "OSPF Hello router 1.1.1.1 area 0.0.0.0"},
		{eth(0x0800, tcp), "TCP 54321 → 22 [SYN,ACK]"},
		{eth(0x8100, vlan), "vlan 10 "},
		{eth(0x88cc, nil), "LLDP"},
		{[]byte{1, 2}, "short frame"},
	} {
		if got := Describe(c.frame); !strings.Contains(got, c.want) {
			t.Errorf("%q: queria %q", got, c.want)
		}
	}
}

func TestReader(t *testing.T) {
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, []uint32{0xA1B2C3D4})
	binary.Write(&b, binary.LittleEndian, []uint16{2, 4})
	binary.Write(&b, binary.LittleEndian, []uint32{0, 0, 65535, 1})
	for i := 0; i < 2; i++ {
		binary.Write(&b, binary.LittleEndian, []uint32{1792431398, 500000, 3, 3})
		b.Write([]byte{1, 2, 3})
	}
	r, err := NewReader(&b)
	if err != nil {
		t.Fatal(err)
	}
	f, err := r.Next()
	if err != nil || len(f.Data) != 3 || f.Time.Unix() != 1792431398 || f.Time.Nanosecond() != 500000000 {
		t.Fatal(f, err)
	}
	if _, err := r.Next(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Next(); err == nil {
		t.Fatal("fim")
	}
	if _, err := NewReader(strings.NewReader(strings.Repeat("x", 24))); err == nil {
		t.Fatal("não é pcap")
	}
}
