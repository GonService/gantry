package server

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"gantry/internal/mcp"
)

// TestMCPAccess: the assistants' token reads what gantry mcp needs, changes
// only with writing on, never reaches Secrets nor other endpoints, and dies
// with the access.
func TestMCPAccess(t *testing.T) {
	e := newEnv(t)
	e.srv.SetAddr(strings.TrimPrefix(e.url, "http://"))
	if _, err := mcp.ReadInfo(e.dir); err == nil {
		t.Fatal("mcp.json with the access off")
	}
	var st map[string]any
	if c := e.call("PUT", "/mcp", map[string]any{"on": true}, &st); c != 200 || st["on"] != true {
		t.Fatalf("%d %v", c, st)
	}
	info, err := mcp.ReadInfo(e.dir)
	if err != nil || info.Token == "" || info.Token == e.srv.Token() || info.URL != e.url || info.Write {
		t.Fatalf("%+v %v", info, err)
	}
	if fi, _ := os.Stat(mcp.InfoFile(e.dir)); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mcp.json mode %v", fi.Mode())
	}
	req := func(method, path string) int {
		r, _ := http.NewRequest(method, e.url+"/api"+path, strings.NewReader("{}"))
		r.Header.Set("Authorization", "Bearer "+info.Token)
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode
	}
	for path, want := range map[string]int{
		"GET /hosts":                                    200,
		"GET /hosts/fake/labs":                          200,
		"GET /hosts/fake/labs/web/file":                 403, // the compose may hold passwords
		"GET /clusters/x/r/secrets":                     403,
		"GET /clusters/x/r/secrets/ns/a/dados":          403,
		"POST /hosts/fake/containers/web-app-1/restart": 403, // writing off
		"PUT /mcp":                            403,
		"DELETE /hosts/fake/containers/solto": 403,
	} {
		f := strings.SplitN(path, " ", 2)
		if got := req(f[0], f[1]); got != want {
			t.Errorf("%s: %d, want %d", path, got, want)
		}
	}
	// gantry mcp against the real API.
	s := &mcp.Server{Client: &mcp.Client{Base: info.URL, Token: info.Token}}
	var out strings.Builder
	in := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_labs","arguments":{"host":"fake"}}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"lab_status","arguments":{"host":"fake","lab":"web"}}}` + "\n"
	if err := s.Serve(context.Background(), strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "web-app-1") || !strings.Contains(out.String(), "1/2") || strings.Contains(out.String(), `"isError":true`) {
		t.Fatalf("mcp: %s", out.String())
	}
	// Writing on: the actions of the list pass.
	e.call("PUT", "/mcp", map[string]any{"on": true, "write": true}, nil)
	if info2, _ := mcp.ReadInfo(e.dir); !info2.Write || info2.Token != info.Token {
		t.Fatalf("%+v", info2)
	}
	if got := req("POST", "/hosts/fake/containers/web-app-1/restart"); got != 200 && got != 204 {
		t.Errorf("restart with writing on: %d", got)
	}
	// Off: the file goes and the token stops working.
	e.call("PUT", "/mcp", map[string]any{"on": false}, nil)
	if _, err := os.Stat(mcp.InfoFile(e.dir)); err == nil {
		t.Error("mcp.json stayed")
	}
	if got := req("GET", "/hosts"); got != 401 {
		t.Errorf("token after off: %d", got)
	}
}
