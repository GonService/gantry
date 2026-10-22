package server

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"gantry/internal/engine"
	"gantry/internal/hosts"
)

// buildImage builds an image, like Portainer's "Build a new image": from a
// Dockerfile typed in the app, from a folder on the host's machine or from
// a git repository. The output goes to a job.
func (s *Server) buildImage(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		Tags       []string          `json:"tags"`
		Source     string            `json:"source"` // editor, dir, git
		Dockerfile string            `json:"dockerfile"`
		Path       string            `json:"path"` // Dockerfile inside the folder or repo
		Dir        string            `json:"dir"`
		Git        string            `json:"git"`
		Args       map[string]string `json:"args"`
		Target     string            `json:"target"`
		NoCache    bool              `json:"noCache"`
		Pull       bool              `json:"pull"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var tags []string
	for _, t := range req.Tags {
		if t = strings.TrimSpace(t); t != "" {
			tags = append(tags, t)
		}
	}
	if len(tags) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("dê um nome à imagem, como meu-app:1.0"))
		return
	}
	opts := engine.BuildOptions{Tags: tags, Dockerfile: strings.TrimSpace(req.Path), Args: req.Args, Target: strings.TrimSpace(req.Target), NoCache: req.NoCache, Pull: req.Pull}
	eng, ok := s.reqEngine(w, r, h)
	if !ok {
		return
	}
	var dir string
	switch req.Source {
	case "editor":
		if strings.TrimSpace(req.Dockerfile) == "" {
			writeErr(w, http.StatusBadRequest, errors.New("escreva o Dockerfile"))
			return
		}
		opts.Dockerfile = "Dockerfile"
	case "dir":
		if !h.Files() {
			writeErr(w, http.StatusBadRequest, errors.New("este tipo de host não lê pastas; use ssh://"))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		d, err := expandDir(ctx, h, req.Dir)
		cancel()
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		dir = d
	case "git":
		g := strings.TrimSpace(req.Git)
		if !strings.HasPrefix(g, "https://") && !strings.HasPrefix(g, "http://") && !strings.HasPrefix(g, "git@") && !strings.HasPrefix(g, "git://") {
			writeErr(w, http.StatusBadRequest, errors.New("informe a URL do repositório (https://… ou git@…)"))
			return
		}
		opts.Remote = g
	default:
		writeErr(w, http.StatusBadRequest, errors.New("origem desconhecida"))
		return
	}
	id, err := s.startJob("build", "build", func(ctx context.Context, out io.Writer) error {
		fmt.Fprintf(out, "$ build -t %s\n\n", strings.Join(tags, " -t "))
		var body io.Reader
		var wait func() error
		switch req.Source {
		case "editor":
			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)
			df := []byte(req.Dockerfile)
			_ = tw.WriteHeader(&tar.Header{Name: "Dockerfile", Mode: 0o644, Size: int64(len(df)), ModTime: time.Now()})
			_, _ = tw.Write(df)
			_ = tw.Close()
			body = &buf
		case "dir":
			// The folder is sent as the build context, honoring .dockerignore
			// as tar exclude patterns (good enough for the usual entries).
			q := hosts.Quote(dir)
			line := "cd " + q + " && if [ -f .dockerignore ]; then tar -X .dockerignore -cf - .; else tar -cf - .; fi"
			rc, wt, err := h.Pipe(ctx, line)
			if err != nil {
				return err
			}
			defer rc.Close()
			body, wait = rc, wt
			fmt.Fprintf(out, "contexto: %s\n", dir)
		}
		err := eng.Build(ctx, body, opts, func(l string) { fmt.Fprintln(out, l) })
		if wait != nil {
			if werr := wait(); werr != nil && err == nil {
				err = fmt.Errorf("ler a pasta: %v", werr)
			}
		}
		if err == nil {
			fmt.Fprintf(out, "\nImagem %s pronta.\n", tags[0])
		}
		return err
	})
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, map[string]string{"job": id})
}

// tagImage gives an image another name.
func (s *Server) tagImage(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		Ref string `json:"ref"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil || strings.TrimSpace(req.Ref) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("informe o novo nome, como meu-app:1.0"))
		return
	}
	eng, ok := s.reqEngine(w, r, h)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	if err := eng.Tag(ctx, r.PathValue("id"), strings.TrimSpace(req.Ref)); err != nil {
		writeErr(w, engineStatus(err), err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
