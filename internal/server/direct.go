package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"gantry/internal/agent"
	"gantry/internal/hosts"
)

// Direct connections to agents: the app has one certificate of its own and
// keeps, per machine, the address and the certificate of the agent it
// paired with. Both private keys stay on their machines; the pairing runs
// over SSH, so trust comes from the SSH login.

type directPeer struct {
	Addr string `json:"addr"`
	Cert string `json:"cert"` // agent certificate (PEM), pinned
}

var directMu sync.Mutex

func (s *Server) configDir() string {
	if p := s.cfg.Path(); p != "" {
		return filepath.Dir(p)
	}
	return "."
}

func (s *Server) directFile() string { return filepath.Join(s.configDir(), "agentes-diretos.json") }

func (s *Server) loadDirect() map[string]directPeer {
	m := map[string]directPeer{}
	if b, err := os.ReadFile(s.directFile()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func (s *Server) saveDirect(m map[string]directPeer) error {
	b, _ := json.MarshalIndent(m, "", "  ")
	if err := os.MkdirAll(s.configDir(), 0o700); err != nil {
		return err
	}
	return os.WriteFile(s.directFile(), b, 0o600)
}

// clientCert returns the app's certificate and key, creating them once.
func (s *Server) clientCert() (crt, key []byte, err error) {
	directMu.Lock()
	defer directMu.Unlock()
	cp, kp := filepath.Join(s.configDir(), "app-agente.crt"), filepath.Join(s.configDir(), "app-agente.key")
	crt, err1 := os.ReadFile(cp)
	key, err2 := os.ReadFile(kp)
	if err1 == nil && err2 == nil {
		return crt, key, nil
	}
	host, _ := os.Hostname()
	if crt, key, err = agent.NewCert("gantry-app " + host); err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(s.configDir(), 0o700); err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(kp, key, 0o600); err != nil {
		return nil, nil, err
	}
	return crt, key, os.WriteFile(cp, crt, 0o644)
}

// applyDirect turns on the direct connection of a host paired before.
func (s *Server) applyDirect(h *hosts.Host) {
	if !h.Remote() {
		return
	}
	directMu.Lock()
	p, ok := s.loadDirect()[h.Machine()]
	directMu.Unlock()
	if !ok {
		h.SetDirect("", nil)
		return
	}
	crt, key, err := s.clientCert()
	if err != nil {
		return
	}
	cfg, err := agent.ClientTLS(crt, key, []byte(p.Cert))
	if err != nil {
		return
	}
	h.SetDirect(p.Addr, cfg)
}

// pairDirect runs gantry-agent pair on the server and stores the result.
func (s *Server) pairDirect(ctx context.Context, h *hosts.Host, bin string, port int, out io.Writer) error {
	name := h.ServerName()
	if name == "" {
		return errors.New("não sei o endereço do servidor")
	}
	crt, _, err := s.clientCert()
	if err != nil {
		return err
	}
	bind := ":" + strconv.Itoa(port)
	if h.Kind == hosts.WSL {
		bind = "127.0.0.1" + bind // WSL2 forwards it to Windows; nothing else sees it
	}
	agentCrt, err := h.Run(ctx, "", []string{bin, "pair", bind}, crt)
	if err != nil {
		return fmt.Errorf("pareamento: %v", err)
	}
	addr := net.JoinHostPort(name, strconv.Itoa(port))
	directMu.Lock()
	m := s.loadDirect()
	m[h.Machine()] = directPeer{Addr: addr, Cert: agentCrt}
	err = s.saveDirect(m)
	directMu.Unlock()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "    certificado do agente fixado: %s…\n", agent.Fingerprint([]byte(agentCrt))[:16])
	for _, x := range s.hostsOfMachine(h.Machine()) {
		s.applyDirect(x)
	}
	return nil
}

// forgetDirect drops the pairing of a machine (agent removed).
func (s *Server) forgetDirect(h *hosts.Host) {
	directMu.Lock()
	m := s.loadDirect()
	delete(m, h.Machine())
	_ = s.saveDirect(m)
	directMu.Unlock()
	for _, x := range s.hostsOfMachine(h.Machine()) {
		x.SetDirect("", nil)
	}
}

func (s *Server) hostsOfMachine(key string) []*hosts.Host {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*hosts.Host
	for _, x := range s.hosts {
		if x.Machine() == key {
			out = append(out, x)
		}
	}
	return out
}

// checkDirect tells whether the agent answers on the direct connection.
func checkDirect(ctx context.Context, h *hosts.Host) error {
	addr := h.Direct()
	if addr == "" {
		return errors.New("não pareado")
	}
	h.ResetDirect() // try again even after a recent failure
	_, err := h.Agent.Info(ctx)
	if err != nil {
		return err
	}
	if v, _ := h.Via(); v != "tls" {
		return fmt.Errorf("a porta %s não respondeu (firewall?)", strings.TrimPrefix(addr, h.ServerName()))
	}
	return nil
}

// reportDirect checks the direct connection and says what to do when the
// port does not answer.
func (s *Server) reportDirect(ctx context.Context, h *hosts.Host, out io.Writer) {
	step(out, "testando a conexão direta em "+h.Direct())
	var err error
	for i := 0; i < 6; i++ {
		ictx, cancel := context.WithTimeout(ctx, 4*time.Second)
		err = checkDirect(ictx, h)
		cancel()
		if err == nil {
			fmt.Fprintln(out, "    ok: o app fala com o agente e com o engine direto por TLS (comandos inclusive); o SSH fica como reserva.")
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	_, port, _ := net.SplitHostPort(h.Direct())
	fmt.Fprintf(out, "    \x1b[33maviso:\x1b[0m %v.\n    Libere a porta TCP %s no firewall do servidor (ex.: sudo ufw allow %s/tcp). Até lá o app usa SSH.\n", err, port, port)
}

// setAgentDirect pairs or unpairs an installed agent and restarts it.
func (s *Server) setAgentDirect(ctx context.Context, h *hosts.Host, out io.Writer, on bool, port int) error {
	ctx = hosts.WithoutAgent(ctx)
	bin := "$HOME/.local/bin/gantry-agent"
	home, err := h.Run(ctx, "", []string{"sh", "-c", `printf %s "$HOME"`}, nil)
	if err != nil {
		return err
	}
	bin = strings.TrimSpace(home) + "/.local/bin/gantry-agent"
	if on {
		step(out, fmt.Sprintf("pareando a conexão direta (TCP %d, TLS mútuo)", port))
		if err := s.pairDirect(ctx, h, bin, port, out); err != nil {
			return err
		}
	} else {
		step(out, "desligando a conexão direta")
		if _, err := h.Run(ctx, "", []string{bin, "unpair"}, nil); err != nil {
			return err
		}
		s.forgetDirect(h)
	}
	step(out, "reiniciando o agente")
	if _, err := h.Run(ctx, "", []string{"sh", "-c", `if command -v systemctl >/dev/null 2>&1 && systemctl --user is-enabled gantry-agent.service >/dev/null 2>&1; then systemctl --user restart gantry-agent.service; else pkill -x gantry-agent 2>/dev/null; sleep 0.5; nohup "$HOME/.local/bin/gantry-agent" >"${XDG_STATE_HOME:-$HOME/.local/state}/gantry/agent.log" 2>&1 </dev/null & fi; echo ok`}, nil); err != nil {
		return err
	}
	if on {
		s.reportDirect(ctx, h, out)
	} else {
		fmt.Fprintln(out, "    ok: o app volta a usar só o SSH.")
	}
	return nil
}

// setupWSL installs and pairs the agent in a WSL distribution: there it is
// how the app reaches the engine, so it is done on first use.
func (s *Server) setupWSL(ctx context.Context, h *hosts.Host) error {
	var log bytes.Buffer
	err := s.installAgent(ctx, h, &log, true, agent.DefaultTLSPort)
	if err != nil {
		return fmt.Errorf("%v\n%s", err, strings.TrimSpace(log.String()))
	}
	s.forgetAgentState(h)
	return nil
}
