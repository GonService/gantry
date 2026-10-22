package metrics

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestBucketAverages(t *testing.T) {
	pts := []Point{{T: 100, CPU: 10, Mem: 100}, {T: 110, CPU: 30, Mem: 300}, {T: 125, CPU: 50, Mem: 500}}
	got := bucket(pts, 20)
	if len(got) != 2 {
		t.Fatalf("want 2 buckets, got %v", got)
	}
	if got[0].T != 100 || got[0].CPU != 20 || got[0].Mem != 200 {
		t.Errorf("first bucket %+v", got[0])
	}
	if got[1].T != 120 || got[1].CPU != 50 {
		t.Errorf("second bucket %+v", got[1])
	}
}

func TestQueryPruneSaveLoad(t *testing.T) {
	c := New(nil, 10*time.Second, time.Hour)
	now := time.Now().Unix()
	c.series["a"] = &Series{ID: "a", Name: "web-1", Lab: "web", Points: []Point{{T: now - 7200, CPU: 1}, {T: now - 60, CPU: 2}, {T: now - 30, CPU: 4}}}
	c.series["b"] = &Series{ID: "b", Name: "x", Lab: LooseLab, Points: []Point{{T: now - 5000}}}
	c.prune(now)
	if _, ok := c.series["b"]; ok {
		t.Fatal("series older than Keep should be dropped")
	}
	r := c.Query(now-3600, "web", 0)
	if len(r.Series) != 1 || len(r.Series[0].Points) != 2 {
		t.Fatalf("query: %+v", r)
	}
	if r := c.Query(now-3600, "other", 0); len(r.Series) != 0 {
		t.Fatal("lab filter")
	}
	p := filepath.Join(t.TempDir(), "m.gob")
	if err := c.Save(p); err != nil {
		t.Fatal(err)
	}
	d := New(nil, 10*time.Second, time.Hour)
	if err := d.Load(p); err != nil {
		t.Fatal(err)
	}
	if r := d.Query(now-3600, "", 0); len(r.Series) != 1 || r.Series[0].Name != "web-1" {
		t.Fatalf("after load: %+v", r)
	}
}

func TestVMSeries(t *testing.T) {
	cpu := 10.0
	c := NewVMs(func(context.Context) ([]VMSample, error) {
		return []VMSample{{Lab: "lab", Name: "web", CPUSec: cpu, Mem: 300 << 20, Disk: 2 << 30}}, nil
	}, 10*time.Second, time.Hour)
	ctx := context.Background()
	c.Sample(ctx)
	if r := c.Query(0, "", 0); len(r.Series) != 0 {
		t.Fatalf("a primeira amostra só marca o CPU: %+v", r.Series)
	}
	c.prev[VMID("lab", "web")] = cpuMark{at: time.Now().Add(-2 * time.Second), sec: 9}
	c.Sample(ctx) // 1 s of CPU in 2 s: 50%
	r := c.Query(0, "lab", 0)
	if len(r.Series) != 1 || r.Series[0].Kind != "vm" || r.Series[0].Name != "web" {
		t.Fatalf("%+v", r.Series)
	}
	p := r.Series[0].Points[0]
	if p.CPU < 45 || p.CPU > 55 || p.Mem != 300<<20 || p.Disk != 2<<30 {
		t.Errorf("%+v", p)
	}
	if l := c.Latest(); l[VMID("lab", "web")].Mem != 300<<20 {
		t.Errorf("latest %+v", l)
	}
	if r := c.Query(0, "outro", 0); len(r.Series) != 0 {
		t.Errorf("filtro por projeto: %+v", r.Series)
	}
	// A restarted VM (CPU time went back) skips one sample instead of a negative rate.
	cpu = 1
	c.Sample(ctx)
	if n := len(c.Query(0, "", 0).Series[0].Points); n != 1 {
		t.Errorf("pontos = %d", n)
	}
}
