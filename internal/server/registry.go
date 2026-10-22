package server

// Private registries and image operations: logins kept in the keychain
// (used for pulls and pushes through the engine API, and written to a
// host's docker CLI on request), push, history, save/load as .tar.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"gantry/internal/engine"
	"gantry/internal/hosts"
)

type registry struct {
	Server string `json:"server"`
	User   string `json:"user"`
	Since  int64  `json:"since"`
}

var registryMu sync.Mutex

func (s *Server) registryFile() string { return filepath.Join(s.configDir(), "registros.json") }

func (s *Server) registries() []registry {
	var list []registry
	if b, err := os.ReadFile(s.registryFile()); err == nil {
		_ = json.Unmarshal(b, &list)
	}
	return list
}

func (s *Server) saveRegistries(list []registry) error {
	sort.Slice(list, func(i, j int) bool { return list[i].Server < list[j].Server })
	b, _ := json.MarshalIndent(list, "", "  ")
	if err := os.MkdirAll(s.configDir(), 0o700); err != nil {
		return err
	}
	return os.WriteFile(s.registryFile(), b, 0o600)
}

func registryAccount(server string) string { return "registro/" + server }

// registryLogin is what engine.RegistryAuth asks.
func (s *Server) registryLogin(server string) (string, string, bool) {
	registryMu.Lock()
	list := s.registries()
	registryMu.Unlock()
	for _, r := range list {
		if r.Server == server {
			pass, err := s.secretStore().Get(registryAccount(server))
			if err != nil {
				return "", "", false
			}
			return r.User, pass, true
		}
	}
	return "", "", false
}

var registryRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9.-]*[a-zA-Z0-9])?(:[0-9]{1,5})?$`)

func normRegistry(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
	s = strings.TrimSuffix(s, "/")
	switch s {
	case "", "hub.docker.com", "index.docker.io", "registry-1.docker.io", "index.docker.io/v1":
		return "docker.io"
	}
	return s
}

func (s *Server) listRegistries(w http.ResponseWriter, r *http.Request) {
	registryMu.Lock()
	list := s.registries()
	registryMu.Unlock()
	if list == nil {
		list = []registry{}
	}
	writeJSON(w, map[string]any{"registries": list, "store": s.secretStore().Name()})
}

func (s *Server) addRegistry(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Server   string `json:"server"`
		User     string `json:"user"`
		Password string `json:"password"`
		Check    string `json:"check"` // host whose engine validates the login
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	req.Server = normRegistry(req.Server)
	if !registryRe.MatchString(req.Server) {
		writeErr(w, http.StatusBadRequest, errors.New("registry inválido (ex.: ghcr.io, registry.exemplo.com:5000)"))
		return
	}
	if strings.TrimSpace(req.User) == "" || req.Password == "" {
		writeErr(w, http.StatusBadRequest, errors.New("informe usuário e senha (ou token)"))
		return
	}
	if h := s.host(req.Check); h != nil && h.Engine != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		err := h.Engine.CheckLogin(ctx, req.Server, req.User, req.Password)
		cancel()
		if err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("o registry recusou o login: %v", err))
			return
		}
	}
	if err := s.secretStore().Set(registryAccount(req.Server), req.Password); err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("não consegui guardar a senha no %s: %v", s.secretStore().Name(), err))
		return
	}
	registryMu.Lock()
	list := s.registries()
	kept := list[:0]
	for _, x := range list {
		if x.Server != req.Server {
			kept = append(kept, x)
		}
	}
	kept = append(kept, registry{Server: req.Server, User: strings.TrimSpace(req.User), Since: time.Now().Unix()})
	err := s.saveRegistries(kept)
	registryMu.Unlock()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]string{"server": req.Server})
}

func (s *Server) removeRegistry(w http.ResponseWriter, r *http.Request) {
	server := normRegistry(r.PathValue("server"))
	registryMu.Lock()
	list := s.registries()
	kept := list[:0]
	for _, x := range list {
		if x.Server != server {
			kept = append(kept, x)
		}
	}
	err := s.saveRegistries(kept)
	registryMu.Unlock()
	_ = s.secretStore().Delete(registryAccount(server))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// registryHostLogin writes the login to the host's container CLI (docker
// login), so compose and the terminal there can pull too.
func (s *Server) registryHostLogin(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	server := normRegistry(r.PathValue("server"))
	user, pass, ok := s.registryLogin(server)
	if !ok {
		writeErr(w, http.StatusNotFound, errors.New("registry não cadastrado (ou senha fora do chaveiro)"))
		return
	}
	if h.Kind == hosts.TCP {
		writeErr(w, http.StatusBadRequest, errors.New("hosts tcp:// não têm linha de comando; o Gantry usa o login direto nas imagens que baixa"))
		return
	}
	cli := path.Base(h.Compose()[0])
	if cli == "docker-compose" || cli == "podman-compose" {
		cli = strings.TrimSuffix(cli, "-compose")
	}
	target := server
	if cli == "docker" && server == "docker.io" {
		target = "" // docker login with no server is Docker Hub
	}
	argv := []string{cli, "login", "-u", user, "--password-stdin"}
	if target != "" {
		argv = append(argv, target)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	out, err := h.Run(ctx, "", argv, []byte(pass+"\n"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, fmt.Errorf("%s login: %v", cli, firstLine(out, err)))
		return
	}
	writeJSON(w, map[string]string{"output": strings.TrimSpace(out)})
}

func firstLine(out string, err error) string {
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return err.Error()
}

/* images */

func (s *Server) imageHistory(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	eng, ok := s.reqEngine(w, r, h)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	hist, err := eng.ImageHistory(ctx, r.PathValue("id"))
	if err != nil {
		writeErr(w, engineStatus(err), err)
		return
	}
	writeJSON(w, hist)
}

func (s *Server) imagePush(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		Ref string `json:"ref"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&req)
	req.Ref = strings.TrimSpace(req.Ref)
	if !refRe.MatchString(req.Ref) {
		writeErr(w, http.StatusBadRequest, errors.New("nome inválido (ex.: ghcr.io/usuario/app:1.0)"))
		return
	}
	eng, ok := s.reqEngine(w, r, h)
	if !ok {
		return
	}
	id := r.PathValue("id")
	jid, err := s.startJob("push:"+h.Name+"/"+req.Ref, "Enviar "+req.Ref, func(ctx context.Context, out io.Writer) error {
		if err := eng.Tag(ctx, id, req.Ref); err != nil {
			return err
		}
		reg := engine.RegistryOf(req.Ref)
		if _, _, ok := s.registryLogin(reg); !ok {
			fmt.Fprintf(out, "(sem login cadastrado para %s: enviando sem autenticação)\n", reg)
		}
		fmt.Fprintf(out, "Enviando %s para %s…\n", req.Ref, reg)
		if err := eng.Push(ctx, req.Ref, func(l string) { fmt.Fprintln(out, l) }); err != nil {
			return err
		}
		fmt.Fprintln(out, "Imagem enviada.")
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, map[string]string{"id": jid})
}

