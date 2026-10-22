//go:build e2e

// Package e2e runs Gantry's API against the real engines of this machine:
// every engine the app finds (Docker, Podman, containerd)
// goes through the same lab life cycle. Run with `make e2e`.
//
// Environment:
//
//	GANTRY_E2E_IMAGE    image for the test services (default alpine:3.20)
//	GANTRY_E2E_NETWORK  "host" to use the host network (no CNI, sandboxes)
//	GANTRY_E2E_ONLY     comma-separated host names to test (default: all)
//	GANTRY_E2E_SSH      ssh://… host to test too (key or agent auth)
//	GANTRY_E2E_VM_IMAGE image for the VM test (debian-12, a path or a URL)
package e2e

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"gantry/internal/config"
	"gantry/internal/engine"
	"gantry/internal/labs"
	"gantry/internal/server"
)

type app struct {
	t   *testing.T
	srv *server.Server
	url string
}

func start(t *testing.T) *app {
	dir := t.TempDir()
	cfg, err := config.Load(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if ssh := os.Getenv("GANTRY_E2E_SSH"); ssh != "" {
		cfg.Hosts = append(cfg.Hosts, config.Host{Name: "e2e-ssh", URL: ssh, Source: "config"})
	}
	s := server.New(cfg, labs.OpenStore(filepath.Join(dir, "state.json")), fstest.MapFS{}, true)
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(func() { hs.Close(); s.Close() })
	return &app{t: t, srv: s, url: hs.URL}
}

func (a *app) call(method, path string, body, out any) int {
	a.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, a.url+"/api"+path, rd)
	req.Header.Set("Authorization", "Bearer "+a.srv.Token())
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 15 * time.Minute}).Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if out != nil && len(b) > 0 {
		_ = json.Unmarshal(b, out)
	}
	if resp.StatusCode >= 400 {
		a.t.Logf("%s %s → %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return resp.StatusCode
}

type host struct {
	Name, URL, Kind, Runtime, Error string
	Idle                            bool
	OK, Files, Connecting           bool
}

func (a *app) hosts() []host {
	var hs []host
	for i := 0; i < 30; i++ {
		hs = nil
		a.call("GET", "/hosts", nil, &hs)
		busy := false
		for _, h := range hs {
			busy = busy || h.Connecting
			if h.Idle && selected(h.Name) {
				// Remote hosts connect only when opened.
				a.call("GET", "/hosts/"+urlPath(h.Name), nil, nil)
				busy = true
			}
		}
		if !busy {
			break
		}
		time.Sleep(2 * time.Second)
	}
	return hs
}

func (a *app) lab(hostName, id string) (labs.Lab, bool) {
	var ls []labs.Lab
	a.call("GET", "/hosts/"+hostName+"/labs", nil, &ls)
	for _, l := range ls {
		if l.ID == id {
			return l, true
		}
	}
	return labs.Lab{}, false
}

func (a *app) waitLab(hostName, id string, ok func(labs.Lab) bool) labs.Lab {
	a.t.Helper()
	var l labs.Lab
	for i := 0; i < 40; i++ {
		l, _ = a.lab(hostName, id)
		if ok(l) {
			return l
		}
		time.Sleep(500 * time.Millisecond)
	}
	a.t.Fatalf("lab %s em %s não chegou ao estado esperado: %+v", id, hostName, l)
	return l
}

// sse reads a stream until marker appears or time runs out.
func (a *app) sse(path, marker string, d time.Duration) string {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", a.url+"/api"+path, nil)
	req.Header.Set("Authorization", "Bearer "+a.srv.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var b strings.Builder
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			if dec, err := base64.StdEncoding.DecodeString(data); err == nil {
				b.Write(dec)
			}
			if strings.Contains(b.String(), marker) {
				break
			}
		}
	}
	return b.String()
}

func image() string {
	if i := os.Getenv("GANTRY_E2E_IMAGE"); i != "" {
		return i
	}
	return "docker.io/library/alpine:3.20"
}

