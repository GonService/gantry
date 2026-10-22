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
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"gantry/internal/applog"
	"gantry/internal/engine"
	"gantry/internal/hosts"
	"gantry/internal/labs"
	"gantry/internal/vms"
)

// Backups of a project: the data of its named volumes, a logical dump of
// each database it runs (pg_dumpall, mysqldump, mongodump) and the project
// folder, in one .tar.gz kept on this computer (outside the server, so a
// lost server does not take its backups along).

type backupManifest struct {
	Project string         `json:"project"`
	Host    string         `json:"host"`
	Machine string         `json:"machine"`
	Created int64          `json:"created"`
	App     string         `json:"app"`
	Stopped bool           `json:"stopped"` // containers were stopped for the copy
	Env     bool           `json:"env"`     // the .env is in projeto.tar.gz
	Volumes []backupVolume `json:"volumes"`
	Dumps   []backupDump   `json:"dumps"`
	Files   bool           `json:"files"`
	VMs     []backupVM     `json:"vms,omitempty"`
}

type backupVolume struct {
	Name      string `json:"name"`
	Dest      string `json:"dest"`      // where it is mounted
	Container string `json:"container"` // the container it was read from
	Service   string `json:"service,omitempty"`
	Size      int64  `json:"size"`
}

type backupVM struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	// Kind "iol": an IOL node, whose configuration (NVRAM) is the file
	// vms/<name>.nvram.tar instead of a disk.
	Kind string `json:"kind,omitempty"`
}

type backupDump struct {
	Container string `json:"container"`
	Service   string `json:"service,omitempty"`
	Kind      string `json:"kind"`
	File      string `json:"file"`
	Size      int64  `json:"size"`
}

type backupItem struct {
	Name     string          `json:"name"`
	Size     int64           `json:"size"`
	Created  int64           `json:"created"`
	Manifest *backupManifest `json:"manifest,omitempty"`
}

type backupSchedule struct {
	Dir   string `json:"dir,omitempty"` // where this project's backups go (chosen by the user)
	Every int    `json:"every"`         // hours; 0 = off
	Keep  int    `json:"keep"`          // backups kept per project (0 = all)
	Stop  bool   `json:"stop"`
	NoVMs bool   `json:"noVms,omitempty"` // leave the VM disks out
	Env   bool   `json:"env"`
	Last  int64  `json:"last,omitempty"`
	// LastTry is the last automatic attempt, successful or not: a failing
	// backup waits an hour before the next try.
	LastTry int64  `json:"lastTry,omitempty"`
	LastErr string `json:"lastErr,omitempty"`
}

var (
	backupMu  sync.Mutex
	safeName  = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
	backupRe  = regexp.MustCompile(`^[A-Za-z0-9._-]+\.tar\.gz$`)
	backupDir = func() string {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Gantry", "backups")
	}
)

// labBackupDir is the folder the user chose for the project's backups, or
// a suggestion under ~/Gantry/backups until they choose.
func (s *Server) labBackupDir(h *hosts.Host, lab string) string {
	backupMu.Lock()
	d := s.backupSchedules()[scheduleKey(h, lab)].Dir
	backupMu.Unlock()
	if d != "" {
		return d
	}
	return filepath.Join(backupDir(), safeName.ReplaceAllString(h.Machine(), "_"), safeName.ReplaceAllString(lab, "_"))
}

