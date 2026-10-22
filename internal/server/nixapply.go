package server

// Aplicar for VMs with nix: the module and the parameters go to /etc/nixos
// over SSH, gantry-apply runs nixos-rebuild inside the VM, and Gantry
// follows /var/lib/gantry/status until it says ok or failed for the hash
// of those files. The definition hash the VM keeps changes only when the
// apply worked, so a failure leaves the change pending.

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"gantry/internal/composex"
	"gantry/internal/hosts"
	"gantry/internal/vms"
)

const (
	nixApplyMax = 15 * time.Minute
	nixPoll     = 2 * time.Second
	// nixChunk keeps each SSH command well under the 128 KB limit of one
	// argument (the command goes as one, base64 inside base64).
	nixChunk = 48 << 10
)

// nixState is what gantry-apply wrote: applying, ok, failed or idle, the
// hash it was about, and whether the service is running now.
type nixState struct {
	State, Hash string
	Active      bool
}

func (s *Server) nixStatus(ctx context.Context, h *hosts.Host, project, name string) (nixState, error) {
	out, err := s.vmExec(ctx, h, project, name, "cat "+vms.NixStatusPath+" 2>/dev/null; echo; echo \"svc=$(systemctl is-active gantry-apply 2>/dev/null)\"")
	if err != nil {
		return nixState{}, err
	}
	var st nixState
	for i, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if i == 0 {
			f := strings.Fields(l)
			if len(f) > 0 {
				st.State = f[0]
			}
			if len(f) > 1 {
				st.Hash = f[1]
			}
		}
		if v, ok := strings.CutPrefix(l, "svc="); ok {
			st.Active = v == "active" || v == "activating" || v == "reloading"
		}
	}
	return st, nil
}

// nixLogTail is the end of the last nixos-rebuild inside the VM.
func (s *Server) nixLogTail(ctx context.Context, h *hosts.Host, project, name string) string {
	out, err := s.vmExec(ctx, h, project, name, "sudo tail -n 80 "+vms.NixLogPath+" 2>/dev/null")
	if err != nil {
		return ""
	}
	return strings.TrimRight(out, "\n")
}

// nixPut writes a file of the VM's Nix configuration as root, in pieces
// small enough for one SSH command each.
func (s *Server) nixPut(ctx context.Context, h *hosts.Host, project, name, dest string, content []byte) error {
	b64 := base64.StdEncoding.EncodeToString(content)
	tmp := "/tmp/gantry-nix-" + strings.ReplaceAll(strings.TrimPrefix(dest, "/"), "/", "_") + ".b64"
	for i := 0; i == 0 || i < len(b64); i += nixChunk {
		part := b64[i:min(i+nixChunk, len(b64))]
		op := ">>"
		if i == 0 {
			op = ">"
		}
		if _, err := s.vmExec(ctx, h, project, name, "printf %s '"+part+"' "+op+" "+tmp); err != nil {
			return err
		}
	}
	_, err := s.vmExec(ctx, h, project, name, "base64 -d "+tmp+" | sudo install -m 0644 /dev/stdin "+dest+" && rm -f "+tmp)
	return err
}

