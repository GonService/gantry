package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"gantry/internal/hosts"
	"gantry/internal/labs"
)

// The Arquivos tree of a project: the whole folder (subfolders included),
// with new file, new folder, rename and delete, so a project made in the
// app gets its configs (nginx/conf.d/default.conf, prometheus/rules/…)
// without leaving it. The same shell scripts serve this Mac and the
// machines over ssh.

// Limits of the tree: a project pointed at the home folder by mistake must
// not freeze the app; what passes is cut and the screen says so.
const (
	treeMax   = 3000
	treeDepth = 10
)

// treeScript lists the folder of $1: "d path" and "f path", relative, the
// generated folders (.git, node_modules…) left out.
const treeScript = `cd "$1" 2>/dev/null || { echo gantry-arvore:3; exit 3; }
find . -maxdepth 10 \( -name .git -o -name node_modules -o -name __pycache__ -o -name .venv -o -name venv -o -name .terraform -o -name .idea -o -name .vscode \) -prune \
  -o \( -type d -exec printf 'd %s\n' {} + \) -o \( -type f -exec printf 'f %s\n' {} + \) -o \( -type l -exec printf 'f %s\n' {} + \) 2>/dev/null | head -n 3001`

// treeOpScript does one change in the folder $1 (op $2 on $3, to $4). The
// exit code says what went wrong: 3 no folder, 4 the path leads out of the
// folder (a link), 5 the name is taken, 6 nothing there.
const treeOpScript = `fail() { echo "gantry-arvore:$1"; exit "$1"; }
root=$(cd "$1" 2>/dev/null && pwd -P) || fail 3
op=$2; rel=$3; to=$4
inside() { p=$(cd "$root/$1" 2>/dev/null && pwd -P) || return 1; case "$p" in "$root"|"$root"/*) return 0 ;; esac; return 1; }
case "$op" in
arquivo)
  [ -e "$root/$rel" ] || [ -L "$root/$rel" ] && fail 5
  d=$(dirname -- "$rel"); mkdir -p -- "$root/$d" || exit 1; inside "$d" || fail 4
  : >"$root/$rel" ;;
pasta)
  [ -e "$root/$rel" ] || [ -L "$root/$rel" ] && fail 5
  mkdir -p -- "$root/$rel" || exit 1; inside "$rel" || fail 4 ;;
renomear)
  [ -e "$root/$rel" ] || [ -L "$root/$rel" ] || fail 6
  [ -e "$root/$to" ] || [ -L "$root/$to" ] && fail 5
  inside "$(dirname -- "$rel")" || fail 4
  d=$(dirname -- "$to"); mkdir -p -- "$root/$d" || exit 1; inside "$d" || fail 4
  mv -- "$root/$rel" "$root/$to" ;;
excluir)
  [ -e "$root/$rel" ] || [ -L "$root/$rel" ] || fail 6
  inside "$(dirname -- "$rel")" || fail 4
  rm -rf -- "$root/$rel" ;;
*) exit 2 ;;
esac`

// fileCheckScript says whether $2 is a text-sized regular file inside $1:
// 4 outside (a link), 6 not a file, 7 too big for the editor.
const fileCheckScript = `fail() { echo "gantry-arvore:$1"; exit "$1"; }
root=$(cd "$1" 2>/dev/null && pwd -P) || fail 3
p=$(cd "$root/$(dirname -- "$2")" 2>/dev/null && pwd -P) || fail 6
case "$p" in "$root"|"$root"/*) ;; *) fail 4 ;; esac
[ -f "$root/$2" ] || fail 6
[ $(wc -c <"$root/$2") -le 4194304 ] || fail 7`

// treeErrs are the messages of the exit codes of the scripts.
var treeErrs = map[string]string{
	"3": "a pasta do projeto não existe mais",
	"4": "o caminho sai da pasta do projeto (por um link)",
	"5": "já existe um arquivo ou pasta com esse nome",
	"6": "não existe mais: a árvore estava desatualizada",
	"7": "arquivo grande demais para o editor (mais de 4 MB)",
}

