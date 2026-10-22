package kube

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"gantry/internal/applog"
)

// PortConn is one TCP connection to a port of a pod, over the WebSocket
// port-forward of the API server (v4.channel.k8s.io: channel 0 carries the
// data, channel 1 the errors; the first message of each is the port).
type PortConn struct {
	ws      *wsConn
	pr      *io.PipeReader
	pw      *io.PipeWriter
	once    sync.Once
	errMu   sync.Mutex
	errText string
}

func (p *PortConn) Read(b []byte) (int, error) { return p.pr.Read(b) }

func (p *PortConn) Write(b []byte) (int, error) {
	if err := p.ws.write(opBinary, append([]byte{0}, b...)); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (p *PortConn) Close() error {
	var err error
	p.once.Do(func() {
		p.pw.Close()
		err = p.ws.Close()
	})
	return err
}

// Err is what the kubelet said went wrong (connection refused…), if anything.
func (p *PortConn) Err() string {
	p.errMu.Lock()
	defer p.errMu.Unlock()
	return p.errText
}

// PortForward opens one connection to port of a pod.
func (k *Client) PortForward(ctx context.Context, ns, pod string, port int) (*PortConn, error) {
	path := "/api/v1/namespaces/" + url.PathEscape(ns) + "/pods/" + url.PathEscape(pod) + "/portforward"
	ws, _, err := k.dialWS(ctx, path, url.Values{"ports": {strconv.Itoa(port)}}, []string{"v4.channel.k8s.io"})
	if err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	p := &PortConn{ws: ws, pr: pr, pw: pw}
	go func() {
		defer applog.Recover("kube/portforward.go:61")
		seen := map[byte]bool{}
		for {
			op, msg, err := ws.read()
			if err != nil {
				msg := p.Err()
				if msg != "" {
					pw.CloseWithError(errors.New(msg))
				} else {
					pw.CloseWithError(io.EOF)
				}
				return
			}
			if (op != opBinary && op != opText) || len(msg) == 0 {
				continue
			}
			ch, data := msg[0], msg[1:]
			// The first message of each channel is the port (2 bytes).
			if !seen[ch] {
				seen[ch] = true
				if len(data) >= 2 && int(binary.LittleEndian.Uint16(data[:2])) == port {
					data = data[2:]
				}
				if len(data) == 0 {
					continue
				}
			}
			switch ch {
			case 0:
				if _, err := pw.Write(data); err != nil {
					ws.Close()
					return
				}
			case 1:
				p.errMu.Lock()
				p.errText = strings.TrimSpace(p.errText + " " + string(data))
				p.errMu.Unlock()
			}
		}
	}()
	return p, nil
}

// ServiceTarget finds a ready pod behind a Service and the container port
// that a port of the Service sends to (kubectl port-forward svc/x does so).
func (k *Client) ServiceTarget(ctx context.Context, ns, svc string, port int) (string, int, error) {
	sk, _ := KindByID("services")
	var s obj
	if err := k.Get(ctx, sk.Path(ns, svc), nil, &s); err != nil {
		return "", 0, err
	}
	var target any
	found := false
	for _, p := range s.list("spec", "ports") {
		if p.num("port") == port {
			target = p.get("targetPort")
			found = true
			break
		}
	}
	if !found {
		return "", 0, fmt.Errorf("o Service %s não tem a porta %d", svc, port)
	}
	sel := selector(s.mapOf("spec", "selector"))
	if sel == "" {
		return "", 0, fmt.Errorf("o Service %s não tem seletor de pods", svc)
	}
	pk, _ := KindByID("pods")
	var l struct {
		Items []obj `json:"items"`
	}
	if err := k.Get(ctx, pk.Path(ns, ""), url.Values{"labelSelector": {sel}}, &l); err != nil {
		return "", 0, err
	}
	for _, p := range l.Items {
		if p.str("status", "phase") != "Running" || p.get("metadata", "deletionTimestamp") != nil {
			continue
		}
		ready := false
		for _, c := range p.list("status", "conditions") {
			if c.str("type") == "Ready" && c.str("status") == "True" {
				ready = true
			}
		}
		if !ready {
			continue
		}
		cport := port
		switch t := target.(type) {
		case float64:
			cport = int(t)
		case string:
			// A named port: look it up in the pod's containers.
			if n, err := strconv.Atoi(t); err == nil {
				cport = n
				break
			}
			cport = 0
			for _, c := range p.list("spec", "containers") {
				for _, cp := range c.list("ports") {
					if cp.str("name") == t {
						cport = cp.num("containerPort")
					}
				}
			}
			if cport == 0 {
				continue
			}
		}
		return p.str("metadata", "name"), cport, nil
	}
	return "", 0, fmt.Errorf("nenhum pod pronto atrás do Service %s", svc)
}
