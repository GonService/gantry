package server

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"gantry/internal/kube"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"gantry/internal/composex"
	"gantry/internal/config"
	"gantry/internal/engine"
	"gantry/internal/i18n"
	"gantry/internal/labs"
	"gantry/internal/obs"
	"gantry/internal/shim"
	"gantry/internal/vms"
)

// fakeDriver is an in-memory container runtime, served through the shim
// as a Docker API, so the whole server can be tested without an engine.
type fakeDriver struct {
	mu    sync.Mutex
	cs    []engine.Container
	vols  []engine.Volume
	log   []string
	specs map[string]engine.ContainerSpec
	seq   int
}

func (f *fakeDriver) Runtime() string                         { return "docker" }
func (f *fakeDriver) Version(context.Context) (string, error) { return "99.0-fake", nil }

func (f *fakeDriver) List(context.Context) ([]engine.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]engine.Container(nil), f.cs...), nil
}

func (f *fakeDriver) Action(_ context.Context, id, action string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, action+" "+id)
	for i := range f.cs {
		if f.cs[i].ID == id {
			if action == "stop" || action == "kill" {
				f.cs[i].State, f.cs[i].Status = "exited", "Exited (0)"
			} else {
				f.cs[i].State, f.cs[i].Status = "running", "Up 1 second"
			}
			return nil
		}
	}
	return fmt.Errorf("container %s: %w", id, shim.ErrNotFound)
}

func (f *fakeDriver) Remove(_ context.Context, id string, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, "rm "+id)
	for i := range f.cs {
		if f.cs[i].ID == id {
			f.cs = append(f.cs[:i], f.cs[i+1:]...)
			return nil
		}
	}
	return shim.ErrNotFound
}

func (f *fakeDriver) Stats(context.Context, string) (shim.Stats, error) {
	return shim.Stats{CPU: 25, Mem: 64 << 20, MemLimit: 1 << 30}, nil
}
func (f *fakeDriver) LogsCmd(id string, _ int, _ bool) []string {
	return []string{"printf", "log de " + id + "\\n"}
}
func (f *fakeDriver) ExecCmd(string, []string) []string { return []string{"sh"} }
func (f *fakeDriver) Images(context.Context) ([]engine.Image, error) {
	return []engine.Image{{ID: "sha256:aaaaaaaaaaaaaaaa", RepoTags: []string{"demo:1"}, Size: 1000}}, nil
}
func (f *fakeDriver) RemoveImage(context.Context, string, bool) error { return nil }
func (f *fakeDriver) Volumes(context.Context) ([]engine.Volume, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]engine.Volume(nil), f.vols...), nil
}
func (f *fakeDriver) RemoveVolume(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, v := range f.vols {
		if v.Name == name {
			f.vols = append(f.vols[:i], f.vols[i+1:]...)
			return nil
		}
	}
	return shim.ErrNotFound
}
func (f *fakeDriver) Networks(context.Context) ([]engine.NetworkInfo, error) {
	return []engine.NetworkInfo{{ID: "n1", Name: "bridge", Driver: "bridge"}, {ID: "n2", Name: "web_default", Driver: "bridge", Labels: map[string]string{"com.docker.compose.project": "web"}}}, nil
}
func (f *fakeDriver) RemoveNetwork(context.Context, string) error { return nil }
func (f *fakeDriver) CreateVolume(_ context.Context, v engine.VolumeSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.vols = append(f.vols, engine.Volume{Name: v.Name, Labels: v.Labels})
	return nil
}
func (f *fakeDriver) CreateNetwork(context.Context, engine.NetworkSpec) error { return nil }
func (f *fakeDriver) PullCmd(ref string) []string                             { return []string{"echo", "pulled " + ref} }
func (f *fakeDriver) Create(_ context.Context, s engine.ContainerSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s.Image == "quebrada" {
		return "", errors.New("imagem quebrada")
	}
	for _, c := range f.cs {
		if c.Name() == s.Name {
			return "", fmt.Errorf("nome %s em uso", s.Name)
		}
	}
	f.seq++
	id := s.Name
	if f.specs == nil {
		f.specs = map[string]engine.ContainerSpec{}
	}
	if _, used := f.specs[id]; used {
		id = fmt.Sprintf("%s-%d", s.Name, f.seq)
	}
	c := ctr(id, "", "", "created", s.Labels)
	c.Names = []string{"/" + s.Name}
	f.cs = append(f.cs, c)
	f.specs[id] = s
	return id, nil
}

func (f *fakeDriver) InspectRaw(_ context.Context, id string) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.cs {
		if c.ID == id {
			sp, ok := f.specs[id]
			if !ok {
				return nil, shim.ErrNotFound // the shim falls back to its own
			}
			b := sp.CreateBody()
			b["Id"], b["Name"] = c.ID, "/"+c.Name()
			b["State"] = map[string]any{"Running": c.State == "running", "Status": c.State}
			return json.Marshal(b)
		}
	}
	return nil, shim.ErrNotFound
}

func (f *fakeDriver) Tag(_ context.Context, src, dst string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, "tag "+src+" "+dst)
	return nil
}

func (f *fakeDriver) Rename(_ context.Context, id, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, "rename "+id+" "+name)
	for i := range f.cs {
		if f.cs[i].ID == id {
			f.cs[i].Names = []string{"/" + name}
			return nil
		}
	}
	return shim.ErrNotFound
}

func (f *fakeDriver) Connect(_ context.Context, network, id, ip string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, "connect "+network+" "+id+" "+ip)
	return nil
}

func (f *fakeDriver) Disconnect(_ context.Context, network, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, "disconnect "+network+" "+id)
	return nil
}
func (f *fakeDriver) Prune(context.Context, string) (int, error) { return 1, nil }

func ctr(id, project, service, state string, labels map[string]string) engine.Container {
	c := engine.Container{ID: id, Names: []string{"/" + id}, Image: "demo:1", ImageID: "sha256:aaaaaaaaaaaaaaaa", State: state, Status: state, Labels: map[string]string{}}
	if project != "" {
		c.Labels["com.docker.compose.project"] = project
		c.Labels["com.docker.compose.service"] = service
	}
	for k, v := range labels {
		c.Labels[k] = v
	}
	c.Mounts = append(c.Mounts, engine.Mount{Type: "volume", Name: "dados", Destination: "/dados"})
	return c
}

type env struct {
	t    *testing.T
	srv  *Server
	url  string
	fake *fakeDriver
	dir  string
}

// newEnv starts the API with one local host ("fake") backed by fakeDriver.
func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	compose := filepath.Join(dir, "web", "compose.yaml")
	_ = os.MkdirAll(filepath.Dir(compose), 0o755)
	_ = os.WriteFile(compose, []byte("services:\n  app:\n    image: demo:1\n    ports: [\"8080:80\"]\n  db:\n    image: demo:1\n"), 0o644)
	files := map[string]string{"com.docker.compose.project.working_dir": filepath.Dir(compose), "com.docker.compose.project.config_files": compose}
	f := &fakeDriver{
		cs: []engine.Container{
			ctr("web-app-1", "web", "app", "running", files),
			ctr("web-db-1", "web", "db", "exited", files),
			ctr("solto", "", "", "running", nil),
		},
		vols: []engine.Volume{{Name: "dados"}, {Name: "orfao"}},
	}
	// The socket in a short folder: a Unix socket path has at most 104
	// bytes on macOS, and t.TempDir() there carries the test's name.
	sockDir, err := os.MkdirTemp("", "gt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "fake.sock")
	sh := shim.New(f)
	if err := sh.Listen(sock); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sh.Close)

	no := false
	cfg, _ := config.Load(filepath.Join(dir, "config.json"))
	cfg.Autodetect = &no
	cfg.Hosts = []config.Host{{Name: "fake", URL: "unix://" + sock, Source: "config"}}
	s := New(cfg, labs.OpenStore(filepath.Join(dir, "state.json")), fstest.MapFS{"index.html": {Data: []byte("ok")}}, true)
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(func() { hs.Close(); s.Close() })
	return &env{t: t, srv: s, url: hs.URL, fake: f, dir: dir}
}

// call does an API request with the session token and decodes JSON.
func (e *env) call(method, path string, body any, out any) int {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, e.url+"/api"+path, rd)
	req.Header.Set("Authorization", "Bearer "+e.srv.Token())
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			e.t.Fatalf("%s %s: %v: %s", method, path, err, b)
		}
	}
	return resp.StatusCode
}

func (e *env) labs() map[string]labs.Lab {
	var ls []labs.Lab
	if c := e.call("GET", "/hosts/fake/labs", nil, &ls); c != 200 {
		e.t.Fatalf("labs: %d", c)
	}
	out := map[string]labs.Lab{}
	for _, l := range ls {
		out[l.ID] = l
	}
	return out
}

func TestAuthRequired(t *testing.T) {
	e := newEnv(t)
	resp, err := http.Get(e.url + "/api/hosts")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("sem token: %d", resp.StatusCode)
	}
	// The link (one use) hands the token to the page, never in a cookie
	// (a cookie for 127.0.0.1 would reach every app on 127.0.0.1).
	e.srv.SetAddr(strings.TrimPrefix(e.url, "http://"))
	link := e.srv.LoginLink()
	get := func(u string) (int, string, []*http.Cookie) {
		resp, err := http.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode, string(b), resp.Cookies()
	}
	code, body, cookies := get(link)
	if code != 200 || !strings.Contains(body, `localStorage.setItem("gantry.token","`+e.srv.Token()+`")`) || len(cookies) != 0 {
		t.Fatalf("link: %d %q %v", code, body, cookies)
	}
	if code, _, _ := get(link); code != http.StatusUnauthorized {
		t.Fatalf("o link vale uma vez só: %d", code)
	}
	// The page itself loads without the token; the API does not.
	if code, _, _ := get(e.url + "/"); code != 200 {
		t.Fatalf("página: %d", code)
	}
	if code, _, _ := get(e.url + "/api/hosts?t=" + e.srv.Token()); code != 200 {
		t.Fatalf("token na query: %d", code)
	}
	req, _ := http.NewRequest("GET", e.url+"/api/hosts", nil)
	req.Header.Set("Cookie", "gantry="+e.srv.Token())
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("cookie não vale mais: %v %v", err, resp)
	}
}

func TestHostAndLabs(t *testing.T) {
	e := newEnv(t)
	var hs []hostView
	e.call("GET", "/hosts", nil, &hs)
	if len(hs) != 1 || !hs[0].OK || hs[0].Version != "99.0-fake" || hs[0].Info.ContainersRunning != 2 {
		t.Fatalf("hosts %+v", hs)
	}
	ls := e.labs()
	web := ls["web"]
	if web.Total != 2 || web.Running != 1 || web.WorkingDir == "" || len(web.Files) != 1 {
		t.Errorf("lab web %+v", web)
	}
	if ls[labs.Loose].Total != 1 {
		t.Errorf("avulsos %+v", ls[labs.Loose])
	}

	// Container actions go to the engine.
	if c := e.call("POST", "/hosts/fake/containers/web-db-1/start", nil, nil); c != 200 {
		t.Fatalf("start: %d", c)
	}
	if e.labs()["web"].Running != 2 {
		t.Error("db should be running")
	}
	if c := e.call("POST", "/hosts/fake/containers/nada/stop", nil, nil); c != 404 {
		t.Errorf("container inexistente: %d", c)
	}

	// Lab stop and down go through the engine API, without compose.
	var r map[string]any
	e.call("POST", "/hosts/fake/labs/web/stop", nil, &r)
	if r["ok"] != true || e.labs()["web"].Running != 0 {
		t.Fatalf("lab stop %+v", r)
	}
	e.call("POST", "/hosts/fake/labs/web/down", nil, &r)
	if r["ok"] != true || e.labs()["web"].Total != 0 {
		t.Fatalf("lab down %+v", r)
	}
	// The lab is remembered after its containers are gone.
	if _, ok := e.labs()["web"]; !ok {
		t.Error("lab should be remembered")
	}
}

func TestStatsLogsAndTerminal(t *testing.T) {
	e := newEnv(t)
	var st map[string]engine.Stats
	e.call("GET", "/hosts/fake/stats", nil, &st)
	if st["web-app-1"].CPU < 24.9 || st["web-app-1"].Mem != 64<<20 || len(st) != 2 {
		t.Fatalf("stats %+v", st)
	}

	logs := e.sse("/hosts/fake/containers/web-app-1/logs", 3*time.Second, "end")
	if !strings.Contains(logs, "log de web-app-1") {
		t.Errorf("logs %q", logs)
	}

	if runtime.GOOS != "linux" {
		return // the shim's terminal uses script(1), whose flags differ; covered by make e2e
	}
	var term struct{ ID string }
	if c := e.call("POST", "/hosts/fake/containers/web-app-1/term", nil, &term); c != 200 || term.ID == "" {
		t.Fatalf("term %d %+v", c, term)
	}
	done := make(chan string)
	go func() { done <- e.sse("/term/"+term.ID+"/stream", 4*time.Second, "GANTRY_OK") }()
	time.Sleep(500 * time.Millisecond)
	req, _ := http.NewRequest("POST", e.url+"/api/term/"+term.ID+"/input", strings.NewReader("echo GANTRY_$((1+0))K\n"))
	req.Header.Set("Authorization", "Bearer "+e.srv.Token())
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
	}
	out := <-done
	if !strings.Contains(out, "GANTRY_1K") {
		t.Errorf("terminal output %q", out)
	}
}

// sse reads an event stream until a marker shows up or the time runs out,
// decoding base64 messages.
func (e *env) sse(path string, d time.Duration, until string) string {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", e.url+"/api"+path, nil)
	req.Header.Set("Authorization", "Bearer "+e.srv.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var b strings.Builder
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		l := sc.Text()
		if strings.HasPrefix(l, "event: ") && strings.Contains(l, until) {
			break
		}
		if data, ok := strings.CutPrefix(l, "data: "); ok {
			if dec, err := base64.StdEncoding.DecodeString(data); err == nil {
				b.Write(dec)
			} else {
				b.WriteString(data)
			}
			if strings.Contains(b.String(), until) {
				break
			}
		}
	}
	return b.String()
}