// waitNix follows gantry-apply until it reports want. A run that ended on
// other files (both files changed close together and the service took the
// first) is started again.
func (s *Server) waitNix(ctx context.Context, h *hosts.Host, project, name, want string, say func(string)) error {
	deadline := time.Now().Add(nixApplyMax)
	last, kicks := "", 0
	var lastKick time.Time
	for time.Now().Before(deadline) {
		st, err := s.nixStatus(ctx, h, project, name)
		if err == nil {
			if st.Hash == want && st.State == "ok" {
				return nil
			}
			if st.Hash == want && st.State == "failed" {
				log := s.nixLogTail(ctx, h, project, name)
				if log != "" {
					say(fmt.Sprintf("── fim do nixos-rebuild (%s)\n", vms.NixLogPath))
					say(log + "\n")
				}
				return fmt.Errorf("VM %s: o nixos-rebuild falhou; a VM continua na configuração anterior", name)
			}
			if cur := st.State + " " + st.Hash; cur != last {
				last = cur
				if st.State == "applying" && st.Hash == want {
					say(fmt.Sprintf("%s: aplicando a configuração Nix (nixos-rebuild)…\n", name))
				}
			}
			if !st.Active && st.Hash != want && st.State != "applying" && kicks < 3 && time.Since(lastKick) > 20*time.Second {
				kicks++
				lastKick = time.Now()
				_, _ = s.vmExec(ctx, h, project, name, "sudo systemctl start --no-block gantry-apply")
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(nixPoll):
		}
	}
	return fmt.Errorf("VM %s: a configuração Nix não terminou de aplicar em %d minutos", name, int(nixApplyMax.Minutes()))
}

// applyNix delivers the VM's Nix files and waits for the result; on
// success the VM's definition hashes are updated.
func (s *Server) applyNix(ctx context.Context, h *hosts.Host, project string, p *composex.Project, v *composex.VM, say func(string)) error {
	net, err := s.projectNet(ctx, h, project, p)
	if err != nil {
		return err
	}
	env, err := vms.Env(project, v, net)
	if err != nil {
		return err
	}
	params, module, err := vms.NixFiles(v)
	if err != nil {
		return err
	}
	want := vms.NixHash(params, module)
	say(fmt.Sprintf("── %s: enviando %s e %s\n", v.Name, vms.NixParamsPath, vms.NixModulePath))
	// Parameters first, module after, as the spec of the image expects.
	if err := s.nixPut(ctx, h, project, v.Name, vms.NixParamsPath, params); err != nil {
		return fmt.Errorf("VM %s: não consegui gravar %s: %v", v.Name, vms.NixParamsPath, err)
	}
	if err := s.nixPut(ctx, h, project, v.Name, vms.NixModulePath, module); err != nil {
		return fmt.Errorf("VM %s: não consegui gravar %s: %v", v.Name, vms.NixModulePath, err)
	}
	if _, err := s.vmExec(ctx, h, project, v.Name, "sudo systemctl start --no-block gantry-apply"); err != nil {
		return fmt.Errorf("VM %s: não consegui iniciar o gantry-apply: %v", v.Name, err)
	}
	if err := s.waitNix(ctx, h, project, v.Name, want, say); err != nil {
		return err
	}
	if _, err := vms.Run(ctx, h, "spec-set", vmEnv(project, v.Name, "GV_HASH", env["GV_HASH"], "GV_BASEHASH", env["GV_BASEHASH"], "GV_NIXHASH", want)); err != nil {
		return fmt.Errorf("a configuração foi aplicada, mas não consegui anotar isso na máquina: %v", err)
	}
	say(fmt.Sprintf("%s: configuração Nix aplicada\n", v.Name))
	return nil
}

// vmNixApply is the Aplicar button of a VM whose Nix configuration changed.
func (s *Server) vmNixApply(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	l, name, ok := s.vmContext(w, r, h)
	if !ok {
		return
	}
	for _, v := range l.VMs {
		if v.Name == name && v.State != "running" {
			writeErr(w, http.StatusConflict, errors.New("inicie a VM para aplicar"))
			return
		}
	}
	s.vmJob(w, h, l, fmt.Sprintf("Aplicar a configuração Nix de %s", name), func(ctx context.Context, out io.Writer) error {
		ctx, cancel := context.WithTimeout(ctx, nixApplyMax+2*time.Minute)
		defer cancel()
		p, _, err := s.loadProject(ctx, h, l.Files)
		if err != nil {
			return err
		}
		v := p.VM(name)
		if v == nil || v.Nix == nil {
			return fmt.Errorf("a VM %s não tem nix: no compose", name)
		}
		return s.applyNix(ctx, h, l.ID, p, v, func(m string) { fmt.Fprint(out, m) })
	})
}
