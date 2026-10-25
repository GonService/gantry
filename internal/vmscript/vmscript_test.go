package vmscript

import (
	"gantry/internal/vpcsbin"

	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// image runs "vmctl image" against a local server that publishes an image
// and its checksum list.
func image(t *testing.T, sums string) (string, error) {
	t.Helper()
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("sem curl")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/img/base.qcow2":
			w.Write([]byte("imagem de teste"))
		case "/img/SUMS":
			w.Write([]byte(sums))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cache := t.TempDir()
	cmd := exec.Command("sh", "-c", Script, "vmctl", "image")
	cmd.Env = append(os.Environ(), "XDG_CACHE_HOME="+cache, "XDG_STATE_HOME="+t.TempDir(),
		"GV_IMAGE="+srv.URL+"/img/base.qcow2", "GV_IMAGE_SUMS="+srv.URL+"/img/SUMS")
	out, err := cmd.CombinedOutput()
	if err == nil {
		if _, e := os.Stat(filepath.Join(cache, "gantry/images/base.qcow2")); e != nil {
			t.Fatalf("a imagem não ficou no cache: %v", e)
		}
	} else if _, e := os.Stat(filepath.Join(cache, "gantry/images/base.qcow2")); e == nil {
		t.Fatal("uma imagem recusada ficou no cache")
	}
	return string(out), err
}

func TestImageChecksum(t *testing.T) {
	s256 := sha256.Sum256([]byte("imagem de teste"))
	s512 := sha512.Sum512([]byte("imagem de teste"))
	h256, h512 := hex.EncodeToString(s256[:]), hex.EncodeToString(s512[:])
	ok := map[string]string{
		"gnu sha256": "# lista\n" + strings.Repeat("0", 64) + "  outra.qcow2\n" + h256 + " *base.qcow2\n",
		"gnu sha512": h512 + "  base.qcow2\n",
		"bsd sha256": "SHA256 (base.qcow2) = " + strings.ToUpper(h256) + "\n",
	}
	for name, sums := range ok {
		if out, err := image(t, sums); err != nil {
			t.Errorf("%s: %v\n%s", name, err, out)
		}
	}
	bad := map[string]string{
		"errado":  strings.Repeat("a", 64) + "  base.qcow2\n",
		"ausente": h256 + "  outra.qcow2\n",
	}
	for name, sums := range bad {
		out, err := image(t, sums)
		if err == nil {
			t.Errorf("%s: aceitou a imagem", name)
		}
		if !strings.Contains(out, "base.qcow2") {
			t.Errorf("%s: erro sem dizer qual imagem: %s", name, out)
		}
	}
}

// The relay of the cables: what a port sends reaches the port at the other
// end of its cable, and a new table rewires without restarting the relay.
func TestRelay(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("sem python3")
	}
	state := t.TempDir()
	run := func(table string) {
		t.Helper()
		cmd := exec.Command("sh", "-c", Script, "vmctl", "relay")
		cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+state, "GV_PROJECT=lab", "GV_RELAY="+base64.StdEncoding.EncodeToString([]byte(table)))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("relay: %v %s", err, out)
		}
	}
	// Three VM ports (a, b, c), each with its relay port.
	vm := map[string]*net.UDPConn{}
	rport := map[string]int{"a": 39100, "b": 39102, "c": 39104}
	for n, r := range rport {
		c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: r + 1})
		if err != nil {
			t.Skip("porta ocupada:", err)
		}
		defer c.Close()
		vm[n] = c
	}
	defer func() {
		if b, err := os.ReadFile(filepath.Join(state, "gantry/vms/lab/relay.pid")); err == nil {
			exec.Command("kill", strings.TrimSpace(string(b))).Run()
		}
	}()
	line := func(n, peer string) string {
		p := 0
		if peer != "" {
			p = rport[peer]
		}
		return fmt.Sprintf("%d %d %d %s 0\n", rport[n], rport[n]+1, p, n)
	}
	recv := func(n string) string {
		buf := make([]byte, 100)
		vm[n].SetReadDeadline(time.Now().Add(800 * time.Millisecond))
		k, err := vm[n].Read(buf)
		if err != nil {
			return ""
		}
		return string(buf[:k])
	}
	send := func(from, msg string) {
		// A VM sends to its relay port from its own port.
		vm[from].WriteToUDP([]byte(msg), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: rport[from]})
	}
	run(line("a", "b") + line("b", "a") + line("c", ""))
	got := ""
	for i := 0; i < 20 && got == ""; i++ { // the relay is starting
		send("a", "oi b")
		got = recv("b")
	}
	if got != "oi b" {
		t.Fatalf("b recebeu %q", got)
	}
	send("c", "ninguém")
	if g := recv("a") + recv("b"); g != "" {
		t.Errorf("porta sem cabo vazou: %q", g)
	}
	// The cable of a goes to c, without restarting the relay.
	run(line("a", "c") + line("c", "a") + line("b", ""))
	time.Sleep(300 * time.Millisecond)
	send("a", "oi c")
	if g := recv("c"); g != "oi c" {
		t.Errorf("c recebeu %q depois de trocar o cabo", g)
	}
	send("a", "de novo")
	if g := recv("b"); g != "" {
		t.Errorf("b ainda recebe: %q", g)
	}
}

// The relay with an IOL node: frames of a VM port reach the IOL instance
// with the IOL header, and what IOL sends comes back to the VM.
func TestRelayIOL(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("sem python3")
	}
	state := t.TempDir()
	netio := fmt.Sprintf("/tmp/netio%d", os.Getuid())
	os.MkdirAll(netio, 0o755)
	const inst, rid, port = 777, 1777, 0x10 // Ethernet0/1
	iolPath := filepath.Join(netio, "777")
	os.Remove(iolPath)
	iol, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: iolPath, Net: "unixgram"})
	if err != nil {
		t.Skip(err)
	}
	defer func() { iol.Close(); os.Remove(iolPath) }()
	vm, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 39201})
	if err != nil {
		t.Skip(err)
	}
	defer vm.Close()
	table := fmt.Sprintf("#rid %d\n39200 39201 39300 vm 0\n39300 iol:%d:%d 39200 sw 1\n", rid, inst, port)
	cmd := exec.Command("sh", "-c", Script, "vmctl", "relay")
	cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+state, "GV_PROJECT=lab", "GV_RELAY="+base64.StdEncoding.EncodeToString([]byte(table)))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	defer func() {
		if b, err := os.ReadFile(filepath.Join(state, "gantry/vms/lab/relay.pid")); err == nil {
			exec.Command("kill", strings.TrimSpace(string(b))).Run()
		}
	}()
	buf := make([]byte, 200)
	var n int
	for i := 0; i < 20; i++ {
		vm.WriteToUDP([]byte("quadro"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 39200})
		iol.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		if n, err = iol.Read(buf); err == nil {
			break
		}
	}
	want := []byte{0x03, 0x09, 0x06, 0xF1, port, port, 1, 0} // 777, 1777
	if n != 8+len("quadro") || string(buf[:8]) != string(want) || string(buf[8:n]) != "quadro" {
		t.Fatalf("o IOL recebeu % x", buf[:n])
	}
	// IOL answers: destination the relay, source its instance and port.
	msg := append([]byte{0x06, 0xF1, 0x03, 0x09, port, port, 1, 0}, []byte("resposta")...)
	if _, err := iol.WriteToUnix(msg, &net.UnixAddr{Name: filepath.Join(netio, "1777"), Net: "unixgram"}); err != nil {
		t.Fatal(err)
	}
	vm.SetReadDeadline(time.Now().Add(time.Second))
	n, err = vm.Read(buf)
	if err != nil || string(buf[:n]) != "resposta" {
		t.Fatalf("a VM recebeu %q (%v)", buf[:n], err)
	}
}