func TestResources(t *testing.T) {
	e := newEnv(t)
	var vols []resourceView
	e.call("GET", "/hosts/fake/res/volumes", nil, &vols)
	if len(vols) != 2 || len(vols[0].UsedBy) != 3 || len(vols[1].UsedBy) != 0 {
		t.Fatalf("volumes %+v", vols)
	}
	if c := e.call("DELETE", "/hosts/fake/res/volumes/orfao", nil, nil); c != 200 {
		t.Errorf("remove volume: %d", c)
	}
	var imgs, nets []resourceView
	e.call("GET", "/hosts/fake/res/images", nil, &imgs)
	e.call("GET", "/hosts/fake/res/networks", nil, &nets)
	if len(imgs) != 1 || len(imgs[0].UsedBy) != 3 || len(nets) != 2 || !nets[0].System || nets[1].Lab != "web" {
		t.Errorf("images %+v networks %+v", imgs, nets)
	}
	var pr map[string]any
	e.call("POST", "/hosts/fake/prune/volumes", nil, &pr)
	if pr["ok"] != true {
		t.Errorf("prune %+v", pr)
	}
}

func TestLabFile(t *testing.T) {
	e := newEnv(t)
	var f struct {
		Path, Content, Hash string
	}
	if c := e.call("GET", "/hosts/fake/labs/web/file", nil, &f); c != 200 || !strings.Contains(f.Content, "services:") {
		t.Fatalf("file %d %+v", c, f)
	}
	var r map[string]any
	if c := e.call("PUT", "/hosts/fake/labs/web/file", map[string]string{"content": f.Content + "# editado\n", "hash": f.Hash}, &r); c != 200 {
		t.Fatalf("put: %d %v", c, r)
	}
	b, _ := os.ReadFile(f.Path)
	if !strings.HasSuffix(string(b), "# editado\n") {
		t.Errorf("arquivo não foi gravado: %q", b)
	}
	// A stale hash means someone changed the file meanwhile.
	if c := e.call("PUT", "/hosts/fake/labs/web/file", map[string]string{"content": "x", "hash": f.Hash}, nil); c != http.StatusConflict {
		t.Errorf("conflito esperado, veio %d", c)
	}
}

func TestTopology(t *testing.T) {
	e := newEnv(t)
	var tp struct {
		Services []struct {
			Name, Runtime string
			Containers    []any
		}
		Networks []struct{ Key string }
		Warnings []string
	}
	if c := e.call("GET", "/hosts/fake/labs/web/topology", nil, &tp); c != 200 {
		t.Fatalf("topology: %d", c)
	}
	if len(tp.Services) != 2 || tp.Services[0].Name != "app" || len(tp.Services[0].Containers) != 1 || tp.Networks[0].Key != "default" {
		t.Errorf("%+v", tp)
	}
}

func TestHostsCRUDAndObs(t *testing.T) {
	e := newEnv(t)
	// Another address: hosts with the same address are shown once.
	addr := "tcp://127.0.0.1:1"
	if c := e.call("POST", "/hosts", map[string]string{"name": "outro", "url": addr}, nil); c != 200 {
		t.Fatalf("add: %d", c)
	}
	if c := e.call("POST", "/hosts", map[string]string{"name": "outro", "url": addr}, nil); c == 200 {
		t.Error("nome repetido deveria falhar")
	}
	if c := e.call("PUT", "/hosts/outro", map[string]string{"name": "renomeado", "url": addr}, nil); c != 200 {
		t.Fatalf("edit: %d", c)
	}
	var hs []hostView
	e.call("GET", "/hosts", nil, &hs)
	if len(hs) != 2 || hs[1].Name != "renomeado" {
		t.Fatalf("hosts %+v", hs)
	}
	if c := e.call("DELETE", "/hosts/renomeado", nil, nil); c != 200 {
		t.Errorf("delete: %d", c)
	}

	if c := e.call("PUT", "/hosts/fake/obs", map[string]string{"prometheus": "http://127.0.0.1:1", "grafanaToken": "segredo123"}, nil); c != 200 {
		t.Fatalf("obs put: %d", c)
	}
	var o struct {
		Services map[string]struct{ OK bool }
		Config   config.Obs
	}
	e.call("GET", "/hosts/fake/obs", nil, &o)
	if o.Services["prometheus"].OK || o.Config.GrafanaToken == "segredo123" || o.Config.Prometheus != "http://127.0.0.1:1" {
		t.Errorf("obs %+v", o)
	}
	if c := e.call("PUT", "/hosts/fake/obs", map[string]string{"prometheus": "ftp://x"}, nil); c != 400 {
		t.Errorf("endereço inválido deveria falhar: %d", c)
	}
}

func TestMetricsCollectedByApp(t *testing.T) {
	e := newEnv(t)
	var r struct {
		Source string
		Result struct{ Series []struct{ Name string } }
	}
	e.call("GET", "/hosts/fake/metrics?lab=web&range=900", nil, &r)
	if r.Source != "app" {
		t.Fatalf("source %q", r.Source)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(r.Result.Series) == 0 && time.Now().Before(deadline) {
		time.Sleep(300 * time.Millisecond)
		e.call("GET", "/hosts/fake/metrics?lab=web&range=900", nil, &r)
	}
	if len(r.Result.Series) != 1 || r.Result.Series[0].Name != "web-app-1" {
		t.Errorf("series %+v", r.Result.Series)
	}
}

func TestCreateLabAndThings(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.dir, "novo")
	compose := "name: novo\nservices:\n  app:\n    image: demo:1\n    volumes: [\"dados:/d\"]\nvolumes:\n  dados: {}\nx-vms:\n  vm1:\n    image: debian-12\n"
	var r map[string]any
	if c := e.call("POST", "/hosts/fake/labs", map[string]any{"name": "novo", "dir": dir, "compose": compose}, &r); c != 200 || r["ok"] != true {
		t.Fatalf("criar lab: %d %v", c, r)
	}
	b, err := os.ReadFile(filepath.Join(dir, "compose.yaml"))
	if err != nil || string(b) != compose {
		t.Fatalf("arquivo: %v %q", err, b)
	}
	l, ok := e.labs()["novo"]
	if !ok || l.WorkingDir != dir || len(l.VMs) != 1 {
		t.Errorf("lab novo %+v", l)
	}
	// Same folder again, a bad name and a broken compose are refused.
	if c := e.call("POST", "/hosts/fake/labs", map[string]any{"name": "outro", "dir": dir, "compose": compose}, nil); c != http.StatusConflict {
		t.Errorf("pasta ocupada: %d", c)
	}
	if c := e.call("POST", "/hosts/fake/labs", map[string]any{"name": "Nome Ruim", "dir": dir + "2", "compose": compose}, nil); c != 400 {
		t.Errorf("nome inválido: %d", c)
	}
	if c := e.call("POST", "/hosts/fake/labs", map[string]any{"name": "quebrado", "dir": dir + "3", "compose": "services: ["}, nil); c != 400 {
		t.Errorf("compose quebrado: %d", c)
	}
	if c := e.call("POST", "/hosts/fake/labs", map[string]any{"name": "rel", "dir": "relativa/x", "compose": compose}, nil); c != 400 {
		t.Errorf("pasta relativa: %d", c)
	}
	// ~ is the home of the host's machine.
	home := t.TempDir()
	t.Setenv("HOME", home)
	if c := e.call("POST", "/hosts/fake/labs", map[string]any{"name": "casa", "dir": "~/labs/casa", "compose": compose}, &r); c != 200 {
		t.Fatalf("pasta com ~: %d %v", c, r)
	}
	if _, err := os.Stat(filepath.Join(home, "labs", "casa", "compose.yaml")); err != nil {
		t.Errorf("~ não expandido: %v", err)
	}

	// A loose container, started right away.
	var cr map[string]any
	if c := e.call("POST", "/hosts/fake/containers", map[string]any{"name": "avulso2", "image": "demo:1", "ports": []string{"8081:80"}, "start": true}, &cr); c != 200 || cr["ok"] != true {
		t.Fatalf("criar container: %d %v", c, cr)
	}
	found := false
	for _, c := range e.labs()[labs.Loose].Containers {
		if c.Name == "avulso2" && c.State == "running" {
			found = true
		}
	}
	if !found {
		t.Error("container novo não apareceu rodando")
	}
	if c := e.call("POST", "/hosts/fake/containers", map[string]any{"image": ""}, nil); c != 400 {
		t.Errorf("sem imagem: %d", c)
	}
	// Volumes and networks.
	if c := e.call("POST", "/hosts/fake/res/volumes", map[string]string{"name": "novo-vol"}, nil); c != 200 {
		t.Errorf("volume: %d", c)
	}
	var vols []resourceView
	e.call("GET", "/hosts/fake/res/volumes", nil, &vols)
	if len(vols) != 3 {
		t.Errorf("volumes %+v", vols)
	}
	if c := e.call("POST", "/hosts/fake/res/networks", map[string]string{"name": "rede", "subnet": "10.1.0.0/24"}, nil); c != 200 {
		t.Errorf("rede: %d", c)
	}
	var job struct{ Job string }
	if c := e.call("POST", "/hosts/fake/res/images", map[string]string{"image": "demo:2"}, &job); c != 200 || job.Job == "" {
		t.Fatalf("pull: %d", c)
	}
	if out := e.sse("/jobs/"+job.Job+"/stream", 5*time.Second, "end"); !strings.Contains(out, "pulled demo:2") {
		t.Errorf("pull output %q", out)
	}
}

func TestEditContainer(t *testing.T) {
	e := newEnv(t)
	spec := map[string]any{"name": "web1", "image": "demo:1", "ports": []string{"8081:80/tcp"}, "env": []string{"A=1"},
		"memory": 256 << 20, "cpus": 1.5, "restart": "unless-stopped", "capAdd": []string{"NET_ADMIN"}, "start": true}
	var cr map[string]any
	if c := e.call("POST", "/hosts/fake/containers", spec, &cr); c != 200 {
		t.Fatalf("criar: %d %v", c, cr)
	}
	id := cr["id"].(string)
	var got struct {
		Spec    engine.ContainerSpec
		Inspect map[string]any
	}
	if c := e.call("GET", "/hosts/fake/containers/"+id, nil, &got); c != 200 {
		t.Fatalf("detalhes: %d", c)
	}
	sp := got.Spec
	if sp.Name != "web1" || sp.Memory != 256<<20 || sp.CPUs != 1.5 || sp.Restart != "unless-stopped" ||
		len(sp.Ports) != 1 || sp.Ports[0] != "8081:80/tcp" || len(sp.CapAdd) != 1 || got.Inspect["Id"] != id {
		t.Errorf("spec lido %+v", sp)
	}

	// Edit: a new container replaces the old one, with the same name, running.
	sp.Env = []string{"A=2"}
	var ur map[string]any
	if c := e.call("PUT", "/hosts/fake/containers/"+id, map[string]any{"spec": sp}, &ur); c != 200 {
		t.Fatalf("editar: %d %v", c, ur)
	}
	newID := ur["id"].(string)
	if newID == id {
		t.Fatal("o container não foi recriado")
	}
	e.fake.mu.Lock()
	names := map[string]string{}
	for _, c := range e.fake.cs {
		names[c.Name()] = c.ID + " " + c.State
	}
	env := e.fake.specs[newID].Env
	log := strings.Join(e.fake.log, ";")
	e.fake.mu.Unlock()
	if names["web1"] != newID+" running" || len(env) != 1 || env[0] != "A=2" {
		t.Errorf("depois de editar: %v env=%v", names, env)
	}
	if _, left := names["web1-gantry-old"]; left || !strings.Contains(log, "rename "+id+" web1-gantry-old") || !strings.Contains(log, "rm "+id) {
		t.Errorf("o antigo deveria sair: %v %s", names, log)
	}

	// A failed edit keeps the current container, running, under its name.
	sp.Image = "quebrada"
	if c := e.call("PUT", "/hosts/fake/containers/"+newID, map[string]any{"spec": sp}, &ur); c < 400 {
		t.Fatalf("edição com erro deveria falhar: %d", c)
	}
	e.fake.mu.Lock()
	names = map[string]string{}
	for _, c := range e.fake.cs {
		names[c.Name()] = c.ID + " " + c.State
	}
	e.fake.mu.Unlock()
	if names["web1"] != newID+" running" || len(names) != 4 {
		t.Errorf("rollback: %v", names)
	}

	// Networks and pause.
	if c := e.call("POST", "/hosts/fake/containers/"+newID+"/net/connect", map[string]string{"network": "rede", "ipv4": "10.1.0.9"}, nil); c != 200 {
		t.Errorf("conectar: %d", c)
	}
	if c := e.call("POST", "/hosts/fake/containers/"+newID+"/net/disconnect", map[string]string{"network": "rede"}, nil); c != 200 {
		t.Errorf("desconectar: %d", c)
	}
	if c := e.call("POST", "/hosts/fake/containers/"+newID+"/pause", nil, nil); c != 200 {
		t.Errorf("pausar: %d", c)
	}
	e.fake.mu.Lock()
	log = strings.Join(e.fake.log, ";")
	e.fake.mu.Unlock()
	for _, want := range []string{"connect rede " + newID + " 10.1.0.9", "disconnect rede " + newID, "pause " + newID} {
		if !strings.Contains(log, want) {
			t.Errorf("faltou %q em %s", want, log)
		}
	}
}

