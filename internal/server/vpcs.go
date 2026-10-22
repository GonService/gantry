package server

import (
	"context"
	"path"
	"strconv"
	"strings"

	"gantry/internal/agentbin"
	"gantry/internal/composex"
	"gantry/internal/hosts"
	"gantry/internal/vpcsbin"
)

// vpcsEnv adds to the environment of a VPCS node the program Gantry brings
// (put on the machine once per version); a machine where it does not run
// (not Linux x86_64) uses its own vpcs.
func (s *Server) vpcsEnv(ctx context.Context, h *hosts.Host, v *composex.VM, env map[string]string) error {
	if v.Kind != "vpcs" {
		return nil
	}
	info, err := h.Run(ctx, "", []string{"sh", "-c", `uname -s; uname -m; echo "${XDG_STATE_HOME:-$HOME/.local/state}"`}, nil)
	if err != nil {
		return err
	}
	l := strings.Split(strings.TrimSpace(info), "\n")
	if len(l) < 3 || strings.TrimSpace(l[0]) != "Linux" {
		return nil
	}
	arch, ok := agentbin.LinuxArch(l[1])
	if !ok {
		return nil
	}
	bin, err := vpcsbin.Binary(arch)
	if err != nil {
		return nil // vmctl looks for a vpcs of the machine and says if there is none
	}
	dst := path.Join(strings.TrimSpace(l[2]), "gantry", "bin", "vpcs")
	stamp := vpcsbin.Version + " " + strconv.Itoa(len(bin))
	if old, err := h.ReadFile(ctx, dst+".versao"); err != nil || strings.TrimSpace(string(old)) != stamp {
		if _, err := h.Run(ctx, "", []string{"mkdir", "-p", path.Dir(dst)}, nil); err != nil {
			return err
		}
		if err := h.WriteFile(ctx, dst+".novo", bin); err != nil {
			return err
		}
		if _, err := h.Run(ctx, "", []string{"sh", "-c", `chmod 755 "$1.novo" && mv -f "$1.novo" "$1"`, "sh", dst}, nil); err != nil {
			return err
		}
		_ = h.WriteFile(ctx, dst+".versao", []byte(stamp+"\n"))
	}
	env["GV_VPCSBIN"] = dst
	return nil
}
