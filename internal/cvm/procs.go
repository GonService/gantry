package cvm

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The processes view reads the machine's counters with one ssh call and
// turns them into rates against the previous call. Nothing runs between
// calls: the page asks only while it is open.

// procScript prints the raw counters, in sections.
const procScript = `export LC_ALL=C
echo ==PS; ps -eo pid=,user:20=,etimes=,rss=,args= 2>/dev/null
echo ==STAT; cat /proc/[0-9]*/stat 2>/dev/null
echo ==CG; grep -H . /proc/[0-9]*/cgroup 2>/dev/null
echo ==EXE; ls -l /proc/[0-9]*/exe 2>/dev/null | grep -F /opt/rosetta/
echo ==CPU; grep '^cpu' /proc/stat
echo ==MEM; cat /proc/meminfo
echo ==LOAD; cat /proc/loadavg
echo ==DISK; cat /proc/diskstats
echo ==NET; cat /proc/net/dev
`

// ctrScript names the containers (cached: it starts the engines' CLIs).
const ctrScript = `docker ps --no-trunc --format '{{.ID}} {{.Names}} {{.Label "com.docker.compose.project"}}' 2>/dev/null
sudo podman ps --no-trunc --format '{{.ID}} {{.Names}} {{index .Labels "com.docker.compose.project"}}' 2>/dev/null`

// Proc is one process.
type Proc struct {
	PID     int     `json:"pid"`
	User    string  `json:"user"`
	State   string  `json:"state"`
	CPU     float64 `json:"cpu"` // % of one core
	RSS     int64   `json:"rss"` // bytes
	Elapsed int64   `json:"elapsed"`
	Threads int     `json:"threads"`
	Cmd     string  `json:"cmd"`
	Kind    string  `json:"kind"` // container, docker, podman, sistema
	Origin  string  `json:"origin"`
	Rosetta bool    `json:"rosetta,omitempty"`
}

// Core is the use of one core, in %.
type Core struct {
	User   float64 `json:"user"`
	System float64 `json:"system"`
}

// Snapshot is what the page shows.
type Snapshot struct {
	At         int64   `json:"at"`
	Procs      []Proc  `json:"procs"`
	Cores      []Core  `json:"cores"`
	CPU        float64 `json:"cpu"`
	MemTotal   int64   `json:"memTotal"`
	MemUsed    int64   `json:"memUsed"`
	MemBuffers int64   `json:"memBuffers"`
	MemCached  int64   `json:"memCached"`
	SwapTotal  int64   `json:"swapTotal"`
	SwapUsed   int64   `json:"swapUsed"`
	Load       string  `json:"load"`
	DiskRead   float64 `json:"diskRead"` // bytes/s
	DiskWrite  float64 `json:"diskWrite"`
	NetIn      float64 `json:"netIn"`
	NetOut     float64 `json:"netOut"`
	Tasks      int     `json:"tasks"`
	Threads    int     `json:"threads"`
	Running    int     `json:"running"`
	First      bool    `json:"first,omitempty"` // no rates yet
}

type rawProc struct {
	user    string
	elapsed int64
	rss     int64
	cmd     string
	state   string
	ticks   int64
	threads int
	cgroup  string
	rosetta bool
}

type raw struct {
	at     time.Time
	procs  map[int]*rawProc
	cpu    [][]int64 // total, then each core: the counters of /proc/stat
	mem    map[string]int64
	load   string
	diskR  int64 // sectors
	diskW  int64
	netIn  int64
	netOut int64
}

func sections(out string) map[string][]string {
	m := map[string][]string{}
	cur := ""
	for _, l := range strings.Split(out, "\n") {
		if name, ok := strings.CutPrefix(l, "=="); ok && !strings.Contains(name, " ") {
			cur = name
			continue
		}
		if cur != "" && strings.TrimSpace(l) != "" {
			m[cur] = append(m[cur], l)
		}
	}
	return m
}

