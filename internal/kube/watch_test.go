package kube

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A fake API server with pages, a watch that sends events, and a 410 that
// forces the list to be read again.
func TestListCachedWatch(t *testing.T) {
	var lists, watches int32
	events := make(chan string, 10)
	var mu sync.Mutex
	pods := map[string]bool{"a": true, "b": true, "c": true}
	podJSON := func(n string) string {
		return fmt.Sprintf(`{"metadata":{"name":%q,"namespace":"ns","uid":"u-%s","resourceVersion":"9"},"status":{"phase":"Running"}}`, n, n)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/namespaces/ns/pods" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if q.Get("watch") == "1" {
			n := atomic.AddInt32(&watches, 1)
			if n == 2 {
				// The resourceVersion is too old: read the list again.
				fmt.Fprint(w, `{"type":"ERROR","object":{"kind":"Status","code":410,"message":"too old"}}`+"\n")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fl := w.(http.Flusher)
			fl.Flush()
			for {
				select {
				case ev, ok := <-events:
					if !ok {
						return
					}
					fmt.Fprintln(w, ev)
					fl.Flush()
				case <-r.Context().Done():
					return
				case <-time.After(300 * time.Millisecond):
					if n == 1 && len(events) == 0 {
						return // end this watch: the client watches again
					}
				}
			}
		}
		atomic.AddInt32(&lists, 1)
		if q.Get("limit") != "500" {
			http.Error(w, "sem limite", 400)
			return
		}
		mu.Lock()
		var names []string
		for n := range pods {
			names = append(names, n)
		}
		mu.Unlock()
		sort.Strings(names)
		// Two per page.
		start := 0
		if c := q.Get("continue"); c != "" {
			fmt.Sscanf(c, "%d", &start)
		}
		end := start + 2
		cont := ""
		if end < len(names) {
			cont = fmt.Sprint(end)
		} else {
			end = len(names)
		}
		var items []string
		for _, n := range names[start:end] {
			items = append(items, podJSON(n))
		}
		fmt.Fprintf(w, `{"metadata":{"resourceVersion":"10","continue":%q},"items":[%s]}`, cont, strings.Join(items, ","))
	}))
	defer srv.Close()
	cfg, _ := Parse([]byte(fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters: [{name: c, cluster: {server: %q}}]\nusers: [{name: u, user: {token: t}}]\ncontexts: [{name: x, context: {cluster: c, user: u}}]\ncurrent-context: x\n", srv.URL)), "")
	k, _ := cfg.NewClient("")
	defer k.StopWatches()
	ctx := context.Background()
	pk, _ := KindByID("pods")
	names := func() string {
		rows, err := k.ListCached(ctx, pk, "ns")
		if err != nil {
			t.Fatal(err)
		}
		var n []string
		for _, r := range rows {
			n = append(n, r.Name)
		}
		return strings.Join(n, ",")
	}
	if got := names(); got != "a,b,c" {
		t.Fatalf("lista em páginas: %s", got)
	}
	if atomic.LoadInt32(&lists) != 2 {
		t.Fatalf("páginas lidas: %d", lists)
	}
	// Reading again uses the cache.
	names()
	if atomic.LoadInt32(&lists) != 2 {
		t.Fatal("leu a lista de novo em vez de usar o cache")
	}
	events <- `{"type":"ADDED","object":` + podJSON("d") + `}`
	events <- `{"type":"DELETED","object":` + podJSON("a") + `}`
	waitFor(t, func() bool { return names() == "b,c,d" })
	// The second watch answers 410: the list is read again (with e in it).
	mu.Lock()
	pods = map[string]bool{"b": true, "c": true, "d": true, "e": true}
	mu.Unlock()
	waitFor(t, func() bool { return names() == "b,c,d,e" })
	if atomic.LoadInt32(&lists) < 4 {
		t.Fatalf("não leu a lista de novo depois do 410: %d", lists)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if ok() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("não aconteceu a tempo")
}

// No permission to watch (403): the list is read again now and then, so
// the data does not stay old forever.
func TestListCachedNoWatch(t *testing.T) {
	var lists int32
	var name atomic.Value
	name.Store("a")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("watch") == "1" {
			http.Error(w, `{"message":"forbidden"}`, 403)
			return
		}
		atomic.AddInt32(&lists, 1)
		fmt.Fprintf(w, `{"metadata":{"resourceVersion":"1"},"items":[{"metadata":{"name":%q,"namespace":"ns","uid":"u"}}]}`, name.Load())
	}))
	defer srv.Close()
	cfg, _ := Parse([]byte(fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters: [{name: c, cluster: {server: %q}}]\nusers: [{name: u, user: {token: t}}]\ncontexts: [{name: x, context: {cluster: c, user: u}}]\ncurrent-context: x\n", srv.URL)), "")
	k, _ := cfg.NewClient("")
	defer k.StopWatches()
	ck, _ := KindByID("configmaps")
	get := func() string {
		rows, err := k.ListCached(context.Background(), ck, "ns")
		if err != nil || len(rows) != 1 {
			t.Fatalf("%v %v", err, rows)
		}
		return rows[0].Name
	}
	if get() != "a" {
		t.Fatal("primeira lista")
	}
	name.Store("b")
	deadline := time.Now().Add(15 * time.Second)
	for get() != "b" {
		if time.Now().After(deadline) {
			t.Fatalf("sem watch, a lista nunca foi lida de novo (%d leituras)", atomic.LoadInt32(&lists))
		}
		time.Sleep(200 * time.Millisecond)
	}
}
