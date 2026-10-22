package kube

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// A fake API server: pod logs, and exec over WebSocket that echoes stdin.
func fakeAPI(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/namespaces/ns/pods/p/log", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, `{"message":"Unauthorized"}`, 401)
			return
		}
		fmt.Fprintf(w, "linha 1\nlinha 2 (%s)\n", r.URL.Query().Get("container"))
	})
	mux.HandleFunc("/api/v1/namespaces/ns/pods/p/exec", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" || r.URL.Query()["command"][0] != "/bin/sh" {
			http.Error(w, "nope", 403)
			return
		}
		key := r.Header.Get("Sec-WebSocket-Key")
		sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		hj, _ := w.(http.Hijacker)
		conn, brw, _ := hj.Hijack()
		defer conn.Close()
		fmt.Fprintf(brw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\nSec-WebSocket-Protocol: v4.channel.k8s.io\r\n\r\n", base64.StdEncoding.EncodeToString(sum[:]))
		brw.Flush()
		send := func(p []byte) { // server frames are not masked
			hdr := []byte{0x82, byte(len(p))}
			conn.Write(append(hdr, p...))
		}
		send(append([]byte{1}, []byte("pronto$ ")...))
		r2 := bufio.NewReader(brw)
		for {
			var h [2]byte
			if _, err := io.ReadFull(r2, h[:]); err != nil {
				return
			}
			n := int(h[1] & 0x7f)
			if n == 126 {
				var l [2]byte
				io.ReadFull(r2, l[:])
				n = int(binary.BigEndian.Uint16(l[:]))
			}
			var mask [4]byte
			io.ReadFull(r2, mask[:])
			p := make([]byte, n)
			io.ReadFull(r2, p)
			for i := range p {
				p[i] ^= mask[i%4]
			}
			if h[0]&0x0f == 8 {
				return
			}
			if len(p) > 0 && p[0] == 0 {
				send(append([]byte{1}, append([]byte("eco:"), p[1:]...)...))
			}
		}
	})
	return httptest.NewServer(mux)
}

func TestExecAndLogs(t *testing.T) {
	srv := fakeAPI(t)
	defer srv.Close()
	cfg, err := Parse([]byte(fmt.Sprintf(`apiVersion: v1
kind: Config
clusters: [{name: c, cluster: {server: %q}}]
users: [{name: u, user: {token: tok}}]
contexts: [{name: x, context: {cluster: c, user: u}}]
current-context: x
`, srv.URL)), "")
	if err != nil {
		t.Fatal(err)
	}
	k, err := cfg.NewClient("")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rc, err := k.Logs(ctx, "ns", "p", LogOptions{Container: "web", Tail: -1})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "linha 1\nlinha 2 (web)\n" {
		t.Fatalf("logs: %q", b)
	}
	ex, err := k.Exec(ctx, "ns", "p", "", []string{"/bin/sh"})
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()
	buf := make([]byte, 64)
	n, _ := ex.Read(buf)
	if string(buf[:n]) != "pronto$ " {
		t.Fatalf("prompt: %q", buf[:n])
	}
	ex.Write([]byte("ls\n"))
	ex.Resize(ctx, 80, 24)
	n, _ = ex.Read(buf)
	if !strings.HasPrefix(string(buf[:n]), "eco:ls") {
		t.Fatalf("eco: %q", buf[:n])
	}
}

func TestExtractAndContexts(t *testing.T) {
	cfg, err := Parse([]byte(`apiVersion: v1
kind: Config
clusters:
- {name: a, cluster: {server: "https://a:6443", certificate-authority-data: Zm9v}}
- {name: b, cluster: {server: "https://b"}}
users:
- {name: ua, user: {token: x}}
- name: ub
  user:
    exec: {command: gke-gcloud-auth-plugin, apiVersion: client.authentication.k8s.io/v1beta1}
contexts:
- {name: prod, context: {cluster: a, user: ua, namespace: app}}
- {name: gke, context: {cluster: b, user: ub}}
current-context: gke
`), "")
	if err != nil {
		t.Fatal(err)
	}
	cs := cfg.ContextList()
	if len(cs) != 2 || cs[0].Auth != "token" || cs[1].Auth != "plugin (gke-gcloud-auth-plugin)" || !cs[1].Current {
		t.Fatalf("%+v", cs)
	}
	one, err := cfg.Extract("prod")
	if err != nil {
		t.Fatal(err)
	}
	c2, err := Parse(one, "")
	if err != nil || len(c2.Contexts) != 1 || c2.CurrentContext != "prod" || c2.cluster("a").Server != "https://a:6443" {
		t.Fatalf("extract: %v\n%s", err, one)
	}
	if strings.Contains(string(one), "gke-gcloud") {
		t.Fatal("levou o outro contexto junto")
	}
}

