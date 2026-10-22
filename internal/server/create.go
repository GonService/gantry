package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"
	"time"

	"gantry/internal/composex"
	"gantry/internal/engine"
	"gantry/internal/hosts"
	"gantry/internal/labs"
	"gantry/internal/secrets"
)

var labName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// expandDir resolves ~ on the host's machine and checks the path.
func expandDir(ctx context.Context, h *hosts.Host, dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		home := ""
		if h.Remote() {
			out, err := h.Run(ctx, "", []string{"sh", "-c", `printf %s "$HOME"`}, nil)
			if err != nil {
				return "", err
			}
			home = strings.TrimSpace(out)
		} else {
			home, _ = os.UserHomeDir()
		}
		dir = home + strings.TrimPrefix(dir, "~")
	}
	if !strings.HasPrefix(dir, "/") {
		return "", errors.New("informe um caminho absoluto ou começando com ~/")
	}
	return path.Clean(dir), nil
}

// createReq is a new project: its compose and files, written in a folder
// of the host.
type createReq struct {
	Name    string `json:"name"`
	Dir     string `json:"dir"`
	Compose string `json:"compose"`
	Env     string `json:"env"`
	// Files go next to the compose file (configs, dashboards…).
	Files     map[string]string `json:"files"`
	Up        bool              `json:"up"`
	Overwrite bool              `json:"overwrite"`
	// Job brings the project up in the background (with live output)
	// instead of answering only when it is up.
	Job bool `json:"job"`
	// Keychain moves the passwords and tokens of the .env to the
	// system keychain before the project goes up.
	Keychain bool `json:"keychain"`
}

// createLab writes a new compose file in a folder of the host and,
// optionally, brings the lab up.
func (s *Server) createLab(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req createReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Minute)
	defer cancel()
	resp, code, err := s.doCreateLab(ctx, h, req)
	if err != nil {
		writeErr(w, code, err)
		return
	}
	writeJSON(w, resp)
}

