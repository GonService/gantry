// Package cvm runs the Container VM: a small Linux VM, run by Gantry itself
// on Apple's Virtualization framework (Code-Hex/vz), that holds the container engines of this Mac. Its Docker
// and Podman sockets come out through vsock as unix sockets in the
// machine's folder, so they show up as engines of this computer; the
// projects stay in the Mac's folders, shared at the same path.
//
// The state lives in ~/.gantry/machines/<name>: the spec, two disks
// (system, replaceable; data, never recreated), the cloud-init seed with
// the provisioning script the user approved, the ssh key and the sockets.
package cvm

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// DefaultName is the name of the machine Gantry creates.
const DefaultName = "gantry-vm"

// Spec is what the user chose when creating the machine (and can change
// later: CPU, memory and the data disk apply on the next start).
type Spec struct {
	Name      string `json:"name"`
	CPUs      int    `json:"cpus"`
	MemoryMiB int    `json:"memoryMiB"`
	SystemGiB int    `json:"systemGiB"`
	DataGiB   int    `json:"dataGiB"`
	Docker    bool   `json:"docker"`
	Podman    bool   `json:"podman"`
	// Containerd adds nerdctl (with BuildKit); on the Mac a nerdctl in
	// ~/.gantry/bin runs it in the machine, in the same folder.
	Containerd bool `json:"containerd"`
	Rosetta    bool `json:"rosetta"`
	QEMU       bool `json:"qemu"`
	// SSHAgent passes the Mac's ssh-agent to the machine (git clone and
	// buildx --ssh with the user's keys, which never leave the Mac).
	SSHAgent bool `json:"sshAgent"`
	// Extra is the user's own steps, run last as root.
	Extra string `json:"extra,omitempty"`
	// Image overrides the address of the system image (tar.xz with disk.raw).
	Image string `json:"image,omitempty"`
	// AutoStart: the app starts the machine when it opens.
	AutoStart bool `json:"autoStart"`
	// IdleStopMin stops the machine after this many minutes with no
	// container running (0: never).
	IdleStopMin int `json:"idleStopMin,omitempty"`
	// Shares are more folders of the Mac, at the same path (the home folder
	// is always there).
	Shares []Share `json:"shares,omitempty"`
	// NoBalloon turns off the dynamic memory: the machine keeps all of it.
	NoBalloon bool `json:"noBalloon,omitempty"`
	// Watch forwards the Mac's file changes into the machine, so watchers
	// in containers (hot reload) see them.
	Watch bool `json:"watch"`
	// Finder shows the engines' volumes in the Finder (NFS from the
	// machine to this Mac only); FinderRW lets the Mac write in them.
	Finder   bool `json:"finder"`
	FinderRW bool `json:"finderRW"`
	// SnapDaily takes a snapshot of the data disk once a day, at start;
	// SnapKeep is how many automatic ones stay.
	SnapDaily bool `json:"snapDaily"`
	SnapKeep  int  `json:"snapKeep,omitempty"`
}

// Share is a folder of the Mac in the machine, at the same path.
type Share struct {
	Path     string `json:"path"`
	ReadOnly bool   `json:"readOnly,omitempty"`
}

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)

// Default is the spec offered on a computer with cpus cores and mem bytes.
func Default(cpus int, mem int64) Spec {
	c := cpus / 2
	if c < 2 {
		c = 2
	}
	if c > 6 {
		c = 6
	}
	m := 8192
	if mem > 0 && int64(m)<<20 > mem/2 {
		m = int(mem/2>>20) / 512 * 512
		if m < 2048 {
			m = 2048
		}
	}
	return Spec{Name: DefaultName, CPUs: c, MemoryMiB: m, SystemGiB: 10, DataGiB: 128,
		Docker: true, Podman: true, Rosetta: runtime.GOARCH == "arm64", SSHAgent: true, AutoStart: true, IdleStopMin: 0,
		SnapDaily: true, SnapKeep: 7, Watch: true}
}