// TestIOLUp starts a fake IOL program (a script that echoes the console)
// the way up does for a real one, and talks to it over the console port.
func TestIOLUp(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("IOL só em Linux x86_64")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("sem python3")
	}
	dir, state := t.TempDir(), t.TempDir()
	bin := filepath.Join(dir, "iol.bin")
	os.WriteFile(bin, []byte("#!/bin/sh\necho \"IOL $* $(cat NETMAP | wc -l)\"\nexec cat\n"), 0o755)
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	cmd := exec.Command("sh", "-c", Script, "vmctl", "up")
	cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+state, "GV_PROJECT=lab", "GV_NAME=r1", "GV_KIND=iol",
		"GV_IMAGE=cisco-iol", "GV_IMAGE_SRC="+bin, "GV_MEM=256", "GV_CPUS=1", "GV_IOLID=42", "GV_IOLPORTS=5",
		"GV_SERIAL="+strconv.Itoa(port), "GV_NETMAP="+base64.StdEncoding.EncodeToString([]byte("42:0/0 1500:0/0\n")))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	d := filepath.Join(state, "gantry/vms/lab/r1")
	defer func() {
		stop := exec.Command("sh", "-c", Script, "vmctl", "stop")
		stop.Env = append(os.Environ(), "XDG_STATE_HOME="+state, "GV_PROJECT=lab", "GV_NAME=r1")
		stop.Run()
	}()
	var c net.Conn
	var err error
	for i := 0; i < 50; i++ {
		if c, err = net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port)); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte("show ip int brief\r"))
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	got := ""
	buf := make([]byte, 4096)
	for !strings.Contains(got, "show ip int brief") {
		n, err := c.Read(buf)
		if err != nil {
			break
		}
		got += string(buf[:n])
	}
	log, _ := os.ReadFile(filepath.Join(d, "console.log"))
	if !strings.Contains(got, "show ip int brief") || !strings.Contains(string(log), "IOL -e 2 -s 0 -m 256 42 1") {
		t.Fatalf("console %q, log %q", got, log)
	}
	env, _ := os.ReadFile(filepath.Join(d, "vm.env"))
	if !strings.Contains(string(env), "accel=iol") {
		t.Fatalf("vm.env: %s", env)
	}
}

// TestRelayContainer: a container port (ctr) is learned from its packets
// (a keepalive first) and joined to a VM port by the cable.
func TestRelayContainer(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("sem python3")
	}
	state := t.TempDir()
	vm, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 39301})
	if err != nil {
		t.Skip(err)
	}
	defer vm.Close()
	ctr, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Skip(err)
	}
	defer ctr.Close()
	table := "39300 39301 39310 vm 0\n39310 ctr 39300 cli 0\n"
	cmd := exec.Command("sh", "-c", Script, "vmctl", "relay")
	cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+state, "GV_PROJECT=lab", "GV_RELAY="+base64.StdEncoding.EncodeToString([]byte(table)))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	defer func() {
		if b, err := os.ReadFile(filepath.Join(state, "gantry/vms/lab/relay.pid")); err == nil {
			exec.Command("kill", strings.TrimSpace(string(b))).Run()
		}
	}()
	relayCtr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 39310}
	buf := make([]byte, 100)
	got := ""
	for i := 0; i < 25 && got == ""; i++ {
		ctr.WriteToUDP(nil, relayCtr) // keepalive: the relay learns the container
		time.Sleep(50 * time.Millisecond)
		vm.WriteToUDP([]byte("oi container"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 39300})
		ctr.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
		if n, err := ctr.Read(buf); err == nil {
			got = string(buf[:n])
		}
	}
	if got != "oi container" {
		t.Fatalf("o container recebeu %q", got)
	}
	ctr.WriteToUDP([]byte("oi vm"), relayCtr)
	vm.SetReadDeadline(time.Now().Add(time.Second))
	n, err := vm.Read(buf)
	if err != nil || string(buf[:n]) != "oi vm" {
		t.Fatalf("a VM recebeu %q %v", buf[:n], err)
	}
	// The keepalive (empty) is not delivered as a frame.
	ctr.WriteToUDP(nil, relayCtr)
	vm.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if n, err := vm.Read(buf); err == nil {
		t.Fatalf("keepalive entregue: %q", buf[:n])
	}
}

// TestRelayCapture: capture-start makes the relay write the cable's frames
// (both ways) to a pcap; capture-stop stops it.
func TestRelayCapture(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("sem python3")
	}
	state := t.TempDir()
	env := append(os.Environ(), "XDG_STATE_HOME="+state, "GV_PROJECT=lab")
	run := func(cmd string, extra ...string) string {
		t.Helper()
		c := exec.Command("sh", "-c", Script, "vmctl", cmd)
		c.Env = append(append([]string{}, env...), extra...)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v %s", cmd, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	a, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 39401})
	if err != nil {
		t.Skip(err)
	}
	defer a.Close()
	b, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 39403})
	if err != nil {
		t.Skip(err)
	}
	defer b.Close()
	run("relay", "GV_RELAY="+base64.StdEncoding.EncodeToString([]byte("39400 39401 39402 a 0\n39402 39403 39400 b 0\n")))
	defer func() {
		if p, err := os.ReadFile(filepath.Join(state, "gantry/vms/lab/relay.pid")); err == nil {
			exec.Command("kill", strings.TrimSpace(string(p))).Run()
		}
	}()
	time.Sleep(300 * time.Millisecond)
	file := run("capture-start", "GV_PORT=39400")
	if !strings.HasSuffix(file, ".pcap") {
		t.Fatal(file)
	}
	time.Sleep(300 * time.Millisecond)
	buf := make([]byte, 64)
	for _, m := range []struct {
		from *net.UDPConn
		to   int
		msg  string
	}{{a, 39400, "de a"}, {b, 39402, "de b"}} {
		m.from.WriteToUDP([]byte(m.msg), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: m.to})
		other := b
		if m.from == b {
			other = a
		}
		other.SetReadDeadline(time.Now().Add(time.Second))
		if n, err := other.Read(buf); err != nil || string(buf[:n]) != m.msg {
			t.Fatalf("%q %v", buf[:n], err)
		}
	}
	time.Sleep(200 * time.Millisecond)
	data, _ := os.ReadFile(file)
	if len(data) != 24+16+4+16+4 || !strings.Contains(string(data), "de a") || !strings.Contains(string(data), "de b") {
		t.Fatalf("pcap com %d bytes: %q", len(data), data)
	}
	run("capture-stop", "GV_FILE="+file)
	time.Sleep(300 * time.Millisecond)
	a.WriteToUDP([]byte("depois"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 39400})
	time.Sleep(300 * time.Millisecond)
	if d2, _ := os.ReadFile(file); len(d2) != len(data) {
		t.Fatal("continuou gravando depois de parar")
	}
	c := exec.Command("sh", "-c", Script, "vmctl", "capture-stop")
	c.Env = append(append([]string{}, env...), "GV_FILE=/etc/passwd")
	if out, err := c.CombinedOutput(); err == nil {
		t.Fatalf("aceitou arquivo fora das capturas: %s", out)
	}
}

