// Package vmscript holds vmctl.sh, the script that runs the QEMU VMs of the
// labs, and reads its output. It has no dependencies so the agent can use it
// on the server without the rest of the app.
package vmscript

import (
	_ "embed"
	"strconv"
	"strings"
)

//go:embed vmctl.sh
var Script string

// Usage is the use of one VM's resources, from vmctl stats.
type Usage struct {
	Project string
	Name    string
	CPUSec  float64 // CPU seconds used by QEMU since it started
	RSS     uint64  // bytes of memory the QEMU process holds
	Disk    uint64  // bytes the disk image takes on the machine
	Mem     uint64  // memory given to the VM, in bytes
	CPUs    int
}

// ParseUsages reads the lines of vmctl stats.
func ParseUsages(out string) []Usage {
	var res []Usage
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) < 7 {
			continue
		}
		u := Usage{Project: f[0], Name: f[1]}
		cpu, ok := ParseCPUTime(f[2])
		if !ok {
			continue
		}
		u.CPUSec = cpu
		rss, _ := strconv.ParseUint(f[3], 10, 64)
		disk, _ := strconv.ParseUint(f[4], 10, 64)
		mem, _ := strconv.ParseUint(f[5], 10, 64)
		u.RSS, u.Disk, u.Mem = rss*1024, disk*1024, mem*1024*1024
		u.CPUs, _ = strconv.Atoi(f[6])
		res = append(res, u)
	}
	return res
}

// ParseCPUTime reads the CPU time as ps or /proc gives it: "12.34",
// "0:01.23" (macOS), "00:01:02" or "1-02:03:04" (Linux).
func ParseCPUTime(s string) (float64, bool) {
	days := 0.0
	if i := strings.IndexByte(s, '-'); i > 0 {
		d, err := strconv.Atoi(s[:i])
		if err != nil {
			return 0, false
		}
		days, s = float64(d), s[i+1:]
	}
	total := 0.0
	for _, p := range strings.Split(s, ":") {
		v, err := strconv.ParseFloat(p, 64)
		if err != nil || v < 0 {
			return 0, false
		}
		total = total*60 + v
	}
	return days*86400 + total, true
}
