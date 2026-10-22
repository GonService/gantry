package kube

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"

	"gantry/internal/applog"
)

// Detail is what the details panel of an object shows: the object itself
// and what is tied to it.
type Detail struct {
	Object map[string]any `json:"object"`
	// SecretKeys are the keys of a Secret and the size of each value; the
	// values themselves never come in a detail.
	SecretKeys map[string]int `json:"secretKeys,omitempty"`
	Events     []Row          `json:"events"`
	// Pods of a workload, of a Service (its selector) or of a node.
	Pods []Row `json:"pods,omitempty"`
	// Jobs of a CronJob.
	Jobs []Row `json:"jobs,omitempty"`
	// Usage of the pods above (and of the pod itself), keyed ns/name.
	Usage map[string]Usage `json:"usage,omitempty"`
	// Containers is the usage of each container of a pod.
	Containers map[string]Usage `json:"containers,omitempty"`
	// Node is the usage of a node.
	Node *Usage `json:"node,omitempty"`
	// Revisions of a Deployment, newest first.
	Revisions []Revision `json:"revisions,omitempty"`
	// Can is what the account may do with the object.
	Can Perms `json:"can"`
	// Diagnosis: why the pod (or the newest pod of a workload with a
	// problem) does not come up.
	Diagnosis []Finding `json:"diagnosis,omitempty"`
}

// Detail reads an object and what is tied to it. Lookups of related
// objects that fail (no permission, no metrics-server) just come empty.
func (k *Client) Detail(ctx context.Context, kind Kind, ns, name string) (*Detail, error) {
	// From a list being followed when there is one; else from the API.
	o := k.cachedObject(kind, ns, name)
	if o == nil {
		if err := k.Get(ctx, kind.Path(ns, name), nil, &o); err != nil {
			return nil, err
		}
	}
	if md, ok := o["metadata"].(map[string]any); ok {
		delete(md, "managedFields")
		if an, ok := md["annotations"].(map[string]any); ok {
			// The previous version of the object, as JSON: noise.
			delete(an, "kubectl.kubernetes.io/last-applied-configuration")
		}
	}
	d := &Detail{Object: o, Events: []Row{}}
	if kind.ID == "secrets" {
		d.SecretKeys = map[string]int{}
		for key, v := range o.mapOf("data") {
			s, _ := v.(string)
			n := len(s) * 3 / 4
			if b, err := base64.StdEncoding.DecodeString(s); err == nil {
				n = len(b)
			}
			d.SecretKeys[key] = n
		}
		delete(o, "data")
		delete(o, "stringData")
	}
	objKind := o.str("kind")
	var wg sync.WaitGroup
	var mu sync.Mutex
	run := func(fn func()) {
		wg.Add(1)
		go func() { defer applog.Recover("kube/detail.go:72"); defer wg.Done(); fn() }()
	}
	// Events and pods come from lists followed by watch (filtered here), so
	// a panel open for a long time does not keep asking the API server.
	run(func() {
		ek, _ := KindByID("events")
		evNS := ns
		if !kind.Namespaced {
			evNS = ""
		}
		evs, err := k.ListCached(ctx, ek, evNS)
		if err != nil {
			return
		}
		mine := []Row{}
		for _, e := range evs {
			if len(e.Cells) > 2 && e.Cells[2] == objKind+"/"+name && (!kind.Namespaced || e.Namespace == ns) {
				mine = append(mine, e)
			}
		}
		mu.Lock()
		d.Events = mine
		mu.Unlock()
	})
	pk, _ := KindByID("pods")
	podsWhere := func(pns string, keep func(Row) bool) {
		run(func() {
			rows, err := k.ListCached(ctx, pk, pns)
			if err != nil {
				return
			}
			var out []Row
			for _, r := range rows {
				if keep(r) {
					out = append(out, r)
				}
			}
			mu.Lock()
			d.Pods = out
			mu.Unlock()
		})
	}
	podsBy := func(pns string, sel map[string]any) {
		if len(sel) > 0 {
			podsWhere(pns, func(r Row) bool { return r.matches(sel) })
		}
	}
	switch kind.ID {
	case "deployments", "statefulsets", "daemonsets", "replicasets", "jobs":
		if kind.ID == "deployments" {
			run(func() {
				if revs, err := k.Revisions(ctx, ns, name); err == nil {
					mu.Lock()
					d.Revisions = revs
					mu.Unlock()
				}
			})
		}
		podsBy(ns, o.mapOf("spec", "selector", "matchLabels"))
	case "services":
		podsBy(ns, o.mapOf("spec", "selector"))
	case "nodes":
		podsWhere("", func(r Row) bool { return len(r.Cells) > 3 && r.Cells[3] == name })
		run(func() {
			if u, ok := k.NodeUsage(ctx)[name]; ok {
				mu.Lock()
				d.Node = &u
				mu.Unlock()
			}
		})
	case "cronjobs":
		run(func() {
			jk, _ := KindByID("jobs")
			var l struct {
				Items []obj `json:"items"`
			}
			if err := k.Get(ctx, jk.Path(ns, ""), nil, &l); err != nil {
				return
			}
			uid := o.str("metadata", "uid")
			var rows []Row
			for _, j := range l.Items {
				for _, ref := range j.list("metadata", "ownerReferences") {
					if ref.str("uid") == uid {
						rows = append(rows, jk.summary(j))
					}
				}
			}
			sort.Slice(rows, func(i, j int) bool { return rows[i].Created > rows[j].Created })
			mu.Lock()
			d.Jobs = rows
			mu.Unlock()
		})
	case "pods":
		run(func() {
			if c := k.containerUsage(ctx, ns, name); len(c) > 0 {
				mu.Lock()
				d.Containers = c
				mu.Unlock()
			}
		})
	}
	if kind.ID != "nodes" && (kind.ID == "pods" || kind.Namespaced) {
		run(func() {
			u := k.PodUsage(ctx, ns)
			mu.Lock()
			d.Usage = u
			mu.Unlock()
		})
	}
	run(func() {
		p := k.PermsFor(ctx, kind, ns)
		mu.Lock()
		d.Can = p
		mu.Unlock()
	})
	wg.Wait()
	switch kind.ID {
	case "pods":
		if _, bad := PodStatus(o); bad || o.str("status", "phase") == "Pending" || hasRestarts(o) {
			d.Diagnosis = k.Diagnose(ctx, ns, o)
		}
	case "deployments", "statefulsets", "daemonsets", "replicasets", "jobs":
		d.Diagnosis = k.DiagnoseWorkload(ctx, ns, d.Pods)
	}
	if d.Usage != nil {
		// Only what this panel shows.
		keep := map[string]Usage{}
		for _, p := range d.Pods {
			if u, ok := d.Usage[p.Namespace+"/"+p.Name]; ok {
				keep[p.Namespace+"/"+p.Name] = u
			}
		}
		if kind.ID == "pods" {
			if u, ok := d.Usage[ns+"/"+name]; ok {
				keep[ns+"/"+name] = u
			}
		}
		d.Usage = keep
	}
	return d, nil
}

