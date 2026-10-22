package engine

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"testing"
)

// Podman answers 500 to the "all" volume filter; prune must retry without it.
func TestPruneVolumesPodman(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "p.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.RawQuery)
		if r.URL.Query().Get("filters") != "" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"cause":"invalid filter","message":"failed to parse filters for all=true: \"all\" is an invalid volume filter","response":500}`))
			return
		}
		_, _ = w.Write([]byte(`{"VolumesDeleted":["a","b"],"SpaceReclaimed":10}`))
	})}
	go srv.Serve(ln)
	defer srv.Close()
	c := New(func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	})
	freed, n, err := c.Prune(context.Background(), "volumes")
	if err != nil || n != 2 || freed != 10 || len(calls) != 2 {
		t.Fatalf("freed=%d n=%d err=%v calls=%v", freed, n, err, calls)
	}
}

// Pull progress adds up the layers: bytes downloaded of the bytes known.
func TestPullWatch(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "p.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, l := range []string{
			`{"status":"Pulling from library/x","id":"1"}`,
			`{"status":"Pulling fs layer","id":"aaaaaaaaaaaa"}`,
			`{"status":"Pulling fs layer","id":"bbbbbbbbbbbb"}`,
			`{"status":"Downloading","progressDetail":{"current":50,"total":100},"progress":"[==>  ]","id":"aaaaaaaaaaaa"}`,
			`{"status":"Downloading","progressDetail":{"current":100,"total":300},"progress":"[=>   ]","id":"bbbbbbbbbbbb"}`,
			`{"status":"Download complete","id":"aaaaaaaaaaaa"}`,
		} {
			_, _ = w.Write([]byte(l + "\n"))
		}
	})}
	go srv.Serve(ln)
	defer srv.Close()
	c := New(func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	})
	var last PullState
	var lines []string
	if err := c.PullWatch(context.Background(), "x:1", func(l string) { lines = append(lines, l) }, func(s PullState) { last = s }); err != nil {
		t.Fatal(err)
	}
	if last.Total != 400 || last.Current != 200 || last.Layers != 2 || last.Done != 1 {
		t.Fatalf("estado final: %+v", last)
	}
	for _, l := range lines {
		if l == "aaaaaaaaaaaa Downloading" {
			t.Errorf("linhas de barra de progresso não deviam aparecer: %v", lines)
		}
	}
}

func TestDemux(t *testing.T) {
	frame := func(s byte, b string) []byte {
		n := len(b)
		return append([]byte{s, 0, 0, 0, byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}, b...)
	}
	raw := append(frame(1, "ola "), frame(2, "erro\n")...)
	if got := demux(raw); got != "ola erro\n" {
		t.Errorf("demux = %q", got)
	}
	if got := demux([]byte("texto puro")); got != "texto puro" {
		t.Errorf("sem frames = %q", got)
	}
}

func TestSharedNetworks(t *testing.T) {
	var cs []Container
	js := `[{"Id":"9c0c1bddb3ce55b8","Names":["/pA"],"Ports":[{"PrivatePort":80,"PublicPort":18081,"Type":"tcp"}],"NetworkSettings":{"Networks":{"podman":{"IPAddress":"10.88.0.4","Aliases":["9c0c1bddb3ce","web"]}}}},
	{"Id":"e42841a20619333d","Names":["/pB"],"Ports":[{"PrivatePort":80,"PublicPort":18081,"Type":"tcp"}],"NetworkSettings":{"Networks":{"podman":{"IPAddress":"10.88.0.4","Aliases":["9c0c1bddb3ce","web"]}}}},
	{"Id":"aaaaaaaaaaaa1111","Names":["/d"],"HostConfig":{"NetworkMode":"container:9c0c1bddb3ce55b8"},"Ports":[{"PrivatePort":80,"PublicPort":18081,"Type":"tcp"}]}]`
	if err := json.Unmarshal([]byte(js), &cs); err != nil {
		t.Fatal(err)
	}
	sharedNetworks(cs)
	if len(cs[0].Ports) != 1 || len(cs[1].Ports) != 0 || len(cs[2].Ports) != 0 || cs[1].HostConfig.NetworkMode != "container:9c0c1bddb3ce55b8" {
		t.Fatalf("%+v", cs)
	}
}