func netMode() string {
	if os.Getenv("GANTRY_E2E_NETWORK") == "host" {
		return "    network_mode: host\n"
	}
	return ""
}

func selected(name string) bool {
	only := os.Getenv("GANTRY_E2E_ONLY")
	if only == "" {
		return true
	}
	for _, n := range strings.Split(only, ",") {
		if strings.TrimSpace(n) == name {
			return true
		}
	}
	return false
}

// labDir writes a compose lab where the host can read it: locally, or on
// the server through ssh.
func labDir(t *testing.T, h host, name, compose string) string {
	if h.Kind == "ssh" {
		dir := "/tmp/gantry-e2e/" + name
		dest := strings.TrimPrefix(h.URL, "ssh://")
		cmd := exec.Command("ssh", "-o", "BatchMode=yes", dest, "mkdir -p "+dir+" && cat > "+dir+"/compose.yaml")
		cmd.Stdin = strings.NewReader(compose)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("ssh: %v %s", err, out)
		}
		return dir
	}
	dir := filepath.Join(t.TempDir(), name)
	_ = os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestLabLifecycle runs a lab through every engine found.
func TestLabLifecycle(t *testing.T) {
	a := start(t)
	hs := a.hosts()
	if len(hs) == 0 {
		t.Skip("nenhum engine encontrado nesta máquina")
	}
	for _, h := range hs {
		h := h
		t.Run(h.Name, func(t *testing.T) {
			if !selected(h.Name) {
				t.Skip("fora de GANTRY_E2E_ONLY")
			}
			if !h.OK {
				t.Skipf("host sem conexão: %s", h.Error)
			}
			if !h.Files {
				t.Skip("host sem compose (tcp://)")
			}
			a.t = t
			lifecycle(t, a, h)
		})
	}
}

func lifecycle(t *testing.T, a *app, h host) {
	name := fmt.Sprintf("e2e%d", time.Now().UnixNano()%1e6)
	compose := "services:\n  app:\n    image: " + image() + "\n    command: [\"sh\", \"-c\", \"echo pronto-" + name + "; sleep 100000\"]\n" + netMode()
	dir := labDir(t, h, name, compose)
	hn := urlPath(h.Name)
	t.Cleanup(func() { a.call("POST", "/hosts/"+hn+"/labs/"+name+"/down", nil, nil) })

	var r map[string]any
	if c := a.call("POST", "/hosts/"+hn+"/open", map[string]string{"dir": dir}, &r); c != 200 || r["ok"] != true {
		t.Fatalf("subir: %d %v", c, r)
	}
	l := a.waitLab(hn, name, func(l labs.Lab) bool { return l.Running == 1 })
	id := l.Containers[0].ID

	logs := a.sse("/hosts/"+hn+"/containers/"+id+"/logs", "pronto-"+name, 20*time.Second)
	if !strings.Contains(logs, "pronto-"+name) {
		t.Errorf("logs sem a linha esperada: %q", logs)
	}

	var term struct{ ID string }
	if c := a.call("POST", "/hosts/"+hn+"/containers/"+id+"/term", nil, &term); c != 200 {
		t.Fatalf("terminal: %d", c)
	}
	got := make(chan string)
	go func() { got <- a.sse("/term/"+term.ID+"/stream", "E2E_OK", 20*time.Second) }()
	time.Sleep(time.Second)
	req, _ := http.NewRequest("POST", a.url+"/api/term/"+term.ID+"/input", strings.NewReader("echo E2E_$((1))OK\n"))
	req.Header.Set("Authorization", "Bearer "+a.srv.Token())
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
	}
	if out := <-got; !strings.Contains(out, "E2E_1OK") {
		t.Errorf("terminal: %q", out)
	}

	var st map[string]map[string]any
	a.call("GET", "/hosts/"+hn+"/stats", nil, &st)
	if _, ok := st[id]; !ok {
		t.Errorf("sem estatísticas do container %s", id)
	}

	var topo map[string]any
	if c := a.call("GET", "/hosts/"+hn+"/labs/"+name+"/topology", nil, &topo); c != 200 {
		t.Errorf("topologia: %d", c)
	}

	if c := a.call("POST", "/hosts/"+hn+"/labs/"+name+"/stop", nil, &r); c != 200 || r["ok"] != true {
		t.Fatalf("parar: %v", r)
	}
	a.waitLab(hn, name, func(l labs.Lab) bool { return l.Total == 1 && l.Running == 0 })
	if c := a.call("POST", "/hosts/"+hn+"/containers/"+id+"/start", nil, nil); c != 200 {
		t.Fatalf("iniciar container: %d", c)
	}
	a.waitLab(hn, name, func(l labs.Lab) bool { return l.Running == 1 })
	if c := a.call("POST", "/hosts/"+hn+"/labs/"+name+"/down", nil, &r); c != 200 || r["ok"] != true {
		t.Fatalf("remover: %v", r)
	}
	a.waitLab(hn, name, func(l labs.Lab) bool { return l.Total == 0 })
}

