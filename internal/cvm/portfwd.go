package cvm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The ports the containers publish in the machine show up on the Mac, as
// with Docker Desktop: localhost:8080 on the Mac leads to the 8080 a
// container published in the machine. The runner asks the engines (Docker
// and Podman, through their sockets on the Mac) which ports are published,
// every couple of seconds, opens the same port on the Mac and relays each
// connection to the machine's address on the network the Mac shares with it.

// pubPort is a port a container published: on which address of the machine
// (0.0.0.0, ::, 127.0.0.1…), the port and tcp or udp.
type pubPort struct {
	IP    string
	Port  int
	Proto string
}

func (p pubPort) key() string { return p.Proto + ":" + strconv.Itoa(p.Port) }

// publishedPorts lists the published ports of the running containers of an
// engine, by its Docker-compatible API (Docker, and Podman's compat API).
func publishedPorts(ctx context.Context, sock string) ([]pubPort, error) {
	c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://engine/containers/json", nil)
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("containers/json: %s", resp.Status)
	}
	var list []struct {
		Ports []struct {
			IP          string
			PrivatePort int
			PublicPort  int
			Type        string
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	var out []pubPort
	for _, ct := range list {
		for _, p := range ct.Ports {
			if p.PublicPort > 0 {
				out = append(out, pubPort{IP: p.IP, Port: p.PublicPort, Proto: strings.ToLower(p.Type)})
			}
		}
	}
	return out, nil
}

// wantedPorts merges the engines' lists: one entry per proto:port, open on
// every address of the Mac when any container published it on every address
// of the machine, and only on localhost otherwise.
func wantedPorts(lists ...[]pubPort) map[string]pubPort {
	want := map[string]pubPort{}
	for _, l := range lists {
		for _, p := range l {
			if p.Proto != "tcp" && p.Proto != "udp" {
				continue
			}
			all := p.IP == "" || p.IP == "0.0.0.0" || p.IP == "::"
			q := pubPort{Port: p.Port, Proto: p.Proto, IP: "127.0.0.1"}
			if all {
				q.IP = ""
			}
			if old, ok := want[q.key()]; ok && old.IP == "" {
				continue
			}
			want[q.key()] = q
		}
	}
	return want
}

// leaseIP finds the machine's address in the DHCP leases macOS hands out to
// its virtual machines, by the MAC address (macOS writes it without the
// leading zeros: 52:54:0:a:…) or, failing that, by the host name.
func leaseIP(leases, mac, name string) string {
	norm := func(m string) string {
		var parts []string
		for _, x := range strings.Split(m, ":") {
			n, err := strconv.ParseUint(x, 16, 8)
			if err != nil {
				return ""
			}
			parts = append(parts, strconv.FormatUint(n, 16))
		}
		return strings.Join(parts, ":")
	}
	want := norm(mac)
	var byName string
	for _, block := range strings.Split(leases, "}") {
		var ip, hw, host string
		for _, l := range strings.Split(block, "\n") {
			l = strings.TrimSpace(l)
			switch {
			case strings.HasPrefix(l, "ip_address="):
				ip = strings.TrimPrefix(l, "ip_address=")
			case strings.HasPrefix(l, "hw_address="):
				hw = strings.TrimPrefix(l, "hw_address=")
				if i := strings.Index(hw, ","); i >= 0 {
					hw = hw[i+1:]
				}
			case strings.HasPrefix(l, "name="):
				host = strings.TrimPrefix(l, "name=")
			}
		}
		if ip == "" {
			continue
		}
		if want != "" && norm(hw) == want {
			return ip
		}
		if host == name && byName == "" {
			byName = ip
		}
	}
	return byName
}

// leasesPath is where macOS keeps the addresses it hands out to the VMs.
var leasesPath = "/var/db/dhcpd_leases"

// Addr is the machine's address on the network it shares with the Mac
// ("" if macOS has not handed it one): the Mac reaches it directly, as in
// http://192.168.64.7:8080.
func Addr(name string) string {
	b, err := os.ReadFile(leasesPath)
	if err != nil {
		return ""
	}
	return leaseIP(string(b), mac(name), name)
}

// portForwarder keeps the Mac's listeners in step with the published ports.
type portForwarder struct {
	name   string
	spec   Spec
	log    *log.Logger
	leases string // /var/db/dhcpd_leases

	mu     sync.Mutex
	ip     string
	open     map[string]io.Closer // proto:port → listener
	failed   map[string]bool      // ports the Mac could not open (logged once)
	answered bool                 // the engines answered the last time
}

func newPortForwarder(name string, s Spec, lg *log.Logger) *portForwarder {
	return &portForwarder{name: name, spec: s, log: lg, leases: leasesPath, open: map[string]io.Closer{}, failed: map[string]bool{}}
}

// guestIP is the machine's address, read again when a connection fails.
func (f *portForwarder) guestIP(fresh bool) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ip != "" && !fresh {
		return f.ip
	}
	b, err := os.ReadFile(f.leases)
	if err == nil {
		if ip := leaseIP(string(b), mac(f.name), f.name); ip != "" {
			f.ip = ip
		}
	}
	return f.ip
}

