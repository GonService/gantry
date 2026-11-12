package cvm

import (
	"bufio"
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLeaseIP(t *testing.T) {
	leases := `{
	name=outra
	ip_address=192.168.64.2
	hw_address=1,52:54:0:1:2:3
	identifier=1,52:54:0:1:2:3
	lease=0x6
}
{
	name=gantry-vm
	ip_address=192.168.64.7
	hw_address=1,52:54:0:a:b:c
	identifier=1,52:54:0:a:b:c
	lease=0x6
}`
	if ip := leaseIP(leases, "52:54:00:0a:0b:0c", "x"); ip != "192.168.64.7" {
		t.Errorf("pelo MAC: %q", ip)
	}
	if ip := leaseIP(leases, "52:54:00:ff:ff:ff", "outra"); ip != "192.168.64.2" {
		t.Errorf("pelo nome: %q", ip)
	}
	if ip := leaseIP(leases, "52:54:00:ff:ff:ff", "nada"); ip != "" {
		t.Errorf("achou o que não existe: %q", ip)
	}
}

func TestWantedPorts(t *testing.T) {
	w := wantedPorts(
		[]pubPort{{IP: "0.0.0.0", Port: 8080, Proto: "tcp"}, {IP: "::", Port: 8080, Proto: "tcp"}, {IP: "127.0.0.1", Port: 5432, Proto: "tcp"}},
		[]pubPort{{IP: "", Port: 53, Proto: "udp"}, {IP: "127.0.0.1", Port: 8080, Proto: "tcp"}, {Port: 9, Proto: "sctp"}},
	)
	if len(w) != 3 || w["tcp:8080"].IP != "" || w["tcp:5432"].IP != "127.0.0.1" || w["udp:53"].IP != "" {
		t.Fatalf("%+v", w)
	}
}

// From the engine's list to a connection: localhost:port on the "Mac" reaches
// the port on the machine's address (here 127.0.0.2).
func TestPortForwarder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	name := "gantry-vm"
	if err := os.MkdirAll(Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	back, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Skip("sem 127.0.0.2 neste sistema")
	}
	defer back.Close()
	port := back.Addr().(*net.TCPAddr).Port
	go func() {
		for {
			c, err := back.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				l, _ := bufio.NewReader(c).ReadString('\n')
				io.WriteString(c, "eco "+l)
			}(c)
		}
	}()
	api, err := net.Listen("unix", DockerSocket(name))
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	go http.Serve(api, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `[{"Ports":[{"IP":"127.0.0.1","PrivatePort":80,"PublicPort":`+strconv.Itoa(port)+`,"Type":"tcp"}]}]`)
	}))
	leases := filepath.Join(home, "leases")
	os.WriteFile(leases, []byte("{\n\tname="+name+"\n\tip_address=127.0.0.2\n\thw_address=1,"+mac(name)+"\n}\n"), 0o644)

	var logb strings.Builder
	f := newPortForwarder(name, Spec{Docker: true}, log.New(&logb, "", 0))
	f.leases = leases
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.loop(ctx)
	deadline := time.Now().Add(6 * time.Second)
	for len(f.Ports()) == 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if got := f.Ports(); len(got) != 1 || got[0] != "tcp:"+strconv.Itoa(port) {
		t.Fatalf("portas abertas: %v (%s)", got, logb.String())
	}
	c, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	io.WriteString(c, "olá\n")
	l, _ := bufio.NewReader(c).ReadString('\n')
	if l != "eco olá\n" {
		t.Fatalf("resposta: %q", l)
	}
}