// TestIOLStates: write memory on the console, the lab states (save,
// restore, start from zero) and the NVRAM carried to another machine,
// with a fake IOL that keeps its configuration in nvram_00042.
func TestIOLStates(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("IOL só em Linux x86_64")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("sem python3")
	}
	dir, state := t.TempDir(), t.TempDir()
	bin := filepath.Join(dir, "iol.bin")
	os.WriteFile(bin, []byte(`#!/bin/sh
for id; do :; done
nv=nvram_$(printf %05d "$id")
cfg=$(cat "$nv" 2>/dev/null)
echo "boot $nv cfg=[$cfg]"
stty icrnl
while IFS= read -r l; do
  case "$l" in
    *"write memory"*) printf '%s' "$cfg" >"$nv"; echo "Building configuration..."; echo "[OK]" ;;
    set\ *) cfg=${l#set }; echo "cfg=$cfg" ;;
  esac
done
`), 0o755)
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	run := func(env ...string) (string, error) {
		cmd := exec.Command("sh", "-c", Script, "vmctl", env[0])
		cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+state, "GV_PROJECT=lab", "GV_NAME=r1")
		cmd.Env = append(cmd.Env, env[1:]...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	must := func(env ...string) string {
		t.Helper()
		out, err := run(env...)
		if err != nil {
			t.Fatalf("%s: %v %s", env[0], err, out)
		}
		return out
	}
	must("up", "GV_KIND=iol", "GV_IMAGE=cisco-iol", "GV_IMAGE_SRC="+bin, "GV_MEM=256", "GV_IOLID=42", "GV_IOLPORTS=4",
		"GV_SERIAL="+strconv.Itoa(port), "GV_NETMAP="+base64.StdEncoding.EncodeToString([]byte("42:0/0 1500:0/0\n")))
	defer run("stop")
	d := filepath.Join(state, "gantry/vms/lab/r1")
	console := func(line string) {
		t.Helper()
		var c net.Conn
		var err error
		for i := 0; i < 50; i++ {
			if c, err = net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port)); err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if err != nil {
			t.Fatal(err)
		}
		c.Write([]byte(line + "\r"))
		time.Sleep(300 * time.Millisecond)
		c.Close()
	}
	nvram := func() string {
		b, err := os.ReadFile(filepath.Join(d, "nvram_00042"))
		if err != nil {
			return "<nada>"
		}
		return string(b)
	}
	console("set ospf-A")
	if out, _ := run("console-save"); !strings.Contains(out, "r1: configuração gravada") || nvram() != "ospf-A" {
		log, _ := os.ReadFile(filepath.Join(d, "console.log"))
		qe, _ := os.ReadFile(filepath.Join(d, "qemu.err"))
		t.Fatalf("console-save: %q, nvram %q\nlog %q\nerr %q", out, nvram(), log, qe)
	}
	must("state-save", "GV_STATE=aula1")
	console("set nada")
	must("console-save")
	if nvram() != "nada" {
		t.Fatal(nvram())
	}
	if out := must("state-list"); !strings.HasPrefix(out, "aula1 ") {
		t.Fatalf("state-list: %q", out)
	}
	must("state-restore", "GV_STATE=aula1")
	if nvram() != "ospf-A" {
		t.Fatalf("restore: %q", nvram())
	}
	if st := must("status"); !strings.Contains(st, "r1 running") {
		t.Fatalf("the node must be running again: %s", st)
	}
	// The node booted with the restored configuration.
	time.Sleep(300 * time.Millisecond)
	if log, _ := os.ReadFile(filepath.Join(d, "console.log")); !strings.Contains(string(log), "boot nvram_00042 cfg=[ospf-A]") {
		t.Fatalf("console: %s", log)
	}
	if _, err := run("state-restore", "GV_STATE=nao-existe"); err == nil {
		t.Fatal("a missing state was restored")
	}
	// The NVRAM goes to another project (a lab imported elsewhere).
	tarPath := strings.TrimSpace(must("nvram-pack"))
	cmd := exec.Command("sh", "-c", Script, "vmctl", "nvram-unpack")
	cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+state, "GV_PROJECT=outro", "GV_NAME=r1", "GV_FILE="+tarPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("unpack: %v %s", err, out)
	}
	if b, _ := os.ReadFile(filepath.Join(state, "gantry/vms/outro/r1/nvram_00042")); string(b) != "ospf-A" {
		t.Fatalf("unpacked %q", b)
	}
	// There the node gets another instance number: the NVRAM follows it.
	l2, _ := net.Listen("tcp", "127.0.0.1:0")
	port2 := l2.Addr().(*net.TCPAddr).Port
	l2.Close()
	up := exec.Command("sh", "-c", Script, "vmctl", "up")
	up.Env = append(os.Environ(), "XDG_STATE_HOME="+state, "GV_PROJECT=outro", "GV_NAME=r1", "GV_KIND=iol", "GV_IMAGE=cisco-iol", "GV_IMAGE_SRC="+bin,
		"GV_MEM=256", "GV_IOLID=7", "GV_IOLPORTS=4", "GV_SERIAL="+strconv.Itoa(port2), "GV_NETMAP="+base64.StdEncoding.EncodeToString([]byte("7:0/0 1500:0/0\n")))
	if out, err := up.CombinedOutput(); err != nil {
		t.Fatalf("up outro: %v %s", err, out)
	}
	defer func() {
		stop := exec.Command("sh", "-c", Script, "vmctl", "stop")
		stop.Env = append(os.Environ(), "XDG_STATE_HOME="+state, "GV_PROJECT=outro", "GV_NAME=r1")
		stop.Run()
	}()
	time.Sleep(500 * time.Millisecond)
	if log, _ := os.ReadFile(filepath.Join(state, "gantry/vms/outro/r1/console.log")); !strings.Contains(string(log), "boot nvram_00007 cfg=[ospf-A]") {
		t.Fatalf("renumbered node: %s", log)
	}
	must("state-zero")
	if nvram() != "<nada>" {
		t.Fatalf("zero: %q", nvram())
	}
	must("state-delete", "GV_STATE=aula1")
	if out := must("state-list"); strings.TrimSpace(out) != "" {
		t.Fatalf("after delete: %q", out)
	}
}