// Ports lists the ports open on the Mac now, for the app.
func (f *portForwarder) Ports() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.open))
	for k := range f.open {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// PortsState is what the app shows: the ports open on the Mac, the ones
// the Mac could not open, the machine's address and whether the engines
// answered.
type PortsState struct {
	Open    []string `json:"open"`
	Failed  []string `json:"failed"`
	IP      string   `json:"ip"`
	Engines bool     `json:"engines"`
}

func (f *portForwarder) State() PortsState {
	st := PortsState{Open: f.Ports(), Failed: []string{}}
	f.mu.Lock()
	for k := range f.failed {
		st.Failed = append(st.Failed, k)
	}
	st.Engines = f.answered
	f.mu.Unlock()
	sort.Strings(st.Failed)
	st.IP = f.guestIP(false)
	return st
}

func (f *portForwarder) loop(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	defer f.closeAll()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		var lists [][]pubPort
		ok := false
		for _, sock := range f.sockets() {
			l, err := publishedPorts(ctx, sock)
			if err != nil {
				continue
			}
			ok = true
			lists = append(lists, l)
		}
		f.mu.Lock()
		f.answered = ok
		f.mu.Unlock()
		if !ok {
			continue // the engines are not up yet (or restarting): keep what is open
		}
		f.sync(wantedPorts(lists...))
	}
}

func (f *portForwarder) sockets() []string {
	var s []string
	if f.spec.Docker {
		s = append(s, DockerSocket(f.name))
	}
	if f.spec.Podman {
		s = append(s, PodmanSocket(f.name))
	}
	return s
}

func (f *portForwarder) sync(want map[string]pubPort) {
	f.mu.Lock()
	var closeNow []io.Closer
	for k, l := range f.open {
		if _, ok := want[k]; !ok {
			closeNow = append(closeNow, l)
			delete(f.open, k)
			f.log.Printf("porta %s: não é mais publicada, fechada no Mac", k)
		}
	}
	for k := range f.failed {
		if _, ok := want[k]; !ok {
			delete(f.failed, k)
		}
	}
	var openNow []pubPort
	for k, p := range want {
		if _, ok := f.open[k]; !ok && !f.failed[k] {
			openNow = append(openNow, p)
		}
	}
	f.mu.Unlock()
	for _, c := range closeNow {
		_ = c.Close()
	}
	for _, p := range openNow {
		c, err := f.listen(p)
		f.mu.Lock()
		if err != nil {
			f.failed[p.key()] = true
			f.log.Printf("porta %s: o Mac não abriu (%v); talvez outro programa já use", p.key(), err)
		} else {
			f.open[p.key()] = c
			f.log.Printf("porta %s: aberta no Mac (%s), levando à máquina", p.key(), addrLabel(p))
		}
		f.mu.Unlock()
	}
}

func addrLabel(p pubPort) string {
	if p.IP == "" {
		return "todos os endereços"
	}
	return p.IP
}

func (f *portForwarder) listen(p pubPort) (io.Closer, error) {
	addr := net.JoinHostPort(p.IP, strconv.Itoa(p.Port))
	if p.Proto == "udp" {
		pc, err := net.ListenPacket("udp", addr)
		if err != nil {
			return nil, err
		}
		go f.relayUDP(pc, p.Port)
		return pc, nil
	}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go f.relayTCP(c, p.Port)
		}
	}()
	return l, nil
}

func (f *portForwarder) dial(port int) (net.Conn, error) {
	for _, fresh := range []bool{false, true} {
		ip := f.guestIP(fresh)
		if ip == "" {
			continue
		}
		c, err := net.DialTimeout("tcp", net.JoinHostPort(ip, strconv.Itoa(port)), 3*time.Second)
		if err == nil {
			return c, nil
		}
		if fresh {
			return nil, err
		}
	}
	return nil, fmt.Errorf("não achei o endereço da máquina em %s", f.leases)
}

func (f *portForwarder) relayTCP(c net.Conn, port int) {
	defer c.Close()
	g, err := f.dial(port)
	if err != nil {
		return
	}
	defer g.Close()
	done := make(chan struct{}, 2)
	half := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if tc, ok := dst.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
		done <- struct{}{}
	}
	go half(g, c)
	go half(c, g)
	<-done
	<-done
}

// relayUDP keeps one socket to the machine per client address, dropped
// after a minute without traffic.
func (f *portForwarder) relayUDP(pc net.PacketConn, port int) {
	type peer struct {
		conn net.Conn
		last time.Time
	}
	var mu sync.Mutex
	peers := map[string]*peer{}
	buf := make([]byte, 64<<10)
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			mu.Lock()
			for _, p := range peers {
				_ = p.conn.Close()
			}
			mu.Unlock()
			return
		}
		mu.Lock()
		p := peers[from.String()]
		if p == nil {
			ip := f.guestIP(false)
			if ip == "" {
				mu.Unlock()
				continue
			}
			c, err := net.Dial("udp", net.JoinHostPort(ip, strconv.Itoa(port)))
			if err != nil {
				mu.Unlock()
				continue
			}
			p = &peer{conn: c}
			peers[from.String()] = p
			go func(key string, p *peer, from net.Addr) {
				back := make([]byte, 64<<10)
				for {
					_ = p.conn.SetReadDeadline(time.Now().Add(time.Minute))
					k, err := p.conn.Read(back)
					if err != nil {
						mu.Lock()
						delete(peers, key)
						mu.Unlock()
						_ = p.conn.Close()
						return
					}
					_, _ = pc.WriteTo(back[:k], from)
				}
			}(from.String(), p, from)
		}
		p.last = time.Now()
		mu.Unlock()
		_, _ = p.conn.Write(buf[:n])
	}
}

func (f *portForwarder) closeAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, l := range f.open {
		_ = l.Close()
		delete(f.open, k)
	}
}