// setBackupDir remembers the folder chosen for a project's backups.
func (s *Server) setBackupDir(h *hosts.Host, lab, dir string) error {
	if !filepath.IsAbs(dir) {
		return errors.New("escolha uma pasta (caminho completo)")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	backupMu.Lock()
	defer backupMu.Unlock()
	m := s.backupSchedules()
	sch := m[scheduleKey(h, lab)]
	sch.Dir = filepath.Clean(dir)
	m[scheduleKey(h, lab)] = sch
	return s.saveBackupSchedules(m)
}

// dumpCmd is the command that writes a logical dump of a database
// container to standard output, with what it needs in env.
func dumpCmd(kind string, ins engine.Inspect) (cmd string, ext string, ok bool) {
	switch kind {
	case "postgres":
		return `export PGPASSWORD="${POSTGRES_PASSWORD:-}"; pg_dumpall -U "${POSTGRES_USER:-postgres}" --clean --if-exists`, "sql", true
	case "mysql":
		return `c=$(command -v mariadb-dump || command -v mysqldump); export MYSQL_PWD="${MYSQL_ROOT_PASSWORD:-$MARIADB_ROOT_PASSWORD}"; "$c" -uroot --all-databases --single-transaction --routines --events`, "sql", true
	case "mongo":
		return `if [ -n "$MONGO_INITDB_ROOT_USERNAME" ]; then mongodump --quiet --archive -u "$MONGO_INITDB_ROOT_USERNAME" -p "$MONGO_INITDB_ROOT_PASSWORD" --authenticationDatabase admin; else mongodump --quiet --archive; fi`, "archive", true
	}
	return "", "", false
}

// backupLab writes a backup of the project and returns its path.
func (s *Server) backupLab(ctx context.Context, h *hosts.Host, l labs.Lab, stop, withEnv, withVMs bool, out io.Writer) (string, error) {
	dir := s.labBackupDir(h, l.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(dir, ".parcial-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	man := backupManifest{Project: l.ID, Host: h.Name, Machine: h.Machine(), Created: time.Now().Unix(), App: Version, Stopped: stop, Env: withEnv}

	// The containers of the project, fresh, with their mounts, per engine.
	type ctr struct {
		c   engine.Container
		eng *engine.Client
	}
	var ctrs []ctr
	rts := l.Runtimes
	if len(rts) == 0 {
		rts = []string{""}
	}
	for _, rt := range rts {
		ref, err := s.engineFor(ctx, h, rt)
		if err != nil {
			return "", err
		}
		list, err := ref.Engine.Containers(ctx)
		if err != nil {
			return "", err
		}
		for _, c := range list {
			if c.Labels[labs.LabelProject] == l.ID {
				ctrs = append(ctrs, ctr{c, ref.Engine})
			}
		}
	}
	if len(ctrs) == 0 && l.WorkingDir == "" {
		return "", errors.New("o projeto não tem containers nem pasta para copiar")
	}

	// 1. Dumps, while the databases run.
	for _, x := range ctrs {
		kind := sharedKind(x.c.Image)
		if x.c.State != "running" {
			continue
		}
		ins, err := x.eng.Inspect(ctx, x.c.ID)
		if err != nil {
			continue
		}
		cmd, ext, ok := dumpCmd(kind, ins)
		if !ok {
			continue
		}
		file := "dumps/" + safeName.ReplaceAllString(x.c.Name(), "_") + "." + ext
		fmt.Fprintf(out, "\x1b[36m==>\x1b[0m dump de %s (%s)\n", x.c.Name(), sharedTitle[kind])
		f, err := os.Create(filepath.Join(tmp, filepath.Base(file)))
		if err != nil {
			return "", err
		}
		stderr, code, err := x.eng.ExecTo(ctx, x.c.ID, []string{"sh", "-c", cmd}, nil, f)
		st, _ := f.Stat()
		f.Close()
		if err != nil || code != 0 {
			os.Remove(f.Name())
			fmt.Fprintf(out, "    \x1b[33maviso:\x1b[0m o dump falhou (%v %s); os volumes ainda vão no backup.\n", err, strings.TrimSpace(stderr))
			continue
		}
		man.Dumps = append(man.Dumps, backupDump{Container: x.c.Name(), Service: x.c.Labels["com.docker.compose.service"], Kind: kind, File: file, Size: st.Size()})
		fmt.Fprintf(out, "    %s\n", humanBytes(st.Size()))
	}

	// 2. Volumes, read through the engine from a container that mounts
	// each one (works for Docker, Podman and remote servers alike).
	var stopped []ctr
	if stop {
		for _, x := range ctrs {
			if x.c.State == "running" {
				fmt.Fprintf(out, "\x1b[36m==>\x1b[0m parando %s para a cópia\n", x.c.Name())
				if err := x.eng.Action(ctx, x.c.ID, "stop"); err != nil {
					return "", err
				}
				stopped = append(stopped, x)
			}
		}
	}
	restart := func() {
		for _, x := range stopped {
			fmt.Fprintf(out, "\x1b[36m==>\x1b[0m iniciando %s\n", x.c.Name())
			_ = x.eng.Action(context.Background(), x.c.ID, "start")
		}
		stopped = nil
	}
	defer restart()
	seen := map[string]bool{}
	for _, x := range ctrs {
		for _, m := range x.c.Mounts {
			if m.Type != "volume" || m.Name == "" || seen[m.Name] {
				continue
			}
			seen[m.Name] = true
			fmt.Fprintf(out, "\x1b[36m==>\x1b[0m volume %s (%s em %s)\n", m.Name, m.Destination, x.c.Name())
			rc, err := x.eng.ArchiveGet(ctx, x.c.ID, m.Destination)
			if err != nil {
				return "", fmt.Errorf("volume %s: %v", m.Name, err)
			}
			f, err := os.Create(filepath.Join(tmp, "vol-"+safeName.ReplaceAllString(m.Name, "_")+".tar"))
			if err != nil {
				rc.Close()
				return "", err
			}
			n, err := io.Copy(f, rc)
			rc.Close()
			f.Close()
			if err != nil {
				return "", fmt.Errorf("volume %s: %v", m.Name, err)
			}
			man.Volumes = append(man.Volumes, backupVolume{Name: m.Name, Dest: m.Destination, Container: x.c.Name(), Service: x.c.Labels["com.docker.compose.service"], Size: n})
			fmt.Fprintf(out, "    %s\n", humanBytes(n))
		}
	}
	restart()

	// 3. VM disks: a standalone qcow2 each, taken from a snapshot when the
	// VM runs (consistent without stopping it).
	if withVMs {
		for _, v := range l.VMs {
			if v.State == "missing" {
				continue
			}
			if progAccel(v.Accel) {
				fmt.Fprintf(out, "\x1b[36m==>\x1b[0m configuração (NVRAM) do nó %s\n", v.Name)
				n, err := s.fetchNVRAM(ctx, h, l.ID, v.Name, filepath.Join(tmp, "vm-"+safeName.ReplaceAllString(v.Name, "_")+".nvram.tar"))
				if err != nil {
					return "", fmt.Errorf("nó %s: %v", v.Name, err)
				}
				man.VMs = append(man.VMs, backupVM{Name: v.Name, Size: n, Kind: "iol"})
				fmt.Fprintf(out, "    %s\n", humanBytes(n))
				continue
			}
			fmt.Fprintf(out, "\x1b[36m==>\x1b[0m disco da VM %s\n", v.Name)
			o, err := vms.Run(ctx, h, "export", vmEnv(l.ID, v.Name))
			if err != nil {
				return "", fmt.Errorf("VM %s: %v", v.Name, err)
			}
			lines := strings.Split(strings.TrimSpace(o), "\n")
			src := strings.TrimSpace(lines[len(lines)-1])
			f, err := os.Create(filepath.Join(tmp, "vm-"+safeName.ReplaceAllString(v.Name, "_")+".qcow2"))
			if err != nil {
				return "", err
			}
			n, err := s.copyFromHost(ctx, h, src, f)
			f.Close()
			_, _ = vms.Run(context.Background(), h, "export-clean", vmEnv(l.ID, v.Name))
			if err != nil {
				return "", fmt.Errorf("VM %s: %v", v.Name, err)
			}
			man.VMs = append(man.VMs, backupVM{Name: v.Name, Size: n})
			fmt.Fprintf(out, "    %s\n", humanBytes(n))
		}
	}

	// 4. The project folder.
	if l.WorkingDir != "" {
		fmt.Fprintf(out, "\x1b[36m==>\x1b[0m arquivos de %s%s\n", l.WorkingDir, map[bool]string{true: " (com o .env)", false: " (sem o .env)"}[withEnv])
		f, err := os.Create(filepath.Join(tmp, "projeto.tar.gz"))
		if err != nil {
			return "", err
		}
		if h.Kind == hosts.Unix {
			err = tarDir(f, l.WorkingDir, withEnv)
		} else {
			err = s.remoteTar(ctx, h, l.WorkingDir, withEnv, f)
		}
		f.Close()
		if err != nil {
			fmt.Fprintf(out, "    \x1b[33maviso:\x1b[0m %v\n", err)
		} else {
			man.Files = true
		}
	}

	// 4. One .tar.gz with the manifest first.
	name := l.ID + "-" + time.Now().Format("20060102-150405") + ".tar.gz"
	final := filepath.Join(dir, name)
	part := final + ".parcial"
	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	mb, _ := json.MarshalIndent(man, "", "  ")
	add := func(name string, r io.Reader, size int64) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: size, ModTime: time.Now()}); err != nil {
			return err
		}
		_, err := io.Copy(tw, r)
		return err
	}
	err = add("gantry-backup.json", bytes.NewReader(mb), int64(len(mb)))
	ents, _ := os.ReadDir(tmp)
	for _, e := range ents {
		if err != nil {
			break
		}
		name := e.Name()
		switch {
		case strings.HasPrefix(name, "vol-"):
			name = "volumes/" + strings.TrimPrefix(name, "vol-")
		case strings.HasPrefix(name, "vm-"):
			name = "vms/" + strings.TrimPrefix(name, "vm-")
		case name == "projeto.tar.gz":
		default:
			name = "dumps/" + name
		}
		var g *os.File
		if g, err = os.Open(filepath.Join(tmp, e.Name())); err != nil {
			break
		}
		st, _ := g.Stat()
		err = add(name, g, st.Size())
		g.Close()
	}
	if err == nil {
		err = tw.Close()
	}
	if err == nil {
		err = gz.Close()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(part)
		return "", err
	}
	if err := os.Rename(part, final); err != nil {
		return "", err
	}
	st, _ := os.Stat(final)
	fmt.Fprintf(out, "\n\x1b[1;32mBackup salvo:\x1b[0m %s (%s)\n", final, humanBytes(st.Size()))
	return final, nil
}

