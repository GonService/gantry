package kube

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Search over the whole cluster: by name, label, image, namespace and kind,
// without knowing where an object is.

// Query is a search: free words (in the name) and filters.
type Query struct {
	Words  []string
	Labels map[string]string // label:k=v (v empty: the key exists)
	Image  string
	NS     string
	Kind   string
}

// ParseQuery reads "web label:app=web image:nginx ns:loja kind:deploy".
func ParseQuery(s string) Query {
	q := Query{Labels: map[string]string{}}
	for _, f := range strings.Fields(s) {
		k, v, ok := strings.Cut(f, ":")
		if !ok || v == "" {
			q.Words = append(q.Words, strings.ToLower(f))
			continue
		}
		switch strings.ToLower(k) {
		case "label", "l":
			lk, lv, _ := strings.Cut(v, "=")
			q.Labels[lk] = lv
		case "image", "img", "imagem":
			q.Image = strings.ToLower(v)
		case "ns", "namespace":
			q.NS = v
		case "kind", "tipo":
			q.Kind = strings.ToLower(v)
		default:
			q.Words = append(q.Words, strings.ToLower(f))
		}
	}
	return q
}

// Empty: nothing to look for.
func (q Query) Empty() bool {
	return len(q.Words) == 0 && len(q.Labels) == 0 && q.Image == "" && q.Kind == "" && q.NS == ""
}

// Hit is an object found.
type Hit struct {
	Kind      string   `json:"kind"` // id (deployments…)
	Label     string   `json:"label"`
	Namespace string   `json:"namespace,omitempty"`
	Name      string   `json:"name"`
	Images    []string `json:"images,omitempty"`
	Why       string   `json:"why,omitempty"`
}

// SearchResult has the hits and the kinds that could not be read.
type SearchResult struct {
	Hits    []Hit    `json:"hits"`
	Skipped []string `json:"skipped,omitempty"`
	More    bool     `json:"more,omitempty"`
}

// kinds with containers: searched by image (spec), the others by metadata.
var imageKinds = map[string][]string{
	"pods":         {"spec"},
	"deployments":  {"spec", "template", "spec"},
	"statefulsets": {"spec", "template", "spec"},
	"daemonsets":   {"spec", "template", "spec"},
	"replicasets":  {"spec", "template", "spec"},
	"jobs":         {"spec", "template", "spec"},
	"cronjobs":     {"spec", "jobTemplate", "spec", "template", "spec"},
}

type searchItem struct {
	ns, name string
	labels   map[string]any
	images   []string
}

type searchCache struct {
	at    time.Time
	items map[string][]searchItem // kind id -> objects
	skip  []string
}

var (
	searchMu     sync.Mutex
	searchCaches = map[*Client]*searchCache{}
)

const searchTTL = 30 * time.Second

func kindMatches(k Kind, want string) bool {
	if want == "" {
		return true
	}
	id, label := strings.ToLower(k.ID), strings.ToLower(k.Label)
	return strings.HasPrefix(id, want) || strings.HasPrefix(label, want) || (want == "deploy" && id == "deployments") ||
		(want == "svc" && id == "services") || (want == "cm" && id == "configmaps") || (want == "sts" && id == "statefulsets") ||
		(want == "ds" && id == "daemonsets") || (want == "ing" && id == "ingresses") || (want == "sa" && id == "serviceaccounts")
}

// index reads every searchable kind (cached for a little while).
func (k *Client) index(ctx context.Context) *searchCache {
	searchMu.Lock()
	c := searchCaches[k]
	searchMu.Unlock()
	if c != nil && time.Since(c.at) < searchTTL {
		return c
	}
	c = &searchCache{at: time.Now(), items: map[string][]searchItem{}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, kd := range Kinds {
		if kd.ID == "events" {
			continue
		}
		wg.Add(1)
		go func(kd Kind) {
			defer wg.Done()
			var l struct {
				Items []obj `json:"items"`
			}
			lctx := ctx
			if _, ok := imageKinds[kd.ID]; !ok {
				lctx = metaOnly(ctx)
			}
			if err := k.Get(lctx, kd.Path("", ""), nil, &l); err != nil {
				var ae *APIError
				if errors.As(err, &ae) && (ae.Status == http.StatusForbidden || ae.Status == http.StatusNotFound) {
					mu.Lock()
					c.skip = append(c.skip, kd.Label)
					mu.Unlock()
				}
				return
			}
			items := make([]searchItem, 0, len(l.Items))
			for _, it := range l.Items {
				si := searchItem{ns: it.str("metadata", "namespace"), name: it.str("metadata", "name"), labels: it.mapOf("metadata", "labels")}
				if path, ok := imageKinds[kd.ID]; ok {
					for _, f := range []string{"containers", "initContainers"} {
						for _, ct := range it.list(append(append([]string{}, path...), f)...) {
							si.images = append(si.images, ct.str("image"))
						}
					}
				}
				items = append(items, si)
			}
			mu.Lock()
			c.items[kd.ID] = items
			mu.Unlock()
		}(kd)
	}
	wg.Wait()
	sort.Strings(c.skip)
	searchMu.Lock()
	searchCaches[k] = c
	searchMu.Unlock()
	return c
}

// Search looks for objects; at most limit hits.
func (k *Client) Search(ctx context.Context, q Query, limit int) SearchResult {
	res := SearchResult{Hits: []Hit{}}
	if q.Empty() {
		return res
	}
	c := k.index(ctx)
	res.Skipped = c.skip
	for _, kd := range Kinds {
		if !kindMatches(kd, q.Kind) {
			continue
		}
		if q.Image != "" {
			if _, ok := imageKinds[kd.ID]; !ok {
				continue
			}
		}
		for _, it := range c.items[kd.ID] {
			if q.NS != "" && it.ns != q.NS {
				continue
			}
			name := strings.ToLower(it.name)
			ok := true
			for _, w := range q.Words {
				if !strings.Contains(name, w) {
					ok = false
					break
				}
			}
			for lk, lv := range q.Labels {
				v, has := it.labels[lk]
				if !has || (lv != "" && v != lv) {
					ok = false
				}
			}
			var why string
			if ok && q.Image != "" {
				ok = false
				for _, im := range it.images {
					if strings.Contains(strings.ToLower(im), q.Image) {
						ok, why = true, im
						break
					}
				}
			}
			if !ok {
				continue
			}
			if len(res.Hits) >= limit {
				res.More = true
				return res
			}
			res.Hits = append(res.Hits, Hit{Kind: kd.ID, Label: kd.Label, Namespace: it.ns, Name: it.name, Images: it.images, Why: why})
		}
	}
	return res
}

// ForgetSearch drops the cached index (after a change in the cluster).
func (k *Client) ForgetSearch() {
	searchMu.Lock()
	delete(searchCaches, k)
	searchMu.Unlock()
}
