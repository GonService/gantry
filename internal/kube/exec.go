package kube

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"gantry/internal/applog"
)

// A minimal WebSocket client (RFC 6455) for the exec subprotocol of the API
// server (v4.channel.k8s.io: the first byte of each binary message is the
// stream: 0 stdin, 1 stdout, 2 stderr, 3 error, 4 resize).

type wsConn struct {
	conn net.Conn
	r    *bufio.Reader
	wmu  sync.Mutex
}

func (k *Client) dialWS(ctx context.Context, path string, q url.Values, protocols []string) (*wsConn, string, error) {
	u, err := url.Parse(k.Server + path)
	if err != nil {
		return nil, "", err
	}
	u.RawQuery = q.Encode()
	host := u.Host
	if u.Port() == "" {
		if u.Scheme == "https" {
			host += ":443"
		} else {
			host += ":80"
		}
	}
	d := &net.Dialer{Timeout: 15 * time.Second}
	var conn net.Conn
	if k.user.Exec != nil {
		if _, err := k.execToken(ctx); err != nil {
			return nil, "", err
		}
	}
	// The same proxy as the other requests (kubeconfig proxy-url or the
	// HTTPS_PROXY/NO_PROXY of the environment).
	var proxy *url.URL
	if tr, ok := k.http.Transport.(*http.Transport); ok && tr.Proxy != nil {
		pu := *u
		if pu.Scheme == "wss" {
			pu.Scheme = "https"
		}
		proxy, err = tr.Proxy(&http.Request{URL: &pu, Header: http.Header{}})
		if err != nil {
			return nil, "", fmt.Errorf("proxy: %v", err)
		}
	}
	if proxy != nil {
		conn, err = dialProxy(ctx, d, proxy, host)
	} else {
		conn, err = d.DialContext(ctx, "tcp", host)
	}
	if err == nil && u.Scheme == "https" {
		tc := k.TLS()
		if tc.ServerName == "" {
			tc.ServerName = u.Hostname()
		}
		tc.NextProtos = []string{"http/1.1"}
		tlsConn := tls.Client(conn, tc)
		if herr := tlsConn.HandshakeContext(ctx); herr != nil {
			conn.Close()
			err = herr
		} else {
			conn = tlsConn
		}
	}
	if err != nil {
		return nil, "", fmt.Errorf("não consegui falar com o cluster: %v", err)
	}
	keyb := make([]byte, 16)
	_, _ = rand.Read(keyb)
	key := base64.StdEncoding.EncodeToString(keyb)
	h := http.Header{}
	if err := k.authorize(ctx, h); err != nil {
		conn.Close()
		return nil, "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Protocol: %s\r\nUser-Agent: gantry\r\n",
		u.RequestURI(), u.Host, key, strings.Join(protocols, ", "))
	for k, vs := range h {
		for _, v := range vs {
			if strings.ContainsAny(v, "\r\n") {
				conn.Close()
				return nil, "", errors.New("credencial do kubeconfig com quebra de linha")
			}
			fmt.Fprintf(&b, "%s: %s\r\n", k, v)
		}
	}
	b.WriteString("\r\n")
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if _, err := io.WriteString(conn, b.String()); err != nil {
		conn.Close()
		return nil, "", err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		conn.Close()
		return nil, "", err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		conn.Close()
		var st struct{ Message string }
		_ = json.Unmarshal(body, &st)
		if st.Message == "" {
			st.Message = strings.TrimSpace(string(body))
		}
		return nil, "", &APIError{Status: resp.StatusCode, Message: st.Message}
	}
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	if resp.Header.Get("Sec-WebSocket-Accept") != base64.StdEncoding.EncodeToString(sum[:]) {
		conn.Close()
		return nil, "", errors.New("resposta WebSocket inválida do cluster")
	}
	_ = conn.SetDeadline(time.Time{})
	return &wsConn{conn: conn, r: br}, resp.Header.Get("Sec-WebSocket-Protocol"), nil
}

// dialProxy opens a tunnel to addr through an HTTP(S) proxy (CONNECT).
func dialProxy(ctx context.Context, d *net.Dialer, proxy *url.URL, addr string) (net.Conn, error) {
	paddr := proxy.Host
	if proxy.Port() == "" {
		switch proxy.Scheme {
		case "https":
			paddr += ":443"
		case "http":
			paddr += ":80"
		}
	}
	var conn net.Conn
	var err error
	switch proxy.Scheme {
	case "http":
		conn, err = d.DialContext(ctx, "tcp", paddr)
	case "https":
		conn, err = (&tls.Dialer{NetDialer: d, Config: &tls.Config{ServerName: proxy.Hostname()}}).DialContext(ctx, "tcp", paddr)
	default:
		return nil, fmt.Errorf("o shell não funciona com proxy %s (só http e https)", proxy.Scheme)
	}
	if err != nil {
		return nil, fmt.Errorf("proxy %s: %v", proxy.Host, err)
	}
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: addr}, Host: addr, Header: http.Header{}}
	if proxy.User != nil {
		pw, _ := proxy.User.Password()
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(proxy.User.Username()+":"+pw)))
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("proxy %s: %v", proxy.Host, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		conn.Close()
		return nil, fmt.Errorf("o proxy %s recusou a conexão: %s", proxy.Host, resp.Status)
	}
	_ = conn.SetDeadline(time.Time{})
	if br.Buffered() > 0 {
		conn.Close()
		return nil, fmt.Errorf("o proxy %s mandou dados antes da hora", proxy.Host)
	}
	return conn, nil
}

