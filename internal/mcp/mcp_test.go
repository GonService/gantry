package mcp

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeAPI answers like Gantry's API for the tools, and records the calls.
func fakeAPI(t *testing.T, calls *[]string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":"invalid session"}`)
			return
		}
		*calls = append(*calls, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		sse := func(text string) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\n", base64.StdEncoding.EncodeToString([]byte(text)))
			fmt.Fprintf(w, "event: end\ndata: {\"message\":\"end of log\"}\n\n")
		}
		switch p := r.URL.Path; {
		case p == "/api/hosts":
			io.WriteString(w, `[{"name":"docker","kind":"unix","runtime":"docker","ok":true},{"name":"srv","kind":"ssh","ok":false,"error":"timeout"}]`)
		case p == "/api/hosts/docker/labs":
			io.WriteString(w, `[{"id":"loja","name":"loja","workingDir":"/p/loja","running":1,"total":2,"containers":[{"id":"abc123456789","name":"loja-web-1","service":"web","image":"nginx","state":"running","status":"Up 2 hours"},{"id":"def","name":"loja-db-1","service":"db","image":"postgres","state":"exited"}]}]`)
		case p == "/api/hosts/docker/labs/loja/drift":
			io.WriteString(w, `{"services":[{"service":"web","kind":"service","state":"ok"},{"service":"db","kind":"service","state":"changed"}]}`)
		case p == "/api/hosts/docker/containers/abc123456789/logs":
			sse("GET / 200\nGET /x 404\n")
		case p == "/api/clusters":
			io.WriteString(w, `[{"id":"c1","name":"prod","context":"prod-ctx","mode":"leitura"}]`)
		case p == "/api/clusters/c1/r/pods":
			io.WriteString(w, `{"columns":["READY","STATUS","RESTARTS"],"rows":[{"name":"api-1","namespace":"loja","cells":["0/1","CrashLoopBackOff","12"],"problem":"CrashLoopBackOff"},{"name":"web-1","namespace":"loja","cells":["1/1","Running","0"]}]}`)
		case p == "/api/clusters/c1/r/pods/loja/api-1/detalhe":
			io.WriteString(w, `{"object":{"metadata":{"name":"api-1","managedFields":[{"manager":"x"}]}},"events":[{"name":"BackOff"}],"diagnosis":[{"severity":"err","title":"The container keeps dying"}]}`)
		case p == "/api/clusters/c1/varios/loja/deployments/api/pods":
			io.WriteString(w, `{"pods":[{"name":"api-1"},{"name":"api-2"}]}`)
		case strings.HasPrefix(p, "/api/clusters/c1/pods/loja/") && strings.HasSuffix(p, "/logs"):
			sse("panic: " + strings.Split(p, "/")[6] + "\n")
		case p == "/api/clusters/c1/helm/loja/web":
			io.WriteString(w, `{"release":{"history":[{"revision":2,"status":"deployed"}]}}`)
		case r.Method == http.MethodPost && p == "/api/hosts/docker/labs/loja/up":
			io.WriteString(w, `{"ok":true,"command":"docker compose up -d","output":"started"}`)
		case r.Method == http.MethodPost && p == "/api/clusters/c1/r/deployments/loja/api/escalar":
			w.WriteHeader(403)
			io.WriteString(w, `{"error":"cluster in read-only mode"}`)
		default:
			w.WriteHeader(404)
			io.WriteString(w, `{"error":"not found `+p+`"}`)
		}
	}))
}

// session runs the MCP server over the messages and returns the answers.
func session(t *testing.T, s *Server, msgs ...string) []map[string]any {
	t.Helper()
	var out strings.Builder
	if err := s.Serve(context.Background(), strings.NewReader(strings.Join(msgs, "\n")+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	var res []map[string]any
	sc := bufio.NewScanner(strings.NewReader(out.String()))
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("%v: %s", err, sc.Text())
		}
		res = append(res, m)
	}
	return res
}

func call(id int, name string, args string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, id, name, args)
}

func text(m map[string]any) (string, bool) {
	r, _ := m["result"].(map[string]any)
	c, _ := r["content"].([]any)
	if len(c) == 0 {
		return "", false
	}
	isErr, _ := r["isError"].(bool)
	return c[0].(map[string]any)["text"].(string), isErr
}

