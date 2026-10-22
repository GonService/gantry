// Package local inspects the container engines installed on this machine and
// runs the commands that install, create or start them.
package local

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Action is something the user can do with an engine from the UI.
type Action struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Primary bool   `json:"primary,omitempty"`
	// Note explains what will happen, e.g. a large download.
	Note string `json:"note,omitempty"`
}

type Engine struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Installed bool     `json:"installed"`
	State     string   `json:"state"` // running, stopped, no-machine, socket-off, missing
	Detail    string   `json:"detail,omitempty"`
	Socket    string   `json:"socket,omitempty"`
	Actions   []Action `json:"actions"`
	Hint      string   `json:"hint,omitempty"` // command to run by hand when the app cannot
}

type Status struct {
	OS      string   `json:"os"`
	Brew    bool     `json:"brew"`
	Engines []Engine `json:"engines"`
	// Install lists installers offered when no engine is installed.
	Install []Action `json:"install"`
}

func has(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func run(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).Output()
}

func home() string {
	h, _ := os.UserHomeDir()
	return h
}

// PodmanMachineSockets returns the API sockets of running podman machines
// (macOS and Windows), as reported by podman itself.
func PodmanMachineSockets(ctx context.Context) []string {
	if runtime.GOOS == "linux" || !has("podman") {
		return nil
	}
	out, err := run(ctx, "podman", "machine", "inspect")
	if err != nil {
		return nil
	}
	var ms []struct {
		State          string
		ConnectionInfo struct {
			PodmanSocket *struct{ Path string }
		}
	}
	if json.Unmarshal(out, &ms) != nil {
		return nil
	}
	var socks []string
	for _, m := range ms {
		if m.ConnectionInfo.PodmanSocket != nil && m.ConnectionInfo.PodmanSocket.Path != "" && exists(m.ConnectionInfo.PodmanSocket.Path) {
			socks = append(socks, m.ConnectionInfo.PodmanSocket.Path)
		}
	}
	return socks
}

func podman(ctx context.Context) Engine {
	e := Engine{ID: "podman", Name: "Podman", Installed: has("podman"), Actions: []Action{}}
	if !e.Installed {
		e.State = "missing"
		return e
	}
	if runtime.GOOS == "linux" {
		sock := filepath.Join("/run/user", strconv.Itoa(os.Getuid()), "podman/podman.sock")
		if x := os.Getenv("XDG_RUNTIME_DIR"); x != "" {
			sock = filepath.Join(x, "podman/podman.sock")
		}
		if exists(sock) {
			e.State, e.Socket = "running", sock
			e.Detail = "socket do usuário ativo"
			return e
		}
		e.State = "socket-off"
		e.Detail = "o Podman está instalado, mas o socket da API está desligado"
		e.Actions = []Action{{ID: "podman-socket", Label: "Ativar o socket", Primary: true}}
		e.Hint = "systemctl --user enable --now podman.socket"
		return e
	}

	out, err := run(ctx, "podman", "machine", "list", "--format", "json")
	if err != nil {
		e.State, e.Detail = "stopped", "não consegui consultar as máquinas do Podman"
		return e
	}
	var ms []struct {
		Name     string
		Default  bool
		Running  bool
		Starting bool
		CPUs     int
		Memory   json.RawMessage
	}
	_ = json.Unmarshal(out, &ms)
	if len(ms) == 0 {
		e.State = "no-machine"
		e.Detail = "no macOS o Podman roda dentro de uma máquina virtual, e nenhuma foi criada ainda"
		e.Actions = []Action{{ID: "podman-init", Label: "Criar e iniciar a máquina", Primary: true,
			Note: "baixa a imagem da VM (cerca de 1 GB) na primeira vez"}}
		e.Hint = "podman machine init --now"
		return e
	}
	m := ms[0]
	for _, x := range ms {
		if x.Default {
			m = x
			break
		}
	}
	name := strings.TrimSuffix(m.Name, "*")
	switch {
	case m.Running:
		e.State = "running"
		e.Detail = "máquina " + name + " rodando"
		if socks := PodmanMachineSockets(ctx); len(socks) > 0 {
			e.Socket = socks[0]
		}
		e.Actions = []Action{{ID: "podman-stop", Label: "Parar a máquina"}}
	case m.Starting:
		e.State = "starting"
		e.Detail = "máquina " + name + " iniciando"
	default:
		e.State = "stopped"
		e.Detail = "máquina " + name + " parada"
		e.Actions = []Action{{ID: "podman-start", Label: "Iniciar a máquina", Primary: true}}
		e.Hint = "podman machine start"
	}
	return e
}

func orbstack(ctx context.Context) Engine {
	e := Engine{ID: "orbstack", Name: "OrbStack", Actions: []Action{}}
	e.Installed = runtime.GOOS == "darwin" && (exists("/Applications/OrbStack.app") || has("orb"))
	if !e.Installed {
		e.State = "missing"
		return e
	}
	sock := filepath.Join(home(), ".orbstack/run/docker.sock")
	if exists(sock) {
		if _, err := run(ctx, "docker", "-H", "unix://"+sock, "info"); err == nil || !has("docker") {
			e.State, e.Socket = "running", sock
			e.Actions = []Action{{ID: "orbstack-stop", Label: "Parar"}}
			return e
		}
	}
	e.State = "stopped"
	e.Actions = []Action{{ID: "orbstack-start", Label: "Iniciar", Primary: true}}
	return e
}

