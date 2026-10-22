package server

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"gantry/internal/engine"
	"gantry/internal/hosts"
	"gantry/internal/labs"
)

var opTitle = map[string]string{"up": "Subir", "stop": "Parar", "restart": "Reiniciar", "down": "Remover", "pull": "Atualizar imagens", "rebuild": "Reconstruir"}

// isUpOp: operations that bring the project up (rebuild is up with a new build).
func isUpOp(op string) bool { return op == "up" || op == "rebuild" }

// labJob runs an operation of a project in the background, streaming the
// output of compose and the VMs, and the byte progress of image downloads.
func (s *Server) labJob(h *hosts.Host, l labs.Lab, op string) (string, error) {
	key := "lab:" + h.Machine() + "/" + l.ID
	return s.startJob(key, opTitle[op]+" "+l.Name, func(ctx context.Context, out io.Writer) error {
		live := hosts.WithLive(ctx, out)
		fmt.Fprintf(out, "\x1b[1m%s o projeto %s\x1b[0m em %s\n\n", opTitle[op], l.Name, h.Name)
		defer s.store.Touch(h.Name, l.ID)
		switch op {
		case "stop", "restart", "down":
			o, err := s.labViaEngine(live, h, l, op)
			fmt.Fprint(out, o)
			if err == nil && op == "down" {
				s.dropKubeLab(h, l.ID)
			}
			return err
		}
		if isUpOp(op) || op == "pull" {
			if err := s.prepullImages(live, h, l, op == "pull", out); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				fmt.Fprintf(out, "\x1b[33maviso:\x1b[0m %v. O compose tenta de novo.\n\n", err)
			}
		}
		if isUpOp(op) && len(l.Containers) > 0 {
			// Services about to be recreated: out first go the containers on
			// their network (Podman refuses otherwise).
			s.dropNetDependents(ctx, h, l, s.computeDrift(ctx, h, l), out)
		}
		o, cmd, err := s.composeLabRun(live, h, l.ID, l.WorkingDir, l.Files, op)
		if err != nil && isUpOp(op) {
			if hint := s.portHint(ctx, h, l.ID, l.WorkingDir, l.Files, o+"\n"+err.Error()); hint != "" {
				fmt.Fprintf(out, "\n\x1b[33m%s\x1b[0m\n", hint)
			}
		}
		if cmd != "" {
			fmt.Fprintf(out, "\n\x1b[2m$ %s\x1b[0m\n", cmd)
		}
		if err == nil && isUpOp(op) {
			if p, _, perr := s.loadProject(ctx, h, l.Files); perr == nil {
				s.watchKubeLab(h, l, p, func(m string) { fmt.Fprintf(out, "\n\x1b[36m%s\x1b[0m\n", m) })
			}
		}
		return err
	})
}

// prepullImages downloads the images of the project through the engine API
// before compose runs, so the app can show how many bytes are left. With
// always, images already present are pulled again (Atualizar imagens).
func (s *Server) prepullImages(ctx context.Context, h *hosts.Host, l labs.Lab, always bool, out io.Writer) error {
	p, _, err := s.loadProject(ctx, h, l.Files)
	if err != nil || len(p.Names) == 0 {
		return nil
	}
	if p.Gantry && p.Mixed(h.Runtime()) {
		return nil // each engine pulls its own images in composeMixed
	}
	ref, err := s.engineFor(ctx, h, h.Runtime())
	if err != nil || ref.Engine == nil {
		return nil
	}
	eng := ref.Engine
	have := map[string]bool{}
	if !always {
		imgs, err := eng.Images(ctx)
		if err != nil {
			return nil
		}
		for _, im := range imgs {
			for _, t := range im.RepoTags {
				have[normImage(t)] = true
			}
		}
	}
	var todo []string
	seen := map[string]bool{}
	for _, name := range p.Names {
		sv := p.Services[name]
		if sv == nil || sv.Image == "" || sv.Build || strings.Contains(sv.Image, "${") {
			continue
		}
		n := normImage(sv.Image)
		if seen[n] || have[n] {
			continue
		}
		seen[n] = true
		todo = append(todo, sv.Image)
	}
	j := jobOf(out)
	defer func() {
		if j != nil {
			j.setProgress("")
		}
	}()
	for i, img := range todo {
		fmt.Fprintf(out, "\x1b[36m==>\x1b[0m baixando %s (%d de %d)\n", img, i+1, len(todo))
		start := time.Now()
		var lastShown time.Time
		var final engine.PullState
		err := eng.PullWatch(ctx, img, nil, func(st engine.PullState) {
			final = st
			if j == nil || time.Since(lastShown) < 300*time.Millisecond {
				return
			}
			lastShown = time.Now()
			j.setProgress(pullLine(img, st))
		})
		if err != nil {
			return fmt.Errorf("não consegui baixar %s: %v", img, err)
		}
		size := ""
		if final.Total > 0 {
			size = ", " + humanBytes(final.Total)
		}
		fmt.Fprintf(out, "    pronto em %s%s\n", time.Since(start).Round(time.Second), size)
	}
	if len(todo) > 0 {
		fmt.Fprintln(out)
	}
	return nil
}

func pullLine(img string, st engine.PullState) string {
	s := "Baixando " + img
	if st.Total > 0 {
		pct := int(float64(st.Current) * 100 / float64(st.Total))
		s += fmt.Sprintf(": %s de %s (%d%%)", humanBytes(st.Current), humanBytes(st.Total), pct)
	}
	if st.Layers > 0 {
		s += fmt.Sprintf(" · camadas %d/%d", st.Done, st.Layers)
	}
	return s
}

// normImage writes an image reference the way engines list it, so
// "nginx:alpine" matches "docker.io/library/nginx:alpine".
func normImage(ref string) string {
	ref = strings.TrimSpace(ref)
	if i := strings.Index(ref, "@"); i >= 0 {
		return ref
	}
	name, tag := ref, "latest"
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		name, tag = ref[:i], ref[i+1:]
	}
	first := name
	if i := strings.Index(name, "/"); i >= 0 {
		first = name[:i]
	}
	if !strings.ContainsAny(first, ".:") && first != "localhost" {
		if !strings.Contains(name, "/") {
			name = "library/" + name
		}
		name = "docker.io/" + name
	}
	return name + ":" + tag
}

func humanBytes(n int64) string {
	const k = 1024
	switch {
	case n >= k*k*k:
		return strings.Replace(fmt.Sprintf("%.1f GB", float64(n)/(k*k*k)), ".", ",", 1)
	case n >= k*k:
		return fmt.Sprintf("%d MB", n/(k*k))
	case n >= k:
		return fmt.Sprintf("%d KB", n/k)
	}
	return fmt.Sprintf("%d B", n)
}