// TestDynamips: a Dynamips node runs the router program under the console
// runner with its adapters and NIOs, finds the idle-PC through the console
// (Ctrl-] i) and starts with it; without dynamips the message says how to
// install it.
func TestDynamips(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("sem python3")
	}
	dir, state, bindir := t.TempDir(), t.TempDir(), t.TempDir()
	img := filepath.Join(dir, "c7200-adventerprisek9-mz.124-24.T5.image")
	os.WriteFile(img, []byte("IOS"), 0o644)
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	env := func(extra ...string) []string {
		return append(append(os.Environ(), "XDG_STATE_HOME="+state, "GV_PROJECT=lab", "GV_NAME=r1"), extra...)
	}
	up := func(path string) (string, error) {
		cmd := exec.Command("sh", "-c", Script, "vmctl", "up")
		cmd.Env = env("PATH="+path, "GV_KIND=dyn", "GV_DYNPLAT=7200", "GV_IMAGE=cisco-c7200", "GV_IMAGE_SRC="+img, "GV_MEM=256", "GV_DYNNPE=npe-200",
			"GV_SERIAL="+strconv.Itoa(port), "GV_DYNARGS=-p 0:C7200-IO-FE -s 0:0:udp:30001:127.0.0.1:30000 -p 1:PA-2FE-TX -s 1:0:udp:30003:127.0.0.1:30002",
			"GV_CONFIG="+base64.StdEncoding.EncodeToString([]byte("hostname R1\n")))
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if _, e := exec.LookPath("dynamips"); e != nil {
		if out, err := up(os.Getenv("PATH")); err == nil || !strings.Contains(out, "Dynamips não está instalado") {
			t.Fatalf("without dynamips: %v %s", err, out)
		}
	}
	os.WriteFile(filepath.Join(bindir, "dynamips"), []byte(`#!/usr/bin/env python3
import os, sys
print("dynamips " + " ".join(sys.argv[1:]), flush=True)
buf = b""
while True:
    b = os.read(0, 1)
    if not b:
        break
    buf += b
    if buf.endswith(b"\x1di"):
        print("Please wait while gathering statistics...\nDone. Suggested idling PC:\n   0x60189600 (count=29)\n   0x6018a0ec (count=55)\n   0x60a1b2c4 (count=91)\nRestart the emulator with \"--idle-pc=0x60189600\" (for example)", flush=True)
`), 0o755)
	path := bindir + string(os.PathListSeparator) + os.Getenv("PATH")
	if out, err := up(path); err != nil {
		t.Fatalf("up: %v %s", err, out)
	}
	stop := func() {
		cmd := exec.Command("sh", "-c", Script, "vmctl", "stop")
		cmd.Env = env("PATH=" + path)
		cmd.Run()
	}
	defer stop()
	d := filepath.Join(state, "gantry/vms/lab/r1")
	waitLog := func(want string) string {
		t.Helper()
		var log []byte
		for i := 0; i < 50; i++ {
			log, _ = os.ReadFile(filepath.Join(d, "console.log"))
			if strings.Contains(string(log), want) {
				return string(log)
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("console.log sem %q: %s", want, log)
		return ""
	}
	waitLog("dynamips -P 7200 -t npe-200 -r 256 -X -C " + filepath.Join(d, "startup-config") + " -p 0:C7200-IO-FE -s 0:0:udp:30001:127.0.0.1:30000 -p 1:PA-2FE-TX -s 1:0:udp:30003:127.0.0.1:30002 " + img)
	if env, _ := os.ReadFile(filepath.Join(d, "vm.env")); !strings.Contains(string(env), "accel=dynamips") {
		t.Fatalf("vm.env: %s", env)
	}
	status := func() string {
		c := exec.Command("sh", "-c", Script, "vmctl", "status")
		c.Env = env("PATH=" + path)
		out, _ := c.CombinedOutput()
		return string(out)
	}
	if st := status(); !strings.Contains(st, " idle=0") {
		t.Fatalf("status sem idle-PC: %q", st)
	}
	cmd := exec.Command("sh", "-c", Script, "vmctl", "idle-pc")
	cmd.Env = env("PATH="+path, "GV_RESTART=1")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "idle-PC 0x6018a0ec guardado") {
		t.Fatalf("idle-pc: %v %s", err, out)
	}
	waitLog("--idle-pc=0x6018a0ec")
	if st := status(); !strings.Contains(st, " idle=1") {
		t.Fatalf("status com idle-PC: %q", st)
	}
	// The NVRAM of the router is what a lab state keeps.
	os.WriteFile(filepath.Join(d, "c7200_i0_nvram"), []byte("cfg"), 0o644)
	st := exec.Command("sh", "-c", Script, "vmctl", "state-save")
	st.Env = env("PATH="+path, "GV_STATE=um")
	if out, err := st.CombinedOutput(); err != nil || !strings.Contains(string(out), "salvo (NVRAM)") {
		t.Fatalf("state-save: %v %s", err, out)
	}
}

// TestPortLive: a port published and taken out of a running VM through the
// QEMU monitor (hostfwd), kept for the next start; a port in use on the
// host is refused without changing anything.
func TestPortLive(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("sem python3")
	}
	state := t.TempDir()
	d := filepath.Join(state, "gantry/vms/lab/web")
	os.MkdirAll(d, 0o755)
	os.WriteFile(filepath.Join(d, "disk.qcow2"), nil, 0o644)
	os.WriteFile(filepath.Join(d, "vm.env"), []byte("ssh=2222\nports=\nimage=debian-12\n"), 0o644)
	start := "qemu-system-x86_64 -netdev 'user,id=n0,hostfwd=tcp:127.0.0.1:2222-:22' -device virtio-net-pci,netdev=n0\n"
	os.WriteFile(filepath.Join(d, "start.sh"), []byte(start), 0o644)
	// A monitor that answers like QEMU's.
	mon := exec.Command("python3", "-c", `import os, socket, sys
p = sys.argv[1]
s = socket.socket(socket.AF_UNIX); s.bind(p); s.listen(5)
open(os.path.join(os.path.dirname(p), "monitor.log"), "w").close()
while True:
    c, _ = s.accept()
    c.sendall(b"QEMU 9.0 monitor - type 'help' for more information\r\n(qemu) ")
    data = b""
    while not data.endswith(b"\n"):
        x = c.recv(1024)
        if not x: break
        data += x
    cmd = data.decode().strip()
    open(os.path.join(os.path.dirname(p), "monitor.log"), "a").write(cmd + "\n")
    out = ""
    if cmd.startswith("hostfwd_remove"):
        out = "host forwarding rule for " + cmd.split()[-1] + " removed\r\n"
    c.sendall(cmd.encode() + b"\r\n" + out.encode() + b"(qemu) ")
    c.close()
`, filepath.Join(d, "mon.sock"))
	if err := mon.Start(); err != nil {
		t.Fatal(err)
	}
	defer mon.Process.Kill()
	os.WriteFile(filepath.Join(d, "qemu.pid"), []byte(strconv.Itoa(mon.Process.Pid)), 0o644)
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(filepath.Join(d, "mon.sock")); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	free := l.Addr().(*net.TCPAddr).Port
	l.Close()
	run := func(op, port string) (string, error) {
		cmd := exec.Command("sh", "-c", Script, "vmctl", "port-live")
		cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+state, "GV_PROJECT=lab", "GV_NAME=web", "GV_OP="+op, "GV_PORT="+port)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	rule := "tcp:127.0.0.1:" + strconv.Itoa(free) + ":80"
	if out, err := run("add", rule); err != nil || !strings.Contains(out, "sem reiniciar") {
		t.Fatalf("add: %v %s", err, out)
	}
	read := func(f string) string { b, _ := os.ReadFile(filepath.Join(d, f)); return string(b) }
	if !strings.Contains(read("monitor.log"), "hostfwd_add n0 tcp:127.0.0.1:"+strconv.Itoa(free)+"-:80") || !strings.Contains(read("vm.env"), "ports="+rule) ||
		!strings.Contains(read("start.sh"), "id=n0,hostfwd=tcp:127.0.0.1:"+strconv.Itoa(free)+"-:80,hostfwd=tcp:127.0.0.1:2222-:22") {
		t.Fatalf("monitor %q\nvm.env %q\nstart.sh %q", read("monitor.log"), read("vm.env"), read("start.sh"))
	}
	// A port in use: refused, nothing changes.
	busy, _ := net.Listen("tcp", "127.0.0.1:0")
	defer busy.Close()
	bp := strconv.Itoa(busy.Addr().(*net.TCPAddr).Port)
	before := read("start.sh") + read("vm.env")
	if out, err := run("add", "tcp:127.0.0.1:"+bp+":81"); err == nil || !strings.Contains(out, "já está em uso") || read("start.sh")+read("vm.env") != before {
		t.Fatalf("busy port: %v %s", err, out)
	}
	if out, err := run("remove", rule); err != nil || !strings.Contains(out, "tirada") {
		t.Fatalf("remove: %v %s", err, out)
	}
	if read("start.sh") != start || strings.Contains(read("vm.env"), rule) {
		t.Fatalf("after remove:\n%s\n%s", read("start.sh"), read("vm.env"))
	}
}

