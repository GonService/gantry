package server

import (
	"testing"

	"gantry/internal/composex"
)

func TestVMPortsCompose(t *testing.T) {
	src := "x-vms:\n  web:\n    image: debian-12\n    ports: [\"8081:80\"]   # site\n    cpus: 2\n"
	list := []composex.Port{composex.ParsePort("8081:80"), composex.ParsePort("127.0.0.1:5353:53/udp"), composex.ParsePort("[::1]:9000:9000")}
	val := "["
	for i, p := range list {
		if i > 0 {
			val += ", "
		}
		val += portItem(p)
	}
	val += "]"
	out, ok := setBlockField(src, "x-vms", "web", "ports", val)
	if !ok {
		t.Fatal("not set")
	}
	p, err := composex.Parse([]byte(out))
	if err != nil {
		t.Fatal(err, out)
	}
	got := p.VM("web").Ports
	if len(got) != 3 || got[1].HostIP != "127.0.0.1" || got[1].Proto != "udp" || got[2].HostIP != "::1" || p.VM("web").CPUs != 2 {
		t.Fatalf("%+v\n%s", got, out)
	}
	// The last port out: the field goes away.
	out, _ = setBlockField(src, "x-vms", "web", "ports", "")
	if p, err := composex.Parse([]byte(out)); err != nil || len(p.VM("web").Ports) != 0 {
		t.Fatalf("%v %s", err, out)
	}
}
