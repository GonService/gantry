package server

import (
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"

	"gantry/internal/agentbin"
	"gantry/internal/composex"
	"gantry/internal/hosts"
	"gantry/internal/vms"
)

// Containers on the lab's cables: each service with x-interfaces gets a
// sidecar (<service>-cabos) that shares its network and creates eth1, eth2…
// as TAP ports joined to the project's relay. The sidecar runs the
// gantry-agent binary (static, any image) with the service's own image.

const tapsFile = "cabos.yaml"

func tapsOverride(dir string) string { return path.Join(dir, ".gantry", tapsFile) }

// tapSidecar is the name of the sidecar of a service.
func tapSidecar(svc string) string { return svc + "-cabos" }

// tapPorts are the arguments of "gantry-agent tap" for a service.
func tapPorts(id string, sv *composex.Service) []string {
	var out []string
	for i, f := range sv.Interfaces {
		addr := strings.Join(f.IPs, "+") // x-addresses already went into IPs
		out = append(out, fmt.Sprintf("eth%d,%d,%s,%s", i+1, f.RPort, vms.IfaceMAC(id, sv.Name, i), addr))
	}
	return out
}

func tapsYAML(id string, p *composex.Project, bin string) string {
	var b strings.Builder
	b.WriteString("# Gerado pelo Gantry ao subir o projeto: as portas de cabo dos containers (x-interfaces).\n")
	b.WriteString("# Não edite; ele é refeito a cada Subir.\nservices:\n")
	for _, t := range p.Taps {
		sv := p.Services[t.Name]
		img := sv.Image
		if img == "" && sv.Build {
			img = id + "-" + sv.Name // the image compose builds
		}
		fmt.Fprintf(&b, "  %s:\n", tapSidecar(sv.Name))
		fmt.Fprintf(&b, "    image: %s\n", strconv.Quote(img))
		fmt.Fprintf(&b, "    network_mode: %s\n", strconv.Quote("service:"+sv.Name))
		b.WriteString("    user: \"0:0\"\n    cap_add: [NET_ADMIN]\n    devices: [\"/dev/net/tun:/dev/net/tun\"]\n")
		// On SELinux hosts (Fedora, RHEL) the confined container may not open
		// /dev/net/tun; the sidecar only runs Gantry's own binary.
		b.WriteString("    security_opt: [\"label=disable\"]\n")
		// z: on SELinux hosts (Fedora, RHEL) the binary gets the label that
		// lets containers run it; elsewhere it is ignored.
		fmt.Fprintf(&b, "    volumes: [%s]\n", strconv.Quote(bin+":/gantry-tap:ro,z"))
		b.WriteString("    entrypoint: [\"/gantry-tap\", \"tap\"]\n")
		var args []string
		for _, a := range tapPorts(id, sv) {
			args = append(args, strconv.Quote(a))
		}
		fmt.Fprintf(&b, "    command: [%s]\n", strings.Join(args, ", "))
		fmt.Fprintf(&b, "    depends_on: [%s]\n", sv.Name)
		b.WriteString("    restart: unless-stopped\n")
		fmt.Fprintf(&b, "    labels: {gantry.cabos: %s}\n", strconv.Quote(sv.Name))
	}
	return b.String()
}

// writeTaps sends the tap binary to the machine and writes the override.
// No service on cables: the override goes (and the sidecars with it, by
// --remove-orphans).
func (s *Server) writeTaps(ctx context.Context, h *hosts.Host, id, dir string, p *composex.Project) (string, error) {
	f := tapsOverride(dir)
	if len(p.Taps) == 0 {
		if h.Exists(ctx, f) {
			_, _ = h.Run(ctx, "", []string{"rm", "-f", f}, nil)
		}
		return "", nil
	}
	info, err := h.Run(ctx, "", []string{"sh", "-c", `uname -s; uname -m; echo "${XDG_STATE_HOME:-$HOME/.local/state}"`}, nil)
	if err != nil {
		return "", err
	}
	l := strings.Split(strings.TrimSpace(info), "\n")
	if len(l) < 3 || strings.TrimSpace(l[0]) != "Linux" {
		return "", fmt.Errorf("containers ligados por cabos (x-interfaces) precisam de um host Linux")
	}
	arch, ok := agentbin.LinuxArch(l[1])
	if !ok {
		return "", fmt.Errorf("arquitetura sem o binário dos cabos: %s", strings.TrimSpace(l[1]))
	}
	bin, err := agentbin.Binary(arch)
	if err != nil {
		return "", err
	}
	tdir := path.Join(strings.TrimSpace(l[2]), "gantry", "vms", id)
	dst := path.Join(tdir, "gantry-tap")
	stamp := Version + " " + strconv.Itoa(len(bin))
	if old, err := h.ReadFile(ctx, dst+".versao"); err != nil || strings.TrimSpace(string(old)) != stamp {
		if _, err := h.Run(ctx, "", []string{"mkdir", "-p", tdir}, nil); err != nil {
			return "", err
		}
		if err := h.WriteFile(ctx, dst+".novo", bin); err != nil {
			return "", err
		}
		if _, err := h.Run(ctx, "", []string{"sh", "-c", `chmod 755 "$1.novo" && mv -f "$1.novo" "$1"`, "sh", dst}, nil); err != nil {
			return "", err
		}
		_ = h.WriteFile(ctx, dst+".versao", []byte(stamp+"\n"))
	}
	if err := h.WriteFile(ctx, f, []byte(tapsYAML(id, p, dst))); err != nil {
		return "", fmt.Errorf("não consegui gravar %s: %v", f, err)
	}
	return f, nil
}

// withTapSidecars adds the sidecars of the services named (multi-engine
// projects bring services up by name).
func withTapSidecars(p *composex.Project, svcs []string) []string {
	out := append([]string{}, svcs...)
	for _, n := range svcs {
		for _, t := range p.Taps {
			if t.Name == n {
				out = append(out, tapSidecar(n))
			}
		}
	}
	return out
}