// TestCloneIdentity: a clone gets no instance-id nor nix.hash of the
// origin, and its seed carries the vendor-data that gives it a machine-id
// and host keys of its own; the origin's seed does not.
func TestCloneIdentity(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("sem ssh-keygen")
	}
	state := t.TempDir()
	d := filepath.Join(state, "gantry/vms/lab/web")
	os.MkdirAll(d, 0o755)
	os.WriteFile(filepath.Join(d, "disk.qcow2"), []byte("disco"), 0o644)
	os.WriteFile(filepath.Join(d, "instance-id"), []byte("gantry-lab-web-1\n"), 0o644)
	os.WriteFile(filepath.Join(d, "nix.hash"), []byte("abc\n"), 0o644)
	run := func(cmd, name string, env ...string) string {
		t.Helper()
		c := exec.Command("sh", "-c", Script, "vmctl", cmd)
		c.Env = append(append(os.Environ(), "XDG_STATE_HOME="+state, "GV_PROJECT=lab", "GV_NAME="+name), env...)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %s: %v %s", cmd, name, err, out)
		}
		return string(out)
	}
	run("clone", "web", "GV_TO=web2")
	c := filepath.Join(state, "gantry/vms/lab/web2")
	if b, _ := os.ReadFile(filepath.Join(c, "disk.qcow2")); string(b) != "disco" {
		t.Fatal("disk not copied")
	}
	for _, f := range []string{"instance-id", "nix.hash"} {
		if _, err := os.Stat(filepath.Join(c, f)); err == nil {
			t.Errorf("the clone inherited %s", f)
		}
	}
	ud := base64.StdEncoding.EncodeToString([]byte("#cloud-config\n"))
	s1, s2 := t.TempDir(), t.TempDir()
	run("seed", "web2", "GV_DIR="+s2, "GV_USERDATA="+ud)
	run("seed", "web", "GV_DIR="+s1, "GV_USERDATA="+ud)
	vd, err := os.ReadFile(filepath.Join(s2, "vendor-data"))
	if err != nil || !strings.Contains(string(vd), "systemd-machine-id-setup") || !strings.Contains(string(vd), "ssh_host_") {
		t.Fatalf("vendor-data: %v %s", err, vd)
	}
	if _, err := os.Stat(filepath.Join(s1, "vendor-data")); err == nil {
		t.Fatal("the origin got the clone's vendor-data")
	}
	m1, _ := os.ReadFile(filepath.Join(s1, "meta-data"))
	m2, _ := os.ReadFile(filepath.Join(s2, "meta-data"))
	if !strings.Contains(string(m1), "gantry-lab-web-1") || strings.Contains(string(m2), "gantry-lab-web-1") || !strings.Contains(string(m2), "local-hostname: web2") {
		t.Fatalf("meta-data:\n%s\n%s", m1, m2)
	}
}

// TestRelaySegment: a lab network is a learning switch with DHCP: a
// broadcast reaches every port on it, a unicast only the port of its MAC
// once learned, DHCP hands out from the pool (and keeps the lease), the
// server answers ARP, and a capture of the network sees it all.
func TestRelaySegment(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("sem python3")
	}
	state := t.TempDir()
	env := append(os.Environ(), "XDG_STATE_HOME="+state, "GV_PROJECT=lab")
	run := func(cmd string, extra ...string) string {
		t.Helper()
		c := exec.Command("sh", "-c", Script, "vmctl", cmd)
		c.Env = append(append([]string{}, env...), extra...)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v %s", cmd, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	vm := map[string]*net.UDPConn{}
	rport := map[string]int{"a": 39500, "b": 39502, "c": 39504, "d": 39506}
	for n, r := range rport {
		c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: r + 1})
		if err != nil {
			t.Skip("porta ocupada:", err)
		}
		defer c.Close()
		vm[n] = c
	}
	defer func() {
		if b, err := os.ReadFile(filepath.Join(state, "gantry/vms/lab/relay.pid")); err == nil {
			exec.Command("kill", strings.TrimSpace(string(b))).Run()
		}
	}()
	table := "#seg lan 10.0.1.0/24 10.0.1.1 10.0.1.100 10.0.1.199 10.0.1.254 1.1.1.1\n"
	for _, n := range []string{"a", "b", "c"} {
		table += fmt.Sprintf("%d %d seg:lan %s 1\n", rport[n], rport[n]+1, n)
	}
	table += fmt.Sprintf("%d %d 0 d 0\n", rport["d"], rport["d"]+1)
	run("relay", "GV_RELAY="+base64.StdEncoding.EncodeToString([]byte(table)))
	mac := map[string][]byte{"a": {2, 0, 0, 0, 0, 0xa}, "b": {2, 0, 0, 0, 0, 0xb}, "c": {2, 0, 0, 0, 0, 0xc}}
	bcast := []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	frame := func(dst, src []byte, et uint16, body []byte) []byte {
		f := append(append(append([]byte{}, dst...), src...), byte(et>>8), byte(et))
		return append(f, body...)
	}
	send := func(from string, f []byte) {
		vm[from].WriteToUDP(f, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: rport[from]})
	}
	recv := func(n string) []byte {
		buf := make([]byte, 2000)
		vm[n].SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		k, err := vm[n].Read(buf)
		if err != nil {
			return nil
		}
		return buf[:k]
	}
	var got []byte
	for i := 0; i < 20 && got == nil; i++ { // the relay is starting
		send("a", frame(bcast, mac["a"], 0x88b5, []byte("todos")))
		got = recv("b")
	}
	if !strings.HasSuffix(string(got), "todos") || !strings.HasSuffix(string(recv("c")), "todos") {
		t.Fatalf("broadcast: b %q", got)
	}
	for _, n := range []string{"a", "b", "c", "d"} { // drain the retries
		for recv(n) != nil {
		}
	}
	file := run("capture-start", "GV_PORT=seg:lan")
	time.Sleep(300 * time.Millisecond)
	send("b", frame(mac["a"], mac["b"], 0x88b5, []byte("só a")))
	if g := recv("a"); !strings.HasSuffix(string(g), "só a") {
		t.Fatalf("unicast: a %q", g)
	}
	if g := recv("c"); g != nil {
		t.Fatalf("unicast vazou para c: %q", g)
	}
	if g := recv("d"); g != nil {
		t.Fatalf("a rede vazou para uma porta de fora: %q", g)
	}
	// Frames of switches and routers pass as they are: 802.1Q tagged (a
	// trunk) to the learned port, a BPDU of spanning tree to every port.
	tagged := frame(mac["a"], mac["b"], 0x8100, []byte{0, 10, 0x08, 0x00, 'v', 'l', 'a', 'n'})
	send("b", tagged)
	if g := recv("a"); string(g) != string(tagged) {
		t.Fatalf("quadro com VLAN: a % x", g)
	}
	bpdu := frame([]byte{0x01, 0x80, 0xc2, 0, 0, 0}, mac["a"], 0x0026, []byte{0x42, 0x42, 0x03, 0, 0, 0, 0})
	send("a", bpdu)
	if gb, gc := recv("b"), recv("c"); string(gb) != string(bpdu) || string(gc) != string(bpdu) {
		t.Fatalf("BPDU: b % x c % x", gb, gc)
	}
	// DHCP: DISCOVER → OFFER, REQUEST → ACK, from c.
	bootp := func(mt byte, extra []byte) []byte {
		b := make([]byte, 240)
		b[0], b[1], b[2] = 1, 1, 6
		copy(b[4:8], []byte{1, 2, 3, 4})
		b[10] = 0x80 // broadcast
		copy(b[28:34], mac["c"])
		copy(b[236:240], []byte{0x63, 0x82, 0x53, 0x63})
		b = append(b, 53, 1, mt)
		b = append(append(b, extra...), 255)
		udp := append([]byte{0, 68, 0, 67, byte((8 + len(b)) >> 8), byte(8 + len(b)), 0, 0}, b...)
		ip := []byte{0x45, 0, byte((20 + len(udp)) >> 8), byte(20 + len(udp)), 0, 0, 0, 0, 64, 17, 0, 0, 0, 0, 0, 0, 255, 255, 255, 255}
		return frame(bcast, mac["c"], 0x0800, append(ip, udp...))
	}
	opts := func(f []byte) (yi string, o map[byte][]byte) {
		b := f[14+20+8:]
		o = map[byte][]byte{}
		for i := 240; i < len(b) && b[i] != 255; i += 2 + int(b[i+1]) {
			o[b[i]] = b[i+2 : i+2+int(b[i+1])]
		}
		return net.IP(b[16:20]).String(), o
	}
	send("c", bootp(1, nil))
	offer := recv("c")
	if offer == nil {
		t.Fatal("sem OFFER")
	}
	yi, o := opts(offer)
	if yi != "10.0.1.100" || o[53][0] != 2 || net.IP(o[54]).String() != "10.0.1.254" || net.IP(o[3]).String() != "10.0.1.1" || net.IP(o[1]).String() != "255.255.255.0" || net.IP(o[6]).String() != "1.1.1.1" {
		t.Fatalf("OFFER %s %v", yi, o)
	}
	send("c", bootp(3, []byte{50, 4, 10, 0, 1, 100, 54, 4, 10, 0, 1, 254}))
	ack := recv("c")
	if ack == nil {
		t.Fatal("sem ACK")
	}
	if yi, o := opts(ack); yi != "10.0.1.100" || o[53][0] != 5 {
		t.Fatalf("ACK %s %v", yi, o)
	}
	if g := recv("a"); g != nil && len(g) > 34 && g[23] == 17 {
		t.Fatalf("a resposta do DHCP foi para a: % x", g[:20])
	}
	if l := run("dhcp-leases"); l != "lan 02:00:00:00:00:0c 10.0.1.100 c 1" {
		t.Fatalf("leases %q", l)
	}
	// The link lights: the ports that sent frames are in ports-seen, the
	// one that never sent (d) is not.
	var seen string
	for i := 0; i < 30 && !strings.Contains(seen, "\nc 1 "); i++ {
		time.Sleep(200 * time.Millisecond)
		send("a", frame(bcast, mac["a"], 0x88b5, []byte("luz")))
		seen = run("ports-seen")
	}
	if !strings.HasPrefix(seen, "now ") || !strings.Contains(seen, "\na ") || !strings.Contains(seen, "\nb ") || !strings.Contains(seen, "\nc 1 ") || strings.Contains(seen, "\nd ") {
		t.Fatalf("ports-seen %q", seen)
	}
	// ARP for the server: it answers.
	arp := []byte{0, 1, 8, 0, 6, 4, 0, 1}
	arp = append(append(append(arp, mac["a"]...), 10, 0, 1, 50), 0, 0, 0, 0, 0, 0, 10, 0, 1, 254)
	send("a", frame(bcast, mac["a"], 0x0806, arp))
	var rep []byte
	for g := recv("a"); g != nil; g = recv("a") {
		if len(g) >= 42 && g[12] == 8 && g[13] == 6 {
			rep = g
		}
	}
	if rep == nil || rep[21] != 2 || net.IP(rep[28:32]).String() != "10.0.1.254" {
		t.Fatalf("ARP: % x", rep)
	}
	time.Sleep(200 * time.Millisecond)
	data, _ := os.ReadFile(file)
	for _, w := range []string{"só a", "\x63\x82\x53\x63"} {
		if !strings.Contains(string(data), w) {
			t.Errorf("a captura da rede não tem %q (%d bytes)", w, len(data))
		}
	}
	run("capture-stop", "GV_FILE="+file)
}

