package server

// History of CPU and memory of pods and nodes (from metrics-server), kept
// in memory for an hour while someone looks at the cluster: Kubernetes
// itself keeps only the value of now.

import (
	"context"
	"sync"
	"time"

	"gantry/internal/applog"
	"gantry/internal/kube"
)

const (
	kubeSampleEvery = 15 * time.Second
	kubeSampleKeep  = 240 // one hour
	kubeSampleIdle  = 5 * time.Minute
)

type kubePoint struct {
	T int64 `json:"t"`
	C int64 `json:"c"` // millicores
	M int64 `json:"m"` // bytes
}

type kubeSampler struct {
	mu    sync.Mutex
	k     *kube.Client
	used  time.Time
	pods  map[string][]kubePoint // ns/name
	nodes map[string][]kubePoint
}

var (
	samplersMu sync.Mutex
	samplers   = map[string]*kubeSampler{}
)

// touchSampler starts (or keeps alive) the history of a cluster.
func touchSampler(id string, k *kube.Client) {
	samplersMu.Lock()
	defer samplersMu.Unlock()
	sm, ok := samplers[id]
	if ok {
		sm.mu.Lock()
		sm.k, sm.used = k, time.Now()
		sm.mu.Unlock()
		return
	}
	sm = &kubeSampler{k: k, used: time.Now(), pods: map[string][]kubePoint{}, nodes: map[string][]kubePoint{}}
	samplers[id] = sm
	go func() { defer applog.Recover("server/kubemetrics.go:53"); sm.run(id) }()
}

func dropSampler(id string) {
	samplersMu.Lock()
	delete(samplers, id)
	samplersMu.Unlock()
}

func (sm *kubeSampler) run(id string) {
	defer func() {
		samplersMu.Lock()
		if samplers[id] == sm {
			delete(samplers, id)
		}
		samplersMu.Unlock()
	}()
	for {
		sm.mu.Lock()
		k, idle := sm.k, time.Since(sm.used) > kubeSampleIdle
		sm.mu.Unlock()
		samplersMu.Lock()
		gone := samplers[id] != sm
		samplersMu.Unlock()
		if idle || gone {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		pods, nodes := k.PodUsage(ctx, ""), k.NodeUsage(ctx)
		cancel()
		now := time.Now().Unix()
		sm.mu.Lock()
		add := func(m map[string][]kubePoint, key string, u kube.Usage) {
			pts := append(m[key], kubePoint{now, u.CPU, u.Mem})
			if len(pts) > kubeSampleKeep {
				pts = pts[len(pts)-kubeSampleKeep:]
			}
			m[key] = pts
		}
		for key, u := range pods {
			add(sm.pods, key, u)
		}
		for key, u := range nodes {
			add(sm.nodes, key, u)
		}
		// Pods that are gone leave once their hour passed.
		for _, m := range []map[string][]kubePoint{sm.pods, sm.nodes} {
			for key, pts := range m {
				if len(pts) == 0 || now-pts[len(pts)-1].T > int64(kubeSampleKeep)*int64(kubeSampleEvery/time.Second) {
					delete(m, key)
				}
			}
		}
		sm.mu.Unlock()
		wait := kubeSampleEvery
		if len(pods) == 0 && len(nodes) == 0 {
			wait = time.Minute // no metrics-server (yet)
		}
		time.Sleep(wait)
	}
}

// kubeHistory is the history of a pod (ns/name), of a node, or the sum of
// several pods (a workload), point by point.
func kubeHistory(id, kind, ns, name string, pods []kube.Row) []kubePoint {
	samplersMu.Lock()
	sm := samplers[id]
	samplersMu.Unlock()
	if sm == nil {
		return nil
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	copyPts := func(p []kubePoint) []kubePoint { return append([]kubePoint(nil), p...) }
	switch kind {
	case "pods":
		return copyPts(sm.pods[ns+"/"+name])
	case "nodes":
		return copyPts(sm.nodes[name])
	}
	if len(pods) == 0 {
		return nil
	}
	// All pods are sampled at the same instants: sum by time.
	sum := map[int64]kubePoint{}
	for _, p := range pods {
		for _, pt := range sm.pods[p.Namespace+"/"+p.Name] {
			s := sum[pt.T]
			s.T, s.C, s.M = pt.T, s.C+pt.C, s.M+pt.M
			sum[pt.T] = s
		}
	}
	out := make([]kubePoint, 0, len(sum))
	for _, p := range sum {
		out = append(out, p)
	}
	sortPoints(out)
	return out
}

func sortPoints(p []kubePoint) {
	for i := 1; i < len(p); i++ {
		for j := i; j > 0 && p[j].T < p[j-1].T; j-- {
			p[j], p[j-1] = p[j-1], p[j]
		}
	}
}