func urlPath(s string) string { return strings.ReplaceAll(s, " ", "%20") }

// TestMixedLab needs Docker and Podman on this machine.
func TestMixedLab(t *testing.T) {
	a := start(t)
	var dockerHost, podmanHost string
	for _, h := range a.hosts() {
		if !h.OK || h.Kind != "unix" {
			continue
		}
		switch h.Runtime {
		case "docker":
			if dockerHost == "" {
				dockerHost = h.Name
			}
		case "podman":
			if podmanHost == "" {
				podmanHost = h.Name
			}
		}
	}
	if dockerHost == "" || podmanHost == "" {
		t.Skip("precisa de Docker e Podman locais")
	}
	name := fmt.Sprintf("e2emix%d", time.Now().UnixNano()%1e6)
	compose := "x-gantry:\n  runtime: podman\nservices:\n  db:\n    image: " + image() + "\n    command: [\"sleep\", \"100000\"]\n" + netMode() +
		"  app:\n    image: " + image() + "\n    command: [\"sleep\", \"100000\"]\n    depends_on: [db]\n" + netMode() + "    x-gantry:\n      runtime: docker\n"
	dir := labDir(t, host{}, name, compose)
	hn := urlPath(dockerHost)
	t.Cleanup(func() { a.call("POST", "/hosts/"+hn+"/labs/"+name+"/down", nil, nil) })
	var r map[string]any
	if c := a.call("POST", "/hosts/"+hn+"/open", map[string]string{"dir": dir}, &r); c != 200 || r["ok"] != true {
		t.Fatalf("subir: %v", r)
	}
	l := a.waitLab(hn, name, func(l labs.Lab) bool { return l.Running == 2 })
	rts := map[string]string{}
	for _, c := range l.Containers {
		rts[c.Service] = c.Runtime
	}
	if rts["db"] != "podman" || rts["app"] != "docker" {
		t.Errorf("runtimes %v", rts)
	}
	if l2, _ := a.lab(urlPath(podmanHost), name); l2.Total != 2 {
		t.Errorf("o lab deveria aparecer inteiro no host do Podman: %+v", l2)
	}
	if c := a.call("POST", "/hosts/"+hn+"/labs/"+name+"/down", nil, &r); c != 200 || r["ok"] != true {
		t.Fatalf("remover: %v", r)
	}
}

