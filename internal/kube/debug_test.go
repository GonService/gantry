package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDebugContainer(t *testing.T) {
	debugWait = 3 * time.Second
	var mu sync.Mutex
	var eph []map[string]any
	pull := "running"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/api/v1/namespaces/app/pods/web-1" && r.Method == http.MethodGet:
			var sts []string
			for _, e := range eph {
				state := `{"running":{}}`
				if pull != "running" {
					state = `{"waiting":{"reason":"` + pull + `","message":"not found"}}`
				}
				sts = append(sts, fmt.Sprintf(`{"name":%q,"state":%s}`, e["name"], state))
			}
			b, _ := json.Marshal(map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "app"}}, "ephemeralContainers": eph}})
			s := strings.TrimSuffix(string(b), "}") + `,"status":{"phase":"Running","ephemeralContainerStatuses":[` + strings.Join(sts, ",") + `]}}`
			io.WriteString(w, s)
		case r.URL.Path == "/api/v1/namespaces/app/pods/web-1/ephemeralcontainers" && r.Method == http.MethodPatch:
			if r.Header.Get("Content-Type") != "application/strategic-merge-patch+json" {
				http.Error(w, "tipo", 415)
				return
			}
			var p struct {
				Spec struct {
					EphemeralContainers []map[string]any `json:"ephemeralContainers"`
				} `json:"spec"`
			}
			json.NewDecoder(r.Body).Decode(&p)
			eph = append(eph, p.Spec.EphemeralContainers...)
			io.WriteString(w, `{}`)
		case r.URL.Path == "/api/v1/namespaces/old/pods/x/ephemeralcontainers":
			w.WriteHeader(404)
			io.WriteString(w, `{"kind":"Status","code":404,"message":"the server could not find the requested resource"}`)
		case r.URL.Path == "/api/v1/namespaces/old/pods/x":
			io.WriteString(w, `{"status":{"phase":"Running"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	k := &Client{Server: srv.URL, http: srv.Client(), token: "t"}
	ctx := context.Background()
	name, err := k.AddDebugContainer(ctx, "app", "web-1", "app", "")
	if err != nil || name != "gantry-debug-1" {
		t.Fatal(name, err)
	}
	if eph[0]["image"] != "nicolaka/netshoot" || eph[0]["targetContainerName"] != "app" || eph[0]["tty"] != true || eph[0]["stdin"] != true {
		t.Fatalf("%v", eph[0])
	}
	// A second one gets the next name.
	if name, err = k.AddDebugContainer(ctx, "app", "web-1", "", "busybox"); err != nil || name != "gantry-debug-2" {
		t.Fatal(name, err)
	}
	list, _ := k.DebugContainers(ctx, "app", "web-1")
	if len(list) != 2 || !list[1].Running || list[1].Image != "busybox" {
		t.Fatalf("%+v", list)
	}
	mu.Lock()
	pull = "ErrImagePull"
	mu.Unlock()
	if _, err := k.AddDebugContainer(ctx, "app", "web-1", "", "nao/existe"); err == nil || !strings.Contains(err.Error(), "baixar a imagem nao/existe") {
		t.Fatal(err)
	}
	if _, err := k.AddDebugContainer(ctx, "old", "x", "", ""); err == nil || !strings.Contains(err.Error(), "1.25") {
		t.Fatal(err)
	}
	if _, err := k.AddDebugContainer(ctx, "app", "web-1", "", "a b"); err == nil {
		t.Fatal("imagem com espaço")
	}
}