func parseRaw(out string, at time.Time) *raw {
	s := sections(out)
	r := &raw{at: at, procs: map[int]*rawProc{}, mem: map[string]int64{}}
	get := func(pid int) *rawProc {
		p := r.procs[pid]
		if p == nil {
			p = &rawProc{}
			r.procs[pid] = p
		}
		return p
	}
	for _, l := range s["PS"] {
		f := strings.Fields(l)
		if len(f) < 5 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		p := get(pid)
		p.user = f[1]
		p.elapsed, _ = strconv.ParseInt(f[2], 10, 64)
		kb, _ := strconv.ParseInt(f[3], 10, 64)
		p.rss = kb * 1024
		p.cmd = strings.Join(f[4:], " ")
	}
	for _, l := range s["STAT"] {
		// pid (comm with spaces) state ppid … utime(14) stime(15) … threads(20)
		i := strings.IndexByte(l, ' ')
		j := strings.LastIndexByte(l, ')')
		if i < 0 || j < 0 {
			continue
		}
		pid, err := strconv.Atoi(l[:i])
		if err != nil {
			continue
		}
		f := strings.Fields(l[j+1:])
		if len(f) < 18 {
			continue
		}
		p := get(pid)
		p.state = f[0]
		u, _ := strconv.ParseInt(f[11], 10, 64)
		st, _ := strconv.ParseInt(f[12], 10, 64)
		p.ticks = u + st
		p.threads, _ = strconv.Atoi(f[17])
	}
	for _, l := range s["CG"] {
		// /proc/123/cgroup:0::/system.slice/docker-<id>.scope
		rest, ok := strings.CutPrefix(l, "/proc/")
		if !ok {
			continue
		}
		k := strings.IndexByte(rest, '/')
		if k < 0 {
			continue
		}
		pid, err := strconv.Atoi(rest[:k])
		if err != nil {
			continue
		}
		if c := strings.LastIndex(rest, ":"); c >= 0 {
			get(pid).cgroup = rest[c+1:]
		}
	}
	for _, l := range s["EXE"] {
		// lrwxrwxrwx 1 root root 0 … /proc/123/exe -> /opt/rosetta/rosetta
		i := strings.Index(l, "/proc/")
		if i < 0 {
			continue
		}
		rest := l[i+6:]
		if k := strings.IndexByte(rest, '/'); k > 0 {
			if pid, err := strconv.Atoi(rest[:k]); err == nil {
				get(pid).rosetta = true
			}
		}
	}
	for _, l := range s["CPU"] {
		f := strings.Fields(l)
		if len(f) < 9 {
			continue
		}
		var v []int64
		for _, x := range f[1:] {
			n, _ := strconv.ParseInt(x, 10, 64)
			v = append(v, n)
		}
		r.cpu = append(r.cpu, v)
	}
	for _, l := range s["MEM"] {
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		f := strings.Fields(v)
		if len(f) > 0 {
			n, _ := strconv.ParseInt(f[0], 10, 64)
			r.mem[k] = n * 1024
		}
	}
	if l := s["LOAD"]; len(l) > 0 {
		f := strings.Fields(l[0])
		if len(f) >= 3 {
			r.load = strings.Join(f[:3], " ")
		}
	}
	for _, l := range s["DISK"] {
		f := strings.Fields(l)
		if len(f) < 10 || !strings.HasPrefix(f[2], "vd") || len(f[2]) != 3 {
			continue // whole virtio disks only, not partitions
		}
		rd, _ := strconv.ParseInt(f[5], 10, 64)
		wr, _ := strconv.ParseInt(f[9], 10, 64)
		r.diskR += rd
		r.diskW += wr
	}
	for _, l := range s["NET"] {
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		iface := strings.TrimSpace(k)
		if iface == "lo" || strings.HasPrefix(iface, "docker") || strings.HasPrefix(iface, "veth") || strings.HasPrefix(iface, "br-") || strings.HasPrefix(iface, "podman") || strings.HasPrefix(iface, "cni") {
			continue
		}
		f := strings.Fields(v)
		if len(f) >= 9 {
			in, _ := strconv.ParseInt(f[0], 10, 64)
			out, _ := strconv.ParseInt(f[8], 10, 64)
			r.netIn += in
			r.netOut += out
		}
	}
	return r
}

// containerOf reads the container id from a cgroup path.
// (…/docker-<id>.scope, …/libpod-<id>.scope/container).
func containerOf(cg string) (kind, id string) {
	for _, seg := range strings.Split(cg, "/") {
		base := strings.TrimSuffix(seg, ".scope")
		switch {
		case strings.HasPrefix(base, "docker-"):
			return "docker", strings.TrimPrefix(base, "docker-")
		case strings.HasPrefix(base, "libpod-conmon-"):
			return "podman-engine", ""
		case strings.HasPrefix(base, "libpod-"):
			return "podman", strings.TrimPrefix(base, "libpod-")
		}
	}
	return "", ""
}

const hz = 100 // USER_HZ on Linux

