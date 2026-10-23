//go:build e2e

package e2e

// Tests with the real things, not the fakes of the unit tests: a VM in the
// QEMU of this machine (metrics from the QEMU process and from the
// node_exporter inside it, ports changed with the VM running) and the
// network devices with their real images (Dynamips and IOL).
//
//	GANTRY_E2E_VM_IMAGE  debian-12 (or a path or URL): TestVMReal
//	GANTRY_E2E_C7200     path of a c7200 IOS image: TestNetworkReal/dynamips
//	GANTRY_E2E_IOL       path of an IOL L3 image (Linux x86_64): TestNetworkReal/iol
//
// Each one is skipped without its variable. They take minutes: the VM
// installs packages on its first boot, and IOS takes a while to start.

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gantry/internal/labs"
)

// localFilesHost is the first local engine host (VMs and devices run on
// the machine of a host that reads files).
func (a *app) localFilesHost(t *testing.T) string {
	for _, h := range a.hosts() {
		if h.OK && h.Kind == "unix" {
			return urlPath(h.Name)
		}
	}
	t.Skip("nenhum host local")
	return ""
}

// until polls cond every 3 s until it holds or d runs out.
func until(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for {
		if cond() {
			return
		}
		if time.Now().After(end) {
			t.Fatalf("%s: não aconteceu em %s", what, d)
		}
		time.Sleep(3 * time.Second)
	}
}

func (a *app) labVM(hn, lab, vm string) (labs.VM, bool) {
	l, ok := a.lab(hn, lab)
	if !ok {
		return labs.VM{}, false
	}
	for _, v := range l.VMs {
		if v.Name == vm {
			return v, true
		}
	}
	return labs.VM{}, false
}

// waitJob waits for a background job and fails with its error.
func (a *app) waitJob(t *testing.T, id string, d time.Duration) {
	t.Helper()
	until(t, d, "operação "+id, func() bool {
		var jobs []struct {
			ID    string `json:"id"`
			Done  bool   `json:"done"`
			Error string `json:"error"`
		}
		a.call("GET", "/jobs", nil, &jobs)
		for _, j := range jobs {
			if j.ID == id && j.Done {
				if j.Error != "" {
					t.Fatalf("operação %s: %s", id, j.Error)
				}
				return true
			}
		}
		return false
	})
}

// console is a terminal of the app (a shell, a serial console) read as it
// goes.
type console struct {
	a      *app
	id     string
	mu     sync.Mutex
	out    strings.Builder
	cancel context.CancelFunc
}

func (a *app) openConsole(t *testing.T, path string) *console {
	t.Helper()
	var term struct{ ID string }
	if c := a.call("POST", path, nil, &term); c != 200 || term.ID == "" {
		t.Fatalf("abrir o console %s: %d", path, c)
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &console{a: a, id: term.ID, cancel: cancel}
	t.Cleanup(cancel)
	go func() {
		req, _ := http.NewRequestWithContext(ctx, "GET", a.url+"/api/term/"+term.ID+"/stream", nil)
		req.Header.Set("Authorization", "Bearer "+a.srv.Token())
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				if dec, err := base64.StdEncoding.DecodeString(data); err == nil {
					c.mu.Lock()
					c.out.Write(dec)
					c.mu.Unlock()
				}
			}
		}
	}()
	return c
}

func (c *console) send(s string) {
	req, _ := http.NewRequest("POST", c.a.url+"/api/term/"+c.id+"/input", strings.NewReader(s))
	req.Header.Set("Authorization", "Bearer "+c.a.srv.Token())
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
	}
}

func (c *console) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.out.String()
}

// get reads an address of this machine (the ports a VM publishes).
func get(url string) (string, error) {
	cl := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
	resp, err := cl.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return string(b), nil
}