// imageSave writes an image to a .tar of this computer (docker save).
func (s *Server) imageSave(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		Dest string `json:"dest"`
		Ref  string `json:"ref"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&req)
	if !filepath.IsAbs(req.Dest) {
		writeErr(w, http.StatusBadRequest, errors.New("escolha onde salvar"))
		return
	}
	eng, ok := s.reqEngine(w, r, h)
	if !ok {
		return
	}
	ref := req.Ref
	if ref == "" || strings.HasPrefix(ref, "<") {
		ref = r.PathValue("id")
	}
	jid, err := s.startJob("save:"+req.Dest, "Salvar "+ref, func(ctx context.Context, out io.Writer) error {
		rc, err := eng.ImageSave(ctx, ref)
		if err != nil {
			return err
		}
		defer rc.Close()
		tmp := req.Dest + ".parcial"
		f, err := os.Create(tmp)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Salvando %s em %s…\n", ref, req.Dest)
		n, err := io.Copy(f, &progressReader{r: rc, j: jobOf(out), verb: "Salvando"})
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(tmp)
			return err
		}
		if err := os.Rename(tmp, req.Dest); err != nil {
			return err
		}
		fmt.Fprintf(out, "Pronto: %s (%s).\n", req.Dest, humanBytes(n))
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, map[string]string{"id": jid})
}

// imageLoad loads a .tar of this computer into the engine (docker load).
func (s *Server) imageLoad(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		Src string `json:"src"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&req)
	st, err := os.Stat(req.Src)
	if err != nil || st.IsDir() || !filepath.IsAbs(req.Src) {
		writeErr(w, http.StatusBadRequest, errors.New("escolha o arquivo .tar da imagem"))
		return
	}
	eng, ok := s.reqEngine(w, r, h)
	if !ok {
		return
	}
	jid, err := s.startJob("load:"+h.Name+"/"+req.Src, "Carregar "+filepath.Base(req.Src), func(ctx context.Context, out io.Writer) error {
		f, err := os.Open(req.Src)
		if err != nil {
			return err
		}
		defer f.Close()
		fmt.Fprintf(out, "Carregando %s (%s)…\n", req.Src, humanBytes(st.Size()))
		return eng.ImageLoad(ctx, &progressReader{r: f, j: jobOf(out), total: st.Size()}, func(l string) { fmt.Fprintln(out, l) })
	})
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, map[string]string{"id": jid})
}