func TestImagesBuildAndTag(t *testing.T) {
	e := newEnv(t)
	if c := e.call("POST", "/hosts/fake/images/sha256:aaaaaaaaaaaaaaaa/tag", map[string]string{"ref": "meu/app:2"}, nil); c != 200 {
		t.Errorf("tag: %d", c)
	}
	e.fake.mu.Lock()
	log := strings.Join(e.fake.log, ";")
	e.fake.mu.Unlock()
	if !strings.Contains(log, "tag sha256:aaaaaaaaaaaaaaaa meu/app:2") {
		t.Errorf("tag não chegou ao engine: %s", log)
	}
	// Builds need a name and a source; the fake engine (a shim) refuses them.
	if c := e.call("POST", "/hosts/fake/build", map[string]any{"source": "editor", "dockerfile": "FROM x"}, nil); c != 400 {
		t.Errorf("sem nome: %d", c)
	}
	if c := e.call("POST", "/hosts/fake/build", map[string]any{"tags": []string{"a:1"}, "source": "git", "git": "ftp://x"}, nil); c != 400 {
		t.Errorf("git inválido: %d", c)
	}
	var job struct{ Job string }
	if c := e.call("POST", "/hosts/fake/build", map[string]any{"tags": []string{"a:1"}, "source": "editor", "dockerfile": "FROM x"}, &job); c != 200 {
		t.Fatalf("build: %d", c)
	}
	if out := e.sse("/jobs/"+job.Job+"/stream", 5*time.Second, "end"); !strings.Contains(out, "build") {
		t.Errorf("saída do build %q", out)
	}
}

func TestLabFromGitAndEnv(t *testing.T) {
	e := newEnv(t)
	repo := filepath.Join(e.dir, "repo")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if err := os.MkdirAll(filepath.Join(repo, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	compose := "services:\n  web:\n    image: demo:1\n"
	_ = os.WriteFile(filepath.Join(repo, "app", "compose.yaml"), []byte(compose), 0o644)
	git("init", "-q", "-b", "main")
	git("add", ".")
	git("commit", "-q", "-m", "primeiro")

	dir := filepath.Join(e.dir, "clone")
	var r map[string]any
	if c := e.call("POST", "/hosts/fake/labs/git", map[string]any{"name": "dogit", "url": "file://" + repo, "dir": dir, "file": "app"}, &r); c != 200 {
		t.Fatalf("lab do git: %d %v", c, r)
	}
	l, ok := e.labs()["dogit"]
	if !ok || l.WorkingDir != filepath.Join(dir, "app") {
		t.Fatalf("lab %+v", l)
	}
	if c := e.call("POST", "/hosts/fake/labs/git", map[string]any{"name": "dogit2", "url": "file://" + repo, "dir": dir}, nil); c != http.StatusConflict {
		t.Errorf("pasta existente: %d", c)
	}
	var g map[string]any
	if c := e.call("GET", "/hosts/fake/labs/dogit/git", nil, &g); c != 200 || g["repo"] != true || g["subject"] != "primeiro" || g["branch"] != "main" {
		t.Errorf("estado do git: %d %v", c, g)
	}
	_ = os.WriteFile(filepath.Join(repo, "app", "compose.yaml"), []byte(compose+"  db:\n    image: demo:1\n"), 0o644)
	git("commit", "-q", "-am", "segundo")
	if c := e.call("POST", "/hosts/fake/labs/dogit/git/pull", map[string]any{}, &r); c != 200 {
		t.Fatalf("pull: %d %v", c, r)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "app", "compose.yaml"))
	if !strings.Contains(string(b), "db:") {
		t.Errorf("pull não trouxe a mudança: %s", b)
	}

	// .env: missing at first, then created and read back.
	var f struct {
		Content string
		Hash    string
		Missing bool
		Files   []map[string]string
	}
	if c := e.call("GET", "/hosts/fake/labs/dogit/file?name=.env", nil, &f); c != 200 || !f.Missing || len(f.Files) != 2 {
		t.Fatalf(".env: %d %+v", c, f)
	}
	if c := e.call("PUT", "/hosts/fake/labs/dogit/file?name=.env", map[string]string{"content": "A=1\n", "hash": f.Hash}, nil); c != 200 {
		t.Fatalf("salvar .env: %d", c)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "app", ".env")); string(b) != "A=1\n" {
		t.Errorf(".env gravado: %q", b)
	}
	if c := e.call("GET", "/hosts/fake/labs/dogit/file?name=/etc/passwd", nil, nil); c != 404 {
		t.Errorf("arquivo de fora do lab: %d", c)
	}
}

func TestOverviewAndTemplates(t *testing.T) {
	e := newEnv(t)
	var ov struct {
		Machine  string
		Engines  []engineView
		Projects []projectView
		VMs      []vmView
	}
	if c := e.call("GET", "/hosts/fake/overview", nil, &ov); c != 200 {
		t.Fatalf("overview: %d", c)
	}
	if ov.Machine != "local" || len(ov.Engines) != 1 || !ov.Engines[0].OK || ov.Engines[0].Containers != 3 || ov.Engines[0].Running != 2 {
		t.Errorf("engines %+v", ov.Engines)
	}
	if len(ov.Projects) != 1 || ov.Projects[0].ID != "web" || ov.Projects[0].Total != 2 {
		t.Errorf("projetos %+v", ov.Projects)
	}
	var cs []ctrView
	if c := e.call("GET", "/hosts/fake/containers", nil, &cs); c != 200 || len(cs) != 3 || cs[0].State != "running" {
		t.Errorf("containers %d %+v", c, cs)
	}
	var sys struct {
		Info  map[string]any
		Error string
	}
	if c := e.call("GET", "/hosts/fake/system", nil, &sys); c != 200 || sys.Error != "" || sys.Info["cpus"] == nil {
		t.Errorf("system %d %+v", c, sys)
	}

	// Templates: built-ins are listed and read-only; own ones are saved.
	var list []map[string]any
	e.call("GET", "/templates", nil, &list)
	if len(list) < 10 {
		t.Fatalf("modelos: %d", len(list))
	}
	if c := e.call("PUT", "/templates/gantry-nginx", map[string]string{"name": "x", "yaml": "services: {}"}, nil); c != http.StatusForbidden {
		t.Errorf("alterou modelo embutido: %d", c)
	}
	if c := e.call("POST", "/templates", map[string]string{"name": "Quebrado", "yaml": "services: ["}, nil); c != 400 {
		t.Errorf("yaml quebrado: %d", c)
	}
	yaml := "services:\n  app:\n    image: demo:${TAG:-1}\n    environment:\n      SENHA: ${SENHA:?informe}\n"
	var saved map[string]any
	if c := e.call("POST", "/templates", map[string]string{"name": "Meu", "yaml": yaml}, &saved); c != 200 || saved["id"] == "" {
		t.Fatalf("salvar: %d %v", c, saved)
	}
	var chk struct {
		OK       bool
		Vars     []map[string]any
		Services []map[string]any
	}
	e.call("POST", "/templates/check", map[string]string{"yaml": yaml}, &chk)
	if !chk.OK || len(chk.Vars) != 2 || chk.Vars[1]["required"] != true || chk.Services[0]["image"] != "demo:1" {
		t.Errorf("check %+v", chk)
	}
	// Deploying writes the compose (with the project's name) and the .env.
	dir := filepath.Join(e.dir, "gerado")
	var r map[string]any
	if c := e.call("POST", "/hosts/fake/labs", map[string]any{"name": "gerado", "dir": dir, "compose": yaml, "env": "SENHA=x\nTAG=2"}, &r); c != 200 {
		t.Fatalf("gerar: %d %v", c, r)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "compose.yaml"))
	env, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if !strings.HasPrefix(string(b), "name: gerado\n") || string(env) != "SENHA=x\nTAG=2\n" {
		t.Errorf("arquivos:\n%s\n%s", b, env)
	}
	if c := e.call("DELETE", "/templates/"+saved["id"].(string), nil, nil); c != 200 {
		t.Errorf("excluir: %d", c)
	}
}

func TestConvertRun(t *testing.T) {
	e := newEnv(t)
	var c struct {
		YAML     string
		Engine   string
		Services []string
	}
	if code := e.call("POST", "/convert", map[string]string{"commands": "podman run -d --name w -p 80:80 nginx"}, &c); code != 200 || c.Engine != "podman" || len(c.Services) != 1 || !strings.Contains(c.YAML, "image: nginx") {
		t.Errorf("%d %+v", code, c)
	}
	if code := e.call("POST", "/convert", map[string]string{"commands": "rm -rf /"}, nil); code != 400 {
		t.Errorf("comando inválido: %d", code)
	}
}

// VMs are sampled from the QEMU process on the machine: a busy process stands in.
func TestVMMetrics(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	vm := filepath.Join(state, "gantry", "vms", "web", "box")
	_ = os.MkdirAll(vm, 0o755)
	busy := exec.Command("sh", "-c", "while :; do :; done")
	if err := busy.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = busy.Process.Kill(); _ = busy.Wait() })
	_ = os.WriteFile(filepath.Join(vm, "qemu.pid"), []byte(strconv.Itoa(busy.Process.Pid)+"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(vm, "vm.env"), []byte("ssh=22222\nports=\nimage=debian-12\ncpus=1\nmem=512\naccel=kvm\nvnc=\n"), 0o644)
	_ = os.WriteFile(filepath.Join(vm, "disk.qcow2"), make([]byte, 64<<10), 0o644)

	e := newEnv(t)
	var st map[string]engine.Stats
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		e.call("GET", "/hosts/fake/stats", nil, &st)
		if _, ok := st["vm:web/box"]; ok {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	s, ok := st["vm:web/box"]
	if !ok || s.CPU < 20 || s.Mem == 0 || s.Disk < 32<<10 {
		t.Fatalf("stats da VM: %+v (%v)", s, ok)
	}
	var r struct {
		Result struct {
			Series []struct{ ID, Name, Kind string }
		}
	}
	e.call("GET", "/hosts/fake/metrics?lab=web&range=900", nil, &r)
	found := false
	for _, x := range r.Result.Series {
		if x.Kind == "vm" && x.Name == "box" && x.ID == "vm:web/box" {
			found = true
		}
	}
	if !found {
		t.Errorf("a VM não está nas métricas do projeto: %+v", r.Result.Series)
	}
	e.call("GET", "/hosts/fake/metrics?lab=web&range=900&id=vm:web/box", nil, &r)
	if len(r.Result.Series) != 1 {
		t.Errorf("filtro por id: %+v", r.Result.Series)
	}
}

func TestPortHint(t *testing.T) {
	e := newEnv(t)
	e.fake.cs[2].Ports = append(e.fake.cs[2].Ports, engine.Port{PublicPort: 9090, PrivatePort: 9090, Type: "tcp"})
	dir := t.TempDir()
	compose := filepath.Join(dir, "compose.yaml")
	_ = os.WriteFile(compose, []byte("services:\n  p:\n    image: demo:1\n    ports: [\"${PORTA_P:-9090}:9090\"]\n"), 0o644)
	h := e.srv.host("fake")
	out := "Error response from daemon: cannot listen on the TCP port: listen tcp4 :9090: bind: address already in use"
	got := e.srv.portHint(context.Background(), h, "novo", dir, []string{compose}, out)
	if !strings.Contains(got, "9090") || !strings.Contains(got, "solto") || !strings.Contains(got, "PORTA_P") {
		t.Errorf("dica: %q", got)
	}
	_ = os.WriteFile(filepath.Join(dir, ".env"), []byte("PORTA_Q=9090\n"), 0o644)
	if got := e.srv.portHint(context.Background(), h, "novo", dir, []string{compose}, out); !strings.Contains(got, "PORTA_Q") {
		t.Errorf("dica com .env: %q", got)
	}
	if got := e.srv.portHint(context.Background(), h, "novo", dir, []string{compose}, "outro erro"); got != "" {
		t.Errorf("sem porta: %q", got)
	}
}

func TestBusyPorts(t *testing.T) {
	if got := parseListen("0.0.0.0:22\n[::]:80\n*.3000\n127.0.0.1.631\nlixo\n*.3000\n"); fmt.Sprint(got) != "[22 80 631 3000]" {
		t.Errorf("parseListen = %v", got)
	}
	e := newEnv(t)
	e.fake.cs[2].Ports = append(e.fake.cs[2].Ports, engine.Port{PublicPort: 9090, PrivatePort: 80, Type: "tcp"})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	mine := ln.Addr().(*net.TCPAddr).Port
	var r struct{ Busy map[string]string }
	e.call("GET", "/hosts/fake/ports", nil, &r)
	if !strings.Contains(r.Busy["9090"], "solto") {
		t.Errorf("9090: %q", r.Busy["9090"])
	}
	if _, err := exec.LookPath("ss"); err == nil && r.Busy[strconv.Itoa(mine)] == "" {
		t.Errorf("a porta %d (ouvindo) não apareceu: %v", mine, r.Busy)
	}
}

// Other text files of the project folder can be edited, and "apply" restarts
// only the services that mount them.
func TestProjectExtraFiles(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.dir, "web")
	_ = os.MkdirAll(filepath.Join(dir, "conf"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "conf", "app.yml"), []byte("a: 1\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "foto.png"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services:\n  app:\n    image: demo:1\n    volumes: [\"./conf:/etc/app:ro\"]\n  db:\n    image: demo:1\n"), 0o644)
	var f struct {
		Path, Content, Hash string
		Files               []map[string]string
	}
	e.call("GET", "/hosts/fake/labs/web/file", nil, &f)
	names := []string{}
	for _, x := range f.Files {
		names = append(names, x["name"])
	}
	if got := strings.Join(names, ","); !strings.Contains(got, "conf/app.yml") || strings.Contains(got, "foto.png") {
		t.Fatalf("arquivos: %s", got)
	}
	if c := e.call("GET", "/hosts/fake/labs/web/file?name=conf/app.yml", nil, &f); c != 200 || f.Content != "a: 1\n" {
		t.Fatalf("ler: %d %+v", c, f)
	}
	if c := e.call("PUT", "/hosts/fake/labs/web/file?name=conf/app.yml", map[string]string{"content": "a: 2\n", "hash": f.Hash}, nil); c != 200 {
		t.Fatalf("gravar: %d", c)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "conf", "app.yml")); string(b) != "a: 2\n" {
		t.Errorf("conteúdo: %q", b)
	}
	for _, bad := range []string{"../web/compose.yaml", "/etc/passwd", "foto.png"} {
		if c := e.call("GET", "/hosts/fake/labs/web/file?name="+url.QueryEscape(bad), nil, nil); c != http.StatusNotFound {
			t.Errorf("%s deveria ser recusado, veio %d", bad, c)
		}
	}
	var r struct{ Services []string }
	e.call("POST", "/hosts/fake/labs/web/file/restart?name=conf/app.yml", nil, &r)
	if strings.Join(r.Services, ",") != "app" {
		t.Errorf("serviços a reiniciar: %v", r.Services)
	}
}