// TestIOLStartupNVRAM: the startup configuration of an IOL node goes into
// its NVRAM before the first boot (the size its -h gives); an image whose
// -h does not say gets it typed at the console instead.
func TestIOLStartupNVRAM(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("IOL só em Linux x86_64")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("sem python3")
	}
	cfg := "hostname R1\ninterface Ethernet0/0\n ip address 10.0.0.1 255.255.255.0\nend\n"
	run := func(t *testing.T, help string) string {
		dir, state := t.TempDir(), t.TempDir()
		bin := filepath.Join(dir, "iol.bin")
		os.WriteFile(bin, []byte("#!/bin/sh\nif [ \"$1\" = -h ]; then echo '"+help+"'; exit 0; fi\nexec cat\n"), 0o755)
		l, _ := net.Listen("tcp", "127.0.0.1:0")
		port := l.Addr().(*net.TCPAddr).Port
		l.Close()
		env := append(os.Environ(), "XDG_STATE_HOME="+state, "GV_PROJECT=lab", "GV_NAME=r1")
		cmd := exec.Command("sh", "-c", Script, "vmctl", "up")
		cmd.Env = append(env, "GV_KIND=iol", "GV_IMAGE=cisco-iol", "GV_IMAGE_SRC="+bin, "GV_MEM=256", "GV_IOLID=7", "GV_IOLPORTS=4",
			"GV_SERIAL="+strconv.Itoa(port), "GV_NETMAP="+base64.StdEncoding.EncodeToString([]byte("7:0/0 1500:0/0\n")),
			"GV_CONFIG="+base64.StdEncoding.EncodeToString([]byte(cfg)))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
		t.Cleanup(func() {
			stop := exec.Command("sh", "-c", Script, "vmctl", "stop")
			stop.Env = env
			stop.Run()
		})
		return filepath.Join(state, "gantry/vms/lab/r1")
	}
	t.Run("nvram", func(t *testing.T) {
		d := run(t, "  -n <n>       Size of nvram in Kb (default 128KB)")
		b, err := os.ReadFile(filepath.Join(d, "nvram_00007"))
		if err != nil || len(b) != 128*1024 {
			t.Fatalf("nvram: %v %d", err, len(b))
		}
		if b[0] != 0xAB || b[1] != 0xCD || !strings.Contains(string(b[36:36+len(cfg)]), "hostname R1") {
			t.Fatalf("cabeçalho %x", b[:40])
		}
		// the checksum: the 16-bit words of the area add to 0xFFFF
		sum := 0
		for i := 0; i < 64*1024; i += 2 {
			sum += int(b[i])<<8 | int(b[i+1])
		}
		for sum>>16 != 0 {
			sum = sum&0xFFFF + sum>>16
		}
		if sum != 0xFFFF {
			t.Fatalf("checksum %x", sum)
		}
		if _, err := os.Stat(filepath.Join(d, "startup.cfg")); err == nil {
			t.Fatal("startup.cfg: a config ia ser digitada também")
		}
		if _, err := os.Stat(filepath.Join(d, "startup.done")); err != nil {
			t.Fatal("sem startup.done")
		}
	})
	t.Run("digitada", func(t *testing.T) {
		d := run(t, "IOL sem ajuda")
		if _, err := os.Stat(filepath.Join(d, "nvram_00007")); err == nil {
			t.Fatal("nvram sem saber o tamanho")
		}
		if b, _ := os.ReadFile(filepath.Join(d, "startup.cfg")); string(b) != cfg {
			t.Fatalf("startup.cfg: %q", b)
		}
	})
}

