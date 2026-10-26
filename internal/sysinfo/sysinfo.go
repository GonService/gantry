// Package sysinfo reads a machine's hardware and usage with a small shell
// script, so it works the same locally and over ssh, without an agent.
package sysinfo

import (
	_ "embed"
	"strconv"
	"strings"
)

//go:embed sysinfo.sh
var Script string

// Disk is one filesystem.
type Disk struct {
	Path  string `json:"path"`
	Total int64  `json:"total"`
	Used  int64  `json:"used"`
}

// Info is what the host page shows about the machine.
type Info struct {
	Hostname  string  `json:"hostname,omitempty"`
	OS        string  `json:"os,omitempty"`
	Kernel    string  `json:"kernel,omitempty"`
	Arch      string  `json:"arch,omitempty"`
	CPUModel  string  `json:"cpuModel,omitempty"`
	CPUs      int     `json:"cpus,omitempty"`
	CPUPct    float64 `json:"cpuPct"`
	HasCPU    bool    `json:"hasCpu"` // CPUPct was measured
	MemTotal  int64   `json:"memTotal,omitempty"`
	MemUsed   int64   `json:"memUsed,omitempty"`
	SwapTotal int64   `json:"swapTotal,omitempty"`
	SwapUsed  int64   `json:"swapUsed,omitempty"`
	Disks     []Disk  `json:"disks,omitempty"`
	Uptime    int64   `json:"uptime,omitempty"` // seconds
	Load      string  `json:"load,omitempty"`
	IP        string  `json:"ip,omitempty"` // address on the local network
	QEMU      bool    `json:"qemu,omitempty"`
	KVM       bool    `json:"kvm,omitempty"`
}

// Parse reads the script's output.
func Parse(out string) Info {
	kv := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), "="); ok {
			kv[k] = strings.TrimSpace(v)
		}
	}
	n := func(k string) int64 { v, _ := strconv.ParseInt(kv[k], 10, 64); return v }
	i := Info{Hostname: kv["hostname"], OS: kv["os"], Kernel: kv["kernel"], Arch: kv["arch"], CPUModel: kv["cpu_model"],
		CPUs: int(n("cpus")), MemTotal: n("mem_total"), MemUsed: n("mem_used"), SwapTotal: n("swap_total"), SwapUsed: n("swap_used"),
		Uptime: n("uptime"), Load: kv["load"], IP: kv["ip"], QEMU: kv["qemu"] == "1", KVM: kv["kvm"] == "1"}
	if v, err := strconv.ParseFloat(kv["cpu_pct"], 64); err == nil {
		i.CPUPct, i.HasCPU = v, true
	}
	for _, l := range strings.Split(out, "\n") {
		// fs=<total> <used> <mount point>
		rest, ok := strings.CutPrefix(strings.TrimSpace(l), "fs=")
		if !ok {
			continue
		}
		f := strings.SplitN(rest, " ", 3)
		if len(f) != 3 {
			continue
		}
		total, _ := strconv.ParseInt(f[0], 10, 64)
		used, _ := strconv.ParseInt(f[1], 10, 64)
		if total > 0 && f[2] != "" {
			i.Disks = append(i.Disks, Disk{Path: f[2], Total: total, Used: used})
		}
	}
	return i
}