func TestMCPServer(t *testing.T) {
	var calls []string
	api := fakeAPI(t, &calls)
	defer api.Close()
	ro := &Server{Client: &Client{Base: api.URL, Token: "tok"}, Version: "test"}
	res := session(t, ro,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		call(3, "list_hosts", `{}`),
		call(4, "lab_status", `{"host":"docker","lab":"loja"}`),
		call(5, "container_logs", `{"host":"docker","container":"web","tail":50}`),
		call(6, "kube_get", `{"kind":"pod"}`),
		call(7, "kube_get", `{"cluster":"prod","kind":"pods","namespace":"loja","name":"api-1"}`),
		call(8, "kube_logs", `{"namespace":"loja","name":"api","kind":"deploy"}`),
		call(9, "kube_get", `{"kind":"secrets"}`),
		call(10, "lab_up", `{"host":"docker","lab":"loja"}`),
		call(11, "helm_release", `{"namespace":"loja","name":"web"}`),
	)
	if len(res) != 11 {
		t.Fatalf("%d answers (the notification gets none): %v", len(res), res)
	}
	if v := res[0]["result"].(map[string]any)["protocolVersion"]; v != "2025-06-18" {
		t.Fatal(v)
	}
	var names []string
	for _, x := range res[1]["result"].(map[string]any)["tools"].([]any) {
		names = append(names, x.(map[string]any)["name"].(string))
	}
	if strings.Contains(strings.Join(names, " "), "lab_up") || !strings.Contains(strings.Join(names, " "), "kube_get") {
		t.Fatalf("read-only tools: %v", names)
	}
	want := map[int]string{
		2:  "not connected: timeout",
		3:  "differs from the compose file",
		4:  "GET /x 404",
		5:  "CrashLoopBackOff",
		6:  "The container keeps dying",
		7:  "=== api-2 ===",
		10: `"revision": 2`,
	}
	for i, w := range want {
		got, isErr := text(res[i])
		if isErr || !strings.Contains(strings.ToLower(got), strings.ToLower(w)) {
			t.Errorf("answer %d: want %q in %q (err %v)", i, w, got, isErr)
		}
	}
	if got, _ := text(res[6]); strings.Contains(got, "managedFields") {
		t.Error("managedFields went out")
	}
	if got, isErr := text(res[8]); !isErr || !strings.Contains(got, "Secrets") {
		t.Errorf("secrets: %q %v", got, isErr)
	}
	if got, isErr := text(res[9]); !isErr || !strings.Contains(got, "read-only") {
		t.Errorf("write with access read-only: %q %v", got, isErr)
	}
	for _, c := range calls {
		if strings.HasPrefix(c, "POST") || strings.Contains(c, "secrets") {
			t.Errorf("read-only session called %s", c)
		}
	}
	if !strings.Contains(strings.Join(calls, "\n"), "/containers/abc123456789/logs?follow=0&tail=50") {
		t.Errorf("logs call: %v", calls)
	}

	// With writing on: the tools are there, and a read-only cluster refuses.
	calls = nil
	rw := &Server{Client: &Client{Base: api.URL, Token: "tok"}, Write: true}
	res = session(t, rw, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, call(2, "lab_up", `{"host":"docker","lab":"loja"}`), call(3, "kube_scale", `{"kind":"deploy","namespace":"loja","name":"api","replicas":3}`))
	if n := len(res[0]["result"].(map[string]any)["tools"].([]any)); n != len(allTools()) {
		t.Fatalf("%d tools with writing on", n)
	}
	if got, isErr := text(res[1]); isErr || !strings.Contains(got, "up done") {
		t.Errorf("lab_up: %q", got)
	}
	if got, isErr := text(res[2]); !isErr || !strings.Contains(got, "read-only") {
		t.Errorf("scale on a read-only cluster: %q %v", got, isErr)
	}
	// A wrong token: the error of Gantry, not a crash.
	bad := &Server{Client: &Client{Base: api.URL, Token: "x"}}
	if got, isErr := text(session(t, bad, call(1, "list_hosts", `{}`))[0]); !isErr || !strings.Contains(got, "invalid session") {
		t.Errorf("bad token: %q", got)
	}
}
