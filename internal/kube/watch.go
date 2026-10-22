package kube

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"sync"
	"time"

	"gantry/internal/applog"
)

// Lists of a big cluster are not read again every few seconds: the first
// read lists in pages and then follows the changes (watch), keeping the
// objects in memory. A list nobody asked for in a while stops.

const (
	listPage     = 500
	watchIdle    = 3 * time.Minute
	watchTimeout = 5 * time.Minute // the API server ends a watch; it starts again
)

type watchCache struct {
	kind  Kind
	ns    string
	mu    sync.Mutex
	objs  map[string]obj // by uid (or ns/name)
	rv    string
	ready chan struct{}
	err   error // of the first list
	used  time.Time
	stop  context.CancelFunc
}

var watchMu sync.Mutex

// ListCached is List (with no selector) from the cache of the kind.
func (k *Client) ListCached(ctx context.Context, kind Kind, ns string) ([]Row, error) {
	if !kind.Namespaced {
		ns = ""
	}
	key := kind.ID + "|" + ns
	watchMu.Lock()
	if k.watches == nil {
		k.watches = map[string]*watchCache{}
	}
	w, ok := k.watches[key]
	if !ok {
		wctx, cancel := context.WithCancel(context.Background())
		w = &watchCache{kind: kind, ns: ns, objs: map[string]obj{}, ready: make(chan struct{}), used: time.Now(), stop: cancel}
		k.watches[key] = w
		go func() { defer applog.Recover("kube/watch.go:55"); k.runWatch(wctx, w, key) }()
	}
	watchMu.Unlock()
	select {
	case <-w.ready:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.used = time.Now()
	if w.err != nil {
		return nil, w.err
	}
	rows := make([]Row, 0, len(w.objs))
	for _, o := range w.objs {
		rows = append(rows, kind.summary(o))
	}
	sortRows(kind, rows)
	return rows, nil
}

// StopWatches ends every watch of the client (the cluster changed or left).
func (k *Client) StopWatches() {
	watchMu.Lock()
	defer watchMu.Unlock()
	for key, w := range k.watches {
		w.stop()
		delete(k.watches, key)
	}
}

func objKey(o obj) string {
	if uid := o.str("metadata", "uid"); uid != "" {
		return uid
	}
	return o.str("metadata", "namespace") + "/" + o.str("metadata", "name")
}

func (k *Client) runWatch(ctx context.Context, w *watchCache, key string) {
	defer func() {
		watchMu.Lock()
		if k.watches[key] == w {
			delete(k.watches, key)
		}
		watchMu.Unlock()
	}()
	first := true
	for ctx.Err() == nil {
		lctx, lcancel := context.WithTimeout(ctx, 60*time.Second)
		objs, rv, err := k.listAll(lctx, w.kind, w.ns, nil)
		lcancel()
		if first {
			w.mu.Lock()
			if err != nil {
				w.err = err
			} else {
				w.objs, w.rv = objs, rv
			}
			w.mu.Unlock()
			close(w.ready)
			first = false
			if err != nil {
				return // the next request tries again from scratch
			}
		} else if err == nil {
			w.mu.Lock()
			w.objs, w.rv = objs, rv
			w.mu.Unlock()
		} else if !sleepCtx(ctx, 5*time.Second) {
			return
		} else {
			continue
		}
		// Follow the changes until the list must be read again (410 Gone).
		for ctx.Err() == nil {
			w.mu.Lock()
			idle := time.Since(w.used) > watchIdle
			rv := w.rv
			w.mu.Unlock()
			if idle {
				return
			}
			started := time.Now()
			gone, err := k.watchOnce(ctx, w, rv)
			if err == nil && !gone && time.Since(started) < time.Second && !sleepCtx(ctx, time.Second) {
				return // a watch that ends at once: not in a tight loop
			}
			if gone {
				break
			}
			if err != nil {
				// A watch that fails (no permission to watch, a proxy that cuts
				// streams…): read the list again after a pause, so the data
				// does not stay old forever.
				if !sleepCtx(ctx, 5*time.Second) {
					return
				}
				break
			}
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// watchOnce follows one watch request; gone says the list must be read again.
func (k *Client) watchOnce(ctx context.Context, w *watchCache, rv string) (gone bool, err error) {
	q := url.Values{"watch": {"1"}, "resourceVersion": {rv}, "allowWatchBookmarks": {"true"}, "timeoutSeconds": {strconv.Itoa(int(watchTimeout.Seconds()))}}
	// Stop following when nobody looks: checked between events and on a timer.
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		defer applog.Recover("kube/watch.go:174")
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-wctx.Done():
				return
			case <-t.C:
				w.mu.Lock()
				idle := time.Since(w.used) > watchIdle
				w.mu.Unlock()
				if idle {
					cancel()
					return
				}
			}
		}
	}()
	resp, err := k.Do(wctx, http.MethodGet, w.kind.Path(w.ns, ""), q, nil, "")
	if err != nil {
		var ae *APIError
		if errors.As(err, &ae) && ae.Status == http.StatusGone {
			return true, nil
		}
		return false, err
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	for {
		var ev struct {
			Type   string `json:"type"`
			Object obj    `json:"object"`
		}
		if err := dec.Decode(&ev); err != nil {
			if errors.Is(err, io.EOF) || wctx.Err() != nil {
				return false, nil
			}
			return false, err
		}
		nrv := ev.Object.str("metadata", "resourceVersion")
		w.mu.Lock()
		switch ev.Type {
		case "ADDED", "MODIFIED":
			key := objKey(ev.Object)
			if w.kind.ID == "pods" {
				k.podLeft(w.objs[key], ev.Object, false)
			}
			w.objs[key] = ev.Object
		case "DELETED":
			key := objKey(ev.Object)
			if w.kind.ID == "pods" {
				k.podLeft(w.objs[key], ev.Object, true)
			}
			delete(w.objs, key)
		case "ERROR":
			w.mu.Unlock()
			if ev.Object.num("code") == http.StatusGone {
				return true, nil
			}
			return false, errors.New(ev.Object.str("message"))
		}
		if nrv != "" {
			w.rv = nrv
		}
		w.mu.Unlock()
	}
}

// listAll reads a whole list in pages, with its resourceVersion.
func (k *Client) listAll(ctx context.Context, kind Kind, ns string, q url.Values) (map[string]obj, string, error) {
	out := map[string]obj{}
	qq := url.Values{}
	for key, v := range q {
		qq[key] = v
	}
	qq.Set("limit", strconv.Itoa(listPage))
	var rv string
	for {
		var l struct {
			Metadata struct {
				ResourceVersion string `json:"resourceVersion"`
				Continue        string `json:"continue"`
			} `json:"metadata"`
			Items []obj `json:"items"`
		}
		if err := k.Get(ctx, kind.Path(ns, ""), qq, &l); err != nil {
			return nil, "", err
		}
		for _, o := range l.Items {
			out[objKey(o)] = o
		}
		if rv == "" {
			rv = l.Metadata.ResourceVersion
		}
		if l.Metadata.Continue == "" {
			return out, rv, nil
		}
		qq.Set("continue", l.Metadata.Continue)
	}
}

func sortRows(kind Kind, out []Row) {
	sort.Slice(out, func(i, j int) bool {
		if kind.ID == "events" {
			return out[i].Created > out[j].Created
		}
		if (out[i].Problem != "") != (out[j].Problem != "") {
			return out[i].Problem != ""
		}
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
}

// cachedObject is an object from a list that is being followed (a copy:
// the caller may change it), or nil.
func (k *Client) cachedObject(kind Kind, ns, name string) obj {
	watchMu.Lock()
	var ws []*watchCache
	for _, key := range []string{kind.ID + "|" + ns, kind.ID + "|"} {
		if w, ok := k.watches[key]; ok {
			ws = append(ws, w)
		}
	}
	watchMu.Unlock()
	for _, w := range ws {
		select {
		case <-w.ready:
		default:
			continue
		}
		w.mu.Lock()
		var found obj
		if w.err == nil {
			for _, o := range w.objs {
				if o.str("metadata", "name") == name && o.str("metadata", "namespace") == ns {
					found = o
					break
				}
			}
		}
		w.mu.Unlock()
		if found != nil {
			b, err := json.Marshal(found)
			if err != nil {
				return nil
			}
			var cp obj
			if json.Unmarshal(b, &cp) != nil {
				return nil
			}
			return cp
		}
	}
	return nil
}
