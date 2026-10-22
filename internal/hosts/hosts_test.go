package hosts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gantry/internal/config"
)

func TestRemoteHostsWaitToBeOpened(t *testing.T) {
	h, err := New(config.Host{Name: "srv", URL: "ssh://usuario@servidor.invalid:2200"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := h.Engine.Version(ctx); !errors.Is(err, ErrIdle) {
		t.Fatalf("conectou sem ser aberto: %v", err)
	}
	if h.Machine() != "ssh:usuario@servidor.invalid:2200" {
		t.Errorf("máquina %q", h.Machine())
	}
	other, _ := New(config.Host{Name: "srv · podman", URL: "ssh://usuario@servidor.invalid:2200", Socket: "/run/podman/podman.sock"})
	if other.Machine() != h.Machine() {
		t.Error("engines do mesmo servidor deveriam ser a mesma máquina")
	}
	local, _ := New(config.Host{Name: "docker", URL: "unix:///var/run/docker.sock"})
	if !local.Wanted() || local.Machine() != "local" {
		t.Error("hosts locais não esperam")
	}
	h.Want()
	if !h.Wanted() {
		t.Error("Want")
	}
	h.Disconnect()
	if h.Wanted() {
		t.Error("Disconnect deveria voltar a esperar")
	}
}

func TestSafeDir(t *testing.T) {
	d := filepath.Join(t.TempDir(), "tun")
	if !safeDir(d) {
		t.Fatal("pasta nova devia servir")
	}
	os.Chmod(d, 0o777)
	if !safeDir(d) {
		t.Fatal("pasta minha aberta devia ser fechada e servir")
	}
	if fi, _ := os.Stat(d); fi.Mode().Perm() != 0o700 {
		t.Fatalf("permissão %v", fi.Mode().Perm())
	}
	link := filepath.Join(t.TempDir(), "link")
	os.Symlink(d, link)
	if safeDir(link) {
		t.Fatal("link simbólico não serve")
	}
}

// A host the user disconnected stays down for a moment even when requests
// already on the way ask for it; Atualizar (Retry) opens it at once.
func TestDisconnectParksTheHost(t *testing.T) {
	h, err := New(config.Host{Name: "srv", URL: "ssh://usuario@servidor.invalid:2200"})
	if err != nil {
		t.Fatal(err)
	}
	h.Want()
	h.Disconnect()
	h.Want()
	if h.Wanted() {
		t.Fatal("um pedido logo depois de desconectar reabriu a conexão")
	}
	h.Retry()
	if !h.Wanted() {
		t.Fatal("Atualizar devia permitir conectar de novo")
	}
	h.Disconnect()
	h.mu.Lock()
	h.parked = h.parked.Add(-parkFor)
	h.mu.Unlock()
	h.Want()
	if !h.Wanted() {
		t.Fatal("abrir de novo depois do intervalo devia conectar")
	}
}