// TestVM needs QEMU and GANTRY_E2E_VM_IMAGE.
func TestVM(t *testing.T) {
	img := os.Getenv("GANTRY_E2E_VM_IMAGE")
	if img == "" {
		t.Skip("defina GANTRY_E2E_VM_IMAGE (debian-12, um caminho ou uma URL)")
	}
	a := start(t)
	var hn string
	for _, h := range a.hosts() {
		if h.OK && h.Kind == "unix" {
			hn = urlPath(h.Name)
			break
		}
	}
	if hn == "" {
		t.Skip("nenhum host local")
	}
	name := fmt.Sprintf("e2evm%d", time.Now().UnixNano()%1e6)
	dir := labDir(t, host{}, name, "x-vms:\n  web:\n    image: "+img+"\n    memory: 512\n    ports: [\"18089:80\"]\n")
	t.Cleanup(func() { a.call("POST", "/hosts/"+hn+"/labs/"+name+"/down", nil, nil) })
	var r map[string]any
	if c := a.call("POST", "/hosts/"+hn+"/open", map[string]string{"dir": dir}, &r); c != 200 || r["ok"] != true {
		t.Fatalf("subir: %v", r)
	}
	l := a.waitLab(hn, name, func(l labs.Lab) bool { return len(l.VMs) == 1 && l.VMs[0].State == "running" })
	if l.VMs[0].SSHPort == 0 {
		t.Error("sem porta SSH")
	}
	out := a.sse("/hosts/"+hn+"/labs/"+name+"/vms/web/console", "\n", 30*time.Second)
	if out == "" {
		t.Error("console vazio")
	}
	if c := a.call("POST", "/hosts/"+hn+"/labs/"+name+"/vms/web/stop", nil, &r); c != 200 || r["ok"] != true {
		t.Errorf("parar VM: %v", r)
	}
	if c := a.call("POST", "/hosts/"+hn+"/labs/"+name+"/down", nil, &r); c != 200 || r["ok"] != true {
		t.Fatalf("remover: %v", r)
	}
	a.waitLab(hn, name, func(l labs.Lab) bool { return len(l.VMs) == 1 && l.VMs[0].State == "missing" })
}

// TestCreate covers what the "Novo lab" and "Novo container" screens do,
// on each local engine.
func TestCreate(t *testing.T) {
	a := start(t)
	for _, h := range a.hosts() {
		h := h
		if !h.OK || h.Kind != "unix" || !selected(h.Name) {
			continue
		}
		t.Run(h.Name, func(t *testing.T) {
			a.t = t
			hn := urlPath(h.Name)
			sfx := fmt.Sprintf("%d", time.Now().UnixNano()%1e6)
			vol, net, ctr, lab := "e2ev"+sfx, "e2en"+sfx, "e2ec"+sfx, "e2elab"+sfx
			if c := a.call("POST", "/hosts/"+hn+"/res/volumes", map[string]string{"name": vol}, nil); c != 200 {
				t.Fatalf("volume: %d", c)
			}
			t.Cleanup(func() { a.call("DELETE", "/hosts/"+hn+"/res/volumes/"+vol, nil, nil) })
			if c := a.call("POST", "/hosts/"+hn+"/res/networks", map[string]string{"name": net}, nil); c != 200 {
				t.Fatalf("rede: %d", c)
			}
			t.Cleanup(func() { a.call("DELETE", "/hosts/"+hn+"/res/networks/"+net, nil, nil) })
			spec := map[string]any{"name": ctr, "image": image(), "cmd": []string{"sleep", "100000"}, "volumes": []string{vol + ":/data"}, "start": true}
			if os.Getenv("GANTRY_E2E_NETWORK") == "host" {
				spec["network"] = "host"
			}
			var cr map[string]any
			if c := a.call("POST", "/hosts/"+hn+"/containers", spec, &cr); c != 200 || cr["ok"] != true {
				t.Fatalf("container: %d %v", c, cr)
			}
			t.Cleanup(func() { a.call("DELETE", "/hosts/"+hn+"/containers/"+ctr, nil, nil) })
			a.waitLab(hn, labs.Loose, func(l labs.Lab) bool {
				for _, c := range l.Containers {
					if c.Name == ctr && c.State == "running" {
						return true
					}
				}
				return false
			})
			dir := filepath.Join(t.TempDir(), lab)
			compose := "name: " + lab + "\nservices:\n  app:\n    image: " + image() + "\n    command: [\"sleep\", \"100000\"]\n" + netMode() + "    volumes: [\"dados:/d\"]\nvolumes:\n  dados: {}\n"
			var r map[string]any
			if c := a.call("POST", "/hosts/"+hn+"/labs", map[string]any{"name": lab, "dir": dir, "compose": compose, "up": true}, &r); c != 200 || r["ok"] != true {
				t.Fatalf("novo lab: %d %v", c, r)
			}
			t.Cleanup(func() { a.call("POST", "/hosts/"+hn+"/labs/"+lab+"/down", nil, nil) })
			a.waitLab(hn, lab, func(l labs.Lab) bool { return l.Running == 1 })
			if _, err := os.Stat(filepath.Join(dir, "compose.yaml")); err != nil {
				t.Error(err)
			}
		})
	}
}

