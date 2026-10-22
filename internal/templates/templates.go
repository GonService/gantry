// Package templates keeps the compose files projects are created from: the
// ones that come with the app and the user's own, like Portainer's custom
// templates.
package templates

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed builtin
var builtinFS embed.FS

// Template is a compose file to create projects from.
type Template struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Category    string `json:"category,omitempty"`
	Description string `json:"description,omitempty"`
	YAML        string `json:"yaml"`
	// Files are written next to the compose file when a project is made
	// (configs, dashboards…), by path relative to the project folder.
	Files   map[string]string `json:"files,omitempty"`
	Builtin bool              `json:"builtin,omitempty"`
	Updated int64             `json:"updated,omitempty"`
}

// Store keeps the user's templates in a JSON file.
type Store struct {
	mu   sync.Mutex
	path string
	own  []Template
}

func Open(path string) *Store {
	s := &Store{path: path}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &s.own)
	}
	return s
}

// header reads "# nome:", "# categoria:" and "# descrição:" from the top
// comments of a built-in template and returns the YAML without them.
func header(src string) (name, cat, desc, body string) {
	lines := strings.SplitAfter(src, "\n")
	i := 0
	for ; i < len(lines); i++ {
		l := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(l, "#") {
			break
		}
		k, v, ok := strings.Cut(strings.TrimSpace(strings.TrimPrefix(l, "#")), ":")
		if !ok {
			break
		}
		switch strings.TrimSpace(k) {
		case "nome":
			name = strings.TrimSpace(v)
		case "categoria":
			cat = strings.TrimSpace(v)
		case "descrição":
			desc = strings.TrimSpace(v)
		default:
			return name, cat, desc, strings.Join(lines[i:], "")
		}
	}
	return name, cat, desc, strings.Join(lines[i:], "")
}

// Builtin lists the templates that come with the app.
func Builtin() []Template {
	ents, _ := builtinFS.ReadDir("builtin")
	var out []Template
	for _, e := range ents {
		id := strings.TrimSuffix(e.Name(), ".yaml")
		if i := strings.IndexByte(id, '-'); i > 0 {
			id = id[i+1:]
		}
		t := Template{ID: "gantry-" + id, Builtin: true}
		var src []byte
		var err error
		if e.IsDir() {
			// A folder: compose.yaml plus the files it uses.
			dir := path.Join("builtin", e.Name())
			if src, err = builtinFS.ReadFile(path.Join(dir, "compose.yaml")); err != nil {
				continue
			}
			t.Files = map[string]string{}
			_ = fs.WalkDir(builtinFS, dir, func(p string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() || p == path.Join(dir, "compose.yaml") {
					return nil
				}
				if b, err := builtinFS.ReadFile(p); err == nil {
					t.Files[strings.TrimPrefix(p, dir+"/")] = string(b)
				}
				return nil
			})
		} else if src, err = builtinFS.ReadFile(path.Join("builtin", e.Name())); err != nil {
			continue
		}
		t.Name, t.Category, t.Description, t.YAML = header(string(src))
		out = append(out, t)
	}
	return out
}

// List returns the user's templates (newest first) and then the built-in ones.
func (s *Store) List() []Template {
	s.mu.Lock()
	own := append([]Template(nil), s.own...)
	s.mu.Unlock()
	sort.SliceStable(own, func(i, j int) bool { return own[i].Updated > own[j].Updated })
	return append(own, Builtin()...)
}

func (s *Store) Get(id string) (Template, bool) {
	for _, t := range s.List() {
		if t.ID == id {
			return t, true
		}
	}
	return Template{}, false
}

var ErrBuiltin = errors.New("os modelos que vêm com o Gantry não podem ser alterados; duplique para editar")

// Save creates (empty ID) or updates one of the user's templates.
func (s *Store) Save(t Template) (Template, error) {
	t.Name = strings.TrimSpace(t.Name)
	if t.Name == "" {
		return t, errors.New("dê um nome ao modelo")
	}
	if strings.HasPrefix(t.ID, "gantry-") {
		return t, ErrBuiltin
	}
	t.Builtin = false
	t.Updated = time.Now().Unix()
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.ID == "" {
		b := make([]byte, 5)
		_, _ = rand.Read(b)
		t.ID = hex.EncodeToString(b)
		s.own = append(s.own, t)
	} else {
		found := false
		for i := range s.own {
			if s.own[i].ID == t.ID {
				s.own[i], found = t, true
			}
		}
		if !found {
			return t, errors.New("modelo não encontrado")
		}
	}
	return t, s.save()
}

func (s *Store) Delete(id string) error {
	if strings.HasPrefix(id, "gantry-") {
		return ErrBuiltin
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.own {
		if s.own[i].ID == id {
			s.own = append(s.own[:i], s.own[i+1:]...)
			return s.save()
		}
	}
	return errors.New("modelo não encontrado")
}

func (s *Store) save() error {
	_ = os.MkdirAll(filepath.Dir(s.path), 0o700)
	b, err := json.MarshalIndent(s.own, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