// containerUsage reads metrics.k8s.io for each container of one pod.
func (k *Client) containerUsage(ctx context.Context, ns, pod string) map[string]Usage {
	var m struct {
		Containers []struct {
			Name  string            `json:"name"`
			Usage map[string]string `json:"usage"`
		} `json:"containers"`
	}
	if err := k.Get(ctx, "/apis/metrics.k8s.io/v1beta1/namespaces/"+url.PathEscape(ns)+"/pods/"+url.PathEscape(pod), nil, &m); err != nil {
		return nil
	}
	out := map[string]Usage{}
	for _, c := range m.Containers {
		out[c.Name] = Usage{CPU: ParseCPU(c.Usage["cpu"]), Mem: ParseMem(c.Usage["memory"])}
	}
	return out
}

// selector turns matchLabels into a labelSelector (k=v,k2=v2).
func selector(m map[string]any) string {
	var parts []string
	for k, v := range m {
		s, _ := v.(string)
		parts = append(parts, k+"="+s)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// LabelSelector turns a LabelSelector object (matchLabels and
// matchExpressions) into the text the API takes.
func LabelSelector(sel map[string]any) string {
	if sel == nil {
		return ""
	}
	var parts []string
	if ml, ok := sel["matchLabels"].(map[string]any); ok {
		if s := selector(ml); s != "" {
			parts = append(parts, s)
		}
	}
	if me, ok := sel["matchExpressions"].([]any); ok {
		for _, x := range me {
			e, _ := x.(map[string]any)
			key, _ := e["key"].(string)
			op, _ := e["operator"].(string)
			var vals []string
			if vs, ok := e["values"].([]any); ok {
				for _, v := range vs {
					vals = append(vals, fmt.Sprint(v))
				}
			}
			switch op {
			case "In":
				parts = append(parts, key+" in ("+strings.Join(vals, ",")+")")
			case "NotIn":
				parts = append(parts, key+" notin ("+strings.Join(vals, ",")+")")
			case "Exists":
				parts = append(parts, key)
			case "DoesNotExist":
				parts = append(parts, "!"+key)
			}
		}
	}
	return strings.Join(parts, ",")
}

// PodRef is a pod and its containers, for logs of many pods.
type PodRef struct {
	Name       string   `json:"name"`
	Containers []string `json:"containers"`
	Phase      string   `json:"phase"`
	Created    int64    `json:"created"`
}

// SelectorOf is the pod selector of a workload (deploy, statefulset,
// daemonset, replicaset, job) or a service.
func (k *Client) SelectorOf(ctx context.Context, kind Kind, ns, name string) (string, error) {
	var o obj
	if err := k.Get(ctx, kind.Path(ns, name), nil, &o); err != nil {
		return "", err
	}
	if kind.ID == "services" {
		return selector(o.mapOf("spec", "selector")), nil
	}
	s := LabelSelector(o.mapOf("spec", "selector"))
	if s == "" {
		return "", fmt.Errorf("%s não tem seletor de pods", name)
	}
	return s, nil
}

// PodsBySelector lists the pods that match a label selector.
func (k *Client) PodsBySelector(ctx context.Context, ns, sel string) ([]PodRef, error) {
	pk, _ := KindByID("pods")
	var l struct {
		Items []obj `json:"items"`
	}
	if err := k.Get(ctx, pk.Path(ns, ""), url.Values{"labelSelector": {sel}}, &l); err != nil {
		return nil, err
	}
	out := make([]PodRef, 0, len(l.Items))
	for _, p := range l.Items {
		if p.get("metadata", "deletionTimestamp") != nil {
			continue
		}
		r := PodRef{Name: p.str("metadata", "name"), Phase: p.str("status", "phase"), Created: ts(p.str("metadata", "creationTimestamp"))}
		for _, c := range p.list("spec", "containers") {
			r.Containers = append(r.Containers, c.str("name"))
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func hasRestarts(o obj) bool {
	for _, c := range o.list("status", "containerStatuses") {
		if c.num("restartCount") > 0 {
			return true
		}
	}
	return false
}
