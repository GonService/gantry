package shim

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gantry/internal/engine"
)

func serve(t *testing.T, d Driver) *engine.Client {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "s.sock")
	s := New(d)
	if err := s.Listen(sock); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return engine.New(func(ctx context.Context) (net.Conn, error) {
		var dl net.Dialer
		return dl.DialContext(ctx, "unix", sock)
	})
}

func fake(name string) string {
	p, _ := filepath.Abs(filepath.Join("testdata", name))
	return p
}

func TestNerdctlThroughDockerAPI(t *testing.T) {
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("FAKE_LOG", log)
	c := serve(t, &Nerdctl{Bin: fake("fake-nerdctl"), Namespace: "default"})
	ctx := context.Background()

	v, err := c.Version(ctx)
	if err != nil || v.Runtime() != "nerdctl" || !strings.Contains(v.Version, "containerd 2.3.6") {
		t.Fatalf("version %+v %v", v, err)
	}
	cs, err := c.Containers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 {
		t.Fatalf("want 2 containers (sandbox hidden), got %d: %+v", len(cs), cs)
	}
	web, dns := cs[0], cs[1]
	if web.Name() != "nlab-web-1" || web.State != "running" || web.Labels["com.docker.compose.project"] != "nlab" {
		t.Errorf("web %+v", web)
	}
	if _, ok := web.Labels["nerdctl/name"]; ok {
		t.Error("nerdctl/ labels should be hidden")
	}
	if len(web.Ports) != 1 || web.Ports[0].PublicPort != 8080 || web.Ports[0].PrivatePort != 80 {
		t.Errorf("ports %+v", web.Ports)
	}
	if web.NetworkSettings.Networks["nlab_default"].IPAddress != "10.4.0.2" || len(web.Mounts) != 1 {
		t.Errorf("net/mounts %+v %+v", web.NetworkSettings, web.Mounts)
	}
	if dns.Labels["com.docker.compose.project"] != "k8s-kube-system" || dns.Labels["com.docker.compose.service"] != "coredns-abc/coredns" ||
		dns.State != "exited" || dns.Status != "Exited (2)" || dns.Name() != "coredns-abc_coredns" {
		t.Errorf("k8s container %+v", dns)
	}

	st, err := c.Stats(ctx, "aaa111")
	if err != nil || st.CPU < 12.4 || st.CPU > 12.6 || st.Mem != 10<<20 || st.MemLimit != 1<<30 {
		t.Errorf("stats %+v %v", st, err)
	}
	if err := c.Action(ctx, "aaa111", "stop"); err != nil {
		t.Error(err)
	}
	var ae *engine.APIError
	if err := c.Action(ctx, "missing", "stop"); !errors.As(err, &ae) || ae.Status != 404 {
		t.Errorf("missing container: %v", err)
	}
	if err := c.Remove(ctx, "aaa111", true); err != nil {
		t.Error(err)
	}
	rc, tty, err := c.Logs(ctx, "aaa111", 10, false)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if !tty || string(b) != "linha 1\nlinha 2\n" {
		t.Errorf("logs %q tty=%v", b, tty)
	}
	imgs, err := c.Images(ctx)
	if err != nil || len(imgs) != 1 || imgs[0].RepoTags[0] != "bancada/demo:latest" || imgs[0].Size != 17430000 {
		t.Errorf("images %+v %v", imgs, err)
	}
	vols, _ := c.Volumes(ctx)
	if len(vols) != 1 || vols[0].Labels["com.docker.compose.project"] != "nlab" {
		t.Errorf("volumes %+v", vols)
	}
	nets, _ := c.NetworksByLabel(ctx, "com.docker.compose.project", "nlab")
	other, _ := c.NetworksByLabel(ctx, "com.docker.compose.project", "x")
	if len(nets) != 1 || len(other) != 0 {
		t.Errorf("network filter %+v %+v", nets, other)
	}
	id, err := c.Create(ctx, engine.ContainerSpec{Name: "x1", Image: "demo:1", Cmd: []string{"sleep", "9"}, Ports: []string{"8080:80"}, Env: []string{"A=1"}, Volumes: []string{"v:/d"}, Restart: "always"})
	if err != nil || id != "novo123" {
		t.Errorf("create %q %v", id, err)
	}
	if err := c.CreateVolume(ctx, engine.VolumeSpec{Name: "v2", Labels: map[string]string{"a": "b"}}); err != nil {
		t.Error(err)
	}
	if err := c.CreateNetwork(ctx, engine.NetworkSpec{Name: "n2", Subnet: "10.9.0.0/24", Internal: true}); err != nil {
		t.Error(err)
	}
	var steps []string
	if err := c.Pull(ctx, "demo:2", func(l string) { steps = append(steps, l) }); err != nil || len(steps) != 3 || steps[1] != "layer 2/2" {
		t.Errorf("pull %v %v", steps, err)
	}
	calls, _ := os.ReadFile(log)
	for _, want := range []string{"--namespace default stop aaa111", "--namespace default rm -f aaa111", "logs --tail 10 aaa111",
		"create --name x1 -p 8080:80/tcp -e A=1 -v v:/d --restart always demo:1 sleep 9",
		"volume create --label a=b v2", "network create --subnet 10.9.0.0/24 --internal n2", "pull demo:2"} {
		if !strings.Contains(string(calls), want) {
			t.Errorf("missing call %q in:\n%s", want, calls)
		}
	}
}

