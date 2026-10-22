//go:build e2e

package e2e

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestKube runs the cluster screens against a real cluster with pods
// running (kind in the CI): apply, lists, logs, shell, port forward,
// scale, restart and roll back, drain. GANTRY_E2E_KUBECONFIG is its
// kubeconfig (the current context is used).
func TestKube(t *testing.T) {
	kc := os.Getenv("GANTRY_E2E_KUBECONFIG")
	if kc == "" {
		t.Skip("defina GANTRY_E2E_KUBECONFIG (um cluster com pods rodando, como o kind)")
	}
	a := start(t)
	var c struct{ ID string }
	if code := a.call("POST", "/clusters", map[string]any{"path": kc, "name": "e2e", "mode": "livre", "trust": true}, &c); code != 200 || c.ID == "" {
		t.Fatalf("adicionar o cluster: %d", code)
	}
	base := "/clusters/" + c.ID

	const ns = "gantry-e2e"
	manifest := fmt.Sprintf(`apiVersion: v1
kind: Namespace
metadata: {name: %[1]s}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: web, namespace: %[1]s}
spec:
  replicas: 1
  selector: {matchLabels: {app: web}}
  template:
    metadata: {labels: {app: web}}
    spec:
      containers:
      - name: nginx
        image: docker.io/library/nginx:1.27-alpine
        ports: [{containerPort: 80}]
---
apiVersion: v1
kind: Service
metadata: {name: web, namespace: %[1]s}
spec:
  selector: {app: web}
  ports: [{port: 8080, targetPort: 80}]
---
apiVersion: v1
kind: Pod
metadata: {name: falador, namespace: %[1]s}
spec:
  containers:
  - name: main
    image: docker.io/library/busybox:1.36
    command: ["sh", "-c", "i=0; while true; do i=$((i+1)); echo linha-$i; sleep 1; done"]
`, ns)
	if code := a.call("POST", base+"/aplicar", map[string]string{"yaml": manifest}, nil); code != 200 {
		t.Fatalf("aplicar: %d", code)
	}
	t.Cleanup(func() {
		a.call("DELETE", base+"/r/namespaces/-/"+ns, nil, nil)
	})

	type row struct {
		Name, Namespace, Problem string
		Cells                    []string
	}
	pods := func() []row {
		var out struct{ Rows []row }
		a.call("GET", base+"/r/pods?ns="+ns, nil, &out)
		return out.Rows
	}
	running := func(prefix string) string {
		for _, p := range pods() {
			if strings.HasPrefix(p.Name, prefix) && len(p.Cells) > 1 && p.Cells[1] == "Running" && strings.HasPrefix(p.Cells[0], "1/") {
				return p.Name
			}
		}
		return ""
	}
	waitFor := func(what string, d time.Duration, ok func() bool) {
		t.Helper()
		end := time.Now().Add(d)
		for time.Now().Before(end) {
			if ok() {
				return
			}
			time.Sleep(2 * time.Second)
		}
		t.Fatalf("%s não aconteceu em %s; pods: %+v", what, d, pods())
	}
	var web, falador string
	waitFor("pods rodando", 4*time.Minute, func() bool {
		web, falador = running("web-"), running("falador")
		return web != "" && falador != ""
	})

	t.Run("logs ao vivo", func(t *testing.T) {
		got := a.sse(base+"/pods/"+ns+"/falador/logs?container=main&tail=5", "linha-", 30*time.Second)
		if !strings.Contains(got, "linha-") {
			t.Fatalf("log: %q", got)
		}
		// Following: lines that did not exist when the log opened arrive
		// (one a second; the stream stays open until the test closes it).
		later := a.sse(base+"/pods/"+ns+"/falador/logs?container=main&tail=1", "zzz-nunca", 6*time.Second)
		if strings.Count(later, "linha-") < 3 {
			t.Fatalf("o log não seguiu: %q", later)
		}
	})

	t.Run("shell", func(t *testing.T) {
		var term struct{ ID string }
		if code := a.call("POST", base+"/shell/"+ns+"/"+web+"?container=nginx", nil, &term); code != 200 || term.ID == "" {
			t.Fatalf("shell: %d", code)
		}
		out := make(chan string, 1)
		go func() { out <- a.sse("/term/"+term.ID+"/stream", "resposta-42", 30*time.Second) }()
		time.Sleep(time.Second)
		req, _ := http.NewRequest("POST", a.url+"/api/term/"+term.ID+"/input", strings.NewReader("echo resposta-$((40+2))\n"))
		req.Header.Set("Authorization", "Bearer "+a.srv.Token())
		if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode >= 300 {
			t.Fatalf("entrada: %v %v", err, resp)
		}
		if got := <-out; !strings.Contains(got, "resposta-42") {
			t.Fatalf("shell: %q", got)
		}
		a.call("DELETE", "/term/"+term.ID, nil, nil)
	})

	t.Run("encaminhar porta", func(t *testing.T) {
		for _, tc := range []struct {
			kind, name string
			port       int
		}{{"pods", web, 80}, {"services", "web", 8080}} {
			var f struct {
				ID    string
				Local int
			}
			if code := a.call("POST", base+"/encaminhar", map[string]any{"ns": ns, "kind": tc.kind, "name": tc.name, "port": tc.port}, &f); code != 200 || f.Local == 0 {
				t.Fatalf("encaminhar %s: %d", tc.kind, code)
			}
			resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", f.Local))
			if err != nil {
				t.Fatalf("%s: %v", tc.kind, err)
			}
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 200 || !strings.Contains(string(b), "nginx") {
				t.Fatalf("%s: %d %.200s", tc.kind, resp.StatusCode, b)
			}
			a.call("DELETE", "/encaminhamentos/"+f.ID, nil, nil)
		}
	})

	t.Run("escalar, reiniciar e voltar versão", func(t *testing.T) {
		if code := a.call("POST", base+"/r/deployments/"+ns+"/web/escalar", map[string]any{"replicas": 2}, nil); code != 200 {
			t.Fatalf("escalar: %d", code)
		}
		waitFor("2 réplicas", 3*time.Minute, func() bool {
			n := 0
			for _, p := range pods() {
				if strings.HasPrefix(p.Name, "web-") && len(p.Cells) > 1 && p.Cells[1] == "Running" {
					n++
				}
			}
			return n == 2
		})
		if code := a.call("POST", base+"/r/deployments/"+ns+"/web/reiniciar", nil, nil); code != 200 {
			t.Fatalf("reiniciar: %d", code)
		}
		type rev struct {
			Number  int
			Current bool
		}
		var d struct{ Revisions []rev }
		waitFor("versão 2", 3*time.Minute, func() bool {
			a.call("GET", base+"/r/deployments/"+ns+"/web/detalhe", nil, &d)
			return len(d.Revisions) >= 2 && d.Revisions[0].Current && d.Revisions[0].Number == 2
		})
		if code := a.call("POST", base+"/r/deployments/"+ns+"/web/voltar", map[string]any{"revision": 1}, nil); code != 200 {
			t.Fatalf("voltar: %d", code)
		}
		waitFor("voltar para a versão 1 (vira a 3)", 3*time.Minute, func() bool {
			a.call("GET", base+"/r/deployments/"+ns+"/web/detalhe", nil, &d)
			return len(d.Revisions) >= 2 && d.Revisions[0].Current && d.Revisions[0].Number == 3
		})
	})

	t.Run("esvaziar e liberar o nó", func(t *testing.T) {
		var nodes struct{ Rows []row }
		a.call("GET", base+"/r/nodes", nil, &nodes)
		if len(nodes.Rows) == 0 {
			t.Fatal("sem nós")
		}
		node := nodes.Rows[0].Name
		if code := a.call("POST", base+"/r/nodes/-/"+node+"/esvaziar", nil, nil); code != 200 {
			t.Fatalf("esvaziar: %d", code)
		}
		// The pods of the deployment were evicted (the new ones wait: the
		// only node is cordoned); a pod without an owner is skipped by the
		// drain (nothing would recreate it), so falador keeps running.
		waitFor("pods do deployment despejados", 3*time.Minute, func() bool { return running("web-") == "" })
		if running("falador") == "" {
			t.Fatal("o drain não devia mexer num pod sem dono")
		}
		if code := a.call("POST", base+"/r/nodes/-/"+node+"/isolar", map[string]any{"on": false}, nil); code != 200 {
			t.Fatalf("liberar: %d", code)
		}
		waitFor("deployment de volta", 3*time.Minute, func() bool { return running("web-") != "" })
	})

	// Kept for the record of what was read (shows in -v).
	if testing.Verbose() {
		var b strings.Builder
		for _, p := range pods() {
			fmt.Fprintf(&b, "%s %v\n", p.Name, p.Cells)
		}
		t.Log(b.String())
	}
}