// pruneBackups keeps the newest keep backups of the project.
func pruneBackups(dir string, keep int, out io.Writer) {
	if keep <= 0 {
		return
	}
	items := listBackupFiles(dir)
	for i, it := range items {
		if i >= keep {
			if os.Remove(filepath.Join(dir, it.Name)) == nil && out != nil {
				fmt.Fprintf(out, "    apagado o backup antigo %s\n", it.Name)
			}
		}
	}
}

func listBackupFiles(dir string) []backupItem {
	ents, _ := os.ReadDir(dir)
	var items []backupItem
	for _, e := range ents {
		if e.IsDir() || !backupRe.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, backupItem{Name: e.Name(), Size: info.Size(), Created: info.ModTime().Unix()})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Created > items[j].Created })
	return items
}

// readManifest reads gantry-backup.json from the start of a backup.
func readManifest(p string) (*backupManifest, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	hdr, err := tr.Next()
	if err != nil || hdr.Name != "gantry-backup.json" {
		return nil, errors.New("não é um backup do Gantry")
	}
	var m backupManifest
	if err := json.NewDecoder(io.LimitReader(tr, 1<<20)).Decode(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

// restoreLab puts a backup back: the volumes are recreated from the copy
// (the project is removed and created again around them) and, if asked,
// the project folder too. Dumps stay in the file for a manual restore.
func (s *Server) restoreLab(ctx context.Context, h *hosts.Host, l labs.Lab, file string, withFiles bool, out io.Writer) error {
	man, err := readManifest(file)
	if err != nil {
		return err
	}
	if l.WorkingDir == "" || len(l.Files) == 0 {
		return errors.New("o projeto precisa da pasta com o compose para ser restaurado")
	}
	if len(man.Volumes) == 0 && len(man.VMs) == 0 {
		if withFiles && man.Files {
			fmt.Fprintf(out, "\x1b[36m==>\x1b[0m restaurando os arquivos em %s\n", l.WorkingDir)
			data, err := extractEntry(file, "projeto.tar.gz")
			if err != nil {
				return err
			}
			if h.Kind == hosts.Unix {
				err = untar(bytes.NewReader(data), l.WorkingDir)
			} else {
				_, err = h.Run(ctx, "", []string{"tar", "xzf", "-", "-C", l.WorkingDir}, data)
			}
			if err != nil {
				return fmt.Errorf("arquivos: %v", err)
			}
		}
		fmt.Fprintln(out, "O backup não tem volumes.")
		return nil
	}
	// Everything the restore needs is read from the file first: a broken
	// backup must fail before anything of the project is touched.
	fmt.Fprintln(out, "\x1b[36m==>\x1b[0m conferindo o backup")
	data := map[string]*os.File{}
	defer func() {
		for _, f := range data {
			f.Close()
			os.Remove(f.Name())
		}
	}()
	for _, v := range man.Volumes {
		f, err := extractEntryFile(file, "volumes/"+safeName.ReplaceAllString(v.Name, "_")+".tar")
		if err != nil {
			return fmt.Errorf("volume %s: %v (nada foi alterado)", v.Name, err)
		}
		data[v.Name] = f
		if err := checkTar(f); err != nil {
			return fmt.Errorf("volume %s está corrompido no backup: %v (nada foi alterado)", v.Name, err)
		}
	}
	vmData := map[string]*os.File{}
	defer func() {
		for _, f := range vmData {
			f.Close()
			os.Remove(f.Name())
		}
	}()
	for _, v := range man.VMs {
		if !labName.MatchString(v.Name) {
			return fmt.Errorf("o backup tem uma VM com nome inválido (%q); nada foi alterado", v.Name)
		}
		entry := "vms/" + safeName.ReplaceAllString(v.Name, "_") + ".qcow2"
		if v.Kind == "iol" {
			entry = "vms/" + safeName.ReplaceAllString(v.Name, "_") + ".nvram.tar"
		}
		f, err := extractEntryFile(file, entry)
		if err != nil {
			return fmt.Errorf("disco da VM %s: %v (nada foi alterado)", v.Name, err)
		}
		vmData[v.Name] = f
	}
	// A copy of how the project is now, in case the restore goes wrong.
	fmt.Fprintln(out, "\x1b[36m==>\x1b[0m guardando o estado atual antes de restaurar")
	if _, err := s.backupLab(ctx, h, l, false, false, true, indentWriter{out}); err != nil {
		return fmt.Errorf("não consegui guardar o estado atual (%v); nada foi alterado", err)
	}
	if withFiles && man.Files {
		fmt.Fprintf(out, "\x1b[36m==>\x1b[0m restaurando os arquivos em %s\n", l.WorkingDir)
		data, err := extractEntry(file, "projeto.tar.gz")
		if err != nil {
			return err
		}
		if h.Kind == hosts.Unix {
			err = untar(bytes.NewReader(data), l.WorkingDir)
		} else {
			_, err = h.Run(ctx, "", []string{"tar", "xzf", "-", "-C", l.WorkingDir}, data)
		}
		if err != nil {
			return fmt.Errorf("arquivos: %v", err)
		}
	}
	fmt.Fprintln(out, "\x1b[36m==>\x1b[0m removendo os containers do projeto")
	o, err := s.labViaEngine(ctx, h, l, "down")
	fmt.Fprint(out, indent(o)+"\n")
	if err != nil {
		return err
	}
	rts := l.Runtimes
	if len(rts) == 0 {
		rts = []string{""}
	}
	for _, v := range man.Volumes {
		for _, rt := range rts {
			ref, err := s.engineFor(ctx, h, rt)
			if err != nil {
				continue
			}
			err = ref.Engine.RemoveVolume(ctx, v.Name)
			var ae *engine.APIError
			switch {
			case err == nil:
				fmt.Fprintf(out, "    volume %s apagado\n", v.Name)
			case errors.As(err, &ae) && ae.Status == http.StatusNotFound:
			default:
				return fmt.Errorf("não consegui apagar o volume %s (%v); o backup do estado anterior está na lista", v.Name, err)
			}
		}
	}
	fmt.Fprintln(out, "\x1b[36m==>\x1b[0m criando o projeto de novo, sem iniciar")
	live := hosts.WithLive(ctx, out)
	if _, cmd, err := s.composeLabRun(live, h, l.ID, l.WorkingDir, l.Files, "create"); err != nil {
		return fmt.Errorf("%v (%s)", err, cmd)
	}
	// Containers again, to find who mounts each volume now.
	type ctr struct {
		c   engine.Container
		eng *engine.Client
	}
	var ctrs []ctr
	for _, rt := range rts {
		ref, err := s.engineFor(ctx, h, rt)
		if err != nil {
			return err
		}
		list, err := ref.Engine.Containers(ctx)
		if err != nil {
			return err
		}
		for _, c := range list {
			if c.Labels[labs.LabelProject] == l.ID {
				ctrs = append(ctrs, ctr{c, ref.Engine})
			}
		}
	}
	for _, v := range man.Volumes {
		var target *ctr
		var dest string
		for i := range ctrs {
			for _, m := range ctrs[i].c.Mounts {
				if m.Name == v.Name {
					target, dest = &ctrs[i], m.Destination
				}
			}
		}
		if target == nil {
			fmt.Fprintf(out, "    \x1b[33maviso:\x1b[0m nenhum container usa mais o volume %s; ficou de fora.\n", v.Name)
			continue
		}
		f := data[v.Name]
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
		fmt.Fprintf(out, "\x1b[36m==>\x1b[0m volume %s → %s em %s\n", v.Name, dest, target.c.Name())
		// The copy starts with the folder's own name: unpack in its parent,
		// renamed when the mount point changed.
		r := renameTop(f, path.Base(v.Dest), path.Base(dest))
		err := target.eng.ArchivePut(ctx, target.c.ID, path.Dir(dest), r)
		r.Close()
		if err != nil {
			return fmt.Errorf("volume %s: %v", v.Name, err)
		}
	}
	fmt.Fprintln(out, "\x1b[36m==>\x1b[0m subindo o projeto")
	if _, cmd, err := s.composeLabRun(live, h, l.ID, l.WorkingDir, l.Files, "up"); err != nil {
		return fmt.Errorf("%v (%s)", err, cmd)
	}
	for _, v := range man.VMs {
		f := vmData[v.Name]
		if v.Kind == "iol" {
			fmt.Fprintf(out, "\x1b[36m==>\x1b[0m configuração (NVRAM) do nó %s\n", v.Name)
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				return err
			}
			o, err := s.placeNVRAM(live, h, l.ID, v.Name, f)
			if err != nil {
				return fmt.Errorf("nó %s: %v", v.Name, err)
			}
			fmt.Fprint(out, indent(o)+"\n")
			continue
		}
		fmt.Fprintf(out, "\x1b[36m==>\x1b[0m disco da VM %s\n", v.Name)
		dir, err := vmStateDir(ctx, h, l.ID, v.Name)
		if err != nil {
			return err
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
		if err := s.copyToHost(ctx, h, f, dir+"/import.qcow2"); err != nil {
			return fmt.Errorf("VM %s: %v", v.Name, err)
		}
		o, err := vms.Run(live, h, "import", vmEnv(l.ID, v.Name))
		if err != nil {
			return fmt.Errorf("VM %s: %v", v.Name, err)
		}
		_ = o
	}
	if len(man.Dumps) > 0 {
		fmt.Fprintf(out, "\nO backup também tem dumps (%d) em dumps/ para restaurar à mão em outro banco.\n", len(man.Dumps))
	}
	return nil
}

// extractEntry returns a small entry of a backup in memory.
func extractEntry(file, name string) ([]byte, error) {
	f, err := extractEntryFile(file, name)
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	return io.ReadAll(f)
}

// extractEntryFile copies an entry of a backup to a temporary file.
func extractEntryFile(file, name string) (*os.File, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err != nil {
			return nil, fmt.Errorf("%s não está no backup", name)
		}
		if hdr.Name != name {
			continue
		}
		tmp, err := os.CreateTemp(filepath.Dir(file), ".restauro-")
		if err != nil {
			return nil, err
		}
		if _, err := io.Copy(tmp, tr); err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			return nil, err
		}
		_, _ = tmp.Seek(0, io.SeekStart)
		return tmp, nil
	}
}