// TestEditContainer creates a container with the advanced options, reads
// them back, edits it (recreate) and pauses it.
func TestEditContainer(t *testing.T) {
	a := start(t)
	for _, h := range a.hosts() {
		h := h
		if !h.OK || h.Kind != "unix" || !selected(h.Name) {
			continue
		}
		t.Run(h.Name, func(t *testing.T) {
			a.t = t
			hn := urlPath(h.Name)
			name := fmt.Sprintf("e2eedit%d", time.Now().UnixNano()%1e6)
			spec := map[string]any{"name": name, "image": image(), "cmd": []string{"sleep", "100000"},
				"env": []string{"A=1"}, "labels": map[string]string{"gantry.e2e": "1"}, "memory": 128 << 20, "cpus": 0.5,
				"restart": "unless-stopped", "capAdd": []string{"NET_ADMIN"}, "tmpfs": []string{"/scratch"}, "start": true}
			if h.Runtime != "nerdctl" && h.Runtime != "k3s" {
				spec["init"] = true // nerdctl needs tini on the host for it
			}
			if os.Getenv("GANTRY_E2E_NETWORK") == "host" {
				spec["network"] = "host"
			} else {
				spec["hostname"] = "editado"
			}
			var cr map[string]any
			if c := a.call("POST", "/hosts/"+hn+"/containers", spec, &cr); c != 200 {
				t.Fatalf("criar: %d %v", c, cr)
			}
			t.Cleanup(func() { a.call("DELETE", "/hosts/"+hn+"/containers/"+name, nil, nil) })
			var got struct {
				Spec engine.ContainerSpec
			}
			if c := a.call("GET", "/hosts/"+hn+"/containers/"+name, nil, &got); c != 200 {
				t.Fatalf("detalhes: %d", c)
			}
			s := got.Spec
			hasEnv := false
			for _, e := range s.Env {
				hasEnv = hasEnv || e == "A=1"
			}
			if s.Name != name || !hasEnv || s.Labels["gantry.e2e"] != "1" || s.Memory != 128<<20 || s.CPUs != 0.5 ||
				s.Restart != "unless-stopped" || len(s.Tmpfs) != 1 || s.Init != (spec["init"] == true) {
				t.Errorf("spec lido: %+v", s)
			}
			for i, e := range s.Env {
				if e == "A=1" {
					s.Env[i] = "A=2"
				}
			}
			var ur map[string]any
			if c := a.call("PUT", "/hosts/"+hn+"/containers/"+name, map[string]any{"spec": s}, &ur); c != 200 {
				t.Fatalf("editar: %d %v", c, ur)
			}
			if c := a.call("GET", "/hosts/"+hn+"/containers/"+name, nil, &got); c != 200 {
				t.Fatalf("detalhes depois: %d", c)
			}
			edited := false
			for _, e := range got.Spec.Env {
				edited = edited || e == "A=2"
			}
			if !edited {
				t.Errorf("env não mudou: %v", got.Spec.Env)
			}
			a.waitLab(hn, labs.Loose, func(l labs.Lab) bool {
				n := 0
				for _, c := range l.Containers {
					if strings.HasPrefix(c.Name, name) {
						n++
						if c.Name != name || c.State != "running" {
							return false
						}
					}
				}
				return n == 1
			})
			for _, op := range []string{"pause", "unpause"} {
				if c := a.call("POST", "/hosts/"+hn+"/containers/"+name+"/"+op, nil, nil); c != 200 {
					t.Errorf("%s: %d", op, c)
				}
			}
		})
	}
}