// doCreateLab writes the project of req (createLab, the imports); the code
// is the HTTP status of an error.
func (s *Server) doCreateLab(ctx context.Context, h *hosts.Host, req createReq) (map[string]any, int, error) {
	if !h.Files() {
		return nil, http.StatusBadRequest, errors.New("este host não dá acesso a arquivos; conecte via ssh://")
	}
	if !labName.MatchString(req.Name) {
		return nil, http.StatusBadRequest, errors.New("nome do projeto: use letras minúsculas, números, - ou _ (começando por letra ou número)")
	}
	req.Compose = withProjectName(req.Compose, req.Name)
	p, err := composex.Parse(composex.Interpolate([]byte(req.Compose), composex.ParseEnv([]byte(req.Env))))
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("compose inválido: %v", err)
	}
	if len(p.Names) == 0 && len(p.VMs) == 0 {
		return nil, http.StatusBadRequest, errors.New("o projeto precisa de pelo menos um serviço ou uma VM")
	}
	dir, err := expandDir(ctx, h, req.Dir)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	if labs, _ := s.labsOf(ctx, h); labs != nil {
		for _, l := range labs {
			if l.ID == req.Name && !req.Overwrite {
				return nil, http.StatusConflict, fmt.Errorf("já existe um projeto chamado %s neste host", req.Name)
			}
		}
	}
	// The NixOS module of a VM with nix: comes with the project (a file of
	// the template) or is already in the folder.
	for _, v := range p.VMs {
		type ref struct{ field, file string }
		var refs []ref
		if v.Nix != nil {
			refs = append(refs, ref{"nix.module", v.Nix.Module})
		}
		if v.CloudInitFile != "" {
			refs = append(refs, ref{"cloud-init", v.CloudInitFile})
		}
		if v.ConfigFile != "" {
			refs = append(refs, ref{"config", v.ConfigFile})
		}
		for _, x := range refs {
			f := nixModulePath(dir, x.file)
			rel := strings.TrimPrefix(f, strings.TrimSuffix(dir, "/")+"/")
			if _, ok := req.Files[rel]; ok {
				continue
			}
			if _, ok := req.Files[strings.TrimPrefix(x.file, "./")]; ok {
				continue
			}
			if !h.Exists(ctx, f) {
				return nil, http.StatusBadRequest, fmt.Errorf("x-vms.%s.%s: %s não está entre os arquivos do projeto nem em %s", v.Name, x.field, x.file, dir)
			}
		}
	}
	if !req.Overwrite {
		for _, n := range composex.FileNames {
			if h.Exists(ctx, path.Join(dir, n)) {
				return nil, http.StatusConflict, fmt.Errorf("a pasta %s já tem um %s; escolha outra pasta ou marque para substituir", dir, n)
			}
		}
	}
	if h.Remote() {
		_, err = h.Run(ctx, "", []string{"mkdir", "-p", dir}, nil)
	} else {
		err = os.MkdirAll(dir, 0o755)
	}
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("não consegui criar a pasta %s: %v", dir, err)
	}
	file := path.Join(dir, "compose.yaml")
	if err := h.WriteFile(ctx, file, []byte(req.Compose)); err != nil {
		return nil, http.StatusBadGateway, err
	}
	for rel, content := range req.Files {
		clean := path.Clean(strings.TrimSpace(rel))
		if clean == "." || strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "../") || clean == ".." || clean == "compose.yaml" || clean == ".env" {
			return nil, http.StatusBadRequest, fmt.Errorf("arquivo do modelo com caminho inválido: %s", rel)
		}
		p := path.Join(dir, clean)
		if sub := path.Dir(p); sub != dir {
			if h.Remote() {
				_, err = h.Run(ctx, "", []string{"mkdir", "-p", sub}, nil)
			} else {
				err = os.MkdirAll(sub, 0o755)
			}
			if err != nil {
				return nil, http.StatusBadGateway, fmt.Errorf("não consegui criar %s: %v", sub, err)
			}
		}
		if err := h.WriteFile(ctx, p, []byte(content)); err != nil {
			return nil, http.StatusBadGateway, err
		}
	}
	if strings.TrimSpace(req.Env) != "" {
		if err := h.WriteFile(ctx, path.Join(dir, ".env"), []byte(strings.TrimRight(req.Env, "\n")+"\n")); err != nil {
			return nil, http.StatusBadGateway, err
		}
	}
	var rts []string
	if p.Gantry && p.Mixed(h.Runtime()) {
		for _, g := range p.Plan(h.Runtime()) {
			rts = append(rts, g.Runtime)
		}
	}
	s.store.SetLab(h.Name, req.Name, dir, []string{file}, rts, vmNames(p))
	resp := map[string]any{"ok": true, "id": req.Name, "path": file}
	if req.Keychain && strings.TrimSpace(req.Env) != "" {
		var keys []string
		for k := range composex.ParseEnv([]byte(req.Env)) {
			if secrets.IsSecretName(k) {
				keys = append(keys, k)
			}
		}
		moved, err := s.moveSecrets(ctx, h, req.Name, dir, keys)
		if err != nil {
			return nil, http.StatusBadGateway, fmt.Errorf("chaveiro: %v", err)
		}
		resp["keychain"] = moved
	}
	if req.Up && req.Job {
		jid, err := s.labJob(h, labs.Lab{ID: req.Name, Name: req.Name, WorkingDir: dir, Files: []string{file}}, "up")
		if err != nil {
			resp["ok"], resp["error"] = false, err.Error()
		} else {
			resp["job"] = jid
		}
	} else if req.Up {
		out, cmd, err := s.composeLab(ctx, h, req.Name, dir, []string{file}, "up")
		resp["output"], resp["command"] = out, cmd
		if err != nil {
			resp["ok"], resp["error"] = false, err.Error()
		}
	}
	return resp, http.StatusOK, nil
}

