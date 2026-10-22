package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"sync"

	"gantry/internal/applog"
)

// Revision is one version of a Deployment (one of its ReplicaSets).
type Revision struct {
	Number      int      `json:"number"`
	ReplicaSet  string   `json:"replicaSet"`
	Images      []string `json:"images"`
	Created     int64    `json:"created"`
	Replicas    int      `json:"replicas"`
	Ready       int      `json:"ready"`
	Current     bool     `json:"current"`
	ChangeCause string   `json:"changeCause,omitempty"`
	template    any
}

// Revisions lists the versions of a Deployment, newest first.
func (k *Client) Revisions(ctx context.Context, ns, deploy string) ([]Revision, error) {
	dk, _ := KindByID("deployments")
	var d obj
	if err := k.Get(ctx, dk.Path(ns, deploy), nil, &d); err != nil {
		return nil, err
	}
	rk, _ := KindByID("replicasets")
	var l struct {
		Items []obj `json:"items"`
	}
	q := url.Values{}
	if sel := selector(d.mapOf("spec", "selector", "matchLabels")); sel != "" {
		q.Set("labelSelector", sel)
	}
	if err := k.Get(ctx, rk.Path(ns, ""), q, &l); err != nil {
		return nil, err
	}
	uid := d.str("metadata", "uid")
	cur, _ := strconv.Atoi(d.str("metadata", "annotations", "deployment.kubernetes.io/revision"))
	var out []Revision
	for _, rs := range l.Items {
		mine := false
		for _, ref := range rs.list("metadata", "ownerReferences") {
			if ref.str("uid") == uid {
				mine = true
			}
		}
		if !mine {
			continue
		}
		n, err := strconv.Atoi(rs.str("metadata", "annotations", "deployment.kubernetes.io/revision"))
		if err != nil {
			continue
		}
		rv := Revision{Number: n, ReplicaSet: rs.str("metadata", "name"), Created: ts(rs.str("metadata", "creationTimestamp")),
			Replicas: rs.num("status", "replicas"), Ready: rs.num("status", "readyReplicas"), Current: n == cur,
			ChangeCause: rs.str("metadata", "annotations", "kubernetes.io/change-cause"), template: rs.get("spec", "template")}
		for _, c := range rs.list("spec", "template", "spec", "containers") {
			rv.Images = append(rv.Images, c.str("image"))
		}
		out = append(out, rv)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number > out[j].Number })
	return out, nil
}

// Undo puts a Deployment back on the pod template of a revision, as
// kubectl rollout undo --to-revision does (that becomes a new revision).
func (k *Client) Undo(ctx context.Context, ns, deploy string, revision int) error {
	revs, err := k.Revisions(ctx, ns, deploy)
	if err != nil {
		return err
	}
	for _, rv := range revs {
		if rv.Number != revision {
			continue
		}
		if rv.Current {
			return errors.New("essa já é a versão atual")
		}
		tpl, _ := rv.template.(map[string]any)
		if tpl == nil {
			return errors.New("a versão não tem o modelo dos pods")
		}
		// The hash label belongs to the ReplicaSet, not to the template.
		if md, ok := tpl["metadata"].(map[string]any); ok {
			if lb, ok := md["labels"].(map[string]any); ok {
				delete(lb, "pod-template-hash")
			}
		}
		patch, _ := json.Marshal([]map[string]any{{"op": "replace", "path": "/spec/template", "value": tpl}})
		dk, _ := KindByID("deployments")
		resp, err := k.Do(ctx, http.MethodPatch, dk.Path(ns, deploy), nil, patch, "application/json-patch+json")
		if err != nil {
			return err
		}
		resp.Body.Close()
		return nil
	}
	return fmt.Errorf("a revisão %d não existe mais", revision)
}

// DrainResult says what happened to each pod of a node.
type DrainResult struct {
	Evicted []string     `json:"evicted"`
	Skipped []DrainIssue `json:"skipped"`
	Failed  []DrainIssue `json:"failed"`
}

type DrainIssue struct {
	Pod    string `json:"pod"`
	Reason string `json:"reason"`
}

// Drain isolates a node and asks the pods on it to leave (Eviction API,
// which respects PodDisruptionBudgets), as kubectl drain
// --ignore-daemonsets --delete-emptydir-data does. Pods without an owner
// stay (nothing would recreate them), as kubectl does without --force.
// It does not wait for the pods to end.
func (k *Client) Drain(ctx context.Context, node string) (*DrainResult, error) {
	nk, _ := KindByID("nodes")
	if err := k.Patch(ctx, nk, "", node, map[string]any{"spec": map[string]any{"unschedulable": true}}); err != nil {
		return nil, err
	}
	pk, _ := KindByID("pods")
	var l struct {
		Items []obj `json:"items"`
	}
	if err := k.Get(ctx, pk.Path("", ""), url.Values{"fieldSelector": {"spec.nodeName=" + node}}, &l); err != nil {
		return nil, err
	}
	res := &DrainResult{Evicted: []string{}, Skipped: []DrainIssue{}, Failed: []DrainIssue{}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 5) // a few evictions at a time
	for _, p := range l.Items {
		ns, name := p.str("metadata", "namespace"), p.str("metadata", "name")
		id := ns + "/" + name
		if p.str("metadata", "annotations", "kubernetes.io/config.mirror") != "" {
			res.Skipped = append(res.Skipped, DrainIssue{id, "pod estático do nó"})
			continue
		}
		// The owner that controls the pod (kubectl looks at controller: true).
		var ctrl obj
		for _, o := range p.list("metadata", "ownerReferences") {
			if o.get("controller") == true || ctrl == nil {
				ctrl = o
			}
		}
		if ctrl == nil {
			res.Skipped = append(res.Skipped, DrainIssue{id, "sem dono: nada o recriaria em outro nó"})
			continue
		}
		if ctrl.str("kind") == "DaemonSet" {
			res.Skipped = append(res.Skipped, DrainIssue{id, "do DaemonSet " + ctrl.str("name")})
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(ns, name, id string) {
			defer applog.Recover("kube/rollout.go:168")
			defer func() { <-sem; wg.Done() }()
			body := map[string]any{"apiVersion": "policy/v1", "kind": "Eviction", "metadata": map[string]any{"name": name, "namespace": ns}}
			resp, err := k.Do(ctx, http.MethodPost, pk.Path(ns, name)+"/eviction", nil, body, "")
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				var ae *APIError
				if errors.As(err, &ae) && ae.Status == http.StatusTooManyRequests {
					err = errors.New("um PodDisruptionBudget não deixa sair agora")
				}
				res.Failed = append(res.Failed, DrainIssue{id, err.Error()})
				return
			}
			resp.Body.Close()
			res.Evicted = append(res.Evicted, id)
		}(ns, name, id)
	}
	wg.Wait()
	sort.Strings(res.Evicted)
	sort.Slice(res.Failed, func(i, j int) bool { return res.Failed[i].Pod < res.Failed[j].Pod })
	return res, nil
}