// renameTop rewrites the first path element of a tar stream (from to to).
// Close it when done, so the copy stops if the reader gave up early.
func renameTop(r io.Reader, from, to string) io.ReadCloser {
	if from == to {
		return io.NopCloser(r)
	}
	pr, pw := io.Pipe()
	go func() {
		defer applog.Recover("server/backup.go:683")
		tr := tar.NewReader(r)
		tw := tar.NewWriter(pw)
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				pw.CloseWithError(tw.Close())
				return
			}
			if err != nil {
				pw.CloseWithError(err)
				return
			}
			if hdr.Name == from || strings.HasPrefix(hdr.Name, from+"/") {
				hdr.Name = to + strings.TrimPrefix(hdr.Name, from)
			}
			if err := tw.WriteHeader(hdr); err != nil {
				pw.CloseWithError(err)
				return
			}
			if _, err := io.Copy(tw, tr); err != nil {
				pw.CloseWithError(err)
				return
			}
		}
	}()
	return pr
}

// checkTar reads a tar to the end, so a truncated copy is found early.
func checkTar(r io.Reader) error {
	tr := tar.NewReader(r)
	for {
		_, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := io.Copy(io.Discard, tr); err != nil {
			return err
		}
	}
}

// indentWriter indents the output of a step inside another.
type indentWriter struct{ w io.Writer }

