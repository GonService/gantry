package server

// The image folders of a machine: Gantry looks in them for the images it
// does not download (qcow2 of the appliances, ISOs, the .bin of IOL and
// Dynamips, the iourc) and tells what each file is by its name, so the
// catalog images need no path one by one, and image: can name a file of
// the folders directly. The folders are set on the machine's page; with
// none set, ~/gantry/imagens and the EVE-NG/PNetLab layout are used.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"gantry/internal/composex"
	"gantry/internal/config"
	"gantry/internal/hosts"
	"gantry/internal/vms"
)

// foundImage is a file of the image folders.
type foundImage struct {
	Path    string `json:"path"`
	Name    string `json:"name"`              // what image: takes (the file name; the folder's, in the EVE-NG layout)
	Catalog string `json:"catalog,omitempty"` // the catalog image it is, by its name
	Type    string `json:"type"`              // qcow2, iso, bin…
}

type imageScan struct {
	at    time.Time
	dirs  string
	found []foundImage
	err   error
}

var (
	scanMu    sync.Mutex
	scanCache = map[string]imageScan{}
)

// eveDisk: the disks of EVE-NG/PNetLab are named by the slot
// (qemu/<image>/virtioa.qcow2): the image is the folder.
func eveDisk(base string) bool {
	b := strings.ToLower(base)
	for _, p := range []string{"virtio", "hd", "sata", "scsi", "cdrom"} {
		if strings.HasPrefix(b, p) && (strings.HasSuffix(b, ".qcow2") || strings.HasSuffix(b, ".iso")) {
			return true
		}
	}
	return false
}

