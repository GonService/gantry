package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// sseText reads an SSE answer of a log: the lines (base64) and the end.
func (e *env) sseText(path string) (string, string) {
	e.t.Helper()
	req, _ := http.NewRequest("GET", e.url+"/api"+path, nil)
	req.Header.Set("Authorization", "Bearer "+e.srv.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var text, end strings.Builder
	event := ""
	for _, l := range strings.Split(string(b), "\n") {
		switch {
		case strings.HasPrefix(l, "event: "):
			event = strings.TrimPrefix(l, "event: ")
		case strings.HasPrefix(l, "data: "):
			d := strings.TrimPrefix(l, "data: ")
			if event == "end" {
				var m map[string]string
				json.Unmarshal([]byte(d), &m)
				end.WriteString(m["message"])
			} else {
				raw, _ := base64.StdEncoding.DecodeString(d)
				text.Write(raw)
			}
		case l == "":
			event = ""
		}
	}
	return text.String(), end.String()
}

// Reiniciar recreates the pods and the cluster forgets their logs: Gantry
// saves them first and shows them under the workload's other pods.
func TestPodLogsSurviveRestart(t *testing.T) {
	e := newEnv(t)
	var gone, patched atomic.Bool
	pod := `{"metadata":{"name":"web-7f9c-x1","namespace":"app","uid":"u1","labels":{"pod-template-hash":"7f9c"},
	  "ownerReferences":[{"kind":"ReplicaSet","name":"web-7f9c","controller":true}]},
	  "spec":{"nodeName":"n1","containers":[{"name":"app"}]},
	  "status":{"phase":"Running","containerStatuses":[{"name":"app","restartCount":1,"state":{"running":{}}}]}}`
	other := `{"metadata":{"name":"db-0","namespace":"app","uid":"u2","ownerReferences":[{"kind":"StatefulSet","name":"db"}]},
	  "spec":{"containers":[{"name":"db"}]},"status":{"containerStatuses":[{"name":"db","state":{"running":{}}}]}}`
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch p := r.URL.Path; {
		case p == "/api/v1/namespaces/app/pods":
			fmt.Fprintf(w, `{"items":[%s,%s]}`, pod, other)
		case p == "/api/v1/namespaces/app/pods/web-7f9c-x1" && !gone.Load():
			fmt.Fprint(w, pod)
		case p == "/api/v1/namespaces/app/pods/web-7f9c-x1/log" && !gone.Load():
			if r.URL.Query().Get("container") != "app" || r.URL.Query().Get("timestamps") != "true" {
				http.Error(w, "parâmetros", 400)
				return
			}
			if r.URL.Query().Get("previous") == "true" {
				fmt.Fprint(w, "2026-10-15T10:00:00.123456789Z panic: caiu\n")
				return
			}
			fmt.Fprint(w, "2026-10-15T10:00:01.5Z servindo\n2026-10-15T10:00:02Z GET /saude\n")
		case p == "/apis/apps/v1/namespaces/app/deployments/web" && r.Method == "PATCH":
			patched.Store(true)
			fmt.Fprint(w, `{}`)
		case p == "/api/v1/namespaces/app/pods/db-0/log":
			t.Error("salvou um pod de outro workload")
		default:
			w.WriteHeader(404)
			fmt.Fprintf(w, `{"kind":"Status","code":404,"message":"pods %q not found"}`, filepath.Base(p))
		}
	}))
	defer api.Close()
	kc := filepath.Join(e.dir, "kc.yaml")
	os.WriteFile(kc, []byte("apiVersion: v1\nkind: Config\nclusters: [{name: c, cluster: {server: \""+api.URL+"\"}}]\nusers: [{name: u, user: {token: t}}]\ncontexts: [{name: k, context: {cluster: c, user: u}}]\ncurrent-context: k\n"), 0o600)
	b, _ := json.Marshal([]clusterRec{{ID: "c1", Name: "k", Source: "kubeconfig", File: kc, Context: "k", Mode: "livre"}})
	os.WriteFile(e.srv.clustersFile(), b, 0o600)

	if code := e.call("POST", "/clusters/c1/r/deployments/app/web/reiniciar", map[string]any{}, nil); code != 200 || !patched.Load() {
		t.Fatalf("reiniciar: %d", code)
	}
	gone.Store(true) // the rollout replaced the pod

	// The new pod's (or the old one's) log: a clear word, not the API's.
	_, end := e.sseText("/clusters/c1/pods/app/web-7f9c-x1/logs")
	if !strings.Contains(end, "Pods anteriores") {
		t.Fatalf("fim: %q", end)
	}
	var list struct {
		Workload string
		Pods     []savedPod
	}
	e.call("GET", "/clusters/c1/podlogs?ns=app&pod=web-7f9c-x1", nil, &list)
	if list.Workload != "Deployment/web" || len(list.Pods) != 1 || len(list.Pods[0].Containers) != 2 || list.Pods[0].Node != "n1" {
		t.Fatalf("%+v", list)
	}
	text, end := e.sseText("/clusters/c1/podlogs/app/web-7f9c-x1/log?container=app")
	if text != "servindo\nGET /saude\n" || !strings.Contains(end, "guardado pelo Gantry") {
		t.Fatalf("%q %q", text, end)
	}
	text, _ = e.sseText("/clusters/c1/podlogs/app/web-7f9c-x1/log?container=app&previous=1&timestamps=1")
	if !strings.HasPrefix(text, "2026-10-15T10:00:00.123456789Z panic: caiu") {
		t.Fatalf("%q", text)
	}
	text, _ = e.sseText("/clusters/c1/podlogs/app/web-7f9c-x1/log?container=app&grep=SAUDE")
	if text != "GET /saude\n" {
		t.Fatalf("filtro: %q", text)
	}
	// A name with a path in it reads nothing outside the folder.
	if _, end := e.sseText("/clusters/c1/podlogs/..%2F..%2F/x/log"); !strings.Contains(end, "não guardou") {
		t.Fatalf("%q", end)
	}
	// A week later it is gone.
	dir := filepath.Join(e.srv.podLogDir("c1"), "app", "web-7f9c-x1")
	var sp savedPod
	raw, _ := os.ReadFile(filepath.Join(dir, "pod.json"))
	json.Unmarshal(raw, &sp)
	sp.Saved = time.Now().Add(-8 * 24 * time.Hour).UnixMilli()
	raw, _ = json.Marshal(sp)
	os.WriteFile(filepath.Join(dir, "pod.json"), raw, 0o600)
	e.srv.prunePodLogs()
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("guardou mais de uma semana")
	}
}
