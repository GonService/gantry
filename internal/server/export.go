package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"gantry/internal/composex"
	"gantry/internal/hosts"
	"gantry/internal/labs"
)

// skipExport are folders a project package leaves out.
var skipExport = map[string]bool{".git": true, "node_modules": true, ".gantry": true, labPackDir: true}

// exportLab packs the project folder (compose, files next to it, and the
// .env if asked) into a .tar.gz in the Downloads folder of this computer.
// Volumes and VM disks stay out: the package rebuilds the project anywhere.
func (s *Server) exportLab(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		Env  bool   `json:"env"`
		Path string `json:"path"` // where the user chose to save it
		// The equipment of a network lab: the configuration of the IOL
		// nodes (NVRAM), the disks of the VMs, write memory first.
		NVRAM bool `json:"nvram"`
		Disks bool `json:"disks"`
		Write bool `json:"write"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
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
	dest, err := downloadPath("gantry-" + l.ID + "-" + time.Now().Format("20060102-1504") + ".tar.gz")
	if req.Path != "" {
		if !filepath.IsAbs(req.Path) {
			writeErr(w, http.StatusBadRequest, errors.New("escolha onde salvar (caminho completo)"))
			return
		}
		dest, err = req.Path, os.MkdirAll(filepath.Dir(req.Path), 0o755)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// The equipment first: a failure leaves no half package behind.
	var extras string
	var output strings.Builder
	if req.NVRAM || req.Disks {
		tmp, err := os.MkdirTemp("", "gantrylab-")
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		defer os.RemoveAll(tmp)
		man, err := s.collectLabEquipment(ctx, h, l, labPackOpts{NVRAM: req.NVRAM, Disks: req.Disks, Write: req.Write}, tmp, &output)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err)
			return
		}
		if man != nil {
			extras = tmp
		}
	}
	f, err := os.Create(dest)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	pack := func(out io.Writer) error {
		if h.Kind == hosts.Unix {
			return tarDir(out, dir, req.Env)
		}
		return s.remoteTar(ctx, h, dir, req.Env, out)
	}
	if extras == "" {
		err = pack(f)
	} else {
		pr, pw := io.Pipe()
		go func() { pw.CloseWithError(pack(pw)) }()
		err = withLabExtras(pr, f, extras)
		pr.Close()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(dest)
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	st, _ := os.Stat(dest)
	writeJSON(w, map[string]any{"path": dest, "size": st.Size(), "lab": extras != "", "output": output.String()})
}

// downloadPath is a free name in ~/Downloads (or the temp folder).
func downloadPath(name string) (string, error) {
	dir := os.TempDir()
	if home, err := os.UserHomeDir(); err == nil {
		if st, err := os.Stat(filepath.Join(home, "Downloads")); err == nil && st.IsDir() {
			dir = filepath.Join(home, "Downloads")
		}
	}
	p := filepath.Join(dir, name)
	for i := 2; ; i++ {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			return p, nil
		}
		p = filepath.Join(dir, strings.TrimSuffix(name, ".tar.gz")+fmt.Sprintf("-%d.tar.gz", i))
	}
}

// tarDir writes dir as a gzipped tar, with paths relative to it.
func tarDir(w io.Writer, dir string, withEnv bool) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() && skipExport[d.Name()] {
			return filepath.SkipDir
		}
		if !withEnv && rel == ".env" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return nil // sockets, links: not part of a project package
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = rel
		if info.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		src, err := os.Open(p)
		if err != nil {
			return err
		}
		_, err = io.Copy(tw, src)
		src.Close()
		return err
	})
	if err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func (s *Server) remoteTar(ctx context.Context, h *hosts.Host, dir string, withEnv bool, w io.Writer) error {
	ex := "--exclude=./.git --exclude=./node_modules --exclude=./.gantry --exclude=./" + labPackDir
	if !withEnv {
		ex += " --exclude=./.env"
	}
	out, wait, err := h.Pipe(ctx, "cd "+hosts.Quote(dir)+" && tar czf - "+ex+" .")
	if err != nil {
		return err
	}
	_, cerr := io.Copy(w, out)
	out.Close()
	if err := wait(); err != nil {
		return err
	}
	return cerr
}

// importLab unpacks a package (.tar.gz) into a new folder on the host and
// opens it as a project, bringing it up when asked.
func (s *Server) importLab(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	if !h.Files() {
		writeErr(w, http.StatusBadRequest, errors.New("este host não dá acesso a arquivos; conecte via ssh://"))
		return
	}
	q := r.URL.Query()
	dir := strings.TrimRight(strings.TrimSpace(q.Get("dir")), "/")
	if dir == "" {
		writeErr(w, http.StatusBadRequest, errors.New("informe a pasta de destino"))
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 512<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	resp, code, err := s.importPackage(ctx, h, dir, data, q.Get("up") == "1", q.Get("job") == "1")
	if err != nil {
		writeErr(w, code, err)
		return
	}
	writeJSON(w, resp)
}

// importPackage unpacks a project package into a new folder of the host and
// registers it (bringing it up when asked).
func (s *Server) importPackage(ctx context.Context, h *hosts.Host, dir string, data []byte, up, job bool) (map[string]any, int, error) {
	dir, err := expandDir(ctx, h, dir)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	if h.Exists(ctx, dir) {
		if out, _ := h.Run(ctx, "", []string{"sh", "-c", `ls -A "$1" | head -1`, "sh", dir}, nil); strings.TrimSpace(out) != "" {
			return nil, http.StatusConflict, fmt.Errorf("a pasta %s já existe e não está vazia em %s", dir, h.Name)
		}
	}
	if h.Kind == hosts.Unix {
		err = untar(bytes.NewReader(data), dir)
	} else {
		_, err = h.Run(ctx, "", []string{"sh", "-c", `mkdir -p "$1" && tar xzf - -C "$1"`, "sh", dir}, data)
	}
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("não consegui abrir o pacote: %v", err)
	}
	files := s.findComposeFiles(ctx, h, dir)
	if len(files) == 0 {
		return nil, http.StatusBadRequest, errors.New("o pacote não tem compose.yaml")
	}
	project := composex.ProjectName(dir)
	p, _, perr := s.loadProject(ctx, h, files)
	if perr == nil && p.Name != "" {
		project = p.Name
	}
	resp := map[string]any{"ok": true, "id": project, "dir": dir, "host": h.Name}
	// The equipment of a network lab (.gantrylab): in the nodes before they
	// go up.
	if o, err := s.applyLabExtras(ctx, h, dir, project); err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("%v (o projeto ficou em %s)", err, dir)
	} else if o != "" {
		resp["equipment"] = o
	}
	if up && job {
		s.store.SetLab(h.Name, project, dir, files, nil, nil)
		jid, err := s.labJob(h, labs.Lab{ID: project, Name: project, WorkingDir: dir, Files: files}, "up")
		if err != nil {
			resp["ok"], resp["error"] = false, err.Error()
		} else {
			resp["job"] = jid
		}
	} else if up {
		out, cmd, err := s.composeLab(ctx, h, project, dir, files, "up")
		resp["output"], resp["command"], resp["ok"] = out, cmd, err == nil
		if err != nil {
			resp["error"] = err.Error()
		}
	} else if perr == nil {
		s.store.SetLab(h.Name, project, dir, files, nil, vmNames(p))
	}
	return resp, http.StatusOK, nil
}

// copyLab sends a project to another host (deploy remoto): packs its folder
// on this host and unpacks it in a new folder there, bringing it up.
func (s *Server) copyLab(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		Target string `json:"target"`
		Dir    string `json:"dir"`
		Env    bool   `json:"env"`
		Up     bool   `json:"up"`
		Job    bool   `json:"job"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	t := s.host(req.Target)
	if t == nil {
		writeErr(w, http.StatusNotFound, errors.New("host de destino não encontrado"))
		return
	}
	if !t.Files() {
		writeErr(w, http.StatusBadRequest, errors.New("o destino não dá acesso a arquivos; conecte-o via ssh://"))
		return
	}
	t.Want()
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Minute)
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
	var buf bytes.Buffer
	if h.Kind == hosts.Unix {
		err = tarDir(&buf, path.Dir(main), req.Env)
	} else {
		err = s.remoteTar(ctx, h, path.Dir(main), req.Env, &buf)
	}
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	// The configuration of the IOL nodes goes along (the VM disks do not).
	if tmp, err := os.MkdirTemp("", "gantrylab-"); err == nil {
		defer os.RemoveAll(tmp)
		if man, err := s.collectLabEquipment(ctx, h, l, labPackOpts{NVRAM: true}, tmp, io.Discard); err != nil {
			writeErr(w, http.StatusBadGateway, err)
			return
		} else if man != nil {
			var full bytes.Buffer
			if err := withLabExtras(&buf, &full, tmp); err != nil {
				writeErr(w, http.StatusInternalServerError, err)
				return
			}
			buf = full
		}
	}
	dir := strings.TrimSpace(req.Dir)
	if dir == "" {
		dir = "~/gantry/" + l.ID
	}
	// Secrets kept in the keychain go along with the .env: the same
	// keychain (on this computer) gets them for the copy.
	if req.Env {
		if env, _ := s.labSecrets(h, l.ID); len(env) > 0 {
			target := composex.ProjectName(dir)
			if p, _, err := s.loadProject(ctx, h, l.Files); err == nil && p.Name != "" {
				target = p.Name
			}
			if err := s.storeSecrets(t, target, env); err != nil {
				writeErr(w, http.StatusBadGateway, err)
				return
			}
		}
	}
	resp, code, err := s.importPackage(ctx, t, dir, buf.Bytes(), req.Up, req.Job)
	if err != nil {
		writeErr(w, code, err)
		return
	}
	writeJSON(w, resp)
}

