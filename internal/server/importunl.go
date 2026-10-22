package server

import (
	"context"
	"io"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"gantry/internal/hosts"
	"gantry/internal/unl"
)

// importUNL makes a project of a PNETLab/EVE-NG lab (.unl, the body): the
// same equipment, adapters, cables and saved configurations, in a new
// folder of the host. What Gantry does not run stops it (blocking); the
// images missing on the machine are said, and the lab is not brought up
// without them.
func (s *Server) importUNL(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	q := r.URL.Query()
	data, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	l, err := unl.Parse(data)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	file := path.Base(strings.TrimSpace(q.Get("file")))
	if file == "." || file == "/" || file == "" {
		file = "lab.unl"
	}
	res := unl.Convert(l, data, file)
	if len(res.Blocking) > 0 {
		writeJSON(w, map[string]any{"ok": false, "blocking": res.Blocking,
			"error": "o lab tem equipamento que o Gantry ainda não roda: " + strings.Join(res.Blocking, "; ")})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Minute)
	defer cancel()
	missing := s.missingImages(ctx, h, res.Images)
	progs := programsMissing(ctx, h, programsFor(res.Kinds)...)
	dir := strings.TrimSpace(q.Get("dir"))
	if dir == "" {
		dir = "~/labs/" + res.Name
	}
	up := q.Get("up") == "1" && len(missing) == 0 && len(progs) == 0
	resp, code, err := s.doCreateLab(ctx, h, createReq{Name: res.Name, Dir: dir, Compose: res.Compose, Files: res.Files, Up: up, Job: up})
	if err != nil {
		writeErr(w, code, err)
		return
	}
	resp["id"], resp["notes"], resp["missing"], resp["programs"] = res.Name, res.Notes, missing, progs
	resp["remarks"] = res.Remarks
	if q.Get("up") == "1" && (len(missing) > 0 || len(progs) > 0) {
		resp["notUp"] = "faltam imagens ou programas nesta máquina; depois de pôr o que falta, use Subir"
	}
	writeJSON(w, resp)
}

// missingImages: the image files the lab uses that the machine's image
// folders do not have.
func (s *Server) missingImages(ctx context.Context, h *hosts.Host, need map[string][]string) []string {
	found, err := s.scanImages(ctx, h, true)
	if err != nil {
		return nil // no way to look: compose says when it goes up
	}
	seen := map[string]bool{}
	var out []string
	for _, list := range need {
		for _, img := range list {
			if seen[img] {
				continue
			}
			seen[img] = true
			if pickName(found, img) == nil {
				out = append(out, img)
			}
		}
	}
	sort.Strings(out)
	return out
}
