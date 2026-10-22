package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The configuration folder (config.json, clusters.json, grupos.json, the
// kubeconfigs in clusters/…) has a format version in formato.json. When the
// app opens a folder written by an older version, it copies the folder to
// copias/ and runs the migrations in order; a folder written by a newer
// version is left alone (opening an older app does not undo anything).
// Besides, once a day the app keeps a copy of the folder (the last 7).

// Format is the version of the folder this build writes.
var Format = 1

// setFormat changes Format for a test.
func setFormat(v int) func() {
	old := Format
	Format = v
	return func() { Format = old }
}

// migrations[i] takes the folder from version i+1 to i+2.
var migrations = []func(dir string) error{}

const (
	formatFile = "formato.json"
	copiesDir  = "copias"
	keepDaily  = 7
	keepBefore = 5 // copies made before migrations
)

type formatInfo struct {
	Version int    `json:"versao"`
	App     string `json:"app,omitempty"`
	At      string `json:"em,omitempty"`
}

// Prepared tells what Prepare did, for the log and the diagnostic.
type Prepared struct {
	Found    int    `json:"encontrado"` // 0: a folder from before formato.json
	Format   int    `json:"formato"`
	Newer    bool   `json:"maisNovo,omitempty"` // written by a newer Gantry
	Copy     string `json:"copia,omitempty"`    // copy made now
	Migrated bool   `json:"migrado,omitempty"`
}

var prepared Prepared

// LastPrepared is what the last Prepare did.
func LastPrepared() Prepared { return prepared }

// Prepare checks the format of the folder, migrates it (after a copy) and
// keeps the daily copy. It runs before anything reads the folder.
func Prepare(dir, app string) (Prepared, error) {
	p := Prepared{Format: Format}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return p, err
	}
	var fi formatInfo
	b, readErr := os.ReadFile(filepath.Join(dir, formatFile))
	switch err := readErr; {
	case err == nil:
		if err := json.Unmarshal(b, &fi); err != nil {
			return p, fmt.Errorf("%s ilegível: %w", formatFile, err)
		}
	case errors.Is(err, os.ErrNotExist):
		// Before formato.json: the format of version 1, or a new folder.
		fi.Version = 1
		if !hasData(dir) {
			fi.Version = Format
		}
	default:
		return p, err
	}
	p.Found = fi.Version
	if fi.Version > Format {
		p.Newer = true
		slog.Warn("pasta de configuração de um Gantry mais novo; nada é migrado", "pasta", dir, "formato", fi.Version, "app", fi.App)
		prepared = p
		return p, nil
	}
	if fi.Version < Format {
		cp, err := copyFolder(dir, fmt.Sprintf("antes-do-formato-%d-%s", Format, stamp()))
		if err != nil {
			return p, fmt.Errorf("cópia antes de migrar: %w", err)
		}
		p.Copy = cp
		for v := fi.Version; v < Format; v++ {
			if err := migrations[v-1](dir); err != nil {
				return p, fmt.Errorf("migração do formato %d para %d: %w (a cópia está em %s)", v, v+1, err, cp)
			}
			slog.Info("configuração migrada", "de", v, "para", v+1)
		}
		p.Migrated = true
		prune(dir, "antes-do-formato-", keepBefore)
	}
	if p.Migrated || readErr != nil || fi.App != app {
		_ = writeFormat(dir, app)
	}
	if cp, err := dailyCopy(dir); err != nil {
		slog.Warn("cópia diária da configuração", "erro", err)
	} else if cp != "" && p.Copy == "" {
		p.Copy = cp
	}
	prepared = p
	return p, nil
}

func writeFormat(dir, app string) error {
	b, _ := json.MarshalIndent(formatInfo{Version: Format, App: app, At: time.Now().Format(time.RFC3339)}, "", "  ")
	tmp := filepath.Join(dir, formatFile+".tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, formatFile))
}

func stamp() string { return time.Now().Format("20060102-150405") }

func hasData(dir string) bool {
	es, _ := os.ReadDir(dir)
	for _, e := range es {
		if (strings.HasSuffix(e.Name(), ".json") && e.Name() != formatFile) || e.Name() == "clusters" {
			return true
		}
	}
	return false
}

// dailyCopy keeps one copy per day (the last keepDaily); "" when today's
// copy already exists or there is nothing to copy.
func dailyCopy(dir string) (string, error) {
	if !hasData(dir) {
		return "", nil
	}
	day := "diaria-" + time.Now().Format("20060102")
	if _, err := os.Stat(filepath.Join(dir, copiesDir, day)); err == nil {
		return "", nil
	}
	cp, err := copyFolder(dir, day)
	if err != nil {
		return "", err
	}
	prune(dir, "diaria-", keepDaily)
	return cp, nil
}

// Copies lists the copies of the folder, newest first.
func Copies(dir string) []string {
	es, _ := os.ReadDir(filepath.Join(dir, copiesDir))
	var out []string
	for _, e := range es {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}

func prune(dir, prefix string, keep int) {
	var mine []string
	for _, c := range Copies(dir) {
		if strings.HasPrefix(c, prefix) {
			mine = append(mine, c)
		}
	}
	for i, c := range mine {
		if i >= keep {
			_ = os.RemoveAll(filepath.Join(dir, copiesDir, c))
		}
	}
}

// copyFolder copies the settings (*.json at the top and the clusters/
// folder) to copias/name. Logs, the temporary files and the copies
// themselves stay out.
func copyFolder(dir, name string) (string, error) {
	dst := filepath.Join(dir, copiesDir, name)
	tmp := dst + ".parcial"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return "", err
	}
	es, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, e := range es {
		n := e.Name()
		switch {
		case e.Type().IsRegular() && strings.HasSuffix(n, ".json"):
			if err := copyFile(filepath.Join(dir, n), filepath.Join(tmp, n)); err != nil {
				return "", err
			}
		case e.IsDir() && n == "clusters":
			err := filepath.WalkDir(filepath.Join(dir, n), func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				rel, _ := filepath.Rel(dir, p)
				if d.IsDir() {
					return os.MkdirAll(filepath.Join(tmp, rel), 0o700)
				}
				if !d.Type().IsRegular() {
					return nil
				}
				return copyFile(p, filepath.Join(tmp, rel))
			})
			if err != nil {
				return "", err
			}
		}
	}
	_ = os.RemoveAll(dst)
	if err := os.Rename(tmp, dst); err != nil {
		return "", err
	}
	return dst, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
