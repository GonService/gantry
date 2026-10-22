package kube

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DebugImages are the images offered for a debug container.
var DebugImages = []string{"nicolaka/netshoot", "busybox", "alpine"}

// DebugContainer is an ephemeral container of a pod.
type DebugContainer struct {
	Name    string `json:"name"`
	Image   string `json:"image"`
	Target  string `json:"target,omitempty"`
	State   string `json:"state"`
	Reason  string `json:"reason,omitempty"`
	Running bool   `json:"running"`
}

func podPath(ns, pod string) string {
	return "/api/v1/namespaces/" + url.PathEscape(ns) + "/pods/" + url.PathEscape(pod)
}

// DebugContainers lists the ephemeral containers of a pod with their state.
func (k *Client) DebugContainers(ctx context.Context, ns, pod string) ([]DebugContainer, error) {
	var o obj
	if err := k.Get(ctx, podPath(ns, pod), nil, &o); err != nil {
		return nil, err
	}
	return debugOf(o), nil
}

func debugOf(o obj) []DebugContainer {
	st := map[string]obj{}
	for _, s := range o.list("status", "ephemeralContainerStatuses") {
		st[s.str("name")] = s
	}
	out := []DebugContainer{}
	for _, c := range o.list("spec", "ephemeralContainers") {
		d := DebugContainer{Name: c.str("name"), Image: c.str("image"), Target: c.str("targetContainerName"), State: "waiting"}
		if s, ok := st[d.Name]; ok {
			switch {
			case s.get("state", "running") != nil:
				d.State, d.Running = "running", true
			case s.get("state", "terminated") != nil:
				d.State, d.Reason = "terminated", s.str("state", "terminated", "reason")
			default:
				d.Reason = s.str("state", "waiting", "reason")
				if m := s.str("state", "waiting", "message"); m != "" && d.Reason != "" && d.Reason != "PodInitializing" && d.Reason != "ContainerCreating" {
					d.Reason += ": " + m
				}
			}
		}
		out = append(out, d)
	}
	return out
}

// debugWait: how long to wait for the image of a debug container.
var debugWait = 3 * time.Minute

// AddDebugContainer adds an ephemeral container (with a TTY, seeing the
// processes of target) to a running pod and waits for it to run. The pod
// does not restart. Ephemeral containers cannot be removed: they stay
// listed (terminated) until the pod goes.
func (k *Client) AddDebugContainer(ctx context.Context, ns, pod, target, image string) (string, error) {
	image = strings.TrimSpace(image)
	if image == "" {
		image = DebugImages[0]
	}
	if strings.ContainsAny(image, " \t\n") {
		return "", errors.New("imagem inválida")
	}
	var o obj
	if err := k.Get(ctx, podPath(ns, pod), nil, &o); err != nil {
		return "", err
	}
	if p := o.str("status", "phase"); p != "Running" {
		return "", fmt.Errorf("o pod está %s; o debug precisa dele rodando", strings.ToLower(p))
	}
	n := 1
	taken := map[string]bool{}
	for _, c := range o.list("spec", "ephemeralContainers") {
		taken[c.str("name")] = true
	}
	for taken[fmt.Sprintf("gantry-debug-%d", n)] {
		n++
	}
	name := fmt.Sprintf("gantry-debug-%d", n)
	c := map[string]any{"name": name, "image": image, "stdin": true, "tty": true, "imagePullPolicy": "IfNotPresent",
		"terminationMessagePolicy": "File"}
	if target != "" {
		c["targetContainerName"] = target
	}
	patch := map[string]any{"spec": map[string]any{"ephemeralContainers": []any{c}}}
	resp, err := k.Do(ctx, http.MethodPatch, podPath(ns, pod)+"/ephemeralcontainers", nil, patch, "application/strategic-merge-patch+json")
	if err != nil {
		var ae *APIError
		if errors.As(err, &ae) && ae.Status == http.StatusNotFound && !strings.Contains(strings.ToLower(ae.Message), pod) {
			return "", errors.New("este cluster não aceita containers efêmeros (precisa do Kubernetes 1.25 ou mais novo)")
		}
		return "", err
	}
	resp.Body.Close()
	deadline := time.Now().Add(debugWait)
	for {
		list, err := k.DebugContainers(ctx, ns, pod)
		if err != nil {
			return name, err
		}
		for _, d := range list {
			if d.Name != name {
				continue
			}
			if d.Running {
				return name, nil
			}
			if d.State == "terminated" {
				return name, fmt.Errorf("o container de debug terminou (%s)", d.Reason)
			}
			if strings.HasPrefix(d.Reason, "ErrImagePull") || strings.HasPrefix(d.Reason, "ImagePullBackOff") || strings.HasPrefix(d.Reason, "InvalidImageName") {
				return name, fmt.Errorf("não consegui baixar a imagem %s: %s", image, d.Reason)
			}
		}
		if time.Now().After(deadline) {
			return name, fmt.Errorf("o container de debug não começou a rodar em %s", debugWait)
		}
		select {
		case <-ctx.Done():
			return name, ctx.Err()
		case <-time.After(700 * time.Millisecond):
		}
	}
}