func TestQuantities(t *testing.T) {
	if ParseCPU("250m") != 250 || ParseCPU("2") != 2000 || ParseCPU("1500000n") != 1 {
		t.Error("cpu")
	}
	if ParseMem("128Mi") != 128<<20 || ParseMem("1G") != 1e9 {
		t.Error("mem")
	}
}

// A CONNECT proxy in front of the fake API: the shell must go through it.
func TestExecThroughProxy(t *testing.T) {
	api := fakeAPI(t)
	defer api.Close()
	var connects int32
	var mu sync.Mutex
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect || r.Header.Get("Proxy-Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("eu:pw")) {
			http.Error(w, "nope", 407)
			return
		}
		mu.Lock()
		connects++
		mu.Unlock()
		up, err := net.Dial("tcp", r.Host)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		hj, _ := w.(http.Hijacker)
		c, brw, _ := hj.Hijack()
		brw.WriteString("HTTP/1.1 200 Connection established\r\n\r\n")
		brw.Flush()
		go func() { io.Copy(up, c); up.Close() }()
		io.Copy(c, up)
		c.Close()
	}))
	defer proxy.Close()
	purl := strings.Replace(proxy.URL, "http://", "http://eu:pw@", 1)
	cfg, err := Parse([]byte(fmt.Sprintf(`apiVersion: v1
kind: Config
clusters: [{name: c, cluster: {server: %q, proxy-url: %q}}]
users: [{name: u, user: {token: tok}}]
contexts: [{name: x, context: {cluster: c, user: u}}]
current-context: x
`, api.URL, purl)), "")
	if err != nil {
		t.Fatal(err)
	}
	k, err := cfg.NewClient("")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ex, err := k.Exec(ctx, "ns", "p", "", []string{"/bin/sh"})
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()
	buf := make([]byte, 64)
	n, _ := ex.Read(buf)
	if string(buf[:n]) != "pronto$ " {
		t.Fatalf("prompt: %q", buf[:n])
	}
	mu.Lock()
	defer mu.Unlock()
	if connects != 1 {
		t.Fatalf("o shell não passou pelo proxy (%d)", connects)
	}
}

// Many requests at once run the credential plugin once; a waiter whose
// context ends gives up without waiting for the plugin.
func TestExecTokenOnce(t *testing.T) {
	dir := t.TempDir()
	count := filepath.Join(dir, "n")
	plugin := filepath.Join(dir, "plugin.sh")
	os.WriteFile(plugin, []byte("#!/bin/sh\necho x >> "+count+"\nsleep 1\necho '{\"apiVersion\":\"client.authentication.k8s.io/v1beta1\",\"kind\":\"ExecCredential\",\"status\":{\"token\":\"tok\"}}'\n"), 0o755)
	cfg, err := Parse([]byte(fmt.Sprintf(`apiVersion: v1
kind: Config
clusters: [{name: c, cluster: {server: "http://127.0.0.1:1"}}]
users: [{name: u, user: {exec: {command: %q, apiVersion: client.authentication.k8s.io/v1beta1}}}]
contexts: [{name: x, context: {cluster: c, user: u}}]
current-context: x
`, plugin)), "")
	if err != nil {
		t.Fatal(err)
	}
	k, err := cfg.NewClient("")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok, err := k.execToken(context.Background())
			if err == nil && tok != "tok" {
				err = fmt.Errorf("token %q", tok)
			}
			errs <- err
		}()
	}
	time.Sleep(100 * time.Millisecond)
	short, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	start := time.Now()
	if _, err := k.execToken(short); err == nil || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("quem desiste não devia esperar o plugin: %v em %v", err, time.Since(start))
	}
	cancel()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(count)
	if n := strings.Count(string(b), "x"); n != 1 {
		t.Fatalf("o plugin rodou %d vezes", n)
	}
}