// build turns two samples into the snapshot.
func build(prev, cur *raw, names map[string]string) Snapshot {
	snap := Snapshot{At: cur.at.Unix(), Load: cur.load, First: prev == nil}
	dt := 0.0
	if prev != nil {
		dt = cur.at.Sub(prev.at).Seconds()
	}
	m := cur.mem
	snap.MemTotal = m["MemTotal"]
	snap.MemBuffers = m["Buffers"]
	snap.MemCached = m["Cached"] + m["SReclaimable"]
	if avail, ok := m["MemAvailable"]; ok {
		snap.MemUsed = snap.MemTotal - avail
	} else {
		snap.MemUsed = snap.MemTotal - m["MemFree"] - snap.MemBuffers - snap.MemCached
	}
	snap.SwapTotal = m["SwapTotal"]
	snap.SwapUsed = m["SwapTotal"] - m["SwapFree"]
	if prev != nil && dt > 0 {
		for i := range cur.cpu {
			if i >= len(prev.cpu) {
				break
			}
			a, b := prev.cpu[i], cur.cpu[i]
			var tot int64
			for k := range b {
				if k < len(a) && k < 8 { // user nice system idle iowait irq softirq steal
					tot += b[k] - a[k]
				}
			}
			if tot <= 0 {
				tot = 1
			}
			user := float64((b[0]-a[0])+(b[1]-a[1])) * 100 / float64(tot)
			sys := float64((b[2]-a[2])+(b[5]-a[5])+(b[6]-a[6])) * 100 / float64(tot)
			if i == 0 {
				snap.CPU = user + sys
			} else {
				snap.Cores = append(snap.Cores, Core{User: round1(user), System: round1(sys)})
			}
		}
		snap.CPU = round1(snap.CPU)
		snap.DiskRead = float64(cur.diskR-prev.diskR) * 512 / dt
		snap.DiskWrite = float64(cur.diskW-prev.diskW) * 512 / dt
		snap.NetIn = float64(cur.netIn-prev.netIn) / dt
		snap.NetOut = float64(cur.netOut-prev.netOut) / dt
	} else {
		for i := 1; i < len(cur.cpu); i++ {
			snap.Cores = append(snap.Cores, Core{})
		}
	}
	for pid, p := range cur.procs {
		if p.cmd == "" && p.state == "" {
			continue
		}
		snap.Tasks++
		snap.Threads += p.threads
		if p.state == "R" {
			snap.Running++
		}
		if p.cmd == "" || strings.HasPrefix(p.cmd, "[") {
			continue // kernel threads stay out of the list
		}
		pr := Proc{PID: pid, User: p.user, State: p.state, RSS: p.rss, Elapsed: p.elapsed, Threads: p.threads, Cmd: p.cmd, Rosetta: p.rosetta}
		if prev != nil && dt > 0 {
			if q := prev.procs[pid]; q != nil && p.ticks >= q.ticks {
				pr.CPU = round1(float64(p.ticks-q.ticks) * 100 / hz / dt)
			}
		}
		pr.Kind, pr.Origin = origin(p, names)
		snap.Procs = append(snap.Procs, pr)
	}
	sort.Slice(snap.Procs, func(i, j int) bool {
		if snap.Procs[i].CPU != snap.Procs[j].CPU {
			return snap.Procs[i].CPU > snap.Procs[j].CPU
		}
		return snap.Procs[i].RSS > snap.Procs[j].RSS
	})
	return snap
}

func origin(p *rawProc, names map[string]string) (string, string) {
	cg := p.cgroup
	if k, id := containerOf(cg); id != "" {
		if n, ok := names[id]; ok {
			return "container", n
		}
		return "container", k + " " + id[:min(12, len(id))]
	} else if k == "podman-engine" {
		return "podman", "Podman"
	}
	switch {
	case strings.Contains(cg, "docker.service"), strings.Contains(cg, "containerd.service"):
		return "docker", "Docker"
	case strings.Contains(cg, "podman"):
		return "podman", "Podman"
	}
	return "sistema", "sistema"
}

func round1(v float64) float64 { return float64(int64(v*10+0.5)) / 10 }

// Sampler keeps the previous sample of each machine.
type Sampler struct {
	mu      sync.Mutex
	prev    map[string]*raw
	names   map[string]map[string]string
	namesAt map[string]time.Time
}

// Sample reads the machine now and returns the rates since the last call
// (the first call has no rates; a call long after the last starts over).
func (s *Sampler) Sample(ctx context.Context, name string) (Snapshot, error) {
	out, err := Run(ctx, name, procScript)
	if err != nil {
		return Snapshot{}, err
	}
	cur := parseRaw(out, time.Now())
	s.mu.Lock()
	if s.prev == nil {
		s.prev, s.names, s.namesAt = map[string]*raw{}, map[string]map[string]string{}, map[string]time.Time{}
	}
	prev := s.prev[name]
	if prev != nil && cur.at.Sub(prev.at) > time.Minute {
		prev = nil
	}
	stale := time.Since(s.namesAt[name]) > 15*time.Second
	s.mu.Unlock()
	if stale {
		if o, err := Run(ctx, name, ctrScript); err == nil || o != "" {
			n := parseNames(o)
			s.mu.Lock()
			s.names[name], s.namesAt[name] = n, time.Now()
			s.mu.Unlock()
		}
	}
	s.mu.Lock()
	names := s.names[name]
	s.prev[name] = cur
	s.mu.Unlock()
	return build(prev, cur, names), nil
}

// Forget drops what the sampler keeps of a machine (the page closed).
func (s *Sampler) Forget(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.prev, name)
}

func parseNames(out string) map[string]string {
	n := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) < 2 || len(f[0]) < 12 {
			continue
		}
		label := f[1]
		if len(f) > 2 && f[2] != "" && f[2] != "<no" {
			label += " · " + f[2]
		}
		n[f[0]] = label
	}
	return n
}
