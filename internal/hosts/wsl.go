package hosts

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"

	"gantry/internal/applog"
	"gantry/internal/config"
)

// WSL hosts (wsl://<distro>) are the Linux distributions of WSL2 on a
// Windows machine. Commands run through wsl.exe, without SSH. The engine
// and the agent are reached through the agent's direct connection (TCP +
// mutual TLS on 127.0.0.1, which WSL2 forwards to Windows); the app
// installs and pairs the agent on first use (see SetSetup).

type wslState struct {
	mu        sync.Mutex
	distro    string
	keep      *exec.Cmd // keeps the distribution running while the app is open
	up        bool
	setup     func(ctx context.Context, h *Host) error
	settingUp bool
	setupErr  error
	setupAt   time.Time
}

// SetSetup sets what installs and pairs the agent of a WSL host.
func (h *Host) SetSetup(fn func(ctx context.Context, h *Host) error) {
	if h.wsl != nil {
		h.wsl.mu.Lock()
		h.wsl.setup = fn
		h.wsl.mu.Unlock()
	}
}

// Distro is the WSL distribution of a wsl:// host.
func (h *Host) Distro() string {
	if h.wsl == nil {
		return ""
	}
	return h.wsl.distro
}

func (h *Host) wslCommand(ctx context.Context, line string) *exec.Cmd {
	return exec.CommandContext(ctx, config.WSLExe(), "-d", h.wsl.distro, "--", "sh", "-c", line)
}

// ensureWSL starts the distribution (and keeps it running) and finds the
// engine socket in it.
func (h *Host) ensureWSL(ctx context.Context) error {
	if !h.Wanted() {
		return ErrIdle
	}
	w := h.wsl
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.up {
		return nil
	}
	if w.keep == nil {
		k := exec.Command(config.WSLExe(), "-d", w.distro, "--", "sh", "-c", "exec sleep 2147483647")
		if err := k.Start(); err != nil {
			return fmt.Errorf("wsl: não consegui iniciar a distribuição %s: %v", w.distro, err)
		}
		w.keep = k
		go func() {
			defer applog.Recover("hosts/wsl.go:72")
			_ = k.Wait()
			w.mu.Lock()
			if w.keep == k {
				w.keep, w.up = nil, false
			}
			w.mu.Unlock()
		}()
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := h.wslCommand(cctx, "GANTRY_SOCK="+shq(h.remote)+" sh -c "+shq(probeScript))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("wsl: %s", strings.TrimSpace(config.DecodeWSL(out)+" "+err.Error()))
	}
	found := ""
	var socks []string
	for _, l := range strings.Split(string(out), "\n") {
		if p, ok := strings.CutPrefix(strings.TrimSpace(l), "OK "); ok {
			if found == "" {
				found = strings.TrimSpace(p)
			}
			socks = append(socks, strings.TrimSpace(p))
		}
	}
	if found == "" && !h.remoteFixed {
		return errors.New("não encontrei Docker nem Podman rodando no WSL " + w.distro + ". Instale um deles na distribuição (ou ative o socket do Podman: systemctl --user enable --now podman.socket)")
	}
	h.mu.Lock()
	if !h.remoteFixed {
		h.remote = found
	}
	h.sockets = dedupeSockets(socks)
	h.mu.Unlock()
	w.up = true
	return nil
}

// wslDirect reaches the agent (engine == false) or the engine through the
// direct connection, installing and pairing the agent when it is missing.
func (h *Host) wslDirect(ctx context.Context, engine bool) (net.Conn, error) {
	if err := h.ensureWSL(ctx); err != nil {
		return nil, err
	}
	sock := ""
	if engine {
		h.mu.Lock()
		sock = h.remote
		h.mu.Unlock()
	}
	if c, ok := h.tryDirect(ctx, sock, engine); ok {
		return c, nil
	}
	w := h.wsl
	w.mu.Lock()
	if w.settingUp || w.setup == nil {
		w.mu.Unlock()
		return nil, errors.New("o agente do Gantry não respondeu no WSL " + w.distro)
	}
	if w.setupErr != nil && time.Since(w.setupAt) < time.Minute {
		err := w.setupErr
		w.mu.Unlock()
		return nil, err
	}
	w.settingUp = true
	fn := w.setup
	w.mu.Unlock()
	err := fn(ctx, h)
	w.mu.Lock()
	w.settingUp, w.setupErr, w.setupAt = false, err, time.Now()
	w.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("instalando o agente no WSL: %v", err)
	}
	h.ResetDirect()
	if c, ok := h.tryDirect(ctx, sock, engine); ok {
		return c, nil
	}
	err = errors.New("o agente foi instalado no WSL, mas não respondeu em " + h.Direct())
	w.mu.Lock()
	w.setupErr, w.setupAt = err, time.Now() // no new install for a minute
	w.mu.Unlock()
	return nil, err
}

// SettingUp tells whether the agent is being installed (its own checks
// must not start another install).
func (h *Host) SettingUp() bool {
	if h.wsl == nil {
		return false
	}
	h.wsl.mu.Lock()
	defer h.wsl.mu.Unlock()
	return h.wsl.settingUp
}

func (h *Host) closeWSL() {
	if h.wsl == nil {
		return
	}
	h.wsl.mu.Lock()
	k := h.wsl.keep
	h.wsl.keep, h.wsl.up = nil, false
	h.wsl.mu.Unlock()
	if k != nil && k.Process != nil {
		_ = k.Process.Kill()
	}
}