// The list of resources is reused, but a missing kind (a CRD installed
// just now) and an old list are looked up again.
func TestDiscoveryExpires(t *testing.T) {
	var hits int32
	var mu sync.Mutex
	kinds := []string{"Widget"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		hits++
		var rs []string
		for _, k := range kinds {
			rs = append(rs, fmt.Sprintf(`{"name":%q,"kind":%q,"namespaced":true}`, strings.ToLower(k)+"s", k))
		}
		fmt.Fprintf(w, `{"resources":[%s]}`, strings.Join(rs, ","))
	}))
	defer srv.Close()
	cfg, _ := Parse([]byte(fmt.Sprintf(`apiVersion: v1
kind: Config
clusters: [{name: c, cluster: {server: %q}}]
users: [{name: u, user: {token: t}}]
contexts: [{name: x, context: {cluster: c, user: u}}]
current-context: x
`, srv.URL)), "")
	k, _ := cfg.NewClient("")
	ctx := context.Background()
	get := func(kind string) error { _, err := k.resourceFor(ctx, "ex.io/v1", kind); return err }
	n := func() int32 { mu.Lock(); defer mu.Unlock(); return hits }
	if get("Widget") != nil || get("Widget") != nil || n() != 1 {
		t.Fatalf("devia buscar uma vez só (%d)", n())
	}
	mu.Lock()
	kinds = append(kinds, "Gadget")
	mu.Unlock()
	if get("Gadget") == nil || n() != 1 {
		t.Fatal("logo depois de buscar, um tipo que falta não busca de novo")
	}
	age := func(d time.Duration) {
		discMu.Lock()
		e := k.disc["ex.io/v1"]
		e.at = time.Now().Add(-d)
		k.disc["ex.io/v1"] = e
		discMu.Unlock()
	}
	age(discRetry + time.Second)
	if err := get("Gadget"); err != nil || n() != 2 {
		t.Fatalf("o tipo novo devia aparecer: %v (%d)", err, n())
	}
	age(discTTL + time.Second)
	if get("Widget") != nil || n() != 3 {
		t.Fatalf("a lista velha devia ser buscada de novo (%d)", n())
	}
}

// fakeWS upgrades a request and returns helpers to send and read frames.
func fakeWS(w http.ResponseWriter, r *http.Request, proto string) (send func([]byte), recv func() ([]byte, bool), closeFn func()) {
	key := r.Header.Get("Sec-WebSocket-Key")
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	hj, _ := w.(http.Hijacker)
	conn, brw, _ := hj.Hijack()
	fmt.Fprintf(brw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\nSec-WebSocket-Protocol: %s\r\n\r\n", base64.StdEncoding.EncodeToString(sum[:]), proto)
	brw.Flush()
	send = func(p []byte) {
		hdr := []byte{0x82}
		if len(p) < 126 {
			hdr = append(hdr, byte(len(p)))
		} else {
			hdr = append(hdr, 126, byte(len(p)>>8), byte(len(p)))
		}
		conn.Write(append(hdr, p...))
	}
	r2 := bufio.NewReader(brw)
	recv = func() ([]byte, bool) {
		var h [2]byte
		if _, err := io.ReadFull(r2, h[:]); err != nil {
			return nil, false
		}
		n := int(h[1] & 0x7f)
		if n == 126 {
			var l [2]byte
			io.ReadFull(r2, l[:])
			n = int(binary.BigEndian.Uint16(l[:]))
		}
		var mask [4]byte
		io.ReadFull(r2, mask[:])
		p := make([]byte, n)
		io.ReadFull(r2, p)
		for i := range p {
			p[i] ^= mask[i%4]
		}
		if h[0]&0x0f == 8 {
			return nil, false
		}
		return p, true
	}
	return send, recv, func() { conn.Close() }
}

func TestPortForward(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/namespaces/db/pods/pg-0/portforward" || r.URL.Query().Get("ports") != "5432" {
			http.NotFound(w, r)
			return
		}
		send, recv, closeFn := fakeWS(w, r, "v4.channel.k8s.io")
		defer closeFn()
		send([]byte{0, 0x38, 0x15}) // port 5432 little endian, data channel
		send([]byte{1, 0x38, 0x15}) // error channel
		for {
			p, ok := recv()
			if !ok {
				return
			}
			if len(p) > 0 && p[0] == 0 {
				if string(p[1:]) == "falha" {
					send(append([]byte{1}, []byte("connection refused")...))
					return
				}
				send(append([]byte{0}, append([]byte("pg:"), p[1:]...)...))
			}
		}
	}))
	defer srv.Close()
	cfg, _ := Parse([]byte(fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters: [{name: c, cluster: {server: %q}}]\nusers: [{name: u, user: {token: t}}]\ncontexts: [{name: x, context: {cluster: c, user: u}}]\ncurrent-context: x\n", srv.URL)), "")
	k, _ := cfg.NewClient("")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pc, err := k.PortForward(ctx, "db", "pg-0", 5432)
	if err != nil {
		t.Fatal(err)
	}
	pc.Write([]byte("SELECT 1"))
	buf := make([]byte, 64)
	n, err := pc.Read(buf)
	if err != nil || string(buf[:n]) != "pg:SELECT 1" {
		t.Fatalf("eco: %q %v", buf[:n], err)
	}
	pc.Write([]byte("falha"))
	if _, err := io.ReadAll(pc); err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("o erro do kubelet devia chegar: %v", err)
	}
	pc.Close()
}
