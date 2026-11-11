package cvm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// syncScript makes the machine match the spec after a start: folders
// shared later, Rosetta turned on later. Idempotent.
func syncScript(s Spec) string {
	var b strings.Builder
	b.WriteString("set -e\n")
	// Machines made before the dynamic memory get its vsock bridge here.
	fmt.Fprintf(&b, `if [ ! -e /etc/systemd/system/gantry-vsock-mem.service ]; then
printf '[Unit]\nDescription=Gantry: memoria para o Mac\n[Service]\nExecStart=/usr/bin/socat VSOCK-LISTEN:%d,reuseaddr,fork OPEN:/proc/meminfo,rdonly\nRestart=always\nRestartSec=1\n[Install]\nWantedBy=multi-user.target\n' | sudo tee /etc/systemd/system/gantry-vsock-mem.service >/dev/null
sudo systemctl daemon-reload && sudo systemctl enable --now gantry-vsock-mem.service || true
fi
`, portMem)
	for i, sh := range s.Shares {
		mode := "rw"
		if sh.ReadOnly {
			mode = "ro"
		}
		fmt.Fprintf(&b, "sudo mkdir -p %s\n", sh.Path)
		fmt.Fprintf(&b, "grep -q '^share%d ' /etc/fstab || echo 'share%d %s virtiofs %s,nofail 0 0' | sudo tee -a /etc/fstab >/dev/null\n", i, i, sh.Path, mode)
		fmt.Fprintf(&b, "mountpoint -q %s || sudo mount %s || true\n", sh.Path, sh.Path)
	}
	if s.Rosetta {
		b.WriteString(`sudo mkdir -p /opt/rosetta
grep -q '^rosetta ' /etc/fstab || echo 'rosetta /opt/rosetta virtiofs ro,nofail 0 0' | sudo tee -a /etc/fstab >/dev/null
mountpoint -q /opt/rosetta || sudo mount /opt/rosetta 2>/dev/null || true
if [ -x /opt/rosetta/rosetta ] && [ ! -e /proc/sys/fs/binfmt_misc/rosetta ]; then
  printf '%s\n' ':rosetta:M::\x7fELF\x02\x01\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x02\x00\x3e\x00:\xff\xff\xff\xff\xff\xfe\xfe\x00\xff\xff\xff\xff\xff\xff\xff\xff\xfe\xff\xff\xff:/opt/rosetta/rosetta:CF' | sudo tee /etc/binfmt.d/rosetta.conf >/dev/null
  sudo systemctl restart systemd-binfmt || true
fi
`)
	}
	return b.String()
}

// SyncGuest applies syncScript once the machine answers ssh.
func SyncGuest(ctx context.Context, name string) error {
	s, err := Load(name)
	if err != nil {
		return err
	}
	if err := WaitSSH(ctx, name, 90*time.Second); err != nil {
		return err
	}
	_, err = Run(ctx, name, syncScript(s))
	return err
}