// classifyFound turns the paths found into images.
func classifyFound(paths []string) []foundImage {
	var out []foundImage
	seen := map[string]bool{}
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		base := path.Base(p)
		name := base
		if eveDisk(base) {
			name = path.Base(path.Dir(p))
		}
		typ := strings.TrimPrefix(strings.ToLower(path.Ext(base)), ".")
		if strings.HasPrefix(strings.ToLower(base), "iourc") || base == ".iourc" {
			typ = "iourc"
		}
		cat := composex.ClassifyImage(p)
		if eveDisk(base) {
			cat = composex.ClassifyImage(name)
		}
		out = append(out, foundImage{Path: p, Name: name, Catalog: cat, Type: typ})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

const scanScript = `for d in "$@"; do
  case "$d" in "~") d=$HOME ;; "~/"*) d=$HOME/${d#"~/"} ;; esac
  [ -d "$d" ] || continue
  find "$d" -maxdepth 5 -type f \( -iname '*.qcow2' -o -iname '*.img' -o -iname '*.bin' -o -iname '*.image' -o -iname '*.iso' -o -iname '*.vmdk' -o -iname '*.iol' -o -iname 'iourc*' -o -name '.iourc' \) 2>/dev/null | head -n 2000
done`

// scanImages lists the image folders of the machine (cached for a minute;
// fresh forces a new look).
func (s *Server) scanImages(ctx context.Context, h *hosts.Host, fresh bool) ([]foundImage, error) {
	dirs := s.cfg.ImageDirsOf(h.Machine())
	key := strings.Join(dirs, "\x00")
	scanMu.Lock()
	c, ok := scanCache[h.Machine()]
	scanMu.Unlock()
	if ok && !fresh && c.dirs == key && time.Since(c.at) < time.Minute {
		return c.found, c.err
	}
	if len(dirs) == 0 {
		return nil, nil
	}
	argv := append([]string{"sh", "-c", scanScript, "scan"}, dirs...)
	out, err := h.Run(ctx, "", argv, nil)
	found := classifyFound(strings.Split(out, "\n"))
	if err != nil && len(found) > 0 {
		err = nil
	}
	scanMu.Lock()
	scanCache[h.Machine()] = imageScan{time.Now(), key, found, err}
	scanMu.Unlock()
	return found, err
}

// pickCatalog is the file of the folders that is a catalog image (the last
// by name: usually the newest version).
func pickCatalog(found []foundImage, name string) string {
	p := ""
	for _, f := range found {
		if f.Catalog == name {
			p = f.Path
		}
	}
	return p
}

func pickName(found []foundImage, name string) *foundImage {
	for i := range found {
		if found[i].Name == name {
			return &found[i]
		}
	}
	return nil
}

// resolveImages fills where the images of the project's VMs are on the
// machine: the path set for a catalog image, else the folders.
func (s *Server) resolveImages(ctx context.Context, h *hosts.Host, p *composex.Project) {
	var found []foundImage
	looked := false
	look := func() []foundImage {
		if !looked {
			looked = true
			found, _ = s.scanImages(ctx, h, false)
		}
		return found
	}
	for _, v := range p.VMs {
		switch {
		case vms.IsLocalImage(v.Image):
			v.ImageSrc = s.cfg.VMImage(h.Machine(), v.Image)
			if v.ImageSrc == "" {
				v.ImageSrc = pickCatalog(look(), v.Image)
			}
		case composex.IsImageFileRef(v.Image):
			if f := pickName(look(), v.Image); f != nil {
				v.ImageSrc = f.Path
			}
		}
		if composex.IsImageFileRef(v.ISO) {
			if f := pickName(look(), v.ISO); f != nil {
				v.ISO = f.Path
			}
		}
		if v.Kind == "iol" {
			v.IOLLicense = s.cfg.VMImage(h.Machine(), vms.IOLLicenseName)
			if v.IOLLicense == "" {
				v.IOLLicense = pickCatalog(look(), vms.IOLLicenseName)
			}
		}
	}
}

type catalogImage struct {
	vms.LocalImage
	Path string `json:"path"`           // set by hand
	Auto string `json:"auto,omitempty"` // found in the folders
}

// listImages: the folders of the machine, what is in them and which
// catalog images it has.
func (s *Server) listImages(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	found, err := s.scanImages(ctx, h, r.URL.Query().Get("novo") == "1")
	set := s.cfg.VMImagesOf(h.Machine())
	out := struct {
		Dirs     []string       `json:"dirs"`
		Defaults []string       `json:"defaults"`
		Found    []foundImage   `json:"found"`
		Catalog  []catalogImage `json:"catalog"`
		Error    string         `json:"error,omitempty"`
		// Needs: programs the machine lacks to run the images found
		// (Dynamips for a 7200 image…).
		Needs []progNeed `json:"needs"`
	}{Dirs: s.cfg.ImageDirsOf(h.Machine()), Defaults: config.DefaultImageDirs, Found: found, Catalog: []catalogImage{}}
	if out.Found == nil {
		out.Found = []foundImage{}
	}
	if err != nil {
		out.Error = err.Error()
	}
	for _, im := range vms.LocalImages {
		out.Catalog = append(out.Catalog, catalogImage{im, set[im.Name], pickCatalog(found, im.Name)})
	}
	kinds := map[string]bool{}
	for _, f := range found {
		if prof, ok := composex.Appliances[f.Catalog]; ok && prof.Kind != "" {
			kinds[prof.Kind] = true
		}
	}
	out.Needs = programsMissing(ctx, h, programsFor(kinds)...)
	if out.Needs == nil {
		out.Needs = []progNeed{}
	}
	writeJSON(w, out)
}

// setImageDirs stores the image folders of the machine.
func (s *Server) setImageDirs(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		Dirs []string `json:"dirs"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var dirs []string
	if req.Dirs != nil {
		dirs = []string{}
		for _, d := range req.Dirs {
			d = strings.TrimRight(strings.TrimSpace(d), "/")
			if d == "" {
				continue
			}
			if !path.IsAbs(d) && d != "~" && !strings.HasPrefix(d, "~/") {
				writeErr(w, http.StatusBadRequest, errors.New("use o caminho completo da pasta (/srv/imagens ou ~/imagens): "+d))
				return
			}
			dirs = append(dirs, d)
		}
	}
	if err := s.cfg.SetImageDirs(h.Machine(), dirs); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	scanMu.Lock()
	delete(scanCache, h.Machine())
	scanMu.Unlock()
	writeJSON(w, map[string]any{"ok": true})
}