const (
	opCont   = 0
	opText   = 1
	opBinary = 2
	opClose  = 8
	opPing   = 9
	opPong   = 10
)

func (c *wsConn) write(op byte, payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	hdr := []byte{0x80 | op}
	n := len(payload)
	switch {
	case n < 126:
		hdr = append(hdr, 0x80|byte(n))
	case n < 1<<16:
		hdr = append(hdr, 0x80|126, byte(n>>8), byte(n))
	default:
		hdr = append(hdr, 0x80|127)
		var l [8]byte
		binary.BigEndian.PutUint64(l[:], uint64(n))
		hdr = append(hdr, l[:]...)
	}
	var mask [4]byte
	_, _ = rand.Read(mask[:])
	hdr = append(hdr, mask[:]...)
	masked := make([]byte, n)
	for i := range payload {
		masked[i] = payload[i] ^ mask[i%4]
	}
	_, err := c.conn.Write(append(hdr, masked...))
	return err
}

// read returns the next data message (answering pings, joining fragments).
func (c *wsConn) read() (byte, []byte, error) {
	var msg []byte
	var msgOp byte
	for {
		var h [2]byte
		if _, err := io.ReadFull(c.r, h[:]); err != nil {
			return 0, nil, err
		}
		fin, op := h[0]&0x80 != 0, h[0]&0x0f
		masked := h[1]&0x80 != 0
		n := uint64(h[1] & 0x7f)
		switch n {
		case 126:
			var l [2]byte
			if _, err := io.ReadFull(c.r, l[:]); err != nil {
				return 0, nil, err
			}
			n = uint64(binary.BigEndian.Uint16(l[:]))
		case 127:
			var l [8]byte
			if _, err := io.ReadFull(c.r, l[:]); err != nil {
				return 0, nil, err
			}
			n = binary.BigEndian.Uint64(l[:])
		}
		if n > 64<<20 {
			return 0, nil, errors.New("mensagem grande demais")
		}
		var mask [4]byte
		if masked {
			if _, err := io.ReadFull(c.r, mask[:]); err != nil {
				return 0, nil, err
			}
		}
		p := make([]byte, n)
		if _, err := io.ReadFull(c.r, p); err != nil {
			return 0, nil, err
		}
		if masked {
			for i := range p {
				p[i] ^= mask[i%4]
			}
		}
		switch op {
		case opPing:
			_ = c.write(opPong, p)
			continue
		case opPong:
			continue
		case opClose:
			return opClose, p, io.EOF
		case opCont:
			msg = append(msg, p...)
		default:
			msgOp, msg = op, p
		}
		if fin {
			return msgOp, msg, nil
		}
	}
}

func (c *wsConn) Close() error {
	_ = c.write(opClose, []byte{0x03, 0xe8})
	return c.conn.Close()
}

// ExecSession is an interactive TTY in a container of a pod.
type ExecSession struct {
	ws      *wsConn
	pr      *io.PipeReader
	pw      *io.PipeWriter
	errMsg  string
	errOnce sync.Once
}

func (e *ExecSession) Read(p []byte) (int, error) { return e.pr.Read(p) }

func (e *ExecSession) Write(p []byte) (int, error) {
	if err := e.ws.write(opBinary, append([]byte{0}, p...)); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (e *ExecSession) Resize(_ context.Context, cols, rows int) error {
	b, _ := json.Marshal(map[string]int{"Width": cols, "Height": rows})
	return e.ws.write(opBinary, append([]byte{4}, b...))
}

func (e *ExecSession) Close() error {
	e.pw.Close()
	return e.ws.Close()
}

// Exec opens a shell (or cmd) with a TTY in a container of a pod.
func (k *Client) Exec(ctx context.Context, ns, pod, container string, cmd []string) (*ExecSession, error) {
	return k.stream(ctx, ns, pod, container, "exec", cmd)
}

// Attach joins the TTY of a running container (the debug containers run
// their shell as the main process).
func (k *Client) Attach(ctx context.Context, ns, pod, container string) (*ExecSession, error) {
	return k.stream(ctx, ns, pod, container, "attach", nil)
}

func (k *Client) stream(ctx context.Context, ns, pod, container, sub string, cmd []string) (*ExecSession, error) {
	q := url.Values{"stdin": {"true"}, "stdout": {"true"}, "stderr": {"true"}, "tty": {"true"}}
	if container != "" {
		q.Set("container", container)
	}
	if len(cmd) > 0 {
		q["command"] = cmd
	}
	path := "/api/v1/namespaces/" + url.PathEscape(ns) + "/pods/" + url.PathEscape(pod) + "/" + sub
	ws, _, err := k.dialWS(ctx, path, q, []string{"v4.channel.k8s.io", "channel.k8s.io"})
	if err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	e := &ExecSession{ws: ws, pr: pr, pw: pw}
	go func() {
		defer applog.Recover("kube/exec.go:343")
		for {
			op, msg, err := ws.read()
			if err != nil {
				pw.CloseWithError(io.EOF)
				return
			}
			if op != opBinary && op != opText || len(msg) == 0 {
				continue
			}
			switch msg[0] {
			case 1, 2:
				if _, err := pw.Write(msg[1:]); err != nil {
					return
				}
			case 3:
				// The final status (v4: a Status object).
				var st struct{ Status, Message string }
				if json.Unmarshal(msg[1:], &st) == nil && st.Status == "Failure" && st.Message != "" {
					_, _ = pw.Write([]byte("\r\n" + st.Message + "\r\n"))
				}
			}
		}
	}()
	return e, nil
}