func TestFromURL(t *testing.T) {
	d, compose, err := FromURL("nerdctl://k8s.io?address=/run/x.sock&sudo=1")
	n := d.(*Nerdctl)
	if err != nil || n.Namespace != "k8s.io" || n.Address != "/run/x.sock" || !n.Sudo {
		t.Fatalf("%+v %v", n, err)
	}
	if strings.Join(compose, " ") != "sudo -n nerdctl --address /run/x.sock --namespace k8s.io compose" {
		t.Errorf("compose %v", compose)
	}
	d, _, _ = FromURL("k3s://")
	if d.Runtime() != "k3s" || d.(*Nerdctl).Address != K3sSocket {
		t.Errorf("k3s %+v", d)
	}
	if _, _, err := FromURL("lxc://x"); err == nil {
		t.Error("want error")
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]uint64{"736KiB": 736 << 10, "17.43MB": 17430000, "1.5 GiB": 3 << 29, "0B": 0, "8EiB": 0, "": 0} {
		if got := parseSize(in); got != want {
			t.Errorf("%q: %d, want %d", in, got, want)
		}
	}
}

func TestCreateArgs(t *testing.T) {
	s := engine.ContainerSpec{Name: "a", Image: "img", Cmd: []string{"run"}, Entrypoint: []string{"/e", "x"},
		Ports: []string{"80:80"}, Env: []string{"A=1"}, Restart: "on-failure", RestartMax: 2, CPUs: 0.5, Memory: 1 << 20,
		Privileged: true, CapAdd: []string{"NET_ADMIN"}, Labels: map[string]string{"b": "2", "a": "1"}, Tty: true}
	got := strings.Join(createArgs(s), " ")
	want := "--name a -p 80:80 -e A=1 --entrypoint /e -t --memory 1048576 --cpus 0.5 --restart on-failure:2 --cap-add NET_ADMIN --privileged --label a=1 --label b=2 img x run"
	if got != want {
		t.Errorf("nerdctl:\n got %s\nwant %s", got, want)
	}
}

func TestUpdateArgs(t *testing.T) {
	got := strings.Join(updateArgs(engine.Resources{NanoCPUs: 1500000000, Memory: 512 << 20, MemorySwap: 1 << 30, PidsLimit: 100}), " ")
	if got != "--cpus 1.5 --memory 536870912 --memory-swap 1073741824 --pids-limit 100" {
		t.Fatal(got)
	}
	if len(updateArgs(engine.Resources{})) != 0 {
		t.Fatal("sem mudanças, sem argumentos (nada de --restart no)")
	}
}
