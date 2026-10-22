package server

import (
	"context"
	"errors"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"gantry/internal/hosts"
	"gantry/internal/i18n"
	"gantry/internal/labs"
)

// projectFilesScript lists the text files of a project folder that are worth
// editing in the app: configs next to the compose (prometheus.yml, routes,
// dashboards…), up to three levels down.
const projectFilesScript = `cd "$1" 2>/dev/null || exit 0
find . -maxdepth 3 -type f -size -1024k \( -name '*.yml' -o -name '*.yaml' -o -name '*.json' -o -name '*.conf' -o -name '*.toml' \
  -o -name '*.ini' -o -name '*.cfg' -o -name '*.txt' -o -name '*.sh' -o -name '*.html' -o -name '*.properties' -o -name '*.nix' -o -name 'Dockerfile*' \) \
  -not -path './.git/*' -not -path '*/node_modules/*' 2>/dev/null | head -100`

// projectFiles returns the extra editable files of the lab, relative to its folder.
func (s *Server) projectFiles(ctx context.Context, h *hosts.Host, dir string) []string {
	out, err := h.Run(ctx, "", []string{"sh", "-c", projectFilesScript, "sh", dir}, nil)
	if err != nil {
		return nil
	}
	var files []string
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimPrefix(strings.TrimSpace(l), "./")
		if l != "" {
			files = append(files, l)
		}
	}
	sort.Strings(files)
	return files
}

// relProjectFile checks a name given by the app: a file inside the folder,
// found by projectFiles.
func (s *Server) relProjectFile(ctx context.Context, h *hosts.Host, dir, name string) (string, error) {
	clean := path.Clean(name)
	if path.IsAbs(clean) || clean == "." || strings.HasPrefix(clean, "..") {
		return "", errors.New("caminho fora da pasta do projeto")
	}
	for _, f := range s.projectFiles(ctx, h, dir) {
		if f == clean {
			return path.Join(dir, clean), nil
		}
	}
	return "", errors.New(name + " não é um arquivo deste projeto")
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

// extraChoices adds the project's other files to the file picker.
func (s *Server) extraChoices(ctx context.Context, h *hosts.Host, main string, l labs.Lab, base []map[string]string) []map[string]string {
	known := map[string]bool{path.Base(main): true, ".env": true}
	for _, f := range l.Files {
		known[strings.TrimPrefix(f, path.Dir(main)+"/")] = true
	}
	for _, f := range s.projectFiles(ctx, h, path.Dir(main)) {
		if !known[f] {
			base = append(base, map[string]string{"name": f, "label": f, "kind": "extra"})
		}
	}
	return base
}
