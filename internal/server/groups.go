package server

// Folders of the sidebar: machines (and, later, clusters) organized in
// folders and subfolders. A folder is only deleted when empty, so nothing
// disappears from the list by accident.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

type folder struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Parent string `json:"parent,omitempty"`
}

type groupsDoc struct {
	Folders []folder `json:"folders"`
	// Items: item key (machine key, cluster:<id>) -> folder id.
	Items map[string]string `json:"items"`
	// Seeded: the first folder was created already (the user may delete it).
	Seeded bool `json:"seeded,omitempty"`
}

var groupsMu sync.Mutex

func (s *Server) groupsFile() string { return filepath.Join(s.configDir(), "grupos.json") }

func (s *Server) loadGroups() groupsDoc {
	d := groupsDoc{Items: map[string]string{}}
	if b, err := os.ReadFile(s.groupsFile()); err == nil {
		_ = json.Unmarshal(b, &d)
	}
	if d.Items == nil {
		d.Items = map[string]string{}
	}
	if d.Folders == nil {
		d.Folders = []folder{}
	}
	if !d.Seeded {
		// A first folder for this computer; renamed or deleted at will.
		d.Folders = append(d.Folders, folder{ID: "localhost", Name: "Localhost"})
		if _, ok := d.Items["local"]; !ok {
			d.Items["local"] = "localhost"
		}
		d.Seeded = true
		_ = s.saveGroups(d)
	}
	return d
}

func (s *Server) saveGroups(d groupsDoc) error {
	sort.SliceStable(d.Folders, func(i, j int) bool {
		return strings.ToLower(d.Folders[i].Name) < strings.ToLower(d.Folders[j].Name)
	})
	b, _ := json.MarshalIndent(d, "", "  ")
	if err := os.MkdirAll(s.configDir(), 0o700); err != nil {
		return err
	}
	return os.WriteFile(s.groupsFile(), b, 0o600)
}

func (d groupsDoc) find(id string) int {
	for i, f := range d.Folders {
		if f.ID == id {
			return i
		}
	}
	return -1
}

// under says whether folder id is inside (or is) ancestor.
func (d groupsDoc) under(id, ancestor string) bool {
	for seen := 0; id != "" && seen < 100; seen++ {
		if id == ancestor {
			return true
		}
		i := d.find(id)
		if i < 0 {
			return false
		}
		id = d.Folders[i].Parent
	}
	return false
}

func folderName(n string) (string, error) {
	n = strings.TrimSpace(n)
	if n == "" || len(n) > 60 || strings.ContainsAny(n, "/\\\n") {
		return "", errors.New("nome da pasta: até 60 caracteres, sem / ou \\")
	}
	return n, nil
}

func (s *Server) getGroups(w http.ResponseWriter, r *http.Request) {
	groupsMu.Lock()
	d := s.loadGroups()
	groupsMu.Unlock()
	writeJSON(w, d)
}

func (s *Server) createFolder(w http.ResponseWriter, r *http.Request) {
	var req struct{ Name, Parent string }
	_ = json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req)
	name, err := folderName(req.Name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	groupsMu.Lock()
	defer groupsMu.Unlock()
	d := s.loadGroups()
	if req.Parent != "" && d.find(req.Parent) < 0 {
		writeErr(w, http.StatusBadRequest, errors.New("a pasta de cima não existe"))
		return
	}
	for _, f := range d.Folders {
		if f.Parent == req.Parent && strings.EqualFold(f.Name, name) {
			writeErr(w, http.StatusConflict, errors.New("já existe uma pasta com esse nome aqui"))
			return
		}
	}
	f := folder{ID: randomID(8), Name: name, Parent: req.Parent}
	d.Folders = append(d.Folders, f)
	if err := s.saveGroups(d); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, f)
}

func (s *Server) editFolder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   *string `json:"name"`
		Parent *string `json:"parent"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req)
	groupsMu.Lock()
	defer groupsMu.Unlock()
	d := s.loadGroups()
	i := d.find(r.PathValue("id"))
	if i < 0 {
		writeErr(w, http.StatusNotFound, errors.New("pasta não encontrada"))
		return
	}
	f := d.Folders[i]
	if req.Name != nil {
		n, err := folderName(*req.Name)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		f.Name = n
	}
	if req.Parent != nil {
		p := *req.Parent
		if p != "" && (d.find(p) < 0 || d.under(p, f.ID)) {
			writeErr(w, http.StatusBadRequest, errors.New("uma pasta não pode ir para dentro dela mesma"))
			return
		}
		f.Parent = p
	}
	for j, o := range d.Folders {
		if j != i && o.Parent == f.Parent && strings.EqualFold(o.Name, f.Name) {
			writeErr(w, http.StatusConflict, errors.New("já existe uma pasta com esse nome aqui"))
			return
		}
	}
	d.Folders[i] = f
	if err := s.saveGroups(d); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, f)
}

// deleteFolder only removes empty folders: no items, no subfolders.
func (s *Server) deleteFolder(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	groupsMu.Lock()
	defer groupsMu.Unlock()
	d := s.loadGroups()
	i := d.find(id)
	if i < 0 {
		writeErr(w, http.StatusNotFound, errors.New("pasta não encontrada"))
		return
	}
	items, subs := 0, 0
	for _, f := range d.Items {
		if f == id {
			items++
		}
	}
	for _, f := range d.Folders {
		if f.Parent == id {
			subs++
		}
	}
	if items > 0 || subs > 0 {
		writeErr(w, http.StatusConflict, errors.New("a pasta não está vazia: mova ou remova o que está dentro dela antes"))
		return
	}
	d.Folders = append(d.Folders[:i], d.Folders[i+1:]...)
	if err := s.saveGroups(d); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// moveItem puts a machine (or cluster) in a folder; "" is the top level.
func (s *Server) moveItem(w http.ResponseWriter, r *http.Request) {
	var req struct{ Item, Folder string }
	_ = json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req)
	if strings.TrimSpace(req.Item) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("item vazio"))
		return
	}
	groupsMu.Lock()
	defer groupsMu.Unlock()
	d := s.loadGroups()
	if req.Folder != "" && d.find(req.Folder) < 0 {
		writeErr(w, http.StatusBadRequest, errors.New("pasta não encontrada"))
		return
	}
	if req.Folder == "" {
		delete(d.Items, req.Item)
	} else {
		d.Items[req.Item] = req.Folder
	}
	if err := s.saveGroups(d); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// forgetItem drops an item that left the app (a removed host).
func (s *Server) forgetItem(key string) {
	groupsMu.Lock()
	defer groupsMu.Unlock()
	d := s.loadGroups()
	if _, ok := d.Items[key]; ok {
		delete(d.Items, key)
		_ = s.saveGroups(d)
	}
}

/* credentials kept for this session */

// sshPasswords lists the hosts whose SSH password or passphrase the app
// remembers while open (never written to disk).
func (s *Server) sshPasswords(w http.ResponseWriter, r *http.Request) {
	s.prompts.mu.Lock()
	seen := map[string]bool{}
	for k := range s.prompts.cache {
		host, _, _ := strings.Cut(k, "\x00")
		seen[host] = true
	}
	s.prompts.mu.Unlock()
	out := []string{}
	for h := range seen {
		out = append(out, h)
	}
	sort.Strings(out)
	writeJSON(w, map[string]any{"hosts": out, "store": s.secretStore().Name()})
}

func (s *Server) forgetSSHPassword(w http.ResponseWriter, r *http.Request) {
	s.prompts.forget(r.PathValue("host"))
	writeJSON(w, map[string]bool{"ok": true})
}
