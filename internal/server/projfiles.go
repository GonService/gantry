package server

import (
	"context"
	"errors"
	"net/http"
	"path"
	"strings"
	"time"

	"gantry/internal/hosts"
	"gantry/internal/i18n"
)

// relProjectFile checks a name given by the app: a text-sized file inside
// the folder, at any depth (the tree of the Arquivo tab opens any of them).
func (s *Server) relProjectFile(ctx context.Context, h *hosts.Host, dir, name string) (string, error) {
	clean, err := cleanRel(name)
	if err != nil {
		return "", err
	}
	if _, err := runTree(ctx, h, fileCheckScript, dir, clean); err != nil {
		return "", errors.New(name + ": " + err.Error())
	}
	return path.Join(dir, clean), nil
}

// restartForFile restarts the services that mount the file (or a folder
// holding it), so a changed config takes effect.
func (s *Server) restartForFile(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	l, err := s.findLab(ctx, h, r.PathValue("lab"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	main, err := s.labFile(ctx, h, l)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	dir := path.Dir(main)
	p, err := s.relProjectFile(ctx, h, dir, r.URL.Query().Get("name"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	files := l.Files
	if len(files) == 0 {
		files = []string{main}
	}
	proj, _, err := s.loadProject(ctx, h, files)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	svcs := servicesMounting(proj.Names, func(n string) []string { return proj.Services[n].Binds }, dir, p)
	if len(svcs) == 0 {
		writeJSON(w, map[string]any{"ok": true, "output": i18n.T("Nenhum serviço monta " + path.Base(p) + "; nada a reiniciar.\n"), "services": svcs})
		return
	}
	if proj.Gantry && proj.Mixed(h.Runtime()) {
		out, cmd, err := s.composeLab(ctx, h, l.ID, dir, files, "restart")
		resp := map[string]any{"ok": err == nil, "output": out, "command": cmd, "services": svcs}
		if err != nil {
			resp["error"] = err.Error()
		}
		writeJSON(w, resp)
		return
	}
	argv := append([]string{}, h.Compose()...)
	argv = append(argv, "-p", l.ID)
	for _, f := range files {
		argv = append(argv, "-f", f)
	}
	argv = append(append(argv, "restart"), svcs...)
	out, err := h.Run(ctx, dir, argv, nil)
	resp := map[string]any{"ok": err == nil, "output": out, "command": strings.Join(argv, " "), "services": svcs}
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSON(w, resp)
}

// servicesMounting lists the services with a bind mount that is the file
// or one of its folders. Relative binds are relative to the project folder.
func servicesMounting(names []string, binds func(string) []string, dir, file string) []string {
	var out []string
	for _, n := range names {
		for _, b := range binds(n) {
			if strings.HasPrefix(b, "~") {
				continue
			}
			src := b
			if !path.IsAbs(src) {
				src = path.Join(dir, src)
			}
			src = path.Clean(src)
			if file == src || strings.HasPrefix(file, src+"/") {
				out = append(out, n)
				break
			}
		}
	}
	return out
}
