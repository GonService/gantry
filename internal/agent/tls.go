package agent

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Protocol is the version of what the agent offers. The app asks for an
// update only when it needs a newer protocol, not on every release.
//
//	1: info, metrics, watch over the SSH-forwarded socket
//	2: VM metrics, direct TCP with mutual TLS
//	3: commands through the agent (/v1/exec), without SSH sessions
//	4: a command does not wait for programs it left in the background;
//	   the service keeps the labs running when it restarts (KillMode)
const Protocol = 4

// DefaultTLSPort is where the agent listens for direct connections.
const DefaultTLSPort = 7781

// Header lines of a direct connection, sent by the app right after the TLS
// handshake. The agent answers "OK\n" or "ERR <message>\n".
const (
	hdrAgent  = "GANTRY/1 agent"
	hdrEngine = "GANTRY/1 engine"
)

// NewCert makes a self-signed ECDSA certificate. Each side of the pairing
// has its own; neither private key ever leaves its machine.
func NewCert(name string) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: name, Organization: []string{"Gantry"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	kder, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}), nil
}

// Fingerprint is the SHA-256 of a certificate, as hex, from its PEM.
func Fingerprint(certPEM []byte) string {
	b, _ := pem.Decode(certPEM)
	if b == nil {
		return ""
	}
	sum := sha256.Sum256(b.Bytes)
	return hex.EncodeToString(sum[:])
}

func certsOf(pemData []byte) map[[32]byte]bool {
	out := map[[32]byte]bool{}
	for {
		var b *pem.Block
		b, pemData = pem.Decode(pemData)
		if b == nil {
			return out
		}
		if b.Type == "CERTIFICATE" {
			out[sha256.Sum256(b.Bytes)] = true
		}
	}
}

// pinned accepts exactly the given certificates, whatever their names:
// trust comes from the pairing over SSH, not from a CA.
func pinned(trusted map[[32]byte]bool) func([][]byte, [][]*x509.Certificate) error {
	return func(raw [][]byte, _ [][]*x509.Certificate) error {
		if len(raw) == 0 {
			return errors.New("sem certificado")
		}
		if !trusted[sha256.Sum256(raw[0])] {
			return errors.New("certificado não pareado com este Gantry")
		}
		return nil
	}
}

// ClientTLS is the app's side: its own certificate, and the agent's
// certificate (from the pairing) as the only one accepted.
func ClientTLS(clientCert, clientKey, agentCert []byte) (*tls.Config, error) {
	kp, err := tls.X509KeyPair(clientCert, clientKey)
	if err != nil {
		return nil, err
	}
	trusted := certsOf(agentCert)
	if len(trusted) == 0 {
		return nil, errors.New("certificado do agente inválido")
	}
	return &tls.Config{
		Certificates:          []tls.Certificate{kp},
		MinVersion:            tls.VersionTLS13,
		InsecureSkipVerify:    true, // checked by VerifyPeerCertificate (pinned)
		VerifyPeerCertificate: pinned(trusted),
	}, nil
}

// DialDirect opens a direct connection to an agent and asks for the agent
// API ("" engine) or for an engine socket on the server.
func DialDirect(ctx context.Context, addr string, cfg *tls.Config, engineSock string, engine bool) (net.Conn, error) {
	d := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 3 * time.Second}, Config: cfg}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	hdr := hdrAgent
	if engine {
		hdr = strings.TrimSpace(hdrEngine + " " + engineSock)
	}
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(c, hdr+"\n"); err != nil {
		c.Close()
		return nil, err
	}
	line, err := readLine(c)
	if err != nil {
		c.Close()
		return nil, err
	}
	if line != "OK" {
		c.Close()
		return nil, fmt.Errorf("agente: %s", strings.TrimPrefix(line, "ERR "))
	}
	_ = c.SetDeadline(time.Time{})
	return c, nil
}

// readLine reads up to a newline one byte at a time, so nothing after it is
// consumed (the HTTP that follows belongs to someone else).
func readLine(r io.Reader) (string, error) {
	var b []byte
	one := make([]byte, 1)
	for len(b) < 1024 {
		if _, err := io.ReadFull(r, one); err != nil {
			return "", err
		}
		if one[0] == '\n' {
			return strings.TrimSpace(string(b)), nil
		}
		b = append(b, one[0])
	}
	return "", errors.New("linha longa demais")
}

// TLSDir holds the agent's certificate, the trusted app certificates and
// the address to listen on.
func TLSDir() string { return filepath.Join(filepath.Dir(DefaultState()), "tls") }