func TestWebApps(t *testing.T) {
	e := newEnv(t)
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/grafana/" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, "<html><head><title>Grafana &amp; cia</title></head></html>")
	}))
	defer plain.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") }))
	defer secure.Close()
	raw, _ := net.Listen("tcp", "127.0.0.1:0") // answers nothing
	defer raw.Close()
	go func() {
		for {
			c, err := raw.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	port := func(u string) int {
		_, p, _ := net.SplitHostPort(strings.TrimPrefix(strings.TrimPrefix(u, "http://"), "https://"))
		n, _ := strconv.Atoi(p)
		return n
	}
	pp, sp, rp := port(plain.URL), port(secure.URL), raw.Addr().(*net.TCPAddr).Port
	c := &e.fake.cs[0]
	c.Labels["gantry.path"] = "/grafana"
	c.Ports = append(c.Ports, engine.Port{PublicPort: pp, PrivatePort: 3000, Type: "tcp"})
	e.fake.cs[2].Ports = append(e.fake.cs[2].Ports, engine.Port{PublicPort: sp, PrivatePort: 443, Type: "tcp"}, engine.Port{PublicPort: rp, PrivatePort: 5432, Type: "tcp"})
	var r struct{ Apps map[string]WebApp }
	e.call("GET", "/hosts/fake/web?fresh=1", nil, &r)
	g := r.Apps[strconv.Itoa(pp)]
	if g.Scheme != "http" || g.Path != "/grafana" || g.Title != "Grafana & cia" || g.Owner != "web-app-1" || g.Lab != "web" {
		t.Errorf("grafana: %+v", g)
	}
	if s := r.Apps[strconv.Itoa(sp)]; s.Scheme != "https" || s.Owner != "solto" {
		t.Errorf("https: %+v", s)
	}
	if _, ok := r.Apps[strconv.Itoa(rp)]; ok {
		t.Error("porta sem HTTP não é aplicação web")
	}
}

func TestNetOverride(t *testing.T) {
	p, _ := composex.Parse([]byte("services:\n  app:\n    image: x\n  db:\n    image: y\n"))
	got := netOverride(p, &vms.Net{IPs: map[string]string{"web": "10.213.0.100", "cache": "10.213.0.101"}})
	for _, want := range []string{"  app:\n    extra_hosts:\n      - \"cache:10.213.0.101\"\n      - \"web:10.213.0.100\"", "  db:\n    extra_hosts:"} {
		if !strings.Contains(got, want) {
			t.Errorf("override sem %q:\n%s", want, got)
		}
	}
}

func TestExportImport(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	_ = os.MkdirAll(filepath.Join(home, "Downloads"), 0o755)
	e := newEnv(t)
	dir := filepath.Join(e.dir, "web")
	_ = os.MkdirAll(filepath.Join(dir, "conf", ".git"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "conf", "a.yml"), []byte("a: 1\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, ".env"), []byte("SENHA=x\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "conf", ".git", "HEAD"), []byte("x"), 0o644)
	var ex struct {
		Path string
		Size int64
	}
	if c := e.call("POST", "/hosts/fake/labs/web/export", map[string]bool{"env": false}, &ex); c != 200 || !strings.HasPrefix(ex.Path, filepath.Join(home, "Downloads")) {
		t.Fatalf("export %d %+v", c, ex)
	}
	data, _ := os.ReadFile(ex.Path)
	names := tarNames(t, data)
	if !names["compose.yaml"] || !names["conf/a.yml"] || names[".env"] || names["conf/.git/HEAD"] {
		t.Errorf("conteúdo: %v", names)
	}
	// Import into a new folder, without bringing it up.
	dest := filepath.Join(e.dir, "copia")
	req, _ := http.NewRequest("POST", e.url+"/api/hosts/fake/labs/import?dir="+url.QueryEscape(dest), bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+e.srv.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("import: %v %v", err, resp.Status)
	}
	resp.Body.Close()
	if b, _ := os.ReadFile(filepath.Join(dest, "conf", "a.yml")); string(b) != "a: 1\n" {
		t.Errorf("arquivo importado: %q", b)
	}
	// The same folder again is refused.
	req, _ = http.NewRequest("POST", e.url+"/api/hosts/fake/labs/import?dir="+url.QueryEscape(dest), bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+e.srv.Token())
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("pasta ocupada: %d", resp.StatusCode)
	}
	resp.Body.Close()
	// With replace, the old folder is kept aside and the package comes in.
	_ = os.WriteFile(filepath.Join(dest, "conf", "a.yml"), []byte("mudado\n"), 0o644)
	req, _ = http.NewRequest("POST", e.url+"/api/hosts/fake/labs/import?replace=1&dir="+url.QueryEscape(dest), bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+e.srv.Token())
	resp, _ = http.DefaultClient.Do(req)
	var rep struct{ Kept string }
	_ = json.NewDecoder(resp.Body).Decode(&rep)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(rep.Kept, dest+".antes-") {
		t.Fatalf("substituir: %d %+v", resp.StatusCode, rep)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "conf", "a.yml")); string(b) != "a: 1\n" {
		t.Errorf("depois de substituir: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(rep.Kept, "conf", "a.yml")); string(b) != "mudado\n" {
		t.Errorf("a pasta antiga não ficou guardada: %q", b)
	}
	// A package that escapes its folder is refused.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "../fora.txt", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("x"))
	tw.Close()
	gz.Close()
	if err := untar(&buf, filepath.Join(e.dir, "mal")); err == nil {
		t.Error("caminho para fora deveria falhar")
	}
	// Nor through a symlink that already exists in the folder.
	proj := filepath.Join(e.dir, "comlink")
	os.MkdirAll(proj, 0o755)
	outside := t.TempDir()
	os.Symlink(outside, filepath.Join(proj, "docs"))
	buf.Reset()
	gz = gzip.NewWriter(&buf)
	tw = tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "docs/pego.txt", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("x"))
	tw.Close()
	gz.Close()
	_ = untar(&buf, proj)
	if _, err := os.Stat(filepath.Join(outside, "pego.txt")); err == nil {
		t.Error("escreveu fora da pasta pelo link simbólico")
	}
}

func tarNames(t *testing.T, data []byte) map[string]bool {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	out := map[string]bool{}
	for {
		h, err := tr.Next()
		if err != nil {
			return out
		}
		out[strings.TrimSuffix(h.Name, "/")] = true
	}
}

func TestCopyLabToHost(t *testing.T) {
	e := newEnv(t)
	dest := filepath.Join(e.dir, "no-servidor")
	var r map[string]any
	if c := e.call("POST", "/hosts/fake/labs/web/copy", map[string]any{"target": "fake", "dir": dest, "up": false}, &r); c != 200 || r["dir"] != dest {
		t.Fatalf("copy %d %v", c, r)
	}
	if _, err := os.Stat(filepath.Join(dest, "compose.yaml")); err != nil {
		t.Errorf("compose não foi copiado: %v", err)
	}
	if c := e.call("POST", "/hosts/fake/labs/web/copy", map[string]any{"target": "outro", "dir": dest}, nil); c != http.StatusNotFound {
		t.Errorf("destino inexistente: %d", c)
	}
}

func TestAlerts(t *testing.T) {
	e := newEnv(t)
	var r struct {
		Prometheus bool
		Alerts     []obs.Alert
	}
	e.call("GET", "/hosts/fake/alerts", nil, &r)
	if r.Prometheus || len(r.Alerts) != 0 {
		t.Fatalf("sem Prometheus não há alertas: %+v", r)
	}
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/api/v1/status/buildinfo":
			fmt.Fprint(w, `{"status":"success","data":{"version":"2.53.0"}}`)
		case "/api/v1/alerts":
			fmt.Fprint(w, `{"status":"success","data":{"alerts":[
				{"labels":{"alertname":"VMWebFora","severity":"critico"},"annotations":{"summary":"O Traefik não alcança o Nginx"},"state":"firing","activeAt":"2026-10-12T20:00:00Z"},
				{"labels":{"alertname":"MemoriaAlta"},"annotations":{},"state":"pending","activeAt":"2026-10-12T20:01:00Z"}]}}`)
		default:
			http.NotFound(w, req)
		}
	}))
	defer prom.Close()
	if c := e.call("PUT", "/hosts/fake/obs", map[string]string{"prometheus": prom.URL}, nil); c != 200 {
		t.Fatalf("obs: %d", c)
	}
	e.call("GET", "/hosts/fake/alerts", nil, &r)
	if !r.Prometheus || len(r.Alerts) != 2 || r.Alerts[0].Name != "VMWebFora" || r.Alerts[0].Severity != "critico" || r.Alerts[0].Summary == "" || r.Alerts[1].State != "pending" {
		t.Errorf("alertas: %+v", r)
	}
}

func TestParseConfigHashes(t *testing.T) {
	out := "\x1b[4m>>>> Executing external compose provider \"/usr/local/bin/docker-compose\". Please see podman-compose(1) for how to disable this message. <<<<\n\n\x1b[0mverificador 3f2a\nweb 9b1c\n"
	got := parseConfigHashes(out)
	if len(got) != 2 || got["verificador"] != "3f2a" || got["web"] != "9b1c" {
		t.Errorf("%q", got)
	}
}

// A VM that only exists in the file does not count: after Remover the
// project must show 0 so the app offers to forget it.
func TestRemovedVMsDoNotCount(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	e := newEnv(t)
	dir := t.TempDir()
	compose := filepath.Join(dir, "compose.yaml")
	_ = os.WriteFile(compose, []byte("x-vms:\n  a:\n    image: debian-12\n  b:\n    image: debian-12\n"), 0o644)
	e.srv.store.SetLab("fake", "so-vms", dir, []string{compose}, nil, []string{"a", "b"})
	var ls []labs.Lab
	e.call("GET", "/hosts/fake/labs", nil, &ls)
	for _, l := range ls {
		if l.ID != "so-vms" {
			continue
		}
		if len(l.VMs) != 2 || l.Total != 0 || l.Running != 0 {
			t.Fatalf("projeto sem VMs criadas: total %d, rodando %d, vms %+v", l.Total, l.Running, l.VMs)
		}
		return
	}
	t.Fatalf("o projeto não apareceu: %+v", ls)
}

func TestNormImage(t *testing.T) {
	for in, want := range map[string]string{
		"nginx":                           "docker.io/library/nginx:latest",
		"nginx:alpine":                    "docker.io/library/nginx:alpine",
		"grafana/grafana:11":              "docker.io/grafana/grafana:11",
		"ghcr.io/paperclipai/paperclip":   "ghcr.io/paperclipai/paperclip:latest",
		"localhost:5000/app:2":            "localhost:5000/app:2",
		"docker.io/library/redis:7":       "docker.io/library/redis:7",
		"localhost/homelab-postgres:17-x": "localhost/homelab-postgres:17-x",
	} {
		if got := normImage(in); got != want {
			t.Errorf("normImage(%q) = %q, quero %q", in, got, want)
		}
	}
}

