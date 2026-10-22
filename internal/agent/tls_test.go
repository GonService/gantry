package agent

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDirectTLS(t *testing.T) {
	// Unix socket paths are short on macOS; keep them out of long temp dirs.
	base, err := os.MkdirTemp("/tmp", "gat")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	t.Setenv("XDG_STATE_HOME", filepath.Join(base, "st"))
	t.Setenv("XDG_RUNTIME_DIR", base)

	// A fake engine socket that answers /_ping.
	engSock := filepath.Join(base, "e.sock")
	eln, err := net.Listen("unix", engSock)
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(eln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("OK-engine")) }))
	defer eln.Close()

	appCrt, appKey, err := NewCert("app")
	if err != nil {
		t.Fatal(err)
	}
	agentCrt, err := Pair(TLSDir(), appCrt, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	// Pairing twice keeps one copy of the app certificate.
	if _, err := Pair(TLSDir(), appCrt, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(TLSDir(), "clients.pem")); strings.Count(string(b), "BEGIN CERTIFICATE") != 1 {
		t.Errorf("clients.pem: %d certificados", strings.Count(string(b), "BEGIN CERTIFICATE"))
	}

	a := New(Options{Version: "t", Socket: filepath.Join(base, "a.sock"), Engine: engSock, Interval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx)
	// The agent opens the direct port after its first round of work; on a
	// busy machine (tests in parallel, -race) that takes a few seconds.
	var addr string
	for end := time.Now().Add(15 * time.Second); addr == "" && time.Now().Before(end); {
		time.Sleep(50 * time.Millisecond)
		addr = a.directAddr()
	}
	if addr == "" {
		t.Fatal("o agente não abriu a porta direta")
	}

	cfg, err := ClientTLS(appCrt, appKey, agentCrt)
	if err != nil {
		t.Fatal(err)
	}
	// Agent API over the direct connection.
	cl := NewClient(func(ctx context.Context) (net.Conn, error) { return DialDirect(ctx, addr, cfg, "", false) })
	info, err := cl.Info(ctx)
	if err != nil || info.Protocol != Protocol || info.Direct == "" || !info.VMs {
		t.Fatalf("info direta: %+v %v", info, err)
	}
	// Engine socket over the direct connection.
	c, err := DialDirect(ctx, addr, cfg, engSock, true)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Write([]byte("GET /_ping HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n"))
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	var body [64]byte
	n, _ := resp.Body.Read(body[:])
	c.Close()
	if !strings.Contains(string(body[:n]), "OK-engine") {
		t.Errorf("engine pela conexão direta: %q", body[:n])
	}

	// Another app (not paired) is refused.
	otherCrt, otherKey, _ := NewCert("intruso")
	bad, _ := ClientTLS(otherCrt, otherKey, agentCrt)
	if _, err := DialDirect(ctx, addr, bad, "", false); err == nil {
		t.Error("um app não pareado conseguiu conectar")
	}
	// An agent the app did not pair with is refused by the app.
	fakeAgent, _, _ := NewCert("falso")
	wrong, _ := ClientTLS(appCrt, appKey, fakeAgent)
	if _, err := DialDirect(ctx, addr, wrong, "", false); err == nil {
		t.Error("o app aceitou um agente com outro certificado")
	}
}