// runTree runs a tree script; a failure the script names ("gantry-arvore:N",
// the same here and over ssh) becomes its message.
func runTree(ctx context.Context, h *hosts.Host, script string, args ...string) (string, error) {
	out, err := h.Run(ctx, "", append([]string{"sh", "-c", script, "sh"}, args...), nil)
	if err == nil {
		return out, nil
	}
	text := out + "\n" + err.Error()
	if i := strings.LastIndex(text, "gantry-arvore:"); i >= 0 {
		if m, ok := treeErrs[text[i+len("gantry-arvore:"):i+len("gantry-arvore:")+1]]; ok {
			return out, errors.New(m)
		}
	}
	return out, err
}

// cleanRel checks a path given by the screen: relative, inside the folder.
func cleanRel(p string) (string, error) {
	p = strings.TrimSpace(p)
	c := path.Clean(strings.TrimPrefix(p, "./"))
	if p == "" || c == "." || c == ".." || path.IsAbs(c) || strings.HasPrefix(c, "../") || strings.ContainsAny(c, "\x00\n\r") {
		return "", errors.New("caminho fora da pasta do projeto")
	}
	return c, nil
}

type treeItem struct {
	Path string `json:"path"`
	Dir  bool   `json:"dir,omitempty"`
	// Name is what the editor asks for to open the file ("" the compose,
	// ".env", the other compose files by their full path, the rest by the
	// relative path).
	Name *string `json:"name,omitempty"`
}

type treeMissing struct {
	Path     string   `json:"path"`
	Dir      bool     `json:"dir"`
	Services []string `json:"services"`
}

// parseTree reads the output of treeScript, sorted folders first.
func parseTree(out string) (items []treeItem, cut bool) {
	for _, l := range strings.Split(out, "\n") {
		if len(l) < 3 || l[1] != ' ' {
			continue
		}
		rel := strings.TrimPrefix(l[2:], "./")
		if rel == "." || rel == "" {
			continue
		}
		if len(items) >= treeMax {
			cut = true
			break
		}
		items = append(items, treeItem{Path: rel, Dir: l[0] == 'd'})
	}
	sortTree(items)
	return items, cut
}

// sortTree orders like a file manager: in each folder, the folders first,
// then the files, each group by name without case.
func sortTree(items []treeItem) {
	key := func(it treeItem) string {
		parts := strings.Split(it.Path, "/")
		for i := range parts {
			parts[i] = strings.ToLower(parts[i])
		}
		// Folders of a level sort before its files: "0" before "1".
		last := len(parts) - 1
		if it.Dir {
			parts[last] = "0" + parts[last]
		} else {
			parts[last] = "1" + parts[last]
		}
		for i := 0; i < last; i++ {
			parts[i] = "0" + parts[i]
		}
		return strings.Join(parts, "\x00")
	}
	sort.SliceStable(items, func(i, j int) bool { return key(items[i]) < key(items[j]) })
}

// fileExts are the endings that make a missing bind a file, not a folder.
var fileExts = map[string]bool{
	"yml": true, "yaml": true, "json": true, "conf": true, "toml": true, "ini": true, "cfg": true, "env": true,
	"txt": true, "md": true, "sh": true, "html": true, "htm": true, "js": true, "css": true, "properties": true,
	"xml": true, "sql": true, "pem": true, "crt": true, "key": true, "lua": true, "py": true, "rb": true,
	"php": true, "nix": true, "cnf": true, "rules": true, "hcl": true, "tf": true, "csv": true, "log": true,
}

// bindIsFile guesses whether a bind source that does not exist yet is a
// file (prometheus.yml, nginx.conf) or a folder (data, conf.d).
func bindIsFile(rel string) bool {
	b := path.Base(rel)
	if strings.HasPrefix(b, ".") && !strings.Contains(b[1:], ".") {
		return true // .env, .htpasswd
	}
	if i := strings.LastIndex(b, "."); i > 0 {
		return fileExts[strings.ToLower(b[i+1:])]
	}
	return false
}

