// Package metrics samples CPU and memory of every running container (and of
// the VMs, when given a source for them) and keeps a short history. The app uses it for hosts without the agent (history only
// while the app is open) and the agent uses it on the server, where it keeps
// collecting with the app closed.
package metrics

import (
	"context"
	"encoding/gob"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"gantry/internal/applog"
	"gantry/internal/engine"
)

// LooseLab is the lab id of containers outside any compose project.
const LooseLab = "_avulsos"

type Point struct {
	T    int64   `json:"t"`           // unix seconds
	CPU  float32 `json:"c"`           // percent of one CPU
	Mem  uint64  `json:"m"`           // bytes
	Disk uint64  `json:"d,omitempty"` // bytes on disk (VMs)
}

type Series struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Lab     string  `json:"lab"`
	Service string  `json:"service,omitempty"`
	Kind    string  `json:"kind,omitempty"` // "vm" for VMs, empty for containers
	Points  []Point `json:"points"`
}

type Result struct {
	Since    int64    `json:"since"`    // first sample kept, or when collecting started
	Interval int      `json:"interval"` // seconds between samples
	Step     int      `json:"step"`     // seconds per returned point
	Series   []Series `json:"series"`
}

// VMSample is what a VM source reports for one running VM.
type VMSample struct {
	Lab    string
	Name   string
	CPUSec float64 // CPU seconds used so far; the collector turns it into a rate
	Mem    uint64
	Disk   uint64
}

type Collector struct {
	Interval time.Duration
	Keep     time.Duration
	// VMs, when set, is sampled together with the containers.
	VMs func(ctx context.Context) ([]VMSample, error)

	eng     *engine.Client // nil: VMs only
	prev    map[string]cpuMark
	mu      sync.Mutex
	series  map[string]*Series
	started int64
	used    time.Time
}

type cpuMark struct {
	at  time.Time
	sec float64
}

func New(eng *engine.Client, interval, keep time.Duration) *Collector {
	return &Collector{eng: eng, Interval: interval, Keep: keep, series: map[string]*Series{}, prev: map[string]cpuMark{}, started: time.Now().Unix(), used: time.Now()}
}

// NewVMs collects only VMs.
func NewVMs(src func(ctx context.Context) ([]VMSample, error), interval, keep time.Duration) *Collector {
	c := New(nil, interval, keep)
	c.VMs = src
	return c
}

// VMID is the series id of a VM.
func VMID(lab, name string) string { return "vm:" + lab + "/" + name }