// TestVPCS: a VPCS node runs the VPCS Gantry brings with the startup.vpc of
// the compose, its console answers, and its port talks UDP to the relay.
func TestVPCS(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("o VPCS do Gantry é para Linux x86_64")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("sem python3")
	}
	b, err := vpcsbin.Binary("amd64")
	if err != nil {
		t.Fatal(err)
	}
	dir, state := t.TempDir(), t.TempDir()
	bin := filepath.Join(dir, "vpcs")
	os.WriteFile(bin, b, 0o755)
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	relay, _ := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	defer relay.Close()
	rport := relay.LocalAddr().(*net.UDPAddr).Port
	env := append(os.Environ(), "XDG_STATE_HOME="+state, "GV_PROJECT=lab", "GV_NAME=PC01")
	cmd := exec.Command("sh", "-c", Script, "vmctl", "up")
	cmd.Env = append(env, "GV_KIND=vpcs", "GV_VPCSBIN="+bin, "GV_SERIAL="+strconv.Itoa(port),
		"GV_VPCSARGS=-m 3 -s 41010 -c "+strconv.Itoa(rport)+" -t 127.0.0.1",
		"GV_CONFIG="+base64.StdEncoding.EncodeToString([]byte("set pcname PC01\nip 192.168.10.100 192.168.10.1 24\n")))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	defer func() {
		stop := exec.Command("sh", "-c", Script, "vmctl", "stop")
		stop.Env = env
		stop.Run()
	}()
	var c net.Conn
	for i := 0; i < 50; i++ {
		if c, err = net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port)); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	time.Sleep(500 * time.Millisecond)
	c.Write([]byte("show ip\n"))
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	got := ""
	buf := make([]byte, 4096)
	for !strings.Contains(got, "RHOST:PORT") {
		n, err := c.Read(buf)
		if err != nil {
			break
		}
		got += string(buf[:n])
	}
	if !strings.Contains(got, "192.168.10.100/24") || !strings.Contains(got, "192.168.10.1") || !strings.Contains(got, "00:50:79:66:68:03") {
		t.Fatalf("show ip: %q", got)
	}
	// a ping leaves through the port: an ARP for the gateway reaches the relay
	c.Write([]byte("ping 192.168.10.1 -c 1\n"))
	relay.SetReadDeadline(time.Now().Add(5 * time.Second))
	arp := false
	for !arp {
		n, _, err := relay.ReadFromUDP(buf)
		if err != nil {
			t.Fatalf("relay sem ARP: %v", err)
		}
		arp = n >= 14 && buf[12] == 0x08 && buf[13] == 0x06
	}
	d := filepath.Join(state, "gantry/vms/lab/PC01")
	if env, _ := os.ReadFile(filepath.Join(d, "vm.env")); !strings.Contains(string(env), "accel=vpcs") {
		t.Fatalf("vm.env: %s", env)
	}
	// An address typed in the console, without save: stopping saves it
	// (startup.vpc), and the next start keeps it instead of the file's.
	time.Sleep(2 * time.Second) // the ping above
	c.Write([]byte("ip 10.7.7.7 10.7.7.1 24\n"))
	time.Sleep(2500 * time.Millisecond) // VPCS checks for a duplicate address
	c.Close()
	stop := exec.Command("sh", "-c", Script, "vmctl", "stop")
	stop.Env = env
	out, err := stop.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "save") {
		t.Fatalf("stop: %v %s", err, out)
	}
	if b, _ := os.ReadFile(filepath.Join(d, "startup.vpc")); !strings.Contains(string(b), "10.7.7.7") {
		t.Fatalf("startup.vpc depois de parar: %q (stop: %s)", b, out)
	}
}

// TestIOLLibcrypto: an IOL that asks for libcrypto.so.4 (the old images)
// runs with a link to the libcrypto of the system, made in Gantry's folder.
func TestIOLLibcrypto(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("IOL só em Linux x86_64")
	}
	for _, p := range []string{"python3", "cc", "ldd"} {
		if _, err := exec.LookPath(p); err != nil {
			t.Skip("sem " + p)
		}
	}
	dir, libs, state := t.TempDir(), t.TempDir(), t.TempDir()
	cc := func(args ...string) {
		if out, err := exec.Command("cc", args...).CombinedOutput(); err != nil {
			t.Fatalf("cc %v: %v %s", args, err, out)
		}
	}
	// The "system" libcrypto (another version) and the IOL built against
	// a libcrypto.so.4 that is not there anymore.
	os.WriteFile(filepath.Join(dir, "c.c"), []byte("int MD5_Init(void *c) { return 7; }\n"), 0o644)
	cc("-shared", "-fPIC", "-Wl,-soname,libcrypto.so.4", "-o", filepath.Join(dir, "libcrypto.so.4"), filepath.Join(dir, "c.c"))
	cc("-shared", "-fPIC", "-Wl,-soname,libcrypto.so.3", "-o", filepath.Join(libs, "libcrypto.so.3"), filepath.Join(dir, "c.c"))
	os.WriteFile(filepath.Join(dir, "iol.c"), []byte(`#include <stdio.h>
int MD5_Init(void *c);
int main(int argc, char **argv) {
	printf("IOL md5=%d\n", MD5_Init(0));
	fflush(stdout);
	char b[256];
	while (fgets(b, sizeof b, stdin)) { fputs(b, stdout); fflush(stdout); }
	return 0;
}
`), 0o644)
	bin := filepath.Join(dir, "iol.bin")
	cc("-o", bin, filepath.Join(dir, "iol.c"), filepath.Join(dir, "libcrypto.so.4"))
	os.Remove(filepath.Join(dir, "libcrypto.so.4"))
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	cmd := exec.Command("sh", "-c", Script, "vmctl", "up")
	cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+state, "GV_PROJECT=lab", "GV_NAME=r1", "GV_KIND=iol", "GV_IOLLIBDIRS="+libs,
		"GV_IMAGE=cisco-iol", "GV_IMAGE_SRC="+bin, "GV_MEM=256", "GV_CPUS=1", "GV_IOLID=42", "GV_IOLPORTS=4",
		"GV_SERIAL="+strconv.Itoa(port), "GV_NETMAP="+base64.StdEncoding.EncodeToString([]byte("42:0/0 1500:0/0\n")))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	d := filepath.Join(state, "gantry/vms/lab/r1")
	defer func() {
		stop := exec.Command("sh", "-c", Script, "vmctl", "stop")
		stop.Env = append(os.Environ(), "XDG_STATE_HOME="+state, "GV_PROJECT=lab", "GV_NAME=r1")
		stop.Run()
	}()
	var log []byte
	for i := 0; i < 50 && !strings.Contains(string(log), "IOL md5=7"); i++ {
		time.Sleep(100 * time.Millisecond)
		log, _ = os.ReadFile(filepath.Join(d, "console.log"))
	}
	if !strings.Contains(string(log), "IOL md5=7") {
		errs, _ := os.ReadFile(filepath.Join(d, "qemu.err"))
		t.Fatalf("console %q, err %q", log, errs)
	}
	if l, _ := os.Readlink(filepath.Join(state, "gantry/vms/.iol-lib/libcrypto.so.4")); l != filepath.Join(libs, "libcrypto.so.3") {
		t.Fatalf("link %q", l)
	}
}

// TestIOLRejected: the lines IOS refuses while reading the startup
// configuration are kept (boot-rejected.txt), counted in status and listed
// by "rejected"; errors after the boot (the user typing) are not.
func TestIOLRejected(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("IOL só em Linux x86_64")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("sem python3")
	}
	dir, state := t.TempDir(), t.TempDir()
	bin := filepath.Join(dir, "iol.bin")
	os.WriteFile(bin, []byte(`#!/bin/sh
printf 'interface Ethernet0/0\r\n duplex full\r\n       ^\r\n%% Invalid input detected at '"'"'^'"'"' marker.\r\n\r\n'
printf 'router ospf 1\r\n netwrk 10.0.0.0\r\n%% Incomplete command.\r\n'
printf '\r\nPress RETURN to get started!\r\n'
sleep 0.3
printf 'R1#sh ip intt\r\n          ^\r\n%% Invalid input detected at '"'"'^'"'"' marker.\r\n'
exec cat
`), 0o755)
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	base := append(os.Environ(), "XDG_STATE_HOME="+state, "GV_PROJECT=lab", "GV_NAME=r1")
	cmd := exec.Command("sh", "-c", Script, "vmctl", "up")
	cmd.Env = append(base, "GV_KIND=iol", "GV_IMAGE=cisco-iol", "GV_IMAGE_SRC="+bin, "GV_MEM=256", "GV_CPUS=1", "GV_IOLID=43", "GV_IOLPORTS=4",
		"GV_SERIAL="+strconv.Itoa(port), "GV_NETMAP="+base64.StdEncoding.EncodeToString([]byte("43:0/0 1500:0/0\n")))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	defer func() {
		stop := exec.Command("sh", "-c", Script, "vmctl", "stop")
		stop.Env = base
		stop.Run()
	}()
	run := func(c string) string {
		cmd := exec.Command("sh", "-c", Script, "vmctl", c)
		cmd.Env = base
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v %s", c, err, out)
		}
		return string(out)
	}
	var got string
	for i := 0; i < 50; i++ {
		time.Sleep(100 * time.Millisecond)
		got = run("rejected")
		if strings.Count(got, "\n") >= 2 && strings.Contains(run("status"), "rej=2") {
			break
		}
	}
	want := "duplex full\t% Invalid input detected at '^' marker.\nnetwrk 10.0.0.0\t% Incomplete command.\n"
	if got != want {
		t.Fatalf("rejected %q, quero %q (status %q)", got, want, run("status"))
	}
	time.Sleep(500 * time.Millisecond)
	if got := run("rejected"); got != want {
		t.Fatalf("depois do boot: %q", got)
	}
}

