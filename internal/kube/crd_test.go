package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJSONPath(t *testing.T) {
	var o map[string]any
	json.Unmarshal([]byte(`{"metadata":{"labels":{"app.kubernetes.io/name":"loja"},"annotations":{"nota.io/x":"sim"}},"spec":{"chart":"nginx","ports":[{"port":80},{"port":443}]},
	  "status":{"conditions":[{"type":"Synced","status":"True"},{"type":"Ready","status":"False","reason":"Erro"}]}}`), &o)
	cases := map[string]any{
		".spec.chart":          "nginx",
		"{.spec.chart}":        "nginx",
		".spec.ports[1].port":  443.0,
		".spec.ports[-1].port": 443.0,
		`.status.conditions[?(@.type=="Ready")].status`: "False",
		`.status.conditions[?(@.type!="Ready")].type`:   "Synced",
		".spec.nada": nil,
		`.metadata.labels.app\.kubernetes\.io/name`:  "loja",
		".metadata.annotations['nota.io/x']":         "sim",
		`.metadata.labels["app.kubernetes.io/name"]`: "loja",
	}
	for path, want := range cases {
		if got := JSONPath(o, path); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: %v, queria %v", path, got, want)
		}
	}
	if got := printerValue(JSONPath(o, ".spec.ports[*].port"), "integer"); got != "80, 443" {
		t.Errorf("lista: %q", got)
	}
}

