package vmmetrics

import (
	"strings"
	"testing"
	"time"
)

const scrape1 = `# HELP node_cpu_seconds_total Seconds the CPUs spent in each mode.
# TYPE node_cpu_seconds_total counter
node_cpu_seconds_total{cpu="0",mode="idle"} 100
node_cpu_seconds_total{cpu="0",mode="user"} 10
node_cpu_seconds_total{cpu="1",mode="idle"} 100
node_cpu_seconds_total{cpu="1",mode="system"} 5
node_memory_MemTotal_bytes 2.147483648e+09
node_memory_MemAvailable_bytes 1.073741824e+09
node_filesystem_size_bytes{device="/dev/vda1",fstype="ext4",mountpoint="/"} 2e+10
node_filesystem_avail_bytes{device="/dev/vda1",fstype="ext4",mountpoint="/"} 1.5e+10
node_filesystem_size_bytes{device="tmpfs",fstype="tmpfs",mountpoint="/run"} 1e+08
node_network_receive_bytes_total{device="lo"} 5000
node_network_receive_bytes_total{device="eth0"} 1000
node_network_transmit_bytes_total{device="eth0"} 2000
node_load1 0.5
node_boot_time_seconds 1.7e+09
node_uname_info{machine="x86_64",nodename="web",release="6.1",sysname="Linux",version="#1 SMP \"debian\""} 1
`

func TestParseAndRates(t *testing.T) {
	s := Parse(strings.NewReader(scrape1))
	if len(s) != 15 {
		t.Fatalf("%d samples", len(s))
	}
	if u := s[len(s)-1]; u.Name != "node_uname_info" || u.Labels["version"] != `#1 SMP "debian"` || u.Labels["nodename"] != "web" {
		t.Fatalf("%+v", u)
	}
	t0 := time.Unix(1_700_000_100, 0)
	a := Read(s, t0)
	if a.CPUs != 2 || a.MemTotal != 2147483648 || a.DiskSize != 2e10 || a.RX != 1000 {
		t.Fatalf("%+v", a)
	}
	p := PointOf(nil, a)
	if p.CPU != -1 || p.MemUsed != 1073741824 || p.DiskUsed != 5e9 || p.Uptime != 100 {
		t.Fatalf("%+v", p)
	}
	// 10 s later: 15 s of CPU busy (1.5 cores) and 5 s idle, 10 KB/s in.
	next := strings.NewReplacer(`cpu="0",mode="user"} 10`, `cpu="0",mode="user"} 20`, `cpu="1",mode="system"} 5`, `cpu="1",mode="system"} 10`,
		`cpu="0",mode="idle"} 100`, `cpu="0",mode="idle"} 103`, `cpu="1",mode="idle"} 100`, `cpu="1",mode="idle"} 102`,
		`{device="eth0"} 1000`, `{device="eth0"} 101000`).Replace(scrape1)
	b := Read(Parse(strings.NewReader(next)), t0.Add(10*time.Second))
	q := PointOf(&a, b)
	if q.CPU != 150 || q.RX != 10000 || q.TX != 0 {
		t.Fatalf("%+v", q)
	}
	// A reboot in between: no rates from the old counters.
	b.Boot++
	if q := PointOf(&a, b); q.CPU != -1 || q.RX != 0 {
		t.Fatalf("after reboot: %+v", q)
	}
}
