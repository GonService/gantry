package shim

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"time"
)

// Found is a CLI runtime available on this machine.
type Found struct {
	Name string // shown in the app
	URL  string // nerdctl://…, k3s://…
}

func works(argv ...string) bool {
	if _, err := exec.LookPath(argv[0]); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, argv[0], argv[1:]...).Run() == nil
}

// K3sSocket is where k3s runs its containerd.
const K3sSocket = "/run/k3s/containerd/containerd.sock"

// Detect lists the CLI runtimes that answer right now. It never prompts
// for a password: sudo is only used when it works without one.
func Detect() []Found {
	var out []Found
	if runtime.GOOS == "darwin" {
		return out
	}
	if _, err := exec.LookPath("nerdctl"); err != nil {
		return out
	}
	switch {
	case works("nerdctl", "ps", "-q"):
		out = append(out, Found{"containerd", "nerdctl://default"})
	case works("sudo", "-n", "nerdctl", "ps", "-q"):
		out = append(out, Found{"containerd", "nerdctl://default?sudo=1"})
	}
	if _, err := os.Stat(K3sSocket); err == nil {
		switch {
		case works("nerdctl", "--address", K3sSocket, "--namespace", "k8s.io", "ps", "-q"):
			out = append(out, Found{"k3s", "k3s://"})
		case works("sudo", "-n", "nerdctl", "--address", K3sSocket, "--namespace", "k8s.io", "ps", "-q"):
			out = append(out, Found{"k3s", "k3s://?sudo=1"})
		}
	}
	return out
}

// FromURL builds the driver for nerdctl://namespace?address=…&sudo=1,
// k3s://[?sudo=1], and the compose command of its labs.
func FromURL(raw string) (Driver, []string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, nil, err
	}
	q := u.Query()
	sudo := q.Get("sudo") == "1" || q.Get("sudo") == "true"
	switch u.Scheme {
	case "k3s":
		addr := q.Get("address")
		if addr == "" {
			addr = K3sSocket
		}
		n := &Nerdctl{Namespace: "k8s.io", Address: addr, Sudo: sudo, Kind: "k3s"}
		return n, n.Compose(), nil
	case "nerdctl":
		ns := u.Host
		if ns == "" {
			ns = "default"
		}
		n := &Nerdctl{Namespace: ns, Address: q.Get("address"), Sudo: sudo}
		return n, n.Compose(), nil
	}
	return nil, nil, &url.Error{Op: "parse", URL: raw, Err: os.ErrInvalid}
}