func TestCRDKinds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/apis/apiextensions.k8s.io/v1/customresourcedefinitions":
			fmt.Fprint(w, `{"items":[{"spec":{"group":"argoproj.io","scope":"Namespaced","names":{"plural":"applications","kind":"Application"},
			  "versions":[{"name":"v1alpha1","served":true,"storage":true,"additionalPrinterColumns":[
			    {"name":"Sync Status","type":"string","jsonPath":".status.sync.status"},
			    {"name":"Health","type":"string","jsonPath":".status.health.status"},
			    {"name":"Revision","type":"string","jsonPath":".status.sync.revision","priority":10},
			    {"name":"Age","type":"date","jsonPath":".metadata.creationTimestamp"}]}]}}]}`)
		case "/apis/argoproj.io/v1alpha1/namespaces/argocd/applications":
			fmt.Fprint(w, `{"items":[{"metadata":{"name":"loja","namespace":"argocd"},"status":{"sync":{"status":"OutOfSync"},"health":{"status":"Degraded"},
			  "conditions":[{"type":"Ready","status":"False","reason":"Erro","message":"imagem não existe"}]}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cfg, _ := Parse([]byte(fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters: [{name: c, cluster: {server: %q}}]\nusers: [{name: u, user: {token: t}}]\ncontexts: [{name: x, context: {cluster: c, user: u}}]\ncurrent-context: x\n", srv.URL)), "")
	k, _ := cfg.NewClient("")
	ctx := context.Background()
	kd, ok := k.KindFor(ctx, "crd.applications.argoproj.io")
	if !ok || kd.APIVersion != "argoproj.io/v1alpha1" || !kd.Namespaced || fmt.Sprint(kd.Columns) != "[Sync Status Health]" {
		t.Fatalf("tipo: %v %+v", ok, kd)
	}
	rows, err := k.List(ctx, kd, "argocd", "")
	if err != nil || len(rows) != 1 || fmt.Sprint(rows[0].Cells) != "[OutOfSync Degraded]" || rows[0].Problem != "Erro: imagem não existe" {
		t.Fatalf("linhas: %v %+v", err, rows)
	}
	if _, ok := k.KindFor(ctx, "crd.nada.exemplo.io"); ok {
		t.Fatal("achou um tipo que não existe")
	}
}

func TestDrain(t *testing.T) {
	var cordoned bool
	evicted := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/nodes/n1" && r.Method == http.MethodPatch:
			cordoned = true
			fmt.Fprint(w, `{}`)
		case r.URL.Path == "/api/v1/pods":
			if r.URL.Query().Get("fieldSelector") != "spec.nodeName=n1" {
				http.Error(w, "seletor", 400)
				return
			}
			fmt.Fprint(w, `{"items":[
			  {"metadata":{"name":"web-1","namespace":"app","ownerReferences":[{"kind":"ReplicaSet","name":"web-x"}]}},
			  {"metadata":{"name":"db-0","namespace":"app","ownerReferences":[{"kind":"StatefulSet","name":"db"}]}},
			  {"metadata":{"name":"fluent-abc","namespace":"log","ownerReferences":[{"kind":"DaemonSet","name":"fluent"}]}},
			  {"metadata":{"name":"solto","namespace":"app"}},
			  {"metadata":{"name":"kube-apiserver-n1","namespace":"kube-system","annotations":{"kubernetes.io/config.mirror":"x"},"ownerReferences":[{"kind":"Node","name":"n1"}]}}]}`)
		case strings.HasSuffix(r.URL.Path, "/eviction") && r.Method == http.MethodPost:
			if strings.Contains(r.URL.Path, "/db-0/") {
				http.Error(w, `{"message":"Cannot evict pod as it would violate the pod's disruption budget."}`, 429)
				return
			}
			evicted[r.URL.Path] = true
			fmt.Fprint(w, `{}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cfg, _ := Parse([]byte(fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters: [{name: c, cluster: {server: %q}}]\nusers: [{name: u, user: {token: t}}]\ncontexts: [{name: x, context: {cluster: c, user: u}}]\ncurrent-context: x\n", srv.URL)), "")
	k, _ := cfg.NewClient("")
	res, err := k.Drain(context.Background(), "n1")
	if err != nil || !cordoned {
		t.Fatalf("%v %v", err, cordoned)
	}
	if fmt.Sprint(res.Evicted) != "[app/web-1]" || !evicted["/api/v1/namespaces/app/pods/web-1/eviction"] {
		t.Fatalf("despejados: %+v", res.Evicted)
	}
	if len(res.Failed) != 1 || res.Failed[0].Pod != "app/db-0" || !strings.Contains(res.Failed[0].Reason, "PodDisruptionBudget") {
		t.Fatalf("falhas: %+v", res.Failed)
	}
	if len(res.Skipped) != 3 {
		t.Fatalf("ficaram: %+v", res.Skipped)
	}
}

// A kubeconfig from inside a VM keeps only inline credentials: no plugin
// that would run here, no local file sent to the API server, no proxy.
func TestUntrustedKubeconfig(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "segredo")
	os.WriteFile(secret, []byte("meu-token-local"), 0o600)
	raw := `apiVersion: v1
clusters: [{name: c, cluster: {server: "https://x", certificate-authority: "` + secret + `", proxy-url: "http://atacante:3128", certificate-authority-data: Q0E=}}]
users: [{name: u, user: {tokenFile: "` + secret + `", client-key-data: S0VZ, client-certificate-data: Q0VSVA==, exec: {command: /bin/sh, args: [-c, "touch /tmp/pwned"]}}}]
contexts: [{name: k, context: {cluster: c, user: u}}]
current-context: k
`
	cfg, err := Parse([]byte(raw), "")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Untrusted()
	out, err := cfg.Extract("k")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, bad := range []string{"meu-token-local", "exec", "/bin/sh", "atacante", "tokenFile", "certificate-authority:"} {
		if strings.Contains(s, bad) {
			t.Fatalf("%q ficou:\n%s", bad, s)
		}
	}
	for _, good := range []string{"client-key-data: S0VZ", "certificate-authority-data: Q0E="} {
		if !strings.Contains(s, good) {
			t.Fatalf("faltou %q:\n%s", good, s)
		}
	}
}

// TestCRDDiscovery: an account that may not list the CRDs still gets the
// custom resources (Argo CD's Applications) from the API discovery.
func TestCRDDiscovery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/apis/apiextensions.k8s.io/v1/customresourcedefinitions":
			http.Error(w, `{"kind":"Status","message":"forbidden","code":403}`, 403)
		case "/apis":
			fmt.Fprint(w, `{"groups":[{"name":"apps","preferredVersion":{"groupVersion":"apps/v1"}},
			  {"name":"metrics.k8s.io","preferredVersion":{"groupVersion":"metrics.k8s.io/v1beta1"}},
			  {"name":"argoproj.io","preferredVersion":{"groupVersion":"argoproj.io/v1alpha1"}}]}`)
		case "/apis/argoproj.io/v1alpha1":
			fmt.Fprint(w, `{"resources":[{"name":"applications","kind":"Application","namespaced":true,"verbs":["get","list","watch"]},
			  {"name":"applications/status","kind":"Application","namespaced":true,"verbs":["get"]},
			  {"name":"applicationsets","kind":"ApplicationSet","namespaced":true,"verbs":["list"]}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cfg, _ := Parse([]byte(fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters: [{name: c, cluster: {server: %q}}]\nusers: [{name: u, user: {token: t}}]\ncontexts: [{name: x, context: {cluster: c, user: u}}]\ncurrent-context: x\n", srv.URL)), "")
	k, _ := cfg.NewClient("")
	list, err := k.CRDs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, kd := range list {
		ids = append(ids, kd.ID)
	}
	if fmt.Sprint(ids) != "[crd.applications.argoproj.io crd.applicationsets.argoproj.io]" {
		t.Fatalf("%v", ids)
	}
}