// Check reports what is wrong with a spec, for the creation form.
func (s Spec) Check() error {
	switch {
	case !nameRe.MatchString(s.Name):
		return errors.New("nome inválido: use letras minúsculas, números e hífen, começando por uma letra")
	case s.CPUs < 1 || s.CPUs > 64:
		return errors.New("número de CPUs fora do intervalo")
	case s.MemoryMiB < 1024:
		return errors.New("a máquina precisa de pelo menos 1 GiB de memória")
	case s.SystemGiB < 6:
		return errors.New("o disco do sistema precisa de pelo menos 6 GiB")
	case s.DataGiB < 8:
		return errors.New("o disco de dados precisa de pelo menos 8 GiB")
	case !s.Docker && !s.Podman && !s.Containerd:
		return errors.New("escolha pelo menos um engine: Docker, Podman ou containerd")
	}
	for _, sh := range s.Shares {
		if !filepath.IsAbs(sh.Path) || strings.ContainsAny(sh.Path, " ,\n") {
			return fmt.Errorf("pasta compartilhada inválida (caminho absoluto, sem espaço nem vírgula): %s", sh.Path)
		}
	}
	return nil
}

// Base is the folder of the machines.
func Base() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".gantry", "machines")
}

// Dir is the folder of one machine.
func Dir(name string) string { return filepath.Join(Base(), name) }

// The files of a machine.
const (
	fSpec    = "spec.json"
	fSystem  = "sistema.raw"
	fData    = "dados.raw"
	fEFI     = "efi.vars"
	fKey     = "id_ed25519"
	fKnown   = "known_hosts"
	fScript  = "provision.sh"
	fSeed    = "cloud-init"
	fConsole = "console.log"
	fVMLog   = "vm.log"
	fSeedISO = "seed.iso"
	fPID     = "vm.pid"
	fREST    = "rest.sock"
	fSSH     = "ssh.sock"
	fDocker  = "docker.sock"
	fPodman  = "podman.sock"
)

// The vsock ports the guest listens on (socat to sshd and the engines).
const (
	portSSH    = 2222
	portDocker = 2375
	portPodman = 2376
)

// DockerSocket and PodmanSocket are where the engines appear on the Mac.
func DockerSocket(name string) string { return filepath.Join(Dir(name), fDocker) }
func PodmanSocket(name string) string { return filepath.Join(Dir(name), fPodman) }

// Exists tells whether the machine was created.
func Exists(name string) bool {
	_, err := os.Stat(filepath.Join(Dir(name), fSystem))
	return err == nil
}

// List returns the names of the machines created.
func List() []string {
	ents, _ := os.ReadDir(Base())
	var out []string
	for _, e := range ents {
		if e.IsDir() && Exists(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out
}

// Load reads the spec of a machine.
func Load(name string) (Spec, error) {
	var s Spec
	b, err := os.ReadFile(filepath.Join(Dir(name), fSpec))
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(b, &s)
	return s, err
}

// Save writes the spec of a machine.
func Save(s Spec) error {
	if err := os.MkdirAll(Dir(s.Name), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	return os.WriteFile(filepath.Join(Dir(s.Name), fSpec), append(b, '\n'), 0o600)
}

// mac is a stable, locally administered MAC address for the machine.
func mac(name string) string {
	h := sha256.Sum256([]byte("gantry-cvm:" + name))
	return fmt.Sprintf("52:54:00:%02x:%02x:%02x", h[0], h[1], h[2])
}

// Supported tells whether this computer can run the Container VM.
func Supported() bool { return runtime.GOOS == "darwin" }

// sanitize keeps a path usable inside the machine's mount options.
func sanitize(p string) error {
	if strings.ContainsAny(p, ",\n") {
		return fmt.Errorf("o caminho %s tem vírgula ou quebra de linha, que a máquina não aceita", p)
	}
	return nil
}
