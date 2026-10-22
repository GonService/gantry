package kube

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSearch(t *testing.T) {
	q := ParseQuery("web label:app=web image:nginx ns:loja kind:deploy outra:coisa")
	if strings.Join(q.Words, ",") != "web,outra:coisa" || q.Labels["app"] != "web" || q.Image != "nginx" || q.NS != "loja" || q.Kind != "deploy" {
		t.Fatalf("%+v", q)
	}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		meta := strings.Contains(r.Header.Get("Accept"), "PartialObjectMetadataList")
		switch r.URL.Path {
		case "/apis/apps/v1/deployments":
			if meta {
				http.Error(w, "esperava spec", 400)
				return
			}
			io.WriteString(w, `{"items":[{"metadata":{"name":"web","namespace":"loja","labels":{"app":"web"}},"spec":{"template":{"spec":{"containers":[{"image":"nginx:1.27"}]}}}},
			  {"metadata":{"name":"api","namespace":"loja","labels":{"app":"api"}},"spec":{"template":{"spec":{"containers":[{"image":"ghcr.io/acme/api:2"}]}}}}]}`)
		case "/api/v1/pods":
			io.WriteString(w, `{"items":[{"metadata":{"name":"web-1","namespace":"loja","labels":{"app":"web"}},"spec":{"containers":[{"image":"nginx:1.27"}],"initContainers":[{"image":"busybox"}]}}]}`)
		case "/api/v1/services":
			if !meta {
				http.Error(w, "esperava só metadados", 400)
				return
			}
			io.WriteString(w, `{"items":[{"metadata":{"name":"web","namespace":"loja","labels":{"app":"web"}}}]}`)
		case "/api/v1/secrets":
			w.WriteHeader(403)
			io.WriteString(w, `{"kind":"Status","code":403,"message":"forbidden"}`)
		default:
			io.WriteString(w, `{"items":[]}`)
		}
	}))
	defer srv.Close()
	k := &Client{Server: srv.URL, http: srv.Client(), token: "t"}
	ctx := context.Background()
	r := k.Search(ctx, ParseQuery("web"), 100)
	names := map[string]bool{}
	for _, h := range r.Hits {
		names[h.Kind+"/"+h.Name] = true
	}
	if !names["deployments/web"] || !names["pods/web-1"] || !names["services/web"] || names["deployments/api"] || len(r.Skipped) != 1 || r.Skipped[0] != "Secrets" {
		t.Fatalf("%+v", r)
	}
	n := calls.Load()
	r = k.Search(ctx, ParseQuery("image:busybox"), 100)
	if len(r.Hits) != 1 || r.Hits[0].Name != "web-1" || r.Hits[0].Why != "busybox" || calls.Load() != n {
		t.Fatalf("imagem (do cache): %+v (%d chamadas)", r, calls.Load()-n)
	}
	r = k.Search(ctx, ParseQuery("label:app=web kind:svc"), 100)
	if len(r.Hits) != 1 || r.Hits[0].Kind != "services" {
		t.Fatalf("%+v", r)
	}
	if r = k.Search(ctx, ParseQuery("label:app"), 1); len(r.Hits) != 1 || !r.More {
		t.Fatalf("limite: %+v", r)
	}
	if r = k.Search(ctx, ParseQuery(""), 10); len(r.Hits) != 0 {
		t.Fatal(r)
	}
}
