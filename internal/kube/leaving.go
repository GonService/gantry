package kube

import (
	"context"
	"net/url"
	"strings"
)

// A pod that is going away takes its log with it: the cluster keeps the log
// of a container only while the pod exists. Gantry saves it before that
// (see the server's podlogs.go); here is what it needs to know of the pod.

// LeavingPod is a pod about to be deleted.
type LeavingPod struct {
	UID        string
	NS, Name   string
	Workload   string // Deployment/web, StatefulSet/db, Job/x… ("Pod/name" with no owner)
	Node       string
	Containers []LeavingContainer
}

// LeavingContainer is one container of a LeavingPod.
type LeavingContainer struct {
	Name     string
	Restarts int // > 0: the run before the current one has a log too
	Started  bool
}

// PodWorkload names what owns a pod, as the user knows it: the Deployment
// of a ReplicaSet made by a Deployment.
func PodWorkload(o obj) string {
	refs := o.list("metadata", "ownerReferences")
	if len(refs) == 0 {
		return "Pod/" + o.str("metadata", "name")
	}
	ref := refs[0]
	for _, r := range refs {
		if c, _ := r["controller"].(bool); c {
			ref = r
			break
		}
	}
	kind, name := ref.str("kind"), ref.str("name")
	if kind == "ReplicaSet" {
		if h := o.str("metadata", "labels", "pod-template-hash"); h != "" && strings.HasSuffix(name, "-"+h) {
			return "Deployment/" + strings.TrimSuffix(name, "-"+h)
		}
	}
	return kind + "/" + name
}

// leavingOf reads a pod object.
func leavingOf(o obj) LeavingPod {
	p := LeavingPod{
		UID: o.str("metadata", "uid"), NS: o.str("metadata", "namespace"), Name: o.str("metadata", "name"),
		Workload: PodWorkload(o), Node: o.str("spec", "nodeName"),
	}
	st := map[string]obj{}
	for _, list := range []string{"initContainerStatuses", "containerStatuses"} {
		for _, c := range o.list("status", list) {
			st[c.str("name")] = c
		}
	}
	for _, list := range []string{"initContainers", "containers"} {
		for _, c := range o.list("spec", list) {
			name := c.str("name")
			s, ok := st[name]
			lc := LeavingContainer{Name: name}
			if ok {
				lc.Restarts = s.num("restartCount")
				lc.Started = s.mapOf("state", "waiting") == nil || lc.Restarts > 0
			}
			p.Containers = append(p.Containers, lc)
		}
	}
	return p
}

// PodLeaving reads a pod as a LeavingPod.
func (k *Client) PodLeaving(ctx context.Context, ns, name string) (LeavingPod, obj, error) {
	var o obj
	if err := k.Get(ctx, "/api/v1/namespaces/"+url.PathEscape(ns)+"/pods/"+url.PathEscape(name), nil, &o); err != nil {
		return LeavingPod{}, nil, err
	}
	return leavingOf(o), o, nil
}

// WorkloadOfPod is PodWorkload of a pod read now.
func (k *Client) WorkloadOfPod(ctx context.Context, ns, name string) (string, error) {
	p, _, err := k.PodLeaving(ctx, ns, name)
	return p.Workload, err
}

// PodsOfWorkload are the pods of a workload (kind as in PodWorkload).
func (k *Client) PodsOfWorkload(ctx context.Context, ns, workload string) ([]LeavingPod, error) {
	var l struct {
		Items []obj `json:"items"`
	}
	if err := k.Get(ctx, "/api/v1/namespaces/"+url.PathEscape(ns)+"/pods", nil, &l); err != nil {
		return nil, err
	}
	var out []LeavingPod
	for _, o := range l.Items {
		if PodWorkload(o) == workload {
			out = append(out, leavingOf(o))
		}
	}
	return out, nil
}

// podLeft tells the watch of pods saw one start to go (or go at once).
func (k *Client) podLeft(old, now obj, deleted bool) {
	k.mu.Lock()
	fn := k.OnPodLeaving
	k.mu.Unlock()
	if fn == nil {
		return
	}
	terminating := now.str("metadata", "deletionTimestamp") != ""
	wasTerminating := old != nil && old.str("metadata", "deletionTimestamp") != ""
	if (terminating && !wasTerminating) || (deleted && !wasTerminating && !terminating) {
		go fn(leavingOf(now))
	}
}

// SetPodLeaving sets what runs when a pod watched by the client starts to go.
func (k *Client) SetPodLeaving(fn func(LeavingPod)) {
	k.mu.Lock()
	k.OnPodLeaving = fn
	k.mu.Unlock()
}
