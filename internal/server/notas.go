package server

// The notes on the drawing of a lab (x-notas): text, tables and images the
// teacher put on the topology in PNETLab or EVE-NG, or written here.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"gantry/internal/composex"
	"gantry/internal/hosts"
)

var topKey = regexp.MustCompile(`^[^\s#]`)

// setTopBlock puts block in place of the top-level key (x-notas or
// x-notes) of a compose text, appends it when absent, removes it when
// block is "". A comment right above the key goes with it.
func setTopBlock(text string, keys []string, block string) string {
	lines := strings.Split(text, "\n")
	start := -1
	for i, l := range lines {
		for _, k := range keys {
			if strings.HasPrefix(l, k+":") {
				start = i
			}
		}
	}
	if start < 0 {
		if block == "" {
			return text
		}
		return strings.TrimRight(text, "\n") + "\n\n" + block
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if topKey.MatchString(lines[i]) {
			end = i
			break
		}
	}
	// the comment lines right above the block, and the ones at its end that
	// belong to what follows, stay where they belong
	for end > start+1 && strings.HasPrefix(lines[end-1], "#") {
		end--
	}
	for start > 0 && strings.HasPrefix(lines[start-1], "#") {
		start--
	}
	out := append([]string{}, lines[:start]...)
	if block != "" {
		out = append(out, strings.Split(strings.TrimRight(block, "\n"), "\n")...)
	}
	rest := lines[end:]
	if block == "" {
		// no blank line doubled where the block was
		for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" && len(rest) > 0 && strings.TrimSpace(rest[0]) == "" {
			rest = rest[1:]
		}
	}
	return strings.Join(append(out, rest...), "\n")
}

// saveNotas writes the notes of the drawing (all of them, as the page has
// them) in the compose.
func (s *Server) saveNotas(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		Notas []*composex.Nota `json:"notas"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	seen := map[string]bool{}
	for _, n := range req.Notas {
		if n == nil || !packNode.MatchString(n.ID) || seen[n.ID] {
			writeErr(w, http.StatusBadRequest, errors.New("nota com nome inválido ou repetido"))
			return
		}
		seen[n.ID] = true
		if !composex.NotaCores[n.Cor] || (n.Pos != nil && len(n.Pos) != 2) || (n.Size != nil && len(n.Size) != 2) {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("nota %s: cor, posição ou tamanho inválido", n.ID))
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	resp, err := s.editLab(ctx, h, r.PathValue("lab"), false, func(p *composex.Project, files []string, texts map[string]string) error {
		// the file that has the notes, else the main one
		target := files[0]
		for _, f := range files {
			for _, l := range strings.Split(texts[f], "\n") {
				if strings.HasPrefix(l, "x-notas:") || strings.HasPrefix(l, "x-notes:") {
					target = f
				}
			}
		}
		for _, f := range files {
			if f != target {
				texts[f] = setTopBlock(texts[f], []string{"x-notas", "x-notes"}, "")
			}
		}
		texts[target] = setTopBlock(texts[target], []string{"x-notas", "x-notes"}, composex.NotasYAML(req.Notas))
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, resp)
}

// notaImage serves an image of the project folder for a note.
func (s *Server) notaImage(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	rel := path.Clean("/" + r.URL.Query().Get("path"))[1:]
	ext := strings.ToLower(path.Ext(rel))
	types := map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp", ".svg": "image/svg+xml"}
	if rel == "" || strings.HasPrefix(rel, "..") || types[ext] == "" {
		writeErr(w, http.StatusBadRequest, errors.New("imagem inválida"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	l, err := s.findLab(ctx, h, r.PathValue("lab"))
	if err != nil || l.WorkingDir == "" {
		writeErr(w, http.StatusNotFound, errors.New("projeto não encontrado"))
		return
	}
	b, err := h.ReadFile(ctx, path.Join(l.WorkingDir, rel))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	w.Header().Set("Content-Type", types[ext])
	// an SVG drawn as an image runs no script, but stays in a sandbox anyway
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	w.Header().Set("Cache-Control", "private, max-age=60")
	_, _ = w.Write(b)
}

// notasOf: the notes of a project, never nil.
func notasOf(p *composex.Project) []*composex.Nota {
	if p.Notas == nil {
		return []*composex.Nota{}
	}
	return p.Notas
}