// Pair is run on the server (over SSH) by the app: it trusts the app's
// certificate, creates the agent's own if needed, saves the address and
// returns the agent's certificate for the app to pin.
func Pair(dir string, clientCert []byte, addr string) ([]byte, error) {
	if len(certsOf(clientCert)) == 0 {
		return nil, errors.New("certificado do app inválido")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	crt, key := filepath.Join(dir, "agent.crt"), filepath.Join(dir, "agent.key")
	if _, err := tls.LoadX509KeyPair(crt, key); err != nil {
		host, _ := os.Hostname()
		c, k, err := NewCert("gantry-agent " + host)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(key, k, 0o600); err != nil {
			return nil, err
		}
		if err := os.WriteFile(crt, c, 0o644); err != nil {
			return nil, err
		}
	}
	clients := filepath.Join(dir, "clients.pem")
	old, _ := os.ReadFile(clients)
	have := certsOf(old)
	for k := range certsOf(clientCert) {
		if !have[k] {
			old = append(old, clientCert...)
			break
		}
	}
	if err := os.WriteFile(clients, old, 0o600); err != nil {
		return nil, err
	}
	if addr == "" {
		addr = fmt.Sprintf(":%d", DefaultTLSPort)
	}
	if err := os.WriteFile(filepath.Join(dir, "addr"), []byte(addr+"\n"), 0o600); err != nil {
		return nil, err
	}
	return os.ReadFile(crt)
}

// serverTLS loads the agent's side, or nil when it was never paired.
func serverTLS(dir string) (*tls.Config, string, error) {
	addr, err := os.ReadFile(filepath.Join(dir, "addr"))
	if err != nil {
		return nil, "", nil
	}
	kp, err := tls.LoadX509KeyPair(filepath.Join(dir, "agent.crt"), filepath.Join(dir, "agent.key"))
	if err != nil {
		return nil, "", err
	}
	clients, err := os.ReadFile(filepath.Join(dir, "clients.pem"))
	if err != nil {
		return nil, "", err
	}
	trusted := certsOf(clients)
	if len(trusted) == 0 {
		return nil, "", errors.New("nenhum app pareado")
	}
	return &tls.Config{
		Certificates:          []tls.Certificate{kp},
		MinVersion:            tls.VersionTLS13,
		ClientAuth:            tls.RequireAnyClientCert,
		VerifyPeerCertificate: pinned(trusted),
	}, strings.TrimSpace(string(addr)), nil
}

// connListener hands connections accepted elsewhere to an http.Server.
type connListener struct {
	ch   chan net.Conn
	done chan struct{}
	addr net.Addr
}

func (l *connListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *connListener) Close() error {
	select {
	case <-l.done:
	default:
		close(l.done)
	}
	return nil
}
func (l *connListener) Addr() net.Addr { return l.addr }

// serveDirect accepts direct connections: after the handshake (both sides
// pinned) a header picks the agent API or an engine socket.
func (a *Agent) serveDirect(ctx context.Context, ln net.Listener, cfg *tls.Config, api *connListener) {
	go func() { <-ctx.Done(); ln.Close() }()
	for {
		raw, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			c := tls.Server(raw, cfg)
			_ = c.SetDeadline(time.Now().Add(10 * time.Second))
			if err := c.HandshakeContext(ctx); err != nil {
				a.opt.Log("conexão direta recusada de %s: %v", raw.RemoteAddr(), err)
				c.Close()
				return
			}
			line, err := readLine(c)
			if err != nil {
				c.Close()
				return
			}
			_ = c.SetDeadline(time.Time{})
			switch {
			case line == hdrAgent:
				if _, err := io.WriteString(c, "OK\n"); err != nil {
					c.Close()
					return
				}
				select {
				case api.ch <- c:
				case <-ctx.Done():
					c.Close()
				}
			case strings.HasPrefix(line, hdrEngine):
				sock := strings.TrimSpace(strings.TrimPrefix(line, hdrEngine))
				var e net.Conn
				if sock == "" {
					e, err = a.dialEngine(ctx)
				} else {
					var d net.Dialer
					e, err = d.DialContext(ctx, "unix", sock)
				}
				if err != nil {
					_, _ = io.WriteString(c, "ERR "+strings.ReplaceAll(err.Error(), "\n", " ")+"\n")
					c.Close()
					return
				}
				if _, err := io.WriteString(c, "OK\n"); err != nil {
					c.Close()
					e.Close()
					return
				}
				splice(c, e)
			default:
				_, _ = io.WriteString(c, "ERR cabeçalho desconhecido\n")
				c.Close()
			}
		}()
	}
}

// splice copies both ways until either side closes.
func splice(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, bufio.NewReader(src))
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	<-done
	a.Close()
	b.Close()
}