// createContainer creates a loose container (pulling the image if
// needed) and starts it.
func (s *Server) createContainer(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		engine.ContainerSpec
		Start bool `json:"start"`
		Pull  bool `json:"pull"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 256<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	spec := req.ContainerSpec
	spec.Image = strings.TrimSpace(spec.Image)
	if spec.Image == "" {
		writeErr(w, http.StatusBadRequest, errors.New("informe a imagem"))
		return
	}
	eng, ok := s.reqEngine(w, r, h)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Minute)
	defer cancel()
	id, pulled, err := createWithPull(ctx, eng, spec, req.Pull)
	if err != nil {
		writeErr(w, engineStatus(err), err)
		return
	}
	if req.Start {
		if err := eng.Action(ctx, id, "start"); err != nil {
			writeErr(w, engineStatus(err), fmt.Errorf("criado, mas não iniciou: %v", err))
			return
		}
	}
	writeJSON(w, map[string]any{"ok": true, "id": id, "pulled": pulled})
}

// createResource creates a volume or a network; images are pulled as a job
// whose progress the UI follows.
func (s *Server) createResource(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	kind := r.PathValue("kind")
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var req struct {
		engine.NetworkSpec
		Image string `json:"image"`
	}
	var vol engine.VolumeSpec
	if err := json.Unmarshal(body, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	_ = json.Unmarshal(body, &vol)
	eng, ok := s.reqEngine(w, r, h)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	switch kind {
	case "volumes":
		if strings.TrimSpace(req.Name) == "" {
			writeErr(w, http.StatusBadRequest, errors.New("informe o nome do volume"))
			return
		}
		vol.Name = strings.TrimSpace(vol.Name)
		if err := eng.CreateVolume(ctx, vol); err != nil {
			writeErr(w, engineStatus(err), err)
			return
		}
	case "networks":
		if strings.TrimSpace(req.Name) == "" {
			writeErr(w, http.StatusBadRequest, errors.New("informe o nome da rede"))
			return
		}
		req.NetworkSpec.Name = strings.TrimSpace(req.Name)
		if err := eng.CreateNetwork(ctx, req.NetworkSpec); err != nil {
			writeErr(w, engineStatus(err), err)
			return
		}
	case "images":
		ref := strings.TrimSpace(req.Image)
		if ref == "" {
			writeErr(w, http.StatusBadRequest, errors.New("informe a imagem, como nginx:alpine"))
			return
		}
		id, err := s.startJob("pull:"+ref, "pull", func(ctx context.Context, out io.Writer) error {
			fmt.Fprintf(out, "$ pull %s\n\n", ref)
			err := eng.Pull(ctx, ref, func(l string) { fmt.Fprintln(out, l) })
			if err == nil {
				fmt.Fprintf(out, "\n%s baixada.\n", ref)
			}
			return err
		})
		if err != nil {
			writeErr(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, map[string]string{"job": id})
		return
	default:
		writeErr(w, http.StatusNotFound, errors.New("tipo desconhecido"))
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

var topName = regexp.MustCompile(`(?m)^name:[^\n]*\n?`)

// withProjectName makes the file's top-level name match the project, so
// running compose by hand in the folder finds the same project.
func withProjectName(compose, name string) string {
	line := "name: " + name + "\n"
	if loc := topName.FindStringIndex(compose); loc != nil {
		return compose[:loc[0]] + line + compose[loc[1]:]
	}
	// After the leading comments, if any.
	lines := strings.SplitAfter(compose, "\n")
	i := 0
	for i < len(lines) && (strings.HasPrefix(lines[i], "#") || strings.TrimSpace(lines[i]) == "") {
		i++
	}
	head := strings.Join(lines[:i], "")
	if head != "" && !strings.HasSuffix(head, "\n\n") {
		head += "\n"
	}
	return head + line + "\n" + strings.Join(lines[i:], "")
}
