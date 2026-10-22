package server

// The images of the VM catalog that Gantry does not download (the NixOS
// base it builds): where their file is, per machine, in the settings.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"

	"gantry/internal/hosts"
	"gantry/internal/vms"
)

type vmImageInfo struct {
	vms.LocalImage
	Path string `json:"path"`
}

func (s *Server) listVMImages(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	set := s.cfg.VMImagesOf(h.Machine())
	found, _ := s.scanImages(r.Context(), h, false)
	out := []vmImageInfo{}
	for _, im := range vms.LocalImages {
		p := set[im.Name]
		if p == "" {
			p = pickCatalog(found, im.Name) // found in the image folders
		}
		out = append(out, vmImageInfo{im, p})
	}
	writeJSON(w, out)
}

func (s *Server) setVMImage(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if !vms.IsLocalImage(req.Name) {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("imagem desconhecida: %s", req.Name))
		return
	}
	p := strings.TrimSpace(req.Path)
	if p != "" {
		if !path.IsAbs(p) && !strings.HasPrefix(p, "~/") {
			writeErr(w, http.StatusBadRequest, errors.New("informe o caminho completo do arquivo .qcow2"))
			return
		}
		if !strings.HasPrefix(p, "~/") && !h.Exists(r.Context(), p) {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("o arquivo %s não existe em %s", p, h.Name))
			return
		}
	}
	if err := s.cfg.SetVMImage(h.Machine(), req.Name, p); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
