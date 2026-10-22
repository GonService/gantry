package server

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"gantry/internal/hosts"
)

// listenScript prints the local address of every listening TCP socket:
// "0.0.0.0:22", "[::]:80" (ss, Linux) or "*.3000", "127.0.0.1.631" (macOS).
// A last line "#min <port> <uid>" tells the lowest port a normal user may
// open on Linux (rootless Podman can't publish below it).
const listenScript = `if command -v ss >/dev/null 2>&1; then ss -Hltn 2>/dev/null | awk '{print $4}'
else netstat -an 2>/dev/null | awk '$1 ~ /^tcp/ && /LISTEN/ {print $4}'; fi
echo "#min $(cat /proc/sys/net/ipv4/ip_unprivileged_port_start 2>/dev/null || echo 0) $(id -u)"`

// busyPorts maps each TCP port in use on the host's machine to who holds it:
// a container (any engine of the machine) or another program.
func (s *Server) busyPorts(ctx context.Context, h *hosts.Host) map[int]string {
	out, _ := s.machinePorts(ctx, h)
	return out
}

// machinePorts also returns the lowest port the host's engine can publish:
// rootless Podman on Linux can't go below ip_unprivileged_port_start.
func (s *Server) machinePorts(ctx context.Context, h *hosts.Host) (map[int]string, int) {
	out := map[int]string{}
	minPort := 0
	if h.Kind != hosts.TCP {
		if txt, err := h.Run(ctx, "", []string{"sh", "-c", listenScript}, nil); err == nil {
			for _, p := range parseListen(txt) {
				out[p] = "outro programa"
			}
			for _, l := range strings.Split(txt, "\n") {
				var m, uid int
				if n, _ := fmt.Sscanf(strings.TrimSpace(l), "#min %d %d", &m, &uid); n == 2 && uid != 0 && h.Runtime() == "podman" {
					minPort = m
				}
			}
		}
	}
	for _, e := range s.machineEngines(ctx, h) {
		cs, err := e.Engine.Containers(ctx)
		if err != nil {
			continue
		}
		for _, c := range cs {
			if c.State != "running" {
				continue
			}
			for _, p := range c.Ports {
				if p.PublicPort > 0 {
					out[p.PublicPort] = "container " + c.Name() + " (" + e.Runtime + ")"
				}
			}
		}
	}
	return out, minPort
}

// parseListen takes the port at the end of each address (after the last
// ":" or ".").
func parseListen(txt string) []int {
	seen := map[int]bool{}
	var out []int
	for _, l := range strings.Split(txt, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "#") {
			continue
		}
		i := strings.LastIndexAny(l, ":.")
		if i < 0 {
			continue
		}
		p, err := strconv.Atoi(l[i+1:])
		if err != nil || p <= 0 || p > 65535 || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

// ports answers which TCP ports are taken on the machine, for the project
// wizard to pick free ones.
func (s *Server) ports(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	busy, minPort := s.machinePorts(ctx, h)
	res := map[string]string{}
	for p, who := range busy {
		res[strconv.Itoa(p)] = who
	}
	writeJSON(w, map[string]any{"busy": res, "minPort": minPort})
}
