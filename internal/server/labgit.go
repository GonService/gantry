package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gantry/internal/composex"
	"gantry/internal/hosts"
)

var gitURL = regexp.MustCompile(`^(https?://|git@|ssh://|git://|file://)\S+$`)

// labFromGit clones a repository on the host and registers its compose
// file as a lab, like a Portainer stack from a repository.
func (s *Server) labFromGit(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	if !h.Files() {
		writeErr(w, http.StatusBadRequest, errors.New("este host não dá acesso a arquivos; conecte via ssh://"))
		return
	}
	var req struct {
		Name   string `json:"name"`
		URL    string `json:"url"`
		Branch string `json:"branch"`
		Dir    string `json:"dir"`
		File   string `json:"file"` // compose file (or its folder) inside the repo
		Up     bool   `json:"up"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	if !gitURL.MatchString(req.URL) {
		writeErr(w, http.StatusBadRequest, errors.New("informe a URL do repositório (https://… ou git@…)"))
		return
	}
	if !labName.MatchString(req.Name) {
		writeErr(w, http.StatusBadRequest, errors.New("nome do projeto: use letras minúsculas, números, - ou _ (começando por letra ou número)"))
		return
	}
	file := strings.Trim(strings.TrimSpace(req.File), "/")
	if strings.Contains(file, "..") {
		writeErr(w, http.StatusBadRequest, errors.New("caminho do compose inválido"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	dir, err := expandDir(ctx, h, req.Dir)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if labs, _ := s.labsOf(ctx, h); labs != nil {
		for _, l := range labs {
			if l.ID == req.Name {
				writeErr(w, http.StatusConflict, fmt.Errorf("já existe um projeto chamado %s neste host", req.Name))
				return
			}
		}
	}
	if h.Exists(ctx, dir) {
		writeErr(w, http.StatusConflict, fmt.Errorf("a pasta %s já existe; escolha uma pasta nova para o clone", dir))
		return
	}
	argv := []string{"git", "clone", "--depth", "1"}
	if b := strings.TrimSpace(req.Branch); b != "" {
		argv = append(argv, "--branch", b)
	}
	argv = append(argv, "--", req.URL, dir)
	cloneOut, err := h.Run(ctx, "", argv, nil)
	if err != nil {
		writeErr(w, http.StatusBadGateway, fmt.Errorf("git clone falhou: %v", err))
		return
	}
	// The compose file: the one given, or the usual names in the folder.
	target := dir
	if file != "" {
		target = path.Join(dir, file)
	}
	compose := ""
	if strings.HasSuffix(target, ".yaml") || strings.HasSuffix(target, ".yml") {
		if h.Exists(ctx, target) {
			compose = target
		}
	} else {
		for _, n := range composeNames {
			if h.Exists(ctx, path.Join(target, n)) {
				compose = path.Join(target, n)
				break
			}
		}
	}
	if compose == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("o repositório foi clonado em %s, mas não achei um compose em %s; informe o caminho do arquivo", dir, target))
		return
	}
	raw, err := h.ReadFile(ctx, compose)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	p, err := composex.Parse(raw)
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("compose inválido em %s: %v", compose, err))
		return
	}
	var rts []string
	if p.Gantry && p.Mixed(h.Runtime()) {
		for _, g := range p.Plan(h.Runtime()) {
			rts = append(rts, g.Runtime)
		}
	}
	wd := path.Dir(compose)
	s.store.SetLab(h.Name, req.Name, wd, []string{compose}, rts, vmNames(p))
	resp := map[string]any{"ok": true, "id": req.Name, "path": compose, "output": cloneOut}
	if req.Up {
		out, cmd, err := s.composeLab(ctx, h, req.Name, wd, []string{compose}, "up")
		resp["output"], resp["command"] = cloneOut+out, cmd
		if err != nil {
			resp["ok"], resp["error"] = false, err.Error()
		}
	}
	writeJSON(w, resp)
}

// labGit tells whether the lab's folder is a git checkout and its state.
func (s *Server) labGit(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	if !h.Files() {
		writeJSON(w, map[string]any{"repo": false})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	l, err := s.findLab(ctx, h, r.PathValue("lab"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	wd := l.WorkingDir
	if wd == "" {
		writeJSON(w, map[string]any{"repo": false})
		return
	}
	git := func(args ...string) (string, error) {
		out, err := h.Run(ctx, "", append([]string{"git", "-C", wd}, args...), nil)
		return strings.TrimSpace(out), err
	}
	top, err := git("rev-parse", "--show-toplevel")
	if err != nil {
		writeJSON(w, map[string]any{"repo": false})
		return
	}
	url, _ := git("remote", "get-url", "origin")
	url = hideUserinfo(url)
	branch, _ := git("rev-parse", "--abbrev-ref", "HEAD")
	commit, _ := git("log", "-1", "--format=%h\x1f%s\x1f%cr")
	status, _ := git("status", "--porcelain", "--untracked-files=no")
	parts := strings.SplitN(commit, "\x1f", 3)
	for len(parts) < 3 {
		parts = append(parts, "")
	}
	out := map[string]any{"repo": true, "root": top, "url": url, "branch": branch,
		"commit": parts[0], "subject": parts[1], "when": parts[2], "dirty": status != ""}
	if r.URL.Query().Get("remoto") == "1" && url != "" {
		// Ask the remote, without ever stopping for a password.
		fctx, fcancel := context.WithTimeout(ctx, 25*time.Second)
		_, ferr := h.Run(fctx, "", []string{"env", "GIT_TERMINAL_PROMPT=0", "GIT_SSH_COMMAND=ssh -o BatchMode=yes", "git", "-C", wd, "fetch", "--quiet", "origin"}, nil)
		fcancel()
		if ferr != nil {
			out["fetchError"] = "não consegui consultar o remoto (sem acesso sem senha?)"
		} else if n, err := git("rev-list", "--count", "HEAD..@{u}"); err == nil {
			out["behind"], _ = strconv.Atoi(n)
		} else {
			out["fetchError"] = "a branch não acompanha uma branch do remoto"
		}
	}
	writeJSON(w, out)
}

// labGitPull updates the checkout (fast-forward only) and optionally
// redeploys the lab.
func (s *Server) labGitPull(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		Up bool `json:"up"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	l, err := s.findLab(ctx, h, r.PathValue("lab"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if l.WorkingDir == "" {
		writeErr(w, http.StatusBadRequest, errors.New("projeto sem pasta conhecida"))
		return
	}
	out, err := h.Run(ctx, "", []string{"git", "-C", l.WorkingDir, "pull", "--ff-only"}, nil)
	if err != nil {
		writeErr(w, http.StatusBadGateway, fmt.Errorf("git pull falhou: %v", err))
		return
	}
	resp := map[string]any{"ok": true, "output": out, "command": "git pull --ff-only"}
	if req.Up {
		files := l.Files
		if len(files) == 0 {
			if f, err := s.labFile(ctx, h, l); err == nil {
				files = []string{f}
			}
		}
		up, cmd, err := s.composeLab(ctx, h, l.ID, l.WorkingDir, files, "up")
		resp["output"], resp["command"] = out+"\n"+up, "git pull --ff-only && "+cmd
		if err != nil {
			resp["ok"], resp["error"] = false, err.Error()
		}
	}
	writeJSON(w, resp)
}

// hideUserinfo drops a password or token written in a remote URL
// (https://user:token@host/...), so it never reaches the screen.
func hideUserinfo(raw string) string {
	u, err := neturl.Parse(raw)
	if err != nil || u.User == nil || u.Scheme == "" {
		return raw
	}
	u.User = nil
	return u.String()
}
