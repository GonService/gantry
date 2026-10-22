package kube

import (
	"context"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Small answers that the screens ask for every few seconds and that change
// slowly (the version, metrics-server's numbers, which itself samples every
// 15 s) are kept for a while, so a screen open all day does not keep
// asking the API server.

const (
	versionTTL = time.Minute
	usageTTL   = 15 * time.Second
)

type timed struct {
	v  any
	at time.Time
}

var smallMu sync.Mutex

func (k *Client) cached(key string, ttl time.Duration, fetch func() (any, bool)) any {
	smallMu.Lock()
	if k.small == nil {
		k.small = map[string]timed{}
	}
	t, ok := k.small[key]
	smallMu.Unlock()
	if ok && time.Since(t.at) < ttl {
		return t.v
	}
	v, keep := fetch()
	if keep {
		smallMu.Lock()
		k.small[key] = timed{v, time.Now()}
		smallMu.Unlock()
	}
	return v
}

// Version is the Kubernetes version of the cluster (kept a minute).
func (k *Client) Version(ctx context.Context) (string, error) {
	var err error
	v := k.cached("version", versionTTL, func() (any, bool) {
		var out struct {
			GitVersion string `json:"gitVersion"`
		}
		err = k.Get(ctx, "/version", nil, &out)
		return out.GitVersion, err == nil
	})
	if err != nil {
		return "", err
	}
	return v.(string), nil
}

// NodeUsage reads metrics.k8s.io for the nodes (empty without metrics-server).
func (k *Client) NodeUsage(ctx context.Context) map[string]Usage {
	return k.cached("usage:nodes", usageTTL, func() (any, bool) {
		return k.usage(ctx, "/apis/metrics.k8s.io/v1beta1/nodes", false), true
	}).(map[string]Usage)
}

// PodUsage reads metrics.k8s.io for the pods of ns ("" = all), keyed ns/name.
// A fresh answer for every namespace serves one namespace too.
func (k *Client) PodUsage(ctx context.Context, ns string) map[string]Usage {
	smallMu.Lock()
	all, ok := k.small["usage:pods:"]
	smallMu.Unlock()
	if ns != "" && ok && time.Since(all.at) < usageTTL {
		out := map[string]Usage{}
		for key, u := range all.v.(map[string]Usage) {
			if strings.HasPrefix(key, ns+"/") {
				out[key] = u
			}
		}
		return out
	}
	return k.cached("usage:pods:"+ns, usageTTL, func() (any, bool) {
		p := "/apis/metrics.k8s.io/v1beta1/pods"
		if ns != "" {
			p = "/apis/metrics.k8s.io/v1beta1/namespaces/" + url.PathEscape(ns) + "/pods"
		}
		return k.usage(ctx, p, true), true
	}).(map[string]Usage)
}