// Project operations run as jobs: the output streams, the same project
// cannot run two at once, and the job can be listed and cancelled.
func TestLabJobs(t *testing.T) {
	e := newEnv(t)
	var r struct{ ID string }
	if c := e.call("POST", "/hosts/fake/labs/web/stop?job=1", nil, &r); c != 200 || r.ID == "" {
		t.Fatalf("job: %d %+v", c, r)
	}
	out, res := e.jobOutput(r.ID)
	if res["ok"] != true || !strings.Contains(out, "Parar o projeto web") || !strings.Contains(out, "parado") {
		t.Fatalf("saída: %q %+v", out, res)
	}
	if e.labs()["web"].Running != 0 {
		t.Error("o projeto devia estar parado")
	}
	// Pull goes through the engine first, with the image list.
	e.call("POST", "/hosts/fake/labs/web/pull?job=1", nil, &r)
	out, _ = e.jobOutput(r.ID)
	if !strings.Contains(out, "baixando demo:1 (1 de 1)") {
		t.Errorf("pull sem o download pelo engine: %q", out)
	}
	var jobs []jobView
	e.call("GET", "/jobs", nil, &jobs)
	if len(jobs) < 2 || jobs[0].Key != "lab:"+e.srv.host("fake").Machine()+"/web" {
		t.Errorf("lista de jobs: %+v", jobs)
	}

	// Same key: refused while running; cancel ends it.
	block := make(chan struct{})
	id, err := e.srv.startJob("k", "teste", func(ctx context.Context, out io.Writer) error {
		close(block)
		<-ctx.Done()
		return ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	<-block
	if _, err := e.srv.startJob("k", "outro", func(context.Context, io.Writer) error { return nil }); err != errJobRunning {
		t.Errorf("mesma chave devia ser recusada: %v", err)
	}
	if _, err := e.srv.startJob("k2", "outro", func(context.Context, io.Writer) error { return nil }); err != nil {
		t.Errorf("outra chave devia rodar: %v", err)
	}
	e.call("DELETE", "/jobs/"+id, nil, nil)
	if _, res := e.jobOutput(id); res["error"] != "cancelado" {
		t.Errorf("cancelar: %+v", res)
	}
}

// jobOutput reads a job stream to the end.
func (e *env) jobOutput(id string) (string, map[string]any) {
	e.t.Helper()
	req, _ := http.NewRequest("GET", e.url+"/api/jobs/"+id+"/stream", nil)
	req.Header.Set("Authorization", "Bearer "+e.srv.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out strings.Builder
	res := map[string]any{}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	event := ""
	for sc.Scan() {
		l := sc.Text()
		switch {
		case strings.HasPrefix(l, "event: "):
			event = strings.TrimPrefix(l, "event: ")
		case strings.HasPrefix(l, "data: "):
			d := strings.TrimPrefix(l, "data: ")
			switch event {
			case "end":
				_ = json.Unmarshal([]byte(d), &res)
				return out.String(), res
			case "":
				b, _ := base64.StdEncoding.DecodeString(d)
				out.Write(b)
			}
		case l == "":
			event = ""
		}
	}
	return out.String(), res
}

func TestSharedHelpers(t *testing.T) {
	for im, want := range map[string]string{
		"postgres:17-alpine":                     "postgres",
		"localhost/homelab-postgres:17-pgvector": "postgres",
		"pgvector/pgvector:pg16":                 "postgres",
		"mariadb:11":                             "mysql",
		"docker.io/library/mysql:8":              "mysql",
		"valkey/valkey:8":                        "redis",
		"rabbitmq:3-management-alpine":           "rabbitmq",
		"mongo:7":                                "mongo",
		"nginx:alpine":                           "",
		"ghcr.io/paperclipai/paperclip:latest":   "",
	} {
		if got := sharedKind(im); got != want {
			t.Errorf("sharedKind(%q) = %q, quero %q", im, got, want)
		}
	}
	if got := sharedIdent("Meu-App.2"); !strings.HasPrefix(got, "g_meu_app_2_") || len(got) != len("g_meu_app_2_")+6 {
		t.Errorf("sharedIdent = %q", got)
	}
	if sharedIdent("my-app") == sharedIdent("my_app") || sharedIdent("postgres") == "postgres" {
		t.Error("nomes parecidos ou reservados não podem dar o mesmo usuário")
	}
	v := map[string]string{"host": "h", "porta": "5432", "usuario": "u", "senha": "s", "banco": "d"}
	if got := sharedURL("postgres", v); got != "postgres://u:s@h:5432/d" {
		t.Errorf("url = %q", got)
	}
}

// Containers that look like databases are listed with their published port.
func TestSharedList(t *testing.T) {
	e := newEnv(t)
	pg := ctr("homelab-postgres", "postgres", "db", "running", nil)
	pg.Image = "localhost/homelab-postgres:17-pgvector"
	pg.Ports = []engine.Port{{PrivatePort: 5432, PublicPort: 5432, Type: "tcp"}}
	e.fake.cs = append(e.fake.cs, pg)
	var list []sharedSvc
	if c := e.call("GET", "/hosts/fake/shared", nil, &list); c != 200 {
		t.Fatalf("status %d", c)
	}
	if len(list) != 1 || list[0].Kind != "postgres" || list[0].Port != 5432 || list[0].Name != "homelab-postgres" {
		t.Fatalf("lista: %+v", list)
	}
	var r map[string]any
	if c := e.call("POST", "/hosts/fake/shared/homelab-postgres/provision", map[string]any{"project": "Inválido!"}, &r); c != 400 {
		t.Errorf("nome inválido: %d", c)
	}
}

func TestRenameTop(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, n := range []string{"data/", "data/a.txt", "outro/b"} {
		_ = tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: 0})
	}
	tw.Close()
	r := renameTop(&buf, "data", "dados")
	tr := tar.NewReader(r)
	var got []string
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		got = append(got, h.Name)
	}
	if strings.Join(got, ",") != "dados/,dados/a.txt,outro/b" {
		t.Errorf("renameTop: %v", got)
	}
}

func TestBackupSchedule(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	e := newEnv(t)
	var sch backupSchedule
	if c := e.call("PUT", "/hosts/fake/labs/web/backups/agenda", map[string]any{"every": 24, "keep": 7}, &sch); c != 200 || sch.Every != 24 {
		t.Fatalf("agenda: %d %+v", c, sch)
	}
	var lst struct {
		Items    []backupItem
		Schedule backupSchedule
	}
	e.call("GET", "/hosts/fake/labs/web/backups", nil, &lst)
	if lst.Schedule.Keep != 7 || len(lst.Items) != 0 {
		t.Errorf("lista: %+v", lst)
	}
	if c := e.call("DELETE", "/hosts/fake/labs/web/backups/..%2Fx.tar.gz", nil, nil); c == 200 {
		t.Error("nome com caminho devia ser recusado")
	}
}

// Passwords move from the .env to the keychain and back; compose gets them.
func TestLabSecrets(t *testing.T) {
	t.Setenv("GANTRY_SECRETS", "arquivo")
	e := newEnv(t)
	dir := filepath.Join(t.TempDir(), "sec")
	var r map[string]any
	c := e.call("POST", "/hosts/fake/labs", map[string]any{"name": "sec", "dir": dir, "compose": "services:\n  a:\n    image: x:1\n    environment: {S: \"${PG_SENHA:?}\"}\n",
		"env": "PG_SENHA=s3nh4\nPORTA=8080\n", "keychain": true}, &r)
	if c != 200 || r["ok"] != true {
		t.Fatalf("criar: %d %+v", c, r)
	}
	b, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if strings.Contains(string(b), "s3nh4") || !strings.Contains(string(b), "PORTA=8080") || !strings.Contains(string(b), "# PG_SENHA: no chaveiro do Gantry") {
		t.Fatalf(".env: %q", b)
	}
	var v struct {
		Stored, Candidates []string
		Store              string
	}
	e.call("GET", "/hosts/fake/labs/sec/segredos", nil, &v)
	if len(v.Stored) != 1 || v.Stored[0] != "PG_SENHA" || !strings.Contains(v.Store, "arquivo") {
		t.Fatalf("segredos: %+v", v)
	}
	// Commands of the project get the value without it being in the file.
	h := e.srv.host("fake")
	out, err := h.Run(e.srv.withSecrets(context.Background(), h, "sec"), "", []string{"sh", "-c", "printf %s \"$PG_SENHA\""}, nil)
	if err != nil || out != "s3nh4" {
		t.Fatalf("injeção: %q %v", out, err)
	}
	p, _, err := e.srv.loadProject(e.srv.withSecrets(context.Background(), h, "sec"), h, []string{filepath.Join(dir, "compose.yaml")})
	if err != nil || p == nil {
		t.Fatalf("loadProject com segredo: %v", err)
	}
	// Back to the .env.
	e.call("POST", "/hosts/fake/labs/sec/segredos", map[string]any{"keys": []string{"PG_SENHA"}, "back": true}, &r)
	b, _ = os.ReadFile(filepath.Join(dir, ".env"))
	if !strings.Contains(string(b), "PG_SENHA=s3nh4") || strings.Contains(string(b), "chaveiro") {
		t.Fatalf(".env de volta: %q", b)
	}
	e.call("GET", "/hosts/fake/labs/sec/segredos", nil, &v)
	if len(v.Stored) != 0 || len(v.Candidates) != 1 {
		t.Errorf("depois de devolver: %+v", v)
	}
}

func TestNewerTags(t *testing.T) {
	tags := []string{"16-alpine", "16.4-alpine", "17-alpine", "18-alpine", "18-bookworm", "18rc1-alpine", "v2.53.0", "v2.53.4", "v2.54.1", "v3.0.0", "v3.1.0-rc.0", "latest",
		"RELEASE.2025-04-22T22-12-26Z", "RELEASE.2025-09-07T16-13-09Z", "11.1.0", "11.6.3", "12.0.2", "1.22", "1.23", "2.0"}
	for _, c := range []struct{ cur, minor, major string }{
		{"16-alpine", "", "18-alpine"},
		{"v2.53.0", "v2.54.1", "v3.0.0"},
		{"11.1.0", "11.6.3", "12.0.2"},
		{"1.22", "1.23", "2.0"},
		{"RELEASE.2025-04-22T22-12-26Z", "RELEASE.2025-09-07T16-13-09Z", ""},
		{"latest", "", ""},
	} {
		mi, ma := newerTags(c.cur, tags)
		if mi != c.minor || ma != c.major {
			t.Errorf("%s: %q %q, quero %q %q", c.cur, mi, ma, c.minor, c.major)
		}
	}
	for in, want := range map[string]imageRef{
		"nginx:1.27":                       {"registry-1.docker.io", "library/nginx", "1.27"},
		"docker.io/grafana/grafana:11.1.0": {"registry-1.docker.io", "grafana/grafana", "11.1.0"},
		"ghcr.io/paperclipai/paperclip":    {"ghcr.io", "paperclipai/paperclip", "latest"},
		"quay.io/minio/minio:RELEASE.x":    {"quay.io", "minio/minio", "RELEASE.x"},
	} {
		if got, ok := parseImageRef(in); !ok || got != want {
			t.Errorf("parseImageRef(%q) = %+v", in, got)
		}
	}
	if _, ok := parseImageRef("localhost/homelab-postgres:17"); ok {
		t.Error("imagem local não tem registro")
	}
}

// The registry answers 401 with a bearer challenge; the app gets a token
// and lists the tags; then switches the tag in the .env or the compose.
func TestLabUpdates(t *testing.T) {
	var reg *httptest.Server
	reg = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			_, _ = w.Write([]byte(`{"token":"t1"}`))
		case r.Header.Get("Authorization") != "Bearer t1":
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+reg.URL+`/token",service="x",scope="repository:demo/app:pull"`)
			w.WriteHeader(401)
		case strings.HasSuffix(r.URL.Path, "/tags/list"):
			_, _ = w.Write([]byte(`{"tags":["1.0","1.2","2.0","latest"]}`))
		}
	}))
	defer reg.Close()
	registryScheme = "http"
	defer func() { registryScheme = "https" }()
	host := strings.TrimPrefix(reg.URL, "http://")
	e := newEnv(t)
	dir := filepath.Join(t.TempDir(), "up")
	compose := "services:\n  a:\n    image: " + host + "/demo/app:1.0\n  b:\n    image: " + host + "/demo/app:${VERSAO_B:-1.0}\n"
	var r map[string]any
	e.call("POST", "/hosts/fake/labs", map[string]any{"name": "up", "dir": dir, "compose": compose}, &r)
	var ups []imageUpdate
	if c := e.call("GET", "/hosts/fake/labs/up/atualizacoes", nil, &ups); c != 200 || len(ups) != 2 {
		t.Fatalf("atualizações: %d %+v", c, ups)
	}
	if ups[0].Minor != "1.2" || ups[0].Major != "2.0" || ups[0].Var != "" || ups[1].Var != "VERSAO_B" {
		t.Fatalf("resultado: %+v", ups)
	}
	e.call("POST", "/hosts/fake/labs/up/atualizacoes", map[string]any{"service": "a", "tag": "1.2"}, &r)
	e.call("POST", "/hosts/fake/labs/up/atualizacoes", map[string]any{"service": "b", "tag": "2.0"}, &r)
	b, _ := os.ReadFile(filepath.Join(dir, "compose.yaml"))
	env, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if !strings.Contains(string(b), "/demo/app:1.2\n") || !strings.Contains(string(env), "VERSAO_B=2.0") {
		t.Errorf("compose %q / env %q", b, env)
	}
}

func TestImageLine(t *testing.T) {
	src := "services:\n  a:\n    image: ${REG}/app:1.0\n  b:\n    image: ${REG}/app:1.0 # outro\n  c:\n    build: .\n"
	lines := strings.Split(src, "\n")
	if i := imageLine(lines, "b", "${REG}/app:1.0"); i != 4 {
		t.Errorf("linha de b = %d", i)
	}
	if i := imageLine(lines, "c", "${REG}/app:1.0"); i != -1 {
		t.Errorf("c não tem image: %d", i)
	}
}

func TestEnvQuote(t *testing.T) {
	for in, want := range map[string]string{"abc": "abc", "a b": "'a b'", "p$x&<": "'p$x&<'", `it's $5`: `"it's $$5"`} {
		if got := envQuote(in); got != want {
			t.Errorf("envQuote(%q) = %s, quero %s", in, got, want)
		}
	}
}

// The system dialog answers with a path, or the user cancels.
func TestPick(t *testing.T) {
	e := newEnv(t)
	old := pickerCmd
	defer func() { pickerCmd = old }()
	var got pickReq
	pickerCmd = func(_ context.Context, q pickReq) (string, error) { got = q; return "/escolhido/pasta", nil }
	var r map[string]any
	e.call("POST", "/escolher", map[string]any{"kind": "pasta", "title": "Backups", "dir": t.TempDir()}, &r)
	if r["path"] != "/escolhido/pasta" || got.Kind != "pasta" || got.Title != "Backups" {
		t.Fatalf("escolher: %+v %+v", r, got)
	}
	pickerCmd = func(context.Context, pickReq) (string, error) { return "", errCanceled }
	e.call("POST", "/escolher", map[string]any{"kind": "salvar", "dir": t.TempDir()}, &r)
	if r["canceled"] != true {
		t.Errorf("cancelar: %+v", r)
	}
}