func dockerDesktop() Engine {
	e := Engine{ID: "docker-desktop", Name: "Docker Desktop", Actions: []Action{}}
	e.Installed = runtime.GOOS == "darwin" && exists("/Applications/Docker.app")
	if !e.Installed {
		e.State = "missing"
		return e
	}
	sock := filepath.Join(home(), ".docker/run/docker.sock")
	if exists(sock) {
		e.State, e.Socket = "running", sock
		return e
	}
	e.State = "stopped"
	e.Actions = []Action{{ID: "docker-desktop-start", Label: "Abrir o Docker Desktop", Primary: true}}
	return e
}

func colima(ctx context.Context) Engine {
	e := Engine{ID: "colima", Name: "Colima", Installed: has("colima"), Actions: []Action{}}
	if !e.Installed {
		e.State = "missing"
		return e
	}
	if _, err := run(ctx, "colima", "status"); err == nil {
		e.State = "running"
		e.Socket = filepath.Join(home(), ".colima/default/docker.sock")
		e.Actions = []Action{{ID: "colima-stop", Label: "Parar"}}
		return e
	}
	e.State = "stopped"
	e.Actions = []Action{{ID: "colima-start", Label: "Iniciar", Primary: true}}
	e.Hint = "colima start"
	return e
}

func dockerLinux() Engine {
	e := Engine{ID: "docker", Name: "Docker", Installed: has("dockerd") || exists("/usr/bin/dockerd"), Actions: []Action{}}
	if !e.Installed {
		e.State = "missing"
		return e
	}
	if exists("/var/run/docker.sock") {
		e.State, e.Socket = "running", "/var/run/docker.sock"
		return e
	}
	e.State = "stopped"
	e.Hint = "sudo systemctl enable --now docker"
	return e
}

// Inspect reports every known engine on this machine.
// containerd is containerd driven by nerdctl, on Linux.
func containerd(ctx context.Context) Engine {
	e := Engine{ID: "containerd", Name: "containerd (nerdctl)", Actions: []Action{}}
	e.Installed = has("nerdctl")
	if !e.Installed {
		e.State = "missing"
		return e
	}
	if _, err := run(ctx, "nerdctl", "ps", "-q"); err == nil {
		e.State, e.Detail = "running", "aparece como host nerdctl://default"
		return e
	}
	if _, err := run(ctx, "sudo", "-n", "nerdctl", "ps", "-q"); err == nil {
		e.State, e.Detail = "running", "com sudo; aparece como host nerdctl://default?sudo=1"
		return e
	}
	e.State = "stopped"
	e.Detail = "o nerdctl não conseguiu falar com o containerd"
	e.Hint = "sudo systemctl enable --now containerd  (ou containerd-rootless-setuptool.sh install)"
	return e
}

func Inspect(ctx context.Context) Status {
	st := Status{OS: runtime.GOOS, Brew: has("brew"), Install: []Action{}}
	var all []Engine
	if runtime.GOOS == "darwin" {
		all = []Engine{podman(ctx), orbstack(ctx), dockerDesktop(), colima(ctx)}
	} else {
		all = []Engine{podman(ctx), dockerLinux(), containerd(ctx)}
	}
	any := false
	for _, e := range all {
		if e.Installed {
			any = true
			st.Engines = append(st.Engines, e)
		}
	}
	if st.Engines == nil {
		st.Engines = []Engine{}
	}
	if !any && runtime.GOOS == "darwin" && st.Brew {
		st.Install = []Action{
			{ID: "install-podman", Label: "Instalar o Podman", Primary: true,
				Note: "brew install podman docker-compose, depois cria e inicia a máquina (cerca de 1 GB)"},
			{ID: "install-orbstack", Label: "Instalar o OrbStack",
				Note: "brew install --cask orbstack (gratuito para uso pessoal)"},
		}
	}
	return st
}

// Command returns the shell command for an action, or "" if unknown.
func Command(id string) string {
	switch id {
	case "podman-init":
		return "podman machine init --now"
	case "podman-start":
		return "podman machine start"
	case "podman-stop":
		return "podman machine stop"
	case "podman-socket":
		return "systemctl --user enable --now podman.socket"
	case "orbstack-start":
		return "if command -v orb >/dev/null; then orb start; else open -a OrbStack; fi"
	case "orbstack-stop":
		return "orb stop"
	case "docker-desktop-start":
		return "open -a Docker && echo 'Docker Desktop abrindo; pode levar alguns segundos.'"
	case "colima-start":
		return "colima start"
	case "colima-stop":
		return "colima stop"
	case "install-podman":
		return "brew install podman docker-compose && (podman machine list --format '{{.Name}}' | grep -q . || podman machine init) && podman machine start"
	case "install-orbstack":
		return "brew install --cask orbstack && open -a OrbStack"
	}
	return ""
}
