package vms

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"gantry/internal/applog"
	"gantry/internal/hosts"
)

// Session is a terminal inside a VM over SSH, with the key Gantry gives the
// VMs: a shell, or one command (journalctl -f for the logs).
type Session struct {
	client *ssh.Client
	sess   *ssh.Session
	stdin  io.WriteCloser
	out    *io.PipeReader
}

func (s *Session) Read(p []byte) (int, error)  { return s.out.Read(p) }
func (s *Session) Write(p []byte) (int, error) { return s.stdin.Write(p) }

func (s *Session) Close() error {
	_ = s.sess.Close()
	return s.client.Close()
}

func (s *Session) Resize(_ context.Context, cols, rows int) error {
	return s.sess.WindowChange(rows, cols)
}

// Open connects to the VM through its SSH port on the machine (through the
// SSH tunnel when the machine is a server). cmd empty opens a shell.
func Open(ctx context.Context, h *hosts.Host, project, name, cmd string) (*Session, error) {
	st, err := Status(ctx, h, project)
	if err != nil {
		return nil, err
	}
	var vm *State
	for i := range st {
		if st[i].Name == name {
			vm = &st[i]
		}
	}
	if vm == nil || vm.State != "running" {
		return nil, errors.New("a VM " + name + " não está rodando")
	}
	if vm.VNC > 0 {
		return nil, errors.New("esta VM foi instalada por ISO e não tem o acesso do Gantry; use Tela")
	}
	keyPath, err := Run(ctx, h, "keyfile", nil)
	if err != nil {
		return nil, err
	}
	key, err := h.ReadFile(ctx, strings.TrimSpace(keyPath))
	if err != nil {
		return nil, fmt.Errorf("não consegui ler a chave das VMs: %v", err)
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("chave das VMs inválida: %v", err)
	}
	addr, err := h.LocalAddr(ctx, net.JoinHostPort("127.0.0.1", strconv.Itoa(vm.SSHPort)))
	if err != nil {
		return nil, err
	}
	cfg := &ssh.ClientConfig{
		User: "gantry",
		Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		// The VM is local to the machine and recreated at will: its host key
		// changes with every disk, so there is nothing stable to pin.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}
	client, err := dialSSH(ctx, addr, cfg)
	if err != nil {
		return nil, fmt.Errorf("a VM %s ainda não aceita SSH (no primeiro boot, o cloud-init leva um ou dois minutos): %v", name, err)
	}
	sess, err := client.NewSession()
	if err != nil {
		client.Close()
		return nil, err
	}
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}
	if err := sess.RequestPty("xterm-256color", 40, 120, modes); err != nil {
		sess.Close()
		client.Close()
		return nil, err
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		sess.Close()
		client.Close()
		return nil, err
	}
	pr, pw := io.Pipe()
	sess.Stdout, sess.Stderr = pw, pw
	if cmd == "" {
		err = sess.Shell()
	} else {
		err = sess.Start(cmd)
	}
	if err != nil {
		sess.Close()
		client.Close()
		return nil, err
	}
	go func() {
		defer applog.Recover("vms/sshterm.go:115")
		_ = sess.Wait()
		pw.Close()
	}()
	return &Session{client: client, sess: sess, stdin: stdin, out: pr}, nil
}

func dialSSH(ctx context.Context, addr string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
	d := net.Dialer{Timeout: cfg.Timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return ssh.NewClient(c, chans, reqs), nil
}

// serialSession is the serial console of a VM: QEMU's chardev socket on
// the machine, after the end of what it already printed.
type serialSession struct {
	conn net.Conn
	r    io.Reader
}

func (s *serialSession) Read(p []byte) (int, error)               { return s.r.Read(p) }
func (s *serialSession) Write(p []byte) (int, error)              { return s.conn.Write(p) }
func (s *serialSession) Close() error                             { return s.conn.Close() }
func (s *serialSession) Resize(_ context.Context, _, _ int) error { return nil }

// OpenSerial connects to the serial console of a running VM: the
// console of network appliances (Cisco IOS…) and of any VM before SSH.
func OpenSerial(ctx context.Context, h *hosts.Host, project, name string) (io.ReadWriteCloser, error) {
	st, err := Status(ctx, h, project)
	if err != nil {
		return nil, err
	}
	var vm *State
	for i := range st {
		if st[i].Name == name {
			vm = &st[i]
		}
	}
	if vm == nil || vm.State != "running" {
		return nil, errors.New("a VM " + name + " não está rodando")
	}
	if vm.Serial == 0 {
		return nil, errors.New("esta VM foi iniciada antes do console interativo existir; reinicie-a para usar o Console")
	}
	tail, _ := Run(ctx, h, "console-tail", map[string]string{"GV_PROJECT": project, "GV_NAME": name})
	conn, err := h.DialTCP(ctx, net.JoinHostPort("127.0.0.1", strconv.Itoa(vm.Serial)))
	if err != nil {
		return nil, fmt.Errorf("não consegui abrir o console serial: %v", err)
	}
	return &serialSession{conn: conn, r: io.MultiReader(strings.NewReader(tail), conn)}, nil
}