// untar extracts a .tar.gz into dir, refusing paths that leave it.
func untar(r io.Reader, dir string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// os.Root keeps every write inside dir, also through symlinks that
	// already exist there (a project cloned from git may have them).
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	mkdirs := func(rel string) error {
		if rel == "." || rel == "" {
			return nil
		}
		cur := ""
		for _, part := range strings.Split(rel, string(os.PathSeparator)) {
			cur = filepath.Join(cur, part)
			if err := root.Mkdir(cur, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
		}
		return nil
	}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(filepath.FromSlash(strings.TrimPrefix(hdr.Name, "./")))
		if name == "." {
			continue
		}
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("caminho fora da pasta: %s", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := mkdirs(name); err != nil {
				return fmt.Errorf("%s: %w", hdr.Name, err)
			}
		case tar.TypeReg:
			if err := mkdirs(filepath.Dir(name)); err != nil {
				return fmt.Errorf("%s: %w", hdr.Name, err)
			}
			f, err := root.OpenFile(name, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode)&0o777|0o600)
			if err != nil {
				return fmt.Errorf("%s: %w", hdr.Name, err)
			}
			_, err = io.Copy(f, io.LimitReader(tr, 1<<30))
			f.Close()
			if err != nil {
				return err
			}
		}
	}
}
