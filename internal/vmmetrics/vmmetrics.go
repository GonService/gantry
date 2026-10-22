// Package vmmetrics reads the node_exporter of a VM (the Prometheus text
// format) and turns two readings into the numbers of the graphs: CPU, memory,
// disk and network from inside the VM.
package vmmetrics

import (
	"bufio"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
)

// Sample is one line of the text format: a metric, its labels and value.
type Sample struct {
	Name   string
	Labels map[string]string
	Value  float64
}

// Parse reads the Prometheus text exposition format (comments and
// timestamps ignored; lines it cannot read are skipped).
func Parse(r io.Reader) []Sample {
	var out []Sample
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		s := Sample{Labels: map[string]string{}}
		rest := line
		if i := strings.IndexAny(line, "{ "); i < 0 {
			continue
		} else {
			s.Name = line[:i]
			rest = line[i:]
		}
		if strings.HasPrefix(rest, "{") {
			end, ok := parseLabels(rest, s.Labels)
			if !ok {
				continue
			}
			rest = rest[end:]
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			continue
		}
		v, err := strconv.ParseFloat(f[0], 64)
		if err != nil {
			continue
		}
		s.Value = v
		out = append(out, s)
	}
	return out
}

// parseLabels reads {a="x",b="y\"z"} into m and returns where it ended.
func parseLabels(s string, m map[string]string) (int, bool) {
	i := 1
	for i < len(s) {
		for i < len(s) && (s[i] == ' ' || s[i] == ',') {
			i++
		}
		if i < len(s) && s[i] == '}' {
			return i + 1, true
		}
		eq := strings.IndexByte(s[i:], '=')
		if eq < 0 || i+eq+1 >= len(s) || s[i+eq+1] != '"' {
			return 0, false
		}
		key := strings.TrimSpace(s[i : i+eq])
		i += eq + 2
		var b strings.Builder
		for i < len(s) && s[i] != '"' {
			if s[i] == '\\' && i+1 < len(s) {
				i++
				switch s[i] {
				case 'n':
					b.WriteByte('\n')
				default:
					b.WriteByte(s[i])
				}
			} else {
				b.WriteByte(s[i])
			}
			i++
		}
		if i >= len(s) {
			return 0, false
		}
		m[key] = b.String()
		i++
	}
	return 0, false
}

// Reading is what a scrape gives, before rates.
type Reading struct {
	At        time.Time
	CPUIdle   float64 // seconds, all cores
	CPUTotal  float64
	CPUs      int
	MemTotal  float64
	MemAvail  float64
	DiskSize  float64 // the root filesystem
	DiskAvail float64
	RX, TX    float64 // bytes, all interfaces but lo
	Load1     float64
	Boot      float64 // unix time the VM booted
}

// Read sums a scrape into a Reading.
func Read(samples []Sample, at time.Time) Reading {
	r := Reading{At: at}
	cpus := map[string]bool{}
	for _, s := range samples {
		switch s.Name {
		case "node_cpu_seconds_total":
			r.CPUTotal += s.Value
			cpus[s.Labels["cpu"]] = true
			if s.Labels["mode"] == "idle" || s.Labels["mode"] == "iowait" {
				r.CPUIdle += s.Value
			}
		case "node_memory_MemTotal_bytes":
			r.MemTotal = s.Value
		case "node_memory_MemAvailable_bytes":
			r.MemAvail = s.Value
		case "node_filesystem_size_bytes":
			if s.Labels["mountpoint"] == "/" {
				r.DiskSize = s.Value
			}
		case "node_filesystem_avail_bytes":
			if s.Labels["mountpoint"] == "/" {
				r.DiskAvail = s.Value
			}
		case "node_network_receive_bytes_total":
			if s.Labels["device"] != "lo" {
				r.RX += s.Value
			}
		case "node_network_transmit_bytes_total":
			if s.Labels["device"] != "lo" {
				r.TX += s.Value
			}
		case "node_load1":
			r.Load1 = s.Value
		case "node_boot_time_seconds":
			r.Boot = s.Value
		}
	}
	r.CPUs = len(cpus)
	return r
}

// Point is what the graphs show at one moment. CPU is in % of one core
// (like the containers'), -1 until there are two readings; RX and TX in
// bytes per second.
type Point struct {
	T         int64   `json:"t"`
	CPU       float64 `json:"cpu"`
	CPUs      int     `json:"cpus"`
	MemUsed   float64 `json:"memUsed"`
	MemTotal  float64 `json:"memTotal"`
	DiskUsed  float64 `json:"diskUsed"`
	DiskTotal float64 `json:"diskTotal"`
	RX        float64 `json:"rx"`
	TX        float64 `json:"tx"`
	Load1     float64 `json:"load1"`
	Uptime    int64   `json:"uptime"`
}

// PointOf is the point of cur, with the rates since prev (nil: none yet;
// a VM that rebooted in between also starts again).
func PointOf(prev *Reading, cur Reading) Point {
	p := Point{T: cur.At.Unix(), CPU: -1, CPUs: cur.CPUs, MemTotal: cur.MemTotal, MemUsed: cur.MemTotal - cur.MemAvail,
		DiskTotal: cur.DiskSize, DiskUsed: cur.DiskSize - cur.DiskAvail, Load1: cur.Load1}
	if cur.Boot > 0 {
		p.Uptime = cur.At.Unix() - int64(cur.Boot)
	}
	if prev == nil || prev.Boot != cur.Boot || cur.CPUTotal < prev.CPUTotal {
		return p
	}
	dt := cur.At.Sub(prev.At).Seconds()
	if total := cur.CPUTotal - prev.CPUTotal; total > 0 && dt > 0 {
		busy := total - (cur.CPUIdle - prev.CPUIdle)
		// busy seconds per wall second = cores in use; ×100 = % of one core.
		p.CPU = math.Max(0, busy/dt*100)
	}
	if dt > 0 {
		p.RX = math.Max(0, (cur.RX-prev.RX)/dt)
		p.TX = math.Max(0, (cur.TX-prev.TX)/dt)
	}
	return p
}