func (i indentWriter) Write(p []byte) (int, error) {
	_, err := i.w.Write([]byte("    " + strings.ReplaceAll(strings.TrimRight(string(p), "\n"), "\n", "\n    ") + "\n"))
	return len(p), err
}

/* HTTP */

func (s *Server) backupSchedules() map[string]backupSchedule {
	m := map[string]backupSchedule{}
	if b, err := os.ReadFile(filepath.Join(s.configDir(), "backups.json")); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func (s *Server) saveBackupSchedules(m map[string]backupSchedule) error {
	b, _ := json.MarshalIndent(m, "", "  ")
	_ = os.MkdirAll(s.configDir(), 0o700)
	return os.WriteFile(filepath.Join(s.configDir(), "backups.json"), b, 0o600)
}

func scheduleKey(h *hosts.Host, lab string) string { return h.Machine() + "|" + lab }

func (s *Server) listBackups(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	lab := r.PathValue("lab")
	dir := s.labBackupDir(h, lab)
	items := listBackupFiles(dir)
	for i := range items {
		if i < 30 {
			items[i].Manifest, _ = readManifest(filepath.Join(dir, items[i].Name))
		}
	}
	if items == nil {
		items = []backupItem{}
	}
	backupMu.Lock()
	sch := s.backupSchedules()[scheduleKey(h, lab)]
	backupMu.Unlock()
	writeJSON(w, map[string]any{"dir": dir, "chosen": sch.Dir != "", "items": items, "schedule": sch})
}

func (s *Server) startBackup(h *hosts.Host, l labs.Lab, stop, withEnv, withVMs bool, keep int) (string, error) {
	return s.startJob("lab:"+h.Machine()+"/"+l.ID, "Backup "+l.Name, func(ctx context.Context, out io.Writer) error {
		fmt.Fprintf(out, "\x1b[1mBackup do projeto %s\x1b[0m em %s\n\n", l.Name, h.Name)
		if _, err := s.backupLab(ctx, h, l, stop, withEnv, withVMs, out); err != nil {
			backupMu.Lock()
			m := s.backupSchedules()
			if sch, ok := m[scheduleKey(h, l.ID)]; ok {
				sch.LastErr = err.Error()
				m[scheduleKey(h, l.ID)] = sch
				_ = s.saveBackupSchedules(m)
			}
			backupMu.Unlock()
			return err
		}
		pruneBackups(s.labBackupDir(h, l.ID), keep, out)
		backupMu.Lock()
		m := s.backupSchedules()
		if sch, ok := m[scheduleKey(h, l.ID)]; ok {
			sch.Last, sch.LastErr = time.Now().Unix(), ""
			m[scheduleKey(h, l.ID)] = sch
			_ = s.saveBackupSchedules(m)
		}
		backupMu.Unlock()
		return nil
	})
}

func (s *Server) createBackup(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		Stop  bool   `json:"stop"`
		Env   bool   `json:"env"`
		Dir   string `json:"dir"`   // the folder the user picked
		NoVMs bool   `json:"noVms"` // leave the VM disks out (they can be large)
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	l, err := s.findLab(ctx, h, r.PathValue("lab"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if req.Dir != "" {
		if err := s.setBackupDir(h, l.ID, req.Dir); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	}
	backupMu.Lock()
	keep := s.backupSchedules()[scheduleKey(h, l.ID)].Keep
	backupMu.Unlock()
	id, err := s.startBackup(h, l, req.Stop, req.Env, !req.NoVMs, keep)
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, map[string]string{"id": id})
}

func (s *Server) backupFile(h *hosts.Host, r *http.Request) (string, error) {
	name := r.PathValue("name")
	if name == "arquivo" {
		// A backup the user picked anywhere (?path=).
		p := r.URL.Query().Get("path")
		if !filepath.IsAbs(p) {
			return "", errors.New("escolha o arquivo do backup")
		}
		if _, err := readManifest(p); err != nil {
			return "", err
		}
		return p, nil
	}
	if !backupRe.MatchString(name) {
		return "", errors.New("nome de backup inválido")
	}
	p := filepath.Join(s.labBackupDir(h, r.PathValue("lab")), name)
	if _, err := os.Stat(p); err != nil {
		return "", errors.New("backup não encontrado")
	}
	return p, nil
}

func (s *Server) restoreBackup(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		Files bool `json:"files"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req)
	file, err := s.backupFile(h, r)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	l, err := s.findLab(ctx, h, r.PathValue("lab"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	id, err := s.startJob("lab:"+h.Machine()+"/"+l.ID, "Restaurar "+l.Name, func(ctx context.Context, out io.Writer) error {
		fmt.Fprintf(out, "\x1b[1mRestaurando %s\x1b[0m de %s\n\n", l.Name, filepath.Base(file))
		return s.restoreLab(ctx, h, l, file, req.Files, out)
	})
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, map[string]string{"id": id})
}

func (s *Server) deleteBackup(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	file, err := s.backupFile(h, r)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if err := os.Remove(file); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) openBackups(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	dir := s.labBackupDir(h, r.PathValue("lab"))
	_ = os.MkdirAll(dir, 0o700)
	if err := openURL(dir); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]string{"dir": dir})
}

func (s *Server) setBackupSchedule(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req backupSchedule
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Every < 0 || req.Every > 24*30 || req.Keep < 0 || req.Keep > 1000 {
		writeErr(w, http.StatusBadRequest, errors.New("intervalo ou quantidade inválidos"))
		return
	}
	backupMu.Lock()
	m := s.backupSchedules()
	k := scheduleKey(h, r.PathValue("lab"))
	req.Last, req.LastTry, req.LastErr = m[k].Last, m[k].LastTry, m[k].LastErr
	if req.Dir == "" {
		req.Dir = m[k].Dir
	} else if !filepath.IsAbs(req.Dir) {
		backupMu.Unlock()
		writeErr(w, http.StatusBadRequest, errors.New("escolha uma pasta (caminho completo)"))
		return
	}
	if req.Every == 0 && req.Keep == 0 && req.Dir == "" {
		delete(m, k)
	} else {
		m[k] = req
	}
	err := s.saveBackupSchedules(m)
	backupMu.Unlock()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, req)
}

// backupLoop runs the scheduled backups while the app is open.
func (s *Server) backupLoop(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		backupMu.Lock()
		m := s.backupSchedules()
		backupMu.Unlock()
		for k, sch := range m {
			if sch.Every <= 0 || time.Since(time.Unix(sch.Last, 0)) < time.Duration(sch.Every)*time.Hour || time.Since(time.Unix(sch.LastTry, 0)) < time.Hour {
				continue
			}
			backupMu.Lock()
			cur := s.backupSchedules()
			if c, ok := cur[k]; ok {
				c.LastTry = time.Now().Unix()
				cur[k] = c
				_ = s.saveBackupSchedules(cur)
			}
			backupMu.Unlock()
			machine, lab, _ := strings.Cut(k, "|")
			h := s.machineHost(machine)
			if h == nil || !h.Files() {
				continue
			}
			lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			l, err := s.findLab(lctx, h, lab)
			cancel()
			if err != nil {
				continue
			}
			_, _ = s.startBackup(h, l, sch.Stop, sch.Env, !sch.NoVMs, sch.Keep)
		}
	}
}

// copyFromHost copies a file of the host's machine to w.
func (s *Server) copyFromHost(ctx context.Context, h *hosts.Host, p string, w io.Writer) (int64, error) {
	if !h.Remote() {
		f, err := os.Open(p)
		if err != nil {
			return 0, err
		}
		defer f.Close()
		return io.Copy(w, f)
	}
	rc, wait, err := h.Pipe(ctx, "cat "+hosts.Quote(p))
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(w, rc)
	rc.Close()
	if werr := wait(); err == nil {
		err = werr
	}
	return n, err
}

// copyToHost writes r to a file of the host's machine.
func (s *Server) copyToHost(ctx context.Context, h *hosts.Host, r io.Reader, p string) error {
	if !h.Remote() {
		f, err := os.Create(p)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, r)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		return err
	}
	return h.RunStdin(ctx, "cat > "+hosts.Quote(p), r)
}
