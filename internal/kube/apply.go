package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/goccy/go-yaml"
)

type apiResource struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Namespaced bool   `json:"namespaced"`
}

var discMu sync.Mutex

type discEntry struct {
	list []apiResource
	at   time.Time
}

// How long the list of resources of an apiVersion is trusted. A kind that
// is missing is looked up again sooner (a CRD may have just been installed).
const (
	discTTL   = 10 * time.Minute
	discRetry = 15 * time.Second
)

// resourceFor finds the plural resource and scope of apiVersion+kind.
func (k *Client) resourceFor(ctx context.Context, apiVersion, kind string) (apiResource, error) {
	discMu.Lock()
	if k.disc == nil {
		k.disc = map[string]discEntry{}
	}
	e, ok := k.disc[apiVersion]
	discMu.Unlock()
	if ok && time.Since(e.at) < discTTL {
		if r, found := findKind(e.list, kind); found {
			return r, nil
		}
		if time.Since(e.at) < discRetry {
			return apiResource{}, fmt.Errorf("o cluster não conhece %s em %s", kind, apiVersion)
		}
	}
	p := "/apis/" + apiVersion
	if !strings.Contains(apiVersion, "/") {
		p = "/api/" + apiVersion
	}
	var d struct {
		Resources []apiResource `json:"resources"`
	}
	if err := k.Get(ctx, p, nil, &d); err != nil {
		if NotFound(err) {
			discMu.Lock()
			delete(k.disc, apiVersion)
			discMu.Unlock()
		}
		return apiResource{}, fmt.Errorf("%s não existe neste cluster: %v", apiVersion, err)
	}
	discMu.Lock()
	k.disc[apiVersion] = discEntry{list: d.Resources, at: time.Now()}
	discMu.Unlock()
	if r, found := findKind(d.Resources, kind); found {
		return r, nil
	}
	return apiResource{}, fmt.Errorf("o cluster não conhece %s em %s", kind, apiVersion)
}

func findKind(list []apiResource, kind string) (apiResource, bool) {
	for _, r := range list {
		if r.Kind == kind && !strings.Contains(r.Name, "/") {
			return r, true
		}
	}
	return apiResource{}, false
}

// Applied is what happened to one document of an apply.
type Applied struct {
	Kind      string  `json:"kind"`
	Name      string  `json:"name"`
	Namespace string  `json:"namespace,omitempty"`
	Error     string  `json:"error,omitempty"`
	Doc       int     `json:"doc"` // which document of the text (from 0)
	Causes    []Cause `json:"causes,omitempty"`
}

// Apply sends YAML documents with server-side apply (as kubectl apply
// --server-side), in order. ns fills documents without a namespace.
func (k *Client) Apply(ctx context.Context, docs, ns string) ([]Applied, error) {
	return k.apply(ctx, docs, ns, false, 0)
}

// ValidateApply is Apply in dry run (nothing changes in the cluster).
func (k *Client) ValidateApply(ctx context.Context, docs, ns string) ([]Applied, error) {
	return k.apply(ctx, docs, ns, true, 0)
}

func (k *Client) apply(ctx context.Context, docs, ns string, dry bool, doc0 int) ([]Applied, error) {
	if ns == "" {
		ns = k.Namespace
	}
	if ns == "" {
		ns = "default"
	}
	var out []Applied
	for di, raw := range splitDocs(docs) {
		di += doc0
		var o map[string]any
		if err := yaml.Unmarshal([]byte(raw), &o); err != nil {
			if dry { // checking: every document has its say
				out = append(out, Applied{Doc: di, Error: fmt.Sprintf("YAML inválido: %v", err)})
				continue
			}
			return out, fmt.Errorf("YAML inválido: %v", err)
		}
		if len(o) == 0 {
			continue
		}
		if o["kind"] == "List" {
			if items, ok := o["items"].([]any); ok {
				for _, it := range items {
					b, _ := yaml.Marshal(it)
					sub, err := k.apply(ctx, string(b), ns, dry, di)
					out = append(out, sub...)
					if err != nil {
						return out, err
					}
				}
			}
			continue
		}
		apiVersion, _ := o["apiVersion"].(string)
		kind, _ := o["kind"].(string)
		md, _ := o["metadata"].(map[string]any)
		name, _ := md["name"].(string)
		a := Applied{Kind: kind, Name: name, Doc: di}
		if apiVersion == "" || kind == "" || name == "" {
			if dry {
				a.Error = "documento sem apiVersion, kind ou metadata.name"
				out = append(out, a)
				continue
			}
			return out, fmt.Errorf("documento sem apiVersion, kind ou metadata.name")
		}
		res, err := k.resourceFor(ctx, apiVersion, kind)
		if err != nil {
			a.Error = err.Error()
			out = append(out, a)
			continue
		}
		p := "/apis/" + apiVersion
		if !strings.Contains(apiVersion, "/") {
			p = "/api/" + apiVersion
		}
		if res.Namespaced {
			docNS, _ := md["namespace"].(string)
			if docNS == "" {
				docNS = ns
				md["namespace"] = ns
			}
			a.Namespace = docNS
			p += "/namespaces/" + url.PathEscape(docNS)
		}
		p += "/" + res.Name + "/" + url.PathEscape(name)
		// JSON is valid apply-patch YAML and keeps the types as written.
		body, err := json.Marshal(o)
		if err != nil {
			a.Error = err.Error()
			out = append(out, a)
			continue
		}
		resp, err := k.Do(ctx, http.MethodPatch, p, dryRun(dry, url.Values{"fieldManager": {"gantry"}, "force": {"true"}}), body, "application/apply-patch+yaml")
		if err != nil {
			a.Error = err.Error()
			var ae *APIError
			if errors.As(err, &ae) {
				a.Causes = ae.Causes
			}
		} else {
			resp.Body.Close()
		}
		out = append(out, a)
	}
	return out, nil
}

func splitDocs(s string) []string {
	var out []string
	var cur []string
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if t := strings.TrimRight(l, " \t"); t == "---" || strings.HasPrefix(t, "--- #") {
			out = append(out, strings.Join(cur, "\n"))
			cur = nil
			continue
		}
		cur = append(cur, l)
	}
	return append(out, strings.Join(cur, "\n"))
}