// TestUntrustedNVRAMAndNames: the NVRAM of a lab package comes in only as
// regular files with NVRAM names (a link planted in the tar does not), and
// a project or node name with a path in it is refused.
func TestUntrustedNVRAMAndNames(t *testing.T) {
	state, dir := t.TempDir(), t.TempDir()
	victim := filepath.Join(dir, "vitima")
	os.WriteFile(victim, []byte("original"), 0o644)
	src := filepath.Join(dir, "src")
	os.MkdirAll(src, 0o755)
	os.WriteFile(filepath.Join(src, "nvram_00001"), []byte("cfg"), 0o644)
	os.Symlink(victim, filepath.Join(src, "startup-config"))
	os.Symlink(victim, filepath.Join(src, "nvram_00002"))
	tarf := filepath.Join(dir, "n.tar")
	if out, err := exec.Command("tar", "-cf", tarf, "-C", src, "nvram_00001", "startup-config", "nvram_00002").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	cmd := exec.Command("sh", "-c", Script, "vmctl", "nvram-unpack")
	cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+state, "GV_PROJECT=lab", "GV_NAME=r1", "GV_FILE="+tarf)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	d := filepath.Join(state, "gantry/vms/lab/r1")
	if b, _ := os.ReadFile(filepath.Join(d, "nvram_00001")); string(b) != "cfg" {
		t.Fatalf("nvram: %q", b)
	}
	for _, f := range []string{"startup-config", "nvram_00002"} {
		if _, err := os.Lstat(filepath.Join(d, f)); err == nil {
			t.Fatalf("%s entrou", f)
		}
	}
	for _, env := range [][]string{{"GV_PROJECT=../../x", "GV_NAME=r1"}, {"GV_PROJECT=lab", "GV_NAME=r1'x"}} {
		cmd := exec.Command("sh", "-c", Script, "vmctl", "status")
		cmd.Env = append(append(os.Environ(), "XDG_STATE_HOME="+state), env...)
		if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "nome inválido") {
			t.Fatalf("%v: %v %s", env, err, out)
		}
	}
}

// The bridge mode of the VM network: net-set keeps the choice, and the
// addresses come from its range, skipping the machine's own address; when
// the mode changes, an address of the other network is given again.
func TestNetBridgeRange(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("usa ip(8)")
	}
	state := t.TempDir()
	run := func(cmd string, env ...string) (string, error) {
		c := exec.Command("sh", "-c", Script, "vmctl", cmd)
		c.Env = append(append(os.Environ(), "XDG_STATE_HOME="+state), env...)
		out, err := c.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	for _, bad := range [][]string{
		{"GV_NET_IFACE=en0;rm", "GV_NET_GW=192.0.2.1", "GV_NET_FIRST=200", "GV_NET_LAST=210"},
		{"GV_NET_IFACE=br0", "GV_NET_GW=192.0.2.1$(id)", "GV_NET_FIRST=200", "GV_NET_LAST=210"},
		{"GV_NET_IFACE=br0", "GV_NET_GW=192.0.2.1", "GV_NET_FIRST=210", "GV_NET_LAST=200"},
	} {
		if out, err := run("net-set", append([]string{"GV_NET_MODE=bridge"}, bad...)...); err == nil {
			t.Errorf("devia recusar %v: %s", bad, out)
		}
	}
	// The machine's own address is in the range: it is skipped.
	self := ""
	if out, err := exec.Command("sh", "-c", "ip -4 -o addr | awk '$2 != \"lo\" {split($4, a, \"/\"); print a[1]; exit}'").Output(); err == nil {
		self = strings.TrimSpace(string(out))
	}
	if self == "" {
		t.Skip("sem endereço IPv4 fora do loopback")
	}
	pre := self[:strings.LastIndex(self, ".")+1]
	n, _ := strconv.Atoi(self[strings.LastIndex(self, ".")+1:])
	gw := pre + "254"
	if n >= 250 {
		t.Skip("endereço da máquina no fim da faixa")
	}
	if out, err := run("net-set", "GV_NET_MODE=bridge", "GV_NET_IFACE=lo", "GV_NET_GW="+gw, "GV_NET_FIRST="+strconv.Itoa(n), "GV_NET_LAST="+strconv.Itoa(n+2)); err != nil {
		t.Fatalf("net-set: %v %s", err, out)
	}
	info, _ := run("netinfo")
	if !strings.Contains(info, "net=bridge") || !strings.Contains(info, "interface=lo") || !strings.Contains(info, "first="+strconv.Itoa(n)) {
		t.Fatalf("netinfo: %s", info)
	}
	// lo is not a bridge, so there is no network yet: lan-ip refuses.
	if out, err := run("lan-ip", "GV_PROJECT=p", "GV_NAME=a"); err == nil {
		t.Fatalf("sem bridge, lan-ip devia recusar: %s", out)
	}
	if exec.Command("sh", "-c", "ip link add gvtbr0 type bridge && ip link set gvtbr0 up").Run() != nil {
		t.Skip("sem permissão para criar uma bridge")
	}
	defer exec.Command("ip", "link", "del", "gvtbr0").Run()
	run("net-set", "GV_NET_MODE=bridge", "GV_NET_IFACE=gvtbr0", "GV_NET_GW="+gw, "GV_NET_FIRST="+strconv.Itoa(n), "GV_NET_LAST="+strconv.Itoa(n+2))
	ip, err := run("lan-ip", "GV_PROJECT=p", "GV_NAME=a")
	if err != nil || ip != pre+strconv.Itoa(n+1) {
		t.Fatalf("lan-ip devia pular o endereço da máquina (%s): %q %v", self, ip, err)
	}
	if again, _ := run("lan-ip", "GV_PROJECT=p", "GV_NAME=a"); again != ip {
		t.Errorf("o endereço devia ser o mesmo: %s × %s", again, ip)
	}
	// Back to the shared network (gantry0): the address of the bridge goes.
	if out, err := run("net-set", "GV_NET_MODE=compartilhada"); err != nil {
		t.Fatalf("voltar: %v %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(state, "gantry/vms/rede.conf")); err == nil {
		t.Error("rede.conf devia sumir")
	}
	if exec.Command("sh", "-c", "ip link add gantry0 type bridge && ip addr add 10.213.0.1/24 dev gantry0 && ip link set gantry0 up").Run() == nil {
		defer exec.Command("ip", "link", "del", "gantry0").Run()
		if ip2, err := run("lan-ip", "GV_PROJECT=p", "GV_NAME=a"); err != nil || ip2 != "10.213.0.100" {
			t.Errorf("de volta à compartilhada: %q %v", ip2, err)
		}
	}
}
