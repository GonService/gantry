package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"gantry/internal/hosts"
)

// The folders of a machine, for the folder picker inside the app: the
// native dialog only sees this computer, and a server over SSH (or a Linux
// without zenity) had only a text field.

type folderList struct {
	Path     string   `json:"path"`     // the folder shown (absolute)
	Home     string   `json:"home"`     // the user's home on the machine
	Parent   string   `json:"parent"`   // "" at the root
	Writable bool     `json:"writable"` // a folder can be created here
	Dirs     []string `json:"dirs"`     // the folders in it, hidden ones too
	// Asked is the path that was asked for, when it does not exist yet and
	// the list is of the nearest folder that does.
	Asked string `json:"asked,omitempty"`
}

// folderScript lists a folder: "~" is the home; a path that does not exist
// yet shows its nearest existing parent.
const folderScript = `p=$1
case "$p" in "" | "~") p=$HOME ;; "~/"*) p=$HOME/${p#"~/"} ;; esac
asked=$p
while [ ! -d "$p" ] && [ "$p" != / ] && [ -n "$p" ]; do p=$(dirname "$p"); done
cd "$p" 2>/dev/null || { echo "ERR sem acesso a $p"; exit 0; }
printf 'PWD %s\nHOME %s\n' "$(pwd -P)" "$HOME"
[ "$asked" != "$p" ] && printf 'ASKED %s\n' "$asked"
[ -w . ] && echo W
for d in .[!.]* ..?* *; do
  [ -d "$d" ] && printf 'D %s\n' "$d"
done
exit 0`

func listFolder(ctx context.Context, h *hosts.Host, path string) (folderList, error) {
	out, err := h.Run(ctx, "", []string{"sh", "-c", folderScript, "sh", path}, nil)
	if err != nil {
		return folderList{}, err
	}
	l := folderList{Dirs: []string{}}
	for _, line := range strings.Split(out, "\n") {
		k, v, _ := strings.Cut(line, " ")
		switch k {
		case "ERR":
			return folderList{}, errors.New(v)
		case "PWD":
			l.Path = v
		case "HOME":
			l.Home = v
		case "ASKED":
			l.Asked = v
		case "W":
			l.Writable = true
		case "D":
			if v != "" {
				l.Dirs = append(l.Dirs, v)
			}
		}
	}
	if l.Path == "" {
		return folderList{}, errors.New("a máquina não respondeu à listagem de pastas")
	}
	if l.Path != "/" {
		i := strings.LastIndex(l.Path, "/")
		l.Parent = l.Path[:i]
		if l.Parent == "" {
			l.Parent = "/"
		}
	}
	return l, nil
}

// hostFolders lists a folder of the machine (?path=, "~" by default).
func (s *Server) hostFolders(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	if !h.Files() {
		writeErr(w, http.StatusBadRequest, errors.New("indisponível para este host"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	l, err := listFolder(ctx, h, r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, l)
}

// hostMkdir creates a folder on the machine and lists it.
func (s *Server) hostMkdir(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	if !h.Files() {
		writeErr(w, http.StatusBadRequest, errors.New("indisponível para este host"))
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&req); err != nil || strings.TrimSpace(req.Path) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("informe a pasta"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	script := `p=$1; case "$p" in "~/"*) p=$HOME/${p#"~/"} ;; esac; mkdir -p -- "$p"`
	if out, err := h.Run(ctx, "", []string{"sh", "-c", script, "sh", req.Path}, nil); err != nil {
		msg := strings.TrimSpace(out)
		if msg == "" {
			msg = err.Error()
		}
		writeErr(w, http.StatusBadGateway, errors.New("não consegui criar a pasta: "+msg))
		return
	}
	l, err := listFolder(ctx, h, req.Path)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, l)
}