// TestVMReal boots a VM and checks what only a real one shows: the web
// server it installs answers on the published port, the app's samples of
// the QEMU process, the node_exporter inside it (metrics: true) and a port
// added and removed with the VM running.
func TestVMReal(t *testing.T) {
	img := os.Getenv("GANTRY_E2E_VM_IMAGE")
	if img == "" {
		t.Skip("defina GANTRY_E2E_VM_IMAGE (debian-12, um caminho ou uma URL)")
	}
	a := start(t)
	hn := a.localFilesHost(t)
	name := fmt.Sprintf("e2evmreal%d", time.Now().UnixNano()%1e6)
	compose := "x-vms:\n  web:\n    image: " + img + "\n    cpus: 2\n    memory: 1024\n    metrics: true\n    ports: [\"18089:80\"]\n    packages: [nginx]\n"
	dir := labDir(t, host{}, name, compose)
	t.Cleanup(func() { a.call("POST", "/hosts/"+hn+"/labs/"+name+"/down", nil, nil) })
	var r map[string]any
	if c := a.call("POST", "/hosts/"+hn+"/open", map[string]string{"dir": dir}, &r); c != 200 || r["ok"] != true {
		t.Fatalf("subir: %v", r)
	}
	until(t, 2*time.Minute, "VM rodando", func() bool { v, _ := a.labVM(hn, name, "web"); return v.State == "running" })

	// The first boot installs nginx (cloud-init): the published port answers.
	until(t, 15*time.Minute, "nginx na porta 18089", func() bool {
		body, err := get("http://127.0.0.1:18089/")
		return err == nil && strings.Contains(strings.ToLower(body), "nginx")
	})

	// The QEMU process, sampled by the app (CPU and memory of the VM).
	until(t, 2*time.Minute, "amostras da VM no gráfico", func() bool {
		var m struct {
			Result struct {
				Series []struct {
					ID     string            `json:"id"`
					Name   string            `json:"name"`
					Points []json.RawMessage `json:"points"`
				} `json:"series"`
			} `json:"result"`
		}
		a.call("GET", "/hosts/"+hn+"/metrics?lab="+name+"&range=900", nil, &m)
		for _, s := range m.Result.Series {
			if strings.HasPrefix(s.ID, "vm:") && len(s.Points) > 0 {
				return true
			}
		}
		return false
	})

	// Inside the VM: the node_exporter cloud-init installed (metrics: true).
	until(t, 10*time.Minute, "node_exporter dentro da VM", func() bool {
		var m struct {
			VMs []struct {
				VM    string          `json:"vm"`
				Point json.RawMessage `json:"point"`
				Error string          `json:"error"`
			} `json:"vms"`
		}
		a.call("GET", "/hosts/"+hn+"/labs/"+name+"/vm-metricas", nil, &m)
		for _, v := range m.VMs {
			if v.VM == "web" && len(v.Point) > 2 && v.Error == "" {
				return true
			}
		}
		return false
	})

	// A port added with the VM running answers right away, and stops
	// answering when removed.
	if c := a.call("POST", "/hosts/"+hn+"/labs/"+name+"/vms/web/portas", map[string]string{"op": "add", "port": "18090:80"}, &r); c != 200 {
		t.Fatalf("acrescentar porta: %d %v", c, r)
	}
	until(t, time.Minute, "nginx na porta nova 18090", func() bool {
		body, err := get("http://127.0.0.1:18090/")
		return err == nil && strings.Contains(strings.ToLower(body), "nginx")
	})
	if c := a.call("POST", "/hosts/"+hn+"/labs/"+name+"/vms/web/portas", map[string]string{"op": "remove", "port": "18090:80"}, &r); c != 200 {
		t.Fatalf("tirar porta: %d %v", c, r)
	}
	until(t, time.Minute, "porta 18090 fechada", func() bool {
		cn, err := net.DialTimeout("tcp", "127.0.0.1:18090", 2*time.Second)
		if err != nil {
			return true
		}
		cn.Close()
		return false
	})
	if b, _ := os.ReadFile(filepath.Join(dir, "compose.yaml")); strings.Contains(string(b), "18090") {
		t.Errorf("a porta tirada continua no compose:\n%s", b)
	}

	if c := a.call("POST", "/hosts/"+hn+"/labs/"+name+"/down", nil, &r); c != 200 || r["ok"] != true {
		t.Fatalf("remover: %v", r)
	}
}