// missingBinds are the bind sources inside the folder that the compose
// mounts and that are not there yet: the configs still to write.
func missingBinds(names []string, binds func(string) []string, dir string, have map[string]bool) []treeMissing {
	byPath := map[string]*treeMissing{}
	var order []string
	for _, n := range names {
		for _, b := range binds(n) {
			if strings.HasPrefix(b, "~") || strings.Contains(b, "$") {
				continue
			}
			src := b
			if path.IsAbs(src) {
				if dir == "" {
					continue
				}
				if !strings.HasPrefix(path.Clean(src), strings.TrimSuffix(dir, "/")+"/") {
					continue
				}
				src = strings.TrimPrefix(path.Clean(src), strings.TrimSuffix(dir, "/")+"/")
			}
			rel, err := cleanRel(src)
			if err != nil || have[rel] {
				continue
			}
			m := byPath[rel]
			if m == nil {
				m = &treeMissing{Path: rel, Dir: !bindIsFile(rel)}
				byPath[rel] = m
				order = append(order, rel)
			}
			m.Services = append(m.Services, n)
		}
	}
	out := []treeMissing{}
	sort.Strings(order)
	for _, p := range order {
		out = append(out, *byPath[p])
	}
	return out
}

// labTree answers the tree of the project folder.
func (s *Server) labTree(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	if !h.Files() {
		writeErr(w, http.StatusBadRequest, errors.New("este host não dá acesso a arquivos; conecte via ssh://"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
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
	out, err := runTree(ctx, h, treeScript, dir)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	items, cut := parseTree(out)
	have := map[string]bool{}
	for i := range items {
		have[items[i].Path] = true
		if !items[i].Dir {
			n := editorName(dir, main, l, items[i].Path)
			items[i].Name = &n
		}
	}
	missing := []treeMissing{}
	files := l.Files
	if len(files) == 0 {
		files = []string{main}
	}
	if proj, _, err := s.loadProject(ctx, h, files); err == nil && !cut {
		missing = missingBinds(proj.Names, func(n string) []string { return proj.Services[n].Binds }, dir, have)
	}
	if items == nil {
		items = []treeItem{}
	}
	writeJSON(w, map[string]any{"root": dir, "items": items, "cut": cut, "missing": missing})
}

// editorName is how the editor asks for a file of the tree.
func editorName(dir, main string, l labs.Lab, rel string) string {
	full := path.Join(dir, rel)
	switch {
	case full == main:
		return ""
	case rel == ".env":
		return ".env"
	}
	for _, f := range l.Files {
		if f == full {
			return f
		}
	}
	return rel
}

// labTreeOp creates, renames or deletes a file or folder of the project.
func (s *Server) labTreeOp(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	if !h.Files() {
		writeErr(w, http.StatusBadRequest, errors.New("este host não dá acesso a arquivos"))
		return
	}
	var req struct {
		Op   string `json:"op"`
		Path string `json:"path"`
		To   string `json:"to"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
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
	rel, err := cleanRel(req.Path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	to := ""
	switch req.Op {
	case "arquivo", "pasta":
	case "renomear":
		if to, err = cleanRel(req.To); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if to == rel {
			writeJSON(w, map[string]any{"ok": true, "path": rel})
			return
		}
		fallthrough
	case "excluir":
		// The compose files are the project: renaming or deleting them
		// here would lose it. That is done by Esquecer, or outside.
		full := path.Join(dir, rel)
		for _, f := range append([]string{main}, l.Files...) {
			if f == full || strings.HasPrefix(f, full+"/") {
				writeErr(w, http.StatusBadRequest, errors.New("esse é o compose do projeto (ou a pasta dele): não dá para renomear nem excluir por aqui"))
				return
			}
		}
	default:
		writeErr(w, http.StatusBadRequest, errors.New("operação desconhecida"))
		return
	}
	if _, err := runTree(ctx, h, treeOpScript, dir, req.Op, rel, to); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	res := rel
	if to != "" {
		res = to
	}
	writeJSON(w, map[string]any{"ok": true, "path": res, "name": editorName(dir, main, l, res)})
}

// isBinary says whether the start of a file has a NUL byte (images,
// archives, databases): those are not for the text editor.
func isBinary(b []byte) bool {
	if len(b) > 8192 {
		b = b[:8192]
	}
	return bytes.IndexByte(b, 0) >= 0
}