func TestYAMLEdits(t *testing.T) {
	src := "x-vms:\n  web:\n    image: debian-12 # sistema\n    disk: ${DISCO:-10G}\n    ports:\n      - \"8080:80\"\n      - \"8443:443\"\n    ssh: 2222\n    run:\n      - echo oi\n\n  db:\n    image: debian-12\nservices:\n  web:\n    image: nginx\n"
	lines := strings.Split(src, "\n")
	s, e := findBlock(lines, "x-vms", "web")
	if s != 1 || e != 10 {
		t.Fatalf("bloco web: %d %d", s, e)
	}
	if i := fieldLine(lines, s, e, "disk"); i != 3 || fieldValue(lines[i]) != "${DISCO:-10G}" {
		t.Errorf("disk: %d", i)
	}
	if v := fieldValue(lines[2]); v != "debian-12" {
		t.Errorf("valor com comentário: %q", v)
	}
	out, ok := cloneBlock(src, "x-vms", "web", "web2", "ports", "ssh")
	if !ok {
		t.Fatal("clone")
	}
	want := "  web2:\n    image: debian-12 # sistema\n    disk: ${DISCO:-10G}\n    run:\n      - echo oi\n\n  db:"
	if !strings.Contains(out, want) {
		t.Errorf("clone:\n%s", out)
	}
	if strings.Count(out, "8080:80") != 1 || strings.Count(out, "services:\n  web:\n    image: nginx") != 1 {
		t.Errorf("clone mexeu no que não devia:\n%s", out)
	}
}

func TestFolders(t *testing.T) {
	e := newEnv(t)
	var d groupsDoc
	e.call("GET", "/grupos", nil, &d)
	if len(d.Folders) != 1 || d.Items["local"] != "localhost" {
		t.Fatalf("pasta inicial: %+v", d)
	}
	var a, b folder
	e.call("POST", "/grupos", map[string]string{"name": "Americanas"}, &a)
	e.call("POST", "/grupos", map[string]string{"name": "Produção", "parent": a.ID}, &b)
	e.call("PUT", "/grupos-itens", map[string]string{"item": "ssh:x@h:22", "folder": b.ID}, nil)
	var r map[string]any
	if code := e.call("DELETE", "/grupos/"+a.ID, nil, &r); code != 409 {
		t.Fatalf("apagou pasta com subpasta: %d", code)
	}
	if code := e.call("PATCH", "/grupos/"+a.ID, map[string]string{"parent": b.ID}, &r); code != 400 {
		t.Fatalf("pasta dentro dela mesma: %d", code)
	}
	if code := e.call("DELETE", "/grupos/"+b.ID, nil, &r); code != 409 {
		t.Fatalf("apagou pasta com máquina: %d", code)
	}
	e.call("PUT", "/grupos-itens", map[string]string{"item": "ssh:x@h:22", "folder": ""}, nil)
	if code := e.call("DELETE", "/grupos/"+b.ID, nil, &r); code != 200 {
		t.Fatalf("pasta vazia: %d %v", code, r)
	}
	if code := e.call("POST", "/grupos", map[string]string{"name": "a/b"}, &r); code != 400 {
		t.Fatalf("nome com barra: %d", code)
	}
}

