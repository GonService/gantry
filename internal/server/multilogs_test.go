package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMultiPodLogs(t *testing.T) {
	e := newEnv(t)
	multiLogPoll = 200 * time.Millisecond // only this test reads it (set before any stream)
	var polls atomic.Int32
	pod := func(n string) string {
		return `{"metadata":{"name":"` + n + `","namespace":"app","labels":{"app":"web"}},"spec":{"containers":[{"name":"app"}]},
		  "status":{"phase":"Running","containerStatuses":[{"name":"app","containerID":"c-` + n + `","state":{"running":{}}}]}}`
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch p := r.URL.Path; {
		case p == "/apis/apps/v1/namespaces/app/deployments/web":
			fmt.Fprint(w, `{"spec":{"selector":{"matchLabels":{"app":"web"},"matchExpressions":[{"key":"tier","operator":"NotIn","values":["db"]}]}}}`)
		case p == "/api/v1/namespaces/app/pods":
			if sel := r.URL.Query().Get("labelSelector"); sel != "app=web,tier notin (db)" {
				http.Error(w, "seletor "+sel, 400)
				return
			}
			// Later polls: web-c came, web-a went.
			if polls.Add(1) <= 2 {
				fmt.Fprintf(w, `{"items":[%s,%s]}`, pod("web-a"), pod("web-b"))
			} else {
				fmt.Fprintf(w, `{"items":[%s,%s]}`, pod("web-b"), pod("web-c"))
			}
		case strings.HasSuffix(p, "/log"):
			n := strings.Split(p, "/")[6]
			fmt.Fprintf(w, "2026-10-19T10:00:00Z linha de %s\n", n)
		case strings.HasPrefix(p, "/api/v1/namespaces/app/pods/"):
			fmt.Fprint(w, pod(filepath.Base(p)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	kc := filepath.Join(e.dir, "kc.yaml")
	os.WriteFile(kc, []byte("apiVersion: v1\nkind: Config\nclusters: [{name: c, cluster: {server: \""+api.URL+"\"}}]\nusers: [{name: u, user: {token: t}}]\ncontexts: [{name: k, context: {cluster: c, user: u}}]\ncurrent-context: k\n"), 0o600)
	b, _ := json.Marshal([]clusterRec{{ID: "c1", Name: "k", Source: "kubeconfig", File: kc, Context: "k", Mode: "livre"}})
	os.WriteFile(e.srv.clustersFile(), b, 0o600)

	var pods struct {
		Selector string
		Pods     []struct{ Name string }
	}
	e.call("GET", "/clusters/c1/varios/app/deployments/web/pods", nil, &pods)
	if pods.Selector != "app=web,tier notin (db)" || len(pods.Pods) != 2 {
		t.Fatalf("%+v", pods)
	}
	out := e.sse("/clusters/c1/varios/app/deployments/web/logs", 4*time.Second, "linha de web-c")
	for _, want := range []string{"logs de 2 pods: web-a, web-b", "web-a\x1b[0m │ linha de web-a", "web-b\x1b[0m │ linha de web-b", "entrou o pod web-c", "web-c\x1b[0m │ linha de web-c", "saiu o pod web-a"} {
		if !strings.Contains(out, want) {
			t.Errorf("faltou %q em %q", want, out)
		}
	}
	// Each pod in its own color.
	if strings.Count(out, "\x1b[36mweb-a") == 0 || strings.Count(out, "\x1b[33mweb-b") == 0 {
		t.Errorf("cores: %q", out)
	}
	// A filter by text and only some pods.
	out = e.sse("/clusters/c1/varios/app/deployments/web/logs?pods=web-b&grep=linha", 1500*time.Millisecond, "linha de web-b")
	if strings.Contains(out, "web-a") || !strings.Contains(out, "linha de web-b") {
		t.Errorf("só web-b: %q", out)
	}
}