// WaitSSH waits until the machine answers ssh.
func WaitSSH(ctx context.Context, name string, d time.Duration) error {
	end := time.Now().Add(d)
	var last error
	for time.Now().Before(end) {
		if _, err := Run(ctx, name, "true"); err == nil {
			return nil
		} else {
			last = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("a máquina não respondeu ao ssh: %v", last)
}

// Platform is one architecture the machine runs.
type Platform struct {
	Name   string `json:"name"`
	How    string `json:"how"` // nativo, rosetta, qemu
	Active bool   `json:"active"`
}

// Platforms reads the binfmt handlers of the machine.
func Platforms(ctx context.Context, name string) ([]Platform, error) {
	out, err := Run(ctx, name, "uname -m; ls /proc/sys/fs/binfmt_misc 2>/dev/null")
	if err != nil {
		return nil, err
	}
	l := strings.Fields(out)
	if len(l) == 0 {
		return nil, errors.New("resposta vazia")
	}
	native := map[string]string{"aarch64": "linux/arm64", "x86_64": "linux/amd64"}[l[0]]
	have := map[string]bool{}
	for _, f := range l[1:] {
		have[f] = true
	}
	ps := []Platform{{Name: native, How: "nativo", Active: true}}
	if native == "linux/arm64" {
		ps = append(ps, Platform{Name: "linux/amd64", How: "rosetta", Active: have["rosetta"]})
		if !have["rosetta"] && have["qemu-x86_64"] {
			ps[len(ps)-1] = Platform{Name: "linux/amd64", How: "qemu", Active: true}
		}
		ps = append(ps, Platform{Name: "linux/arm/v7", How: "qemu", Active: have["qemu-arm"]})
	} else {
		ps = append(ps, Platform{Name: "linux/arm64", How: "qemu", Active: have["qemu-aarch64"]})
	}
	for _, q := range []struct{ p, f string }{{"linux/riscv64", "qemu-riscv64"}, {"linux/ppc64le", "qemu-ppc64le"}, {"linux/s390x", "qemu-s390x"}} {
		ps = append(ps, Platform{Name: q.p, How: "qemu", Active: have[q.f]})
	}
	return ps, nil
}

// QEMUScript installs the QEMU handlers (binfmt) in a running machine.
const QEMUScript = "sudo DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends qemu-user-static || sudo DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends qemu-user qemu-user-binfmt"

// Creds is what the machine uses of the Mac's credentials.
type Creds struct {
	Agent      bool     `json:"agent"`      // the forward is up
	AgentKeys  int      `json:"agentKeys"`  // keys the machine sees (-1: unknown)
	AgentSock  string   `json:"agentSock"`  // SSH_AUTH_SOCK of the Mac
	Registries []string `json:"registries"` // logins in ~/.docker/config.json
	Store      string   `json:"store"`      // credsStore (osxkeychain…)
	Folders    []string `json:"folders"`    // credential folders under home
}

// Credentials reports the credentials side of the machine.
func Credentials(ctx context.Context, name string) Creds {
	c := Creds{AgentKeys: -1, AgentSock: os.Getenv("SSH_AUTH_SOCK"), Registries: []string{}, Folders: []string{}}
	agent.Lock()
	if f := agent.cmd[name]; f != nil {
		select {
		case <-f.done:
		default:
			c.Agent = true
		}
	}
	agent.Unlock()
	if c.Agent {
		if out, err := Run(ctx, name, "SSH_AUTH_SOCK=/run/host-services/ssh-auth.sock ssh-add -l 2>/dev/null | grep -c . || true"); err == nil {
			fmt.Sscan(strings.TrimSpace(out), &c.AgentKeys)
		}
	}
	home, _ := os.UserHomeDir()
	if b, err := os.ReadFile(filepath.Join(home, ".docker", "config.json")); err == nil {
		var cfg struct {
			Auths       map[string]json.RawMessage `json:"auths"`
			CredHelpers map[string]string          `json:"credHelpers"`
			CredsStore  string                     `json:"credsStore"`
		}
		if json.Unmarshal(b, &cfg) == nil {
			seen := map[string]bool{}
			for k := range cfg.Auths {
				seen[k] = true
			}
			for k := range cfg.CredHelpers {
				seen[k] = true
			}
			for k := range seen {
				c.Registries = append(c.Registries, strings.TrimPrefix(strings.TrimPrefix(k, "https://"), "http://"))
			}
			sort.Strings(c.Registries)
			c.Store = cfg.CredsStore
		}
	}
	for _, d := range []string{".kube", ".config/gcloud", ".aws", ".azure", ".docker"} {
		if _, err := os.Stat(filepath.Join(home, d)); err == nil {
			c.Folders = append(c.Folders, "~/"+d)
		}
	}
	return c
}

// Term is an interactive ssh session in the machine (for the terminal).
type Term struct {
	cl   *ssh.Client
	sess *ssh.Session
	in   io.WriteCloser
	out  io.Reader
}

func (s *Term) Read(p []byte) (int, error)  { return s.out.Read(p) }
func (s *Term) Write(p []byte) (int, error) { return s.in.Write(p) }
func (s *Term) Close() error {
	_ = s.sess.Close()
	return s.cl.Close()
}
func (s *Term) Resize(_ context.Context, cols, rows int) error {
	return s.sess.WindowChange(rows, cols)
}

// Shell opens a shell (or runs cmd) in the machine with a terminal, over
// the vsock socket, with the machine's key.
func Shell(ctx context.Context, name, cmd string) (*Term, error) {
	key, err := os.ReadFile(filepath.Join(Dir(name), fKey))
	if err != nil {
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, err
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", filepath.Join(Dir(name), fSSH))
	if err != nil {
		return nil, fmt.Errorf("a máquina não está ligada: %v", err)
	}
	cfg := &ssh.ClientConfig{
		User: "gantry",
		Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		// The socket is the machine's own, in the user's folder: no one
		// else sits between.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
		Timeout:         10 * time.Second,
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, name, cfg)
	if err != nil {
		conn.Close()
		return nil, err
	}
	cl := ssh.NewClient(c, chans, reqs)
	sess, err := cl.NewSession()
	if err != nil {
		cl.Close()
		return nil, err
	}
	in, _ := sess.StdinPipe()
	out, _ := sess.StdoutPipe()
	sess.Stderr = nil
	if err := sess.RequestPty("xterm-256color", 40, 120, ssh.TerminalModes{ssh.ECHO: 1}); err != nil {
		cl.Close()
		return nil, err
	}
	if cmd == "" {
		err = sess.Shell()
	} else {
		err = sess.Start(cmd)
	}
	if err != nil {
		cl.Close()
		return nil, err
	}
	return &Term{cl: cl, sess: sess, in: in, out: out}, nil
}