func TestClusterProxyAndModes(t *testing.T) {
	e := newEnv(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/version":
			fmt.Fprint(w, `{"gitVersion":"v1.33.0"}`)
		case strings.HasPrefix(r.URL.Path, "/api/v1/namespaces/app/services/web:80/proxy/"):
			fmt.Fprintf(w, "app em %s auth=%s", strings.TrimPrefix(r.URL.Path, "/api/v1/namespaces/app/services/web:80/proxy"), r.Header.Get("Authorization"))
		case r.URL.Path == "/apis/apps/v1/namespaces/app/deployments/web/scale":
			fmt.Fprint(w, `{}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	kc := fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters: [{name: c, cluster: {server: %q}}]\nusers: [{name: u, user: {token: segredo}}]\ncontexts: [{name: lab, context: {cluster: c, user: u}}]\ncurrent-context: lab\n", api.URL)
	var c clusterRec
	if code := e.call("POST", "/clusters", map[string]string{"yaml": kc, "context": "lab", "mode": "leitura"}, &c); code != 200 || c.ID == "" {
		t.Fatalf("adicionar: %d %+v", code, c)
	}
	var r map[string]string
	e.call("POST", "/clusters/"+c.ID+"/abrir", map[string]any{"ns": "app", "name": "web", "port": 80, "path": "/painel", "only": true}, &r)
	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar}
	resp, err := cl.Get(r["url"])
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "app em /painel auth=Bearer segredo" {
		t.Fatalf("proxy: %q", b)
	}
	// The link opens once: used again (from the history, another process
	// that read it), it is refused.
	if resp, err := (&http.Client{}).Get(r["url"]); err != nil || resp.StatusCode != 403 {
		t.Fatalf("link usado de novo: %v %v", err, resp)
	}
	// Leitura: only reads go through the tunnel.
	preq, _ := http.NewRequest("POST", strings.Split(r["url"], "?")[0], strings.NewReader("{}"))
	if resp, err := cl.Do(preq); err != nil || resp.StatusCode != 403 {
		t.Fatalf("POST em leitura: %v %v", err, resp)
	}
	// Without the link's token (another site, another user): refused.
	u, _ := url.Parse(r["url"])
	if resp, err := http.Get("http://" + u.Host + "/painel"); err != nil || resp.StatusCode != 403 {
		t.Fatalf("sem token: %v %v", err, resp)
	}
	req, _ := http.NewRequest("GET", "http://"+u.Host+"/painel", nil)
	req.Host = "evil.example"
	if resp, err := cl.Do(req); err != nil || resp.StatusCode != 403 {
		t.Fatalf("outro Host: %v %v", err, resp)
	}
	var out map[string]any
	if code := e.call("POST", "/clusters/"+c.ID+"/r/deployments/app/web/escalar", map[string]int{"replicas": 2}, &out); code != 403 {
		t.Fatalf("só leitura devia recusar: %d", code)
	}
	if code := e.call("PATCH", "/clusters/"+c.ID, map[string]string{"mode": "confirmar"}, nil); code != 400 {
		t.Fatalf("o modo confirmar não existe mais: %d", code)
	}
	// A cluster saved as "confirmar" (until 0.30) is read as normal.
	saved, _ := os.ReadFile(e.srv.clustersFile())
	os.WriteFile(e.srv.clustersFile(), bytes.ReplaceAll(saved, []byte(`"mode": "leitura"`), []byte(`"mode": "confirmar"`)), 0o600)
	var cl2 []clusterRec
	e.call("GET", "/clusters", nil, &cl2)
	if len(cl2) != 1 || cl2[0].Mode != "livre" {
		t.Fatalf("migração: %+v", cl2)
	}
	if code := e.call("POST", "/clusters/"+c.ID+"/r/deployments/app/web/escalar", map[string]int{"replicas": 2}, &out); code != 200 {
		t.Fatalf("sem perguntar: %d %v", code, out)
	}
	e.call("PATCH", "/clusters/"+c.ID, map[string]string{"mode": "livre"}, nil)
	if code := e.call("POST", "/clusters/"+c.ID+"/r/deployments/app/web/escalar", map[string]int{"replicas": 2}, &out); code != 200 {
		t.Fatalf("livre: %d %v", code, out)
	}
	e.call("DELETE", "/clusters/"+c.ID, nil, nil)
	var list []clusterRec
	e.call("GET", "/clusters", nil, &list)
	if len(list) != 0 {
		t.Fatalf("remover: %+v", list)
	}
}

func TestVMOrder(t *testing.T) {
	vm := func(n string, after ...string) *composex.VM { return &composex.VM{Name: n, After: after} }
	got, err := vmOrder([]*composex.VM{vm("a1", "srv"), vm("a2", "srv"), vm("srv")})
	if err != nil || got[0].Name != "srv" || got[1].Name != "a1" || got[2].Name != "a2" {
		t.Fatalf("%v %v", err, got)
	}
	if _, err := vmOrder([]*composex.VM{vm("a", "b"), vm("b", "a")}); err == nil || !strings.Contains(err.Error(), "círculo") {
		t.Fatalf("círculo: %v", err)
	}
	if _, err := vmOrder([]*composex.VM{vm("a", "x")}); err == nil {
		t.Fatal("dependência que não existe")
	}
}

// The cluster of a k3s project: its kubeconfig is read from the VM (a fake
// ssh here) and the cluster joins the list, next to its machine.
func TestKubeLabRegister(t *testing.T) {
	e := newEnv(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	vmDir := filepath.Join(state, "gantry", "vms", "klab", "k3s")
	os.MkdirAll(vmDir, 0o755)
	os.WriteFile(filepath.Join(vmDir, "disk.qcow2"), nil, 0o644)
	os.WriteFile(filepath.Join(vmDir, "qemu.pid"), []byte(strconv.Itoa(os.Getpid())), 0o644)
	os.WriteFile(filepath.Join(vmDir, "vm.env"), []byte("ssh=22999\n"), 0o644)
	os.WriteFile(filepath.Join(state, "gantry", "vms", "id_ed25519"), []byte("x"), 0o600)
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\ncat <<'FIM'\napiVersion: v1\nkind: Config\nclusters: [{name: default, cluster: {server: \"https://127.0.0.1:6443\", certificate-authority-data: Zm9v}}]\nusers: [{name: default, user: {token: abc}}]\ncontexts: [{name: default, context: {cluster: default, user: default}}]\ncurrent-context: default\nFIM\n"), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	h := e.srv.host("fake")
	groupsMu.Lock()
	d := e.srv.loadGroups()
	d.Items[h.Machine()] = "localhost"
	e.srv.saveGroups(d)
	groupsMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := e.srv.registerKubeLab(ctx, h, labs.Lab{ID: "klab", Name: "klab"}, &composex.KubeSpec{VM: "k3s", Port: 16443}); err != nil {
		t.Fatal(err)
	}
	var list []clusterRec
	e.call("GET", "/clusters", nil, &list)
	if len(list) != 1 || list[0].Source != "gantry" || list[0].Mode != "livre" || list[0].Port != 16443 {
		t.Fatalf("%+v", list)
	}
	b, _ := os.ReadFile(list[0].File)
	if !strings.Contains(string(b), "https://127.0.0.1:16443") || !strings.Contains(string(b), "tls-server-name: 127.0.0.1") {
		t.Fatalf("kubeconfig:\n%s", b)
	}
	var g groupsDoc
	e.call("GET", "/grupos", nil, &g)
	if g.Items["cluster:"+list[0].ID] != "localhost" {
		t.Fatalf("pasta: %+v", g.Items)
	}
	e.srv.dropKubeLab(h, "klab")
	e.call("GET", "/clusters", nil, &list)
	if len(list) != 0 {
		t.Fatalf("remover com o projeto: %+v", list)
	}
}

// The log panel builds its URL as base + "/logs" + query; the container
// must reach the API as its own parameter.
func TestClusterPodLogs(t *testing.T) {
	e := newEnv(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/version" {
			fmt.Fprint(w, `{"gitVersion":"v1.33.0"}`)
			return
		}
		if r.URL.Path != "/api/v1/namespaces/app/pods/web-1/log" {
			http.NotFound(w, r)
			return
		}
		if ct := r.URL.Query().Get("container"); ct != "nginx" {
			http.Error(w, `{"message":"container `+ct+` is not valid"}`, 400)
			return
		}
		if r.URL.Query().Get("previous") == "true" {
			fmt.Fprint(w, "panic: conexão recusada\n")
			return
		}
		fmt.Fprint(w, "start worker process 1\nerro 500 em /api\nstart worker process 2\n")
	}))
	defer api.Close()
	kc := fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters: [{name: c, cluster: {server: %q}}]\nusers: [{name: u, user: {token: t}}]\ncontexts: [{name: lab, context: {cluster: c, user: u}}]\ncurrent-context: lab\n", api.URL)
	var c clusterRec
	if code := e.call("POST", "/clusters", map[string]string{"yaml": kc, "context": "lab", "mode": "leitura"}, &c); code != 200 {
		t.Fatalf("adicionar: %d", code)
	}
	base := "/clusters/" + c.ID + "/pods/app/web-1"
	if got := e.sse(base+"/logs?container=nginx&follow=0", 5*time.Second, "fim do log"); !strings.Contains(got, "start worker process 2") {
		t.Fatalf("logs: %q", got)
	}
	if got := e.sse(base+"/logs?container=nginx&grep=erro&follow=0", 5*time.Second, "fim do log"); !strings.Contains(got, "erro 500") || strings.Contains(got, "worker") {
		t.Fatalf("filtro: %q", got)
	}
	if got := e.sse(base+"/logs?container=nginx&previous=1&follow=0", 5*time.Second, "fim do log"); !strings.Contains(got, "panic: conexão recusada") {
		t.Fatalf("anterior: %q", got)
	}
	// A refusal of the API reaches the panel with its reason.
	if got := e.sse(base+"/logs?container=php&follow=0", 5*time.Second, "zzz"); !strings.Contains(got, "container php is not valid") {
		t.Fatalf("erro: %q", got)
	}
	dest := filepath.Join(t.TempDir(), "web.log")
	var out map[string]any
	if code := e.call("POST", base+"/logs/salvar?container=nginx", map[string]string{"dest": dest}, &out); code != 200 {
		t.Fatalf("salvar: %d %v", code, out)
	}
	if b, _ := os.ReadFile(dest); !strings.Contains(string(b), "start worker process 1") {
		t.Fatalf("arquivo: %q", b)
	}
}

// A followed log that the API server ends while the container goes on is
// read again from the last line, without repeating lines; a restart of the
// container is followed in its new run; the log ends when the pod finishes.
func TestClusterPodLogsFollowReconnects(t *testing.T) {
	e := newEnv(t)
	var mu sync.Mutex
	calls := 0
	var sinces []string
	pod := func(id, phase string) string {
		state := `{"running":{}}`
		if phase == "Succeeded" {
			state = `{"terminated":{"exitCode":0,"reason":"Completed"}}`
		}
		return `{"metadata":{"name":"web-1"},"spec":{"restartPolicy":"Always","containers":[{"name":"nginx"}]},"status":{"phase":"` + phase +
			`","containerStatuses":[{"name":"nginx","containerID":"` + id + `","state":` + state + `}]}}`
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/version":
			fmt.Fprint(w, `{"gitVersion":"v1.33.0"}`)
		case "/api/v1/namespaces/app/pods/web-1":
			switch {
			case calls < 2:
				fmt.Fprint(w, pod("containerd://a", "Running"))
			case calls == 2:
				fmt.Fprint(w, pod("containerd://b", "Running"))
			default:
				fmt.Fprint(w, pod("containerd://b", "Succeeded"))
			}
		case "/api/v1/namespaces/app/pods/web-1/log":
			if r.URL.Query().Get("timestamps") != "true" || r.URL.Query().Get("follow") != "true" {
				t.Errorf("query: %s", r.URL.RawQuery)
			}
			calls++
			sinces = append(sinces, r.URL.Query().Get("sinceTime"))
			switch calls {
			case 1:
				fmt.Fprint(w, "2026-10-14T10:00:00.1Z um\n2026-10-14T10:00:01.5Z dois\n2026-10-14T10:00:02.25Z a\n2026-10-14T10:00:02.25Z b\n")
			case 2:
				// Read again from 10:00:02.25: the API repeats that instant.
				fmt.Fprint(w, "2026-10-14T10:00:02.25Z a\n2026-10-14T10:00:02.25Z b\n2026-10-14T10:00:03Z c\n")
			default:
				fmt.Fprint(w, "2026-10-14T10:05:00Z nova execução\n")
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	kc := fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters: [{name: c, cluster: {server: %q}}]\nusers: [{name: u, user: {token: t}}]\ncontexts: [{name: lab, context: {cluster: c, user: u}}]\ncurrent-context: lab\n", api.URL)
	var c clusterRec
	if code := e.call("POST", "/clusters", map[string]string{"yaml": kc, "context": "lab", "mode": "leitura"}, &c); code != 200 {
		t.Fatalf("adicionar: %d", code)
	}
	start := time.Now()
	got := e.sse("/clusters/"+c.ID+"/pods/app/web-1/logs?container=nginx", 15*time.Second, "end")
	if time.Since(start) > 10*time.Second {
		t.Fatalf("o log não terminou quando o pod terminou")
	}
	want := "um\ndois\na\nb\nc\n\x1b[2m— o container reiniciou —\x1b[0m\nnova execução\n"
	if got != want {
		t.Fatalf("log:\n%q\nquero\n%q", got, want)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sinces) != 3 || sinces[0] != "" || sinces[1] != "2026-10-14T10:00:02.25Z" || sinces[2] != "" {
		t.Fatalf("sinceTime: %q", sinces)
	}
}

// The details of an object: managedFields and the values of a Secret
// never come; a Deployment brings its pods (by its selector) and events.
func TestClusterDetail(t *testing.T) {
	e := newEnv(t)
	var podQuery string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/version":
			fmt.Fprint(w, `{"gitVersion":"v1.33.0"}`)
		case "/api/v1/namespaces/app/secrets/db":
			fmt.Fprint(w, `{"kind":"Secret","type":"Opaque","metadata":{"name":"db","namespace":"app","managedFields":[{"x":1}]},"data":{"senha":"czNncjNkMA=="}}`)
		case "/apis/apps/v1/namespaces/app/deployments/web":
			fmt.Fprint(w, `{"kind":"Deployment","metadata":{"name":"web","namespace":"app","uid":"u1","annotations":{"kubectl.kubernetes.io/last-applied-configuration":"{}","dono":"time"}},"spec":{"replicas":2,"selector":{"matchLabels":{"app":"web","tier":"front"}}}}`)
		case "/api/v1/namespaces/app/pods":
			if r.URL.Query().Get("watch") == "1" {
				time.Sleep(200 * time.Millisecond)
				return
			}
			podQuery = r.URL.Query().Get("limit")
			fmt.Fprint(w, `{"metadata":{"resourceVersion":"1"},"items":[
			  {"metadata":{"name":"web-1","namespace":"app","uid":"p1","labels":{"app":"web","tier":"front","x":"y"}},"spec":{"containers":[{"name":"nginx"}]},"status":{"phase":"Running","containerStatuses":[{"name":"nginx","ready":true,"state":{"running":{}}}]}},
			  {"metadata":{"name":"outro-1","namespace":"app","uid":"p2","labels":{"app":"web"}},"spec":{"containers":[{"name":"nginx"}]},"status":{"phase":"Running"}}]}`)
		case "/api/v1/namespaces/app/events":
			if r.URL.Query().Get("watch") == "1" {
				time.Sleep(200 * time.Millisecond)
				return
			}
			fmt.Fprint(w, `{"metadata":{"resourceVersion":"1"},"items":[
			  {"metadata":{"name":"e1","namespace":"app","uid":"e1"},"type":"Normal","reason":"ScalingReplicaSet","message":"Scaled up","count":1,"involvedObject":{"kind":"Deployment","name":"web"}},
			  {"metadata":{"name":"e2","namespace":"app","uid":"e2"},"type":"Normal","reason":"Outro","message":"de outro","count":1,"involvedObject":{"kind":"Deployment","name":"web2"}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	kc := fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters: [{name: c, cluster: {server: %q}}]\nusers: [{name: u, user: {token: t}}]\ncontexts: [{name: lab, context: {cluster: c, user: u}}]\ncurrent-context: lab\n", api.URL)
	var c clusterRec
	if code := e.call("POST", "/clusters", map[string]string{"yaml": kc, "context": "lab", "mode": "livre"}, &c); code != 200 {
		t.Fatalf("adicionar: %d", code)
	}
	var sec struct {
		Object     map[string]any `json:"object"`
		SecretKeys map[string]int `json:"secretKeys"`
	}
	if code := e.call("GET", "/clusters/"+c.ID+"/r/secrets/app/db/detalhe", nil, &sec); code != 200 {
		t.Fatalf("secret: %d", code)
	}
	if _, has := sec.Object["data"]; has || sec.SecretKeys["senha"] != 7 {
		t.Fatalf("o valor do Secret veio: %+v %v", sec.Object, sec.SecretKeys)
	}
	if md := sec.Object["metadata"].(map[string]any); md["managedFields"] != nil {
		t.Fatal("managedFields veio")
	}
	var dep struct {
		Object map[string]any `json:"object"`
		Pods   []struct{ Name string }
		Events []struct{ Cells []string }
	}
	if code := e.call("GET", "/clusters/"+c.ID+"/r/deployments/app/web/detalhe", nil, &dep); code != 200 {
		t.Fatalf("deployment: %d", code)
	}
	if podQuery != "500" || len(dep.Pods) != 1 || dep.Pods[0].Name != "web-1" {
		t.Fatalf("pods: %q %+v", podQuery, dep.Pods)
	}
	if len(dep.Events) != 1 || dep.Events[0].Cells[1] != "ScalingReplicaSet" {
		t.Fatalf("eventos: %+v", dep.Events)
	}
	ann := dep.Object["metadata"].(map[string]any)["annotations"].(map[string]any)
	if _, has := ann["kubectl.kubernetes.io/last-applied-configuration"]; has || ann["dono"] != "time" {
		t.Fatalf("annotations: %v", ann)
	}
}

// What the account may not do disappears from the screens: the list says it.
func TestClusterPerms(t *testing.T) {
	e := newEnv(t)
	var asked int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/version":
			fmt.Fprint(w, `{"gitVersion":"v1.33.0"}`)
		case "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews":
			atomic.AddInt32(&asked, 1)
			var req struct {
				Spec struct {
					ResourceAttributes struct{ Verb, Resource, Subresource string } `json:"resourceAttributes"`
				} `json:"spec"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			a := req.Spec.ResourceAttributes
			// A viewer: reads and logs, nothing else.
			ok := a.Verb == "get" && (a.Subresource == "log" || a.Subresource == "")
			fmt.Fprintf(w, `{"status":{"allowed":%v}}`, ok)
		case "/api/v1/namespaces/app/pods":
			fmt.Fprint(w, `{"items":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	kc := fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters: [{name: c, cluster: {server: %q}}]\nusers: [{name: u, user: {token: t}}]\ncontexts: [{name: lab, context: {cluster: c, user: u}}]\ncurrent-context: lab\n", api.URL)
	var c clusterRec
	if code := e.call("POST", "/clusters", map[string]string{"yaml": kc, "context": "lab", "mode": "livre"}, &c); code != 200 {
		t.Fatalf("adicionar: %d", code)
	}
	var out struct {
		Can map[string]bool `json:"can"`
	}
	e.call("GET", "/clusters/"+c.ID+"/r/pods?ns=app", nil, &out)
	if !out.Can["logs"] || out.Can["exec"] || out.Can["delete"] || out.Can["update"] || out.Can["portforward"] {
		t.Fatalf("permissões: %v", out.Can)
	}
	n := atomic.LoadInt32(&asked)
	e.call("GET", "/clusters/"+c.ID+"/r/pods?ns=app", nil, &out)
	if atomic.LoadInt32(&asked) != n {
		t.Fatal("perguntou de novo em vez de usar a resposta guardada")
	}
}

// Port-forward: a local TCP port reaches the pod (through the fake
// kubelet protocol), shows in the list and closes.
func TestClusterPortForward(t *testing.T) {
	e := newEnv(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/version":
			fmt.Fprint(w, `{"gitVersion":"v1.33.0"}`)
		case "/api/v1/namespaces/db/pods/pg-0":
			fmt.Fprint(w, `{"metadata":{"name":"pg-0"}}`)
		case "/api/v1/namespaces/db/pods/pg-0/portforward":
			key := r.Header.Get("Sec-WebSocket-Key")
			sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
			hj, _ := w.(http.Hijacker)
			conn, brw, _ := hj.Hijack()
			defer conn.Close()
			fmt.Fprintf(brw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\nSec-WebSocket-Protocol: v4.channel.k8s.io\r\n\r\n", base64.StdEncoding.EncodeToString(sum[:]))
			brw.Flush()
			send := func(p []byte) { conn.Write(append([]byte{0x82, byte(len(p))}, p...)) }
			send([]byte{0, 0x38, 0x15})
			send([]byte{1, 0x38, 0x15})
			for {
				var h [2]byte
				if _, err := io.ReadFull(brw, h[:]); err != nil || h[0]&0x0f == 8 {
					return
				}
				n := int(h[1] & 0x7f)
				var mask [4]byte
				io.ReadFull(brw, mask[:])
				p := make([]byte, n)
				io.ReadFull(brw, p)
				for i := range p {
					p[i] ^= mask[i%4]
				}
				if len(p) > 0 && p[0] == 0 {
					send(append([]byte{0}, append([]byte("pg:"), p[1:]...)...))
				}
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	kc := fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters: [{name: c, cluster: {server: %q}}]\nusers: [{name: u, user: {token: t}}]\ncontexts: [{name: lab, context: {cluster: c, user: u}}]\ncurrent-context: lab\n", api.URL)
	var c clusterRec
	if code := e.call("POST", "/clusters", map[string]string{"yaml": kc, "context": "lab", "mode": "leitura"}, &c); code != 200 {
		t.Fatalf("adicionar: %d", code)
	}
	var f pfInfo
	if code := e.call("POST", "/clusters/"+c.ID+"/encaminhar", map[string]any{"ns": "db", "name": "pg-0", "port": 5432}, &f); code != 403 {
		t.Fatalf("só leitura devia recusar: %d", code)
	}
	e.call("PATCH", "/clusters/"+c.ID, map[string]string{"mode": "livre"}, nil)
	if code := e.call("POST", "/clusters/"+c.ID+"/encaminhar", map[string]any{"ns": "db", "name": "nao-existe", "port": 5432}, &f); code != 404 {
		t.Fatalf("pod errado: %d", code)
	}
	if code := e.call("POST", "/clusters/"+c.ID+"/encaminhar", map[string]any{"ns": "db", "name": "pg-0", "port": 5432}, &f); code != 200 || f.Local == 0 {
		t.Fatalf("encaminhar: %d %+v", code, f)
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", f.Local), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	conn.Write([]byte("SELECT 1"))
	buf := make([]byte, 64)
	n, _ := conn.Read(buf)
	if string(buf[:n]) != "pg:SELECT 1" {
		t.Fatalf("eco: %q", buf[:n])
	}
	var list []pfInfo
	e.call("GET", "/encaminhamentos?cluster="+c.ID, nil, &list)
	if len(list) != 1 || list[0].Conns != 1 || list[0].Total != 1 {
		t.Fatalf("lista: %+v", list)
	}
	conn.Close()
	if code := e.call("DELETE", "/encaminhamentos/"+f.ID, nil, nil); code != 200 {
		t.Fatalf("fechar: %d", code)
	}
	if c2, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", f.Local), time.Second); err == nil {
		c2.Close()
		t.Fatal("a porta continuou aberta")
	}
	// Switching to read-only closes the forwards (they let writes through).
	e.call("POST", "/clusters/"+c.ID+"/encaminhar", map[string]any{"ns": "db", "name": "pg-0", "port": 5432}, &f)
	e.call("PATCH", "/clusters/"+c.ID, map[string]string{"mode": "leitura"}, nil)
	e.call("GET", "/encaminhamentos?cluster="+c.ID, nil, &list)
	if len(list) != 0 {
		t.Fatalf("só leitura devia fechar os encaminhamentos: %+v", list)
	}
	e.call("PATCH", "/clusters/"+c.ID, map[string]string{"mode": "livre"}, nil)
	// Removing the cluster closes its forwards.
	e.call("POST", "/clusters/"+c.ID+"/encaminhar", map[string]any{"ns": "db", "name": "pg-0", "port": 5432}, &f)
	e.call("DELETE", "/clusters/"+c.ID, nil, nil)
	e.call("GET", "/encaminhamentos", nil, &list)
	if len(list) != 0 {
		t.Fatalf("ficou aberto depois de remover o cluster: %+v", list)
	}
}

// helmSecret builds a Helm release Secret (base64 of gzip of JSON, inside
// the base64 of the Secret's data).
func helmSecret(name, ns string, rev int, status string, rel map[string]any) string {
	js, _ := json.Marshal(rel)
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write(js)
	zw.Close()
	inner := base64.StdEncoding.EncodeToString(gz.Bytes())
	data := base64.StdEncoding.EncodeToString([]byte(inner))
	return fmt.Sprintf(`{"metadata":{"name":"sh.helm.release.v1.%s.v%d","namespace":%q,"creationTimestamp":"2026-10-01T10:00:00Z","labels":{"owner":"helm","name":%q,"version":"%d","status":%q}},"data":{"release":%q}}`,
		name, rev, ns, name, rev, status, data)
}

func TestClusterHelmRelease(t *testing.T) {
	e := newEnv(t)
	rel := func(ver, desc string) map[string]any {
		rc := 2
		if ver == "15.0.0" {
			rc = 1
		}
		return map[string]any{"chart": map[string]any{"metadata": map[string]any{"name": "nginx", "version": ver, "appVersion": "1.25"}, "values": map[string]any{"replicaCount": 1}},
			"info": map[string]any{"description": desc, "notes": "Acesse em http://loja"}, "config": map[string]any{"replicaCount": rc, "senha": "s3gr3d0"},
			"manifest": "apiVersion: v1\nkind: Service\nmetadata:\n  name: loja\n"}
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/version":
			fmt.Fprint(w, `{"gitVersion":"v1.33.0"}`)
		case "/api/v1/namespaces/web/secrets":
			if r.URL.Query().Get("labelSelector") != "owner=helm,name=loja" {
				fmt.Fprint(w, `{"items":[]}`)
				return
			}
			fmt.Fprintf(w, `{"items":[%s,%s]}`, helmSecret("loja", "web", 1, "superseded", rel("15.0.0", "Install complete")), helmSecret("loja", "web", 2, "deployed", rel("15.1.0", "Upgrade complete")))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	kc := fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters: [{name: c, cluster: {server: %q}}]\nusers: [{name: u, user: {token: t}}]\ncontexts: [{name: lab, context: {cluster: c, user: u}}, {name: outro, context: {cluster: c, user: u}}]\ncurrent-context: lab\n", api.URL)
	var c clusterRec
	if code := e.call("POST", "/clusters", map[string]string{"yaml": kc, "context": "lab", "mode": "leitura"}, &c); code != 200 {
		t.Fatalf("adicionar: %d", code)
	}
	var out struct {
		Release kube.HelmDetail `json:"release"`
	}
	e.call("GET", "/clusters/"+c.ID+"/helm/web/loja", nil, &out)
	if len(out.Release.History) != 2 || out.Release.History[0].Revision != 2 || out.Release.History[1].Description != "Install complete" {
		t.Fatalf("histórico: %+v", out.Release.History)
	}
	if out.Release.Values != nil || out.Release.Manifest != "" {
		t.Fatal("em só leitura os values (com senhas) não deviam vir")
	}
	e.call("PATCH", "/clusters/"+c.ID, map[string]string{"mode": "livre"}, nil)
	e.call("GET", "/clusters/"+c.ID+"/helm/web/loja", nil, &out)
	if out.Release.Values["senha"] != "s3gr3d0" || !strings.Contains(out.Release.Manifest, "kind: Service") || out.Release.Notes == "" {
		t.Fatalf("conteúdo: %+v", out.Release)
	}
	if out.Release.Revision != 2 || out.Release.Defaults["replicaCount"] != float64(1) {
		t.Fatalf("revisão e padrões do chart: %+v", out.Release)
	}
	// Another revision: its own content; one Helm no longer keeps: 404.
	var old struct {
		Release kube.HelmDetail `json:"release"`
	}
	e.call("GET", "/clusters/"+c.ID+"/helm/web/loja?rev=1", nil, &old)
	if old.Release.Revision != 1 || old.Release.Values["replicaCount"] != float64(1) || len(old.Release.History) != 2 {
		t.Fatalf("revisão 1: %+v", old.Release)
	}
	if code := e.call("GET", "/clusters/"+c.ID+"/helm/web/loja?rev=7", nil, nil); code != 404 {
		t.Fatalf("revisão que não existe: %d", code)
	}
	// Rollback runs the helm of this computer with a one-context kubeconfig.
	bin := t.TempDir()
	logf := filepath.Join(bin, "args")
	os.WriteFile(filepath.Join(bin, "helm"), []byte("#!/bin/sh\necho \"$@\" > "+logf+"\nfor a; do case \"$prev\" in --kubeconfig) cp \"$a\" "+logf+".kc;; esac; prev=$a; done\necho 'Rollback was a success!'\n"), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var rb map[string]any
	if code := e.call("POST", "/clusters/"+c.ID+"/helm/web/loja/voltar", map[string]int{"revision": 1}, &rb); code != 200 {
		t.Fatalf("rollback: %d %v", code, rb)
	}
	if code := e.call("POST", "/clusters/"+c.ID+"/helm/web/--kube-apiserver=x/voltar", map[string]int{"revision": 1}, &rb); code != 400 {
		t.Fatalf("nome que parece opção do helm: %d", code)
	}
	args, _ := os.ReadFile(logf)
	if !strings.HasPrefix(string(args), "rollback --namespace web --kubeconfig ") || !strings.HasSuffix(strings.TrimSpace(string(args)), "-- loja 1") {
		t.Fatalf("argumentos: %q", args)
	}
	kcf, _ := os.ReadFile(logf + ".kc")
	if !strings.Contains(string(kcf), api.URL) || strings.Contains(string(kcf), "outro") {
		t.Fatalf("kubeconfig do helm: %s", kcf)
	}
}

func TestKubeHistory(t *testing.T) {
	sm := &kubeSampler{used: time.Now(), pods: map[string][]kubePoint{
		"app/web-1": {{T: 100, C: 10, M: 100}, {T: 115, C: 20, M: 110}},
		"app/web-2": {{T: 100, C: 5, M: 50}, {T: 115, C: 5, M: 60}},
		"app/db-0":  {{T: 100, C: 900, M: 900}},
	}, nodes: map[string][]kubePoint{"n1": {{T: 100, C: 1500, M: 1 << 30}}}}
	samplersMu.Lock()
	samplers["hist"] = sm
	samplersMu.Unlock()
	defer dropSampler("hist")
	if got := kubeHistory("hist", "pods", "app", "web-1", nil); len(got) != 2 || got[1].C != 20 {
		t.Fatalf("pod: %+v", got)
	}
	if got := kubeHistory("hist", "nodes", "", "n1", nil); len(got) != 1 || got[0].C != 1500 {
		t.Fatalf("nó: %+v", got)
	}
	pods := []kube.Row{{Name: "web-1", Namespace: "app"}, {Name: "web-2", Namespace: "app"}}
	got := kubeHistory("hist", "deployments", "app", "web", pods)
	if len(got) != 2 || got[0] != (kubePoint{100, 15, 150}) || got[1] != (kubePoint{115, 25, 170}) {
		t.Fatalf("soma: %+v", got)
	}
	if kubeHistory("outro", "pods", "app", "web-1", nil) != nil {
		t.Fatal("cluster sem amostras devia vir vazio")
	}
}

// In read-only, the YAML of a Secret hides its values, including the copy
// kubectl apply keeps in an annotation.
func TestClusterSecretYAMLReadOnly(t *testing.T) {
	e := newEnv(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/version":
			fmt.Fprint(w, `{"gitVersion":"v1.33.0"}`)
		case "/api/v1/namespaces/app/secrets/db":
			fmt.Fprint(w, `{"apiVersion":"v1","kind":"Secret","metadata":{"name":"db","namespace":"app","annotations":{"kubectl.kubernetes.io/last-applied-configuration":"{\"data\":{\"senha\":\"czNncjNkMA==\"}}"}},"data":{"senha":"czNncjNkMA=="}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	kc := fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters: [{name: c, cluster: {server: %q}}]\nusers: [{name: u, user: {token: t}}]\ncontexts: [{name: lab, context: {cluster: c, user: u}}]\ncurrent-context: lab\n", api.URL)
	var c clusterRec
	if code := e.call("POST", "/clusters", map[string]string{"yaml": kc, "context": "lab", "mode": "leitura"}, &c); code != 200 {
		t.Fatalf("adicionar: %d", code)
	}
	var out map[string]string
	if code := e.call("GET", "/clusters/"+c.ID+"/r/secrets/app/db", nil, &out); code != 200 {
		t.Fatalf("yaml: %d", code)
	}
	if out["yaml"] == "" || !strings.Contains(out["yaml"], "escondido") || strings.Contains(out["yaml"], "czNncjNkMA") {
		t.Fatalf("o valor do Secret apareceu no modo só leitura:\n%s", out["yaml"])
	}
}

// The diagnostic tells versions and the state of each machine, without
// addresses; a handler that panics answers 500 and the panic shows there.
func TestDiagnosticAndPanics(t *testing.T) {
	e := newEnv(t)
	req, _ := http.NewRequest("GET", e.url+"/api/diagnostico", nil)
	req.Header.Set("Authorization", "Bearer "+e.srv.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	got := string(b)
	if resp.StatusCode != 200 || !strings.Contains(got, "# Diagnóstico do Gantry") || !strings.Contains(got, "máquina 1: unix") {
		t.Fatalf("%d\n%s", resp.StatusCode, got)
	}
	if strings.Contains(got, "fake.sock") || strings.Contains(got, e.dir) {
		t.Fatalf("o diagnóstico tem endereços:\n%s", got)
	}
	h := recoverPanics(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { panic("nil por aí") }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/x", nil))
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), "erro interno") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(e.srv.diagnostic(context.Background()), "GET /api/x: nil por aí") {
		t.Fatal("o panic não apareceu no diagnóstico")
	}
}

// A cluster whose kubeconfig logs in with a plugin missing here says so
// in the list (the page shows how to install it).
func TestClusterMissingPlugin(t *testing.T) {
	e := newEnv(t)
	kc := filepath.Join(e.dir, "kc.yaml")
	os.WriteFile(kc, []byte("apiVersion: v1\nkind: Config\nclusters: [{name: c, cluster: {server: \"https://x.invalid\"}}]\nusers: [{name: u, user: {exec: {command: aws, args: [eks, get-token]}}}]\ncontexts: [{name: eks, context: {cluster: c, user: u}}]\ncurrent-context: eks\n"), 0o600)
	b, _ := json.Marshal([]clusterRec{{ID: "abc", Name: "eks", Source: "kubeconfig", File: kc, Context: "eks", Mode: "livre"}})
	os.WriteFile(e.srv.clustersFile(), b, 0o600)
	t.Setenv("PATH", t.TempDir())
	var list []clusterView
	e.call("GET", "/clusters", nil, &list)
	if len(list) != 1 || list[0].Plugin != "aws" || list[0].PluginHint != "brew install awscli" {
		t.Fatalf("%+v", list)
	}
}

// The tools of this computer and the hosts of ~/.ssh/config.
func TestToolsAndSSHConfig(t *testing.T) {
	e := newEnv(t)
	var rep struct {
		OS    string
		Tools []struct{ ID string }
	}
	if code := e.call("GET", "/local/ferramentas", nil, &rep); code != 200 || rep.OS == "" || len(rep.Tools) < 5 {
		t.Fatalf("%d %+v", code, rep)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte("Host fake\n  HostName 10.1.1.1\nHost novo\n  HostName 10.1.1.2\n"), 0o600)
	var hs []struct {
		Alias string
		Added bool
	}
	e.call("GET", "/ssh-config", nil, &hs)
	if len(hs) != 2 || !hs[0].Added || hs[1].Added {
		t.Fatalf("o host fake já está no Gantry; novo não: %+v", hs)
	}
}

// A pasted kubeconfig with a proxy or without the server certificate check
// asks for trust first, saying both, also in English.
func TestClusterPastedRisks(t *testing.T) {
	e := newEnv(t)
	kc := "apiVersion: v1\nkind: Config\nclusters: [{name: c, cluster: {server: https://192.0.2.5:6443, proxy-url: http://192.0.2.9:3128, insecure-skip-tls-verify: true}}]\nusers: [{name: u, user: {token: x}}]\ncontexts: [{name: lab, context: {cluster: c, user: u}}]\ncurrent-context: lab\n"
	var r map[string]string
	if code := e.call("POST", "/clusters", map[string]string{"yaml": kc, "context": "lab"}, &r); code != http.StatusPreconditionFailed {
		t.Fatalf("devia pedir confiança: %d %v", code, r)
	}
	for _, want := range []string{"Este kubeconfig, neste computador:\n", "- manda as conexões com o cluster pelo proxy http://192.0.2.9:3128", "insecure-skip-tls-verify"} {
		if !strings.Contains(r["error"], want) {
			t.Errorf("faltou %q em %q", want, r["error"])
		}
	}
	if en := i18n.ToEnglish(r["error"]); !strings.Contains(en, "This kubeconfig, on this computer:\n") || !strings.Contains(en, "- sends the connections to the cluster through the proxy http://192.0.2.9:3128") || !strings.Contains(en, "does not check the cluster's certificate") {
		t.Errorf("em inglês: %q", en)
	}
	// A plain kubeconfig (token, CA inline) goes straight in.
	plain := "apiVersion: v1\nkind: Config\nclusters: [{name: c, cluster: {server: https://192.0.2.5:6443}}]\nusers: [{name: u, user: {token: x}}]\ncontexts: [{name: lab, context: {cluster: c, user: u}}]\ncurrent-context: lab\n"
	if code := e.call("POST", "/clusters", map[string]string{"yaml": plain, "context": "lab"}, nil); code == http.StatusPreconditionFailed {
		t.Fatalf("kubeconfig simples não devia pedir confiança")
	}
}
