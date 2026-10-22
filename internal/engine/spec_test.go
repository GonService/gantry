package engine

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSpecRoundTrip(t *testing.T) {
	in := ContainerSpec{
		Name: "app", Image: "nginx:1", Cmd: []string{"nginx", "-g", "daemon off;"}, Entrypoint: []string{"/entry.sh"},
		WorkingDir: "/srv", User: "1000:1000", Tty: true, OpenStdin: true,
		Env: []string{"A=1"}, Labels: map[string]string{"x": "y"},
		Ports: []string{"53/udp", "127.0.0.1:8080:80/tcp", "9000:9000/tcp"}, PublishAll: true,
		Volumes: []string{"dados:/data", "/etc/hosts:/h:ro"}, Tmpfs: []string{"/run:size=64m", "/tmp"},
		Network: "rede", IPv4: "10.1.0.5", Hostname: "app", Domainname: "lab", MacAddress: "02:42:ac:11:00:02",
		DNS: []string{"1.1.1.1"}, ExtraHosts: []string{"db:10.0.0.2"},
		Restart: "on-failure", RestartMax: 3,
		Privileged: true, Init: true, ReadOnly: true, CapAdd: []string{"NET_ADMIN"}, CapDrop: []string{"MKNOD"},
		Devices: []string{"/dev/fuse", "/dev/sda:/dev/xvda:r"}, Sysctls: map[string]string{"net.ipv4.ip_forward": "1"},
		Memory: 512 << 20, MemoryReservation: 256 << 20, CPUs: 1.5, ShmSize: 128 << 20,
		LogDriver: "json-file", LogOpts: map[string]string{"max-size": "10m"},
	}
	b, _ := json.Marshal(in.CreateBody())
	b2, _ := json.Marshal(map[string]any{"Name": "/app"})
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	var n map[string]any
	_ = json.Unmarshal(b2, &n)
	m["Name"] = n["Name"]
	b, _ = json.Marshal(m)
	out, err := SpecFromJSON(b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Errorf("ida e volta:\n in %+v\nout %+v", in, out)
	}
}

func TestSpecFromInspect(t *testing.T) {
	// The shape of a real `docker inspect`, with a named volume only in Mounts.
	raw := `{"Id":"abc","Name":"/web","Config":{"Image":"nginx","Env":["PATH=/bin"],"ExposedPorts":{"80/tcp":{}},"Tty":false},
	"HostConfig":{"Binds":null,"PortBindings":{"80/tcp":[{"HostIp":"","HostPort":"8080"}]},"NetworkMode":"bridge",
	"RestartPolicy":{"Name":"no","MaximumRetryCount":0},"NanoCpus":0,"Init":null,"LogConfig":{"Type":"json-file","Config":{}}},
	"Mounts":[{"Type":"volume","Name":"dados","Source":"/var/lib/docker/volumes/dados/_data","Destination":"/data","RW":true}],
	"NetworkSettings":{"Ports":{"80/tcp":[{"HostIp":"0.0.0.0","HostPort":"8080"},{"HostIp":"::","HostPort":"8080"}]}}}`
	s, err := SpecFromJSON([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "web" || s.Image != "nginx" || s.Restart != "" || s.Network != "bridge" ||
		!reflect.DeepEqual(s.Ports, []string{"8080:80/tcp"}) || !reflect.DeepEqual(s.Volumes, []string{"dados:/data"}) {
		t.Errorf("%+v", s)
	}
	// nerdctl reports only the live bindings.
	raw = `{"Name":"x","Config":{"Image":"a"},"NetworkSettings":{"Ports":{"80/tcp":[{"HostIp":"0.0.0.0","HostPort":"8080"}]}}}`
	s, _ = SpecFromJSON([]byte(raw))
	if !reflect.DeepEqual(s.Ports, []string{"8080:80/tcp"}) {
		t.Errorf("portas do nerdctl %v", s.Ports)
	}
}

func TestParseBytes(t *testing.T) {
	for in, want := range map[string]int64{"": 0, "512m": 512 << 20, "2g": 2 << 30, "1.5G": 3 << 29, "64MiB": 64 << 20, "1024": 1024, "10k": 10 << 10} {
		if got, err := ParseBytes(in); err != nil || got != want {
			t.Errorf("%q: %d %v", in, got, err)
		}
	}
	if _, err := ParseBytes("muito"); err == nil {
		t.Error("deveria falhar")
	}
}