// Run samples until ctx ends.
func (c *Collector) Run(ctx context.Context) {
	t := time.NewTicker(c.Interval)
	defer t.Stop()
	if c.VMs != nil {
		// A VM's CPU is a rate: take a second sample soon so the first
		// numbers show up in seconds, not after a whole interval.
		c.Sample(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
	for {
		c.Sample(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Since is the oldest sample kept, or when collecting started.
func (c *Collector) Since() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.started
}

// Idle reports how long nobody asked for data.
func (c *Collector) Idle() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Since(c.used)
}

func (c *Collector) Sample(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, c.Interval)
	defer cancel()
	now := time.Now().Unix()
	if c.VMs != nil {
		c.sampleVMs(ctx, now)
	}
	if c.eng != nil {
		c.sampleContainers(ctx, now)
	}
	c.prune(now)
}

func (c *Collector) sampleVMs(ctx context.Context, now int64) {
	list, err := c.VMs(ctx)
	if err != nil {
		return
	}
	at := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	seen := map[string]bool{}
	for _, v := range list {
		id := VMID(v.Lab, v.Name)
		seen[id] = true
		last, ok := c.prev[id]
		c.prev[id] = cpuMark{at: at, sec: v.CPUSec}
		// The rate needs two samples; a restarted VM starts over.
		if !ok || v.CPUSec < last.sec {
			continue
		}
		wall := at.Sub(last.at).Seconds()
		if wall <= 0 {
			continue
		}
		s := c.series[id]
		if s == nil {
			s = &Series{ID: id, Kind: "vm"}
			c.series[id] = s
		}
		s.Name, s.Lab, s.Service = v.Name, v.Lab, v.Name
		s.Points = append(s.Points, Point{T: now, CPU: float32((v.CPUSec - last.sec) / wall * 100), Mem: v.Mem, Disk: v.Disk})
	}
	for id := range c.prev {
		if !seen[id] {
			delete(c.prev, id)
		}
	}
}

func (c *Collector) sampleContainers(ctx context.Context, now int64) {
	cs, err := c.eng.Containers(ctx)
	if err != nil {
		return
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, ct := range cs {
		if ct.State != "running" {
			continue
		}
		wg.Add(1)
		go func() {
			defer applog.Recover("metrics/metrics.go:187")
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			st, err := c.eng.Stats(ctx, ct.ID)
			if err != nil {
				return
			}
			lab := ct.Labels["com.docker.compose.project"]
			if lab == "" {
				lab = LooseLab
			}
			c.mu.Lock()
			s := c.series[ct.ID]
			if s == nil {
				s = &Series{ID: ct.ID}
				c.series[ct.ID] = s
			}
			s.Name, s.Lab, s.Service = ct.Name(), lab, ct.Labels["com.docker.compose.service"]
			s.Points = append(s.Points, Point{T: now, CPU: float32(st.CPU), Mem: st.Mem})
			c.mu.Unlock()
		}()
	}
	wg.Wait()
}

func (c *Collector) prune(now int64) {
	cut := now - int64(c.Keep/time.Second)
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, s := range c.series {
		i := sort.Search(len(s.Points), func(i int) bool { return s.Points[i].T >= cut })
		if i == len(s.Points) {
			delete(c.series, id)
			continue
		}
		if i > 0 {
			s.Points = append([]Point(nil), s.Points[i:]...)
		}
	}
	if c.started < cut {
		c.started = cut
	}
}

// Query returns the series since a unix time, optionally for one lab, with
// at most about maxPoints points per series (averaged buckets).
func (c *Collector) Query(since int64, lab string, maxPoints int) Result {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.used = time.Now()
	iv := int(c.Interval / time.Second)
	if iv < 1 {
		iv = 1
	}
	now := time.Now().Unix()
	if since <= 0 || since > now {
		since = now - 3600
	}
	step := iv
	if maxPoints > 0 {
		if s := int(now-since) / maxPoints; s > step {
			step = s
		}
	}
	res := Result{Since: c.started, Interval: iv, Step: step, Series: []Series{}}
	for _, s := range c.series {
		if lab != "" && s.Lab != lab {
			continue
		}
		i := sort.Search(len(s.Points), func(i int) bool { return s.Points[i].T >= since })
		pts := s.Points[i:]
		if len(pts) == 0 {
			continue
		}
		out := Series{ID: s.ID, Name: s.Name, Lab: s.Lab, Service: s.Service, Kind: s.Kind, Points: bucket(pts, int64(step))}
		res.Series = append(res.Series, out)
	}
	sort.Slice(res.Series, func(i, j int) bool { return res.Series[i].Name < res.Series[j].Name })
	return res
}

// Latest returns the newest point of each series sampled in the last two
// intervals, by series id.
func (c *Collector) Latest() map[string]Point {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.used = time.Now()
	cut := time.Now().Unix() - int64(2*c.Interval/time.Second) - 1
	out := map[string]Point{}
	for id, s := range c.series {
		if n := len(s.Points); n > 0 && s.Points[n-1].T >= cut {
			out[id] = s.Points[n-1]
		}
	}
	return out
}

func bucket(pts []Point, step int64) []Point {
	if step <= 1 {
		return append([]Point(nil), pts...)
	}
	var out []Point
	var cur int64 = -1
	var n int
	var cpu float64
	var mem, disk uint64
	flush := func() {
		if n > 0 {
			out = append(out, Point{T: cur, CPU: float32(cpu / float64(n)), Mem: mem / uint64(n), Disk: disk / uint64(n)})
		}
	}
	for _, p := range pts {
		b := p.T - p.T%step
		if b != cur {
			flush()
			cur, n, cpu, mem, disk = b, 0, 0, 0, 0
		}
		n++
		cpu += float64(p.CPU)
		mem += p.Mem
		disk += p.Disk
	}
	flush()
	return out
}

type saved struct {
	Started int64
	Series  map[string]*Series
}

// Save writes the history to path, for the agent to survive restarts.
func (c *Collector) Save(path string) error {
	c.mu.Lock()
	data := saved{Started: c.started, Series: c.series}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		c.mu.Unlock()
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		c.mu.Unlock()
		return err
	}
	err = gob.NewEncoder(f).Encode(data)
	c.mu.Unlock()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// Load reads history saved by Save. A missing file is not an error.
func (c *Collector) Load(path string) error {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	var data saved
	if err := gob.NewDecoder(f).Decode(&data); err != nil {
		return err
	}
	c.mu.Lock()
	if data.Series != nil {
		c.series = data.Series
	}
	if data.Started > 0 && data.Started < c.started {
		c.started = data.Started
	}
	c.mu.Unlock()
	c.prune(time.Now().Unix())
	return nil
}