// TestNetworkReal brings up a router with its real image wired to a VPCS:
// the startup config goes in (with a wrong line on purpose, which must
// show as refused), the PC pings the router, and on Dynamips the idle-PC
// is calculated.
func TestNetworkReal(t *testing.T) {
	cases := []struct {
		name, env, prog, iface string
		idle                   bool
	}{
		{"dynamips", "GANTRY_E2E_C7200", "dynamips", "FastEthernet0/0", true},
		{"iol", "GANTRY_E2E_IOL", "", "Ethernet0/0", false},
	}
	ran := false
	for _, tc := range cases {
		img := os.Getenv(tc.env)
		if img == "" {
			continue
		}
		ran = true
		t.Run(tc.name, func(t *testing.T) {
			if tc.prog != "" {
				if _, err := exec.LookPath(tc.prog); err != nil {
					t.Skipf("%s não está instalado", tc.prog)
				}
			}
			if _, err := os.Stat(img); err != nil {
				t.Fatalf("%s=%s: %v", tc.env, img, err)
			}
			networkLab(t, img, tc.iface, tc.idle)
		})
	}
	if !ran {
		t.Skip("defina GANTRY_E2E_C7200 e/ou GANTRY_E2E_IOL com o caminho da imagem")
	}
}

func networkLab(t *testing.T, img, iface string, idle bool) {
	a := start(t)
	hn := a.localFilesHost(t)
	// The image is found by its name in the machine's image folder.
	if c := a.call("PUT", "/hosts/"+hn+"/imagens/pastas", map[string][]string{"dirs": {filepath.Dir(img)}}, nil); c != 200 {
		t.Fatalf("pasta de imagens: %d", c)
	}
	name := fmt.Sprintf("e2erede%d", time.Now().UnixNano()%1e6)
	compose := "x-vms:\n  R1:\n    image: " + filepath.Base(img) + "\n    interfaces: [r1-pc1]\n    config: R1.cfg\n  PC1:\n    image: vpcs\n    interfaces: [r1-pc1]\n    config: PC1.vpc\n"
	dir := labDir(t, host{}, name, compose)
	cfg := "hostname R1\n!\ninterface " + iface + "\n ip address 10.9.0.1 255.255.255.0\n no shutdown\n!\ninterface " + iface + "\n ip adress 10.9.9.9 255.255.255.0\n!\nend\n"
	if err := os.WriteFile(filepath.Join(dir, "R1.cfg"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "PC1.vpc"), []byte("ip 10.9.0.10 10.9.0.1 24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.call("POST", "/hosts/"+hn+"/labs/"+name+"/down", nil, nil) })
	var r map[string]any
	if c := a.call("POST", "/hosts/"+hn+"/open", map[string]string{"dir": dir}, &r); c != 200 || r["ok"] != true {
		t.Fatalf("subir: %v", r)
	}
	until(t, 2*time.Minute, "R1 e PC1 rodando", func() bool {
		r1, _ := a.labVM(hn, name, "R1")
		pc, _ := a.labVM(hn, name, "PC1")
		return r1.State == "running" && pc.State == "running"
	})

	// The wrong line of the startup config shows as refused.
	until(t, 6*time.Minute, "a linha errada aparece como recusada", func() bool {
		v, _ := a.labVM(hn, name, "R1")
		return v.Rejected >= 1
	})
	var rej []map[string]string
	a.call("GET", "/hosts/"+hn+"/labs/"+name+"/vms/R1/recusadas", nil, &rej)
	found := false
	for _, l := range rej {
		for _, v := range l {
			if strings.Contains(v, "ip adress") {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("a linha \"ip adress\" não está entre as recusadas: %v", rej)
	}

	// The PC reaches the router over the cable.
	pc := a.openConsole(t, "/hosts/"+hn+"/labs/"+name+"/vms/PC1/term?serial=1")
	until(t, 6*time.Minute, "PC1 pinga o R1", func() bool {
		pc.send("ping 10.9.0.1 -c 2\n")
		time.Sleep(4 * time.Second)
		return strings.Contains(pc.text(), "bytes from 10.9.0.1")
	})

	if idle {
		var j struct{ ID string }
		if c := a.call("POST", "/hosts/"+hn+"/labs/"+name+"/vms/R1/idle-pc", map[string]bool{"restart": false}, &j); c != 200 || j.ID == "" {
			t.Fatalf("idle-PC: %d", c)
		}
		a.waitJob(t, j.ID, 5*time.Minute)
		until(t, time.Minute, "imagem com idle-PC", func() bool {
			v, _ := a.labVM(hn, name, "R1")
			return !v.NoIdlePC
		})
	}

	if c := a.call("POST", "/hosts/"+hn+"/labs/"+name+"/down", nil, &r); c != 200 || r["ok"] != true {
		t.Fatalf("remover: %v", r)
	}
}
