package server

// Container management beyond start/stop: files inside the container,
// filtered and saved logs, live resource limits, commit, events.

import (
	"archive/tar"
	"bufio"
	"bytes"
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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gantry/internal/applog"
	"gantry/internal/engine"
	"gantry/internal/hosts"
)

/* logs */

// logOptions reads ?tail=&since=&timestamps= of a log request. since is
// how far back, in seconds.
func logOptions(r *http.Request, follow bool) engine.LogOptions {
	q := r.URL.Query()
	o := engine.LogOptions{Tail: 300, Follow: follow, Timestamps: q.Get("timestamps") == "1"}
	switch t := q.Get("tail"); t {
	case "":
	case "all":
		o.Tail = -1
	default:
		if n, err := strconv.Atoi(t); err == nil && n >= 0 {
			o.Tail = n
		}
	}
	if s, err := strconv.ParseInt(q.Get("since"), 10, 64); err == nil && s > 0 {
		o.Since = time.Now().Unix() - s
		if q.Get("tail") == "" {
			o.Tail = -1 // a time window means everything in it
		}
	}
	return o
}

// lineFilter passes on only the lines that contain the text (any case).
type lineFilter struct {
	w       io.Writer
	needle  string
	pending []byte
}

func newLineFilter(w io.Writer, text string) io.Writer {
	if text == "" {
		return w
	}
	return &lineFilter{w: w, needle: strings.ToLower(text)}
}

func (f *lineFilter) Write(p []byte) (int, error) {
	f.pending = append(f.pending, p...)
	var out []byte
	for {
		i := bytes.IndexByte(f.pending, '\n')
		if i < 0 {
			break
		}
		line := f.pending[:i+1]
		if strings.Contains(strings.ToLower(string(line)), f.needle) {
			out = append(out, line...)
		}
		f.pending = f.pending[i+1:]
	}
	if len(f.pending) > 64<<10 { // a huge line without newline: judge it now
		if strings.Contains(strings.ToLower(string(f.pending)), f.needle) {
			out = append(out, f.pending...)
		}
		f.pending = nil
	}
	if len(out) > 0 {
		if _, err := f.w.Write(out); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// saveLogs writes the log of a container to a file of this computer.
func (s *Server) saveLogs(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		Dest string `json:"dest"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&req)
	if !filepath.IsAbs(req.Dest) {
		writeErr(w, http.StatusBadRequest, errors.New("escolha onde salvar o log"))
		return
	}
	eng, ok := s.reqEngine(w, r, h)
	if !ok {
		return
	}
	o := logOptions(r, false)
	if r.URL.Query().Get("tail") == "" {
		o.Tail = -1
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	rc, tty, err := eng.LogsWith(ctx, r.PathValue("id"), o)
	if err != nil {
		writeErr(w, engineStatus(err), err)
		return
	}
	defer rc.Close()
	tmp := req.Dest + ".parcial"
	f, err := os.Create(tmp)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	cw := &countWriter{w: f}
	out := newLineFilter(cw, r.URL.Query().Get("grep"))
	if tty {
		_, err = io.Copy(out, rc)
	} else {
		err = engine.Demux(out, rc)
	}
	if lf, ok := out.(*lineFilter); ok && err == nil && len(lf.pending) > 0 {
		lf.pending = append(lf.pending, '\n')
		_, err = lf.Write(nil)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, req.Dest)
	}
	if err != nil {
		os.Remove(tmp)
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]any{"path": req.Dest, "bytes": cw.n})
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

/* files inside the container */

type ctrFile struct {
	Name  string `json:"name"`
	Type  string `json:"type"` // dir, file, link, other
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"`
}

// listScript lists a folder with one stat call (busybox and GNU alike).
const listScript = `cd -- "$1" 2>/dev/null || { echo "gantry:nao-e-pasta"; exit 3; }
shift
for f in * .[!.]* ..?*; do if [ -e "$f" ] || [ -L "$f" ]; then set -- "$@" "$f"; fi; done
[ $# -eq 0 ] && exit 0
if command -v stat >/dev/null 2>&1; then stat -c '%F|%s|%Y|%n' -- "$@" 2>/dev/null; exit 0; fi
for f; do if [ -L "$f" ]; then t="symbolic link"; elif [ -d "$f" ]; then t=directory; elif [ -f "$f" ]; then t="regular file"; else t=other; fi; echo "$t|-1|0|$f"; done`

func cleanCtrPath(p string) (string, error) {
	if p == "" {
		p = "/"
	}
	if !strings.HasPrefix(p, "/") {
		return "", errors.New("use um caminho absoluto, como /etc")
	}
	return path.Clean(p), nil
}

func parseListing(out string) []ctrFile {
	files := []ctrFile{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(line, "|", 4)
		if len(f) < 4 || f[3] == "" {
			continue
		}
		x := ctrFile{Name: f[3]}
		x.Size, _ = strconv.ParseInt(f[1], 10, 64)
		x.MTime, _ = strconv.ParseInt(f[2], 10, 64)
		switch {
		case f[0] == "directory":
			x.Type = "dir"
		case f[0] == "symbolic link":
			x.Type = "link"
		case strings.Contains(f[0], "regular"):
			x.Type = "file"
		default:
			x.Type = "other"
		}
		files = append(files, x)
	}
	sort.Slice(files, func(i, j int) bool {
		if (files[i].Type == "dir") != (files[j].Type == "dir") {
			return files[i].Type == "dir"
		}
		return strings.ToLower(files[i].Name) < strings.ToLower(files[j].Name)
	})
	return files
}

func (s *Server) ctrFiles(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	eng, ok := s.reqEngine(w, r, h)
	if !ok {
		return
	}
	p, err := cleanCtrPath(r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	out, code, err := eng.Exec(ctx, r.PathValue("id"), []string{"sh", "-c", listScript, "gantry", p}, nil)
	if err == nil && strings.Contains(out, "gantry:nao-e-pasta") {
		writeErr(w, http.StatusNotFound, fmt.Errorf("%s não é uma pasta (ou não existe)", p))
		return
	}
	if err != nil || (code != 0 && code != 1) {
		msg := "o container precisa estar rodando e ter um shell (sh) para navegar; baixar um caminho conhecido funciona sempre"
		if err != nil && !strings.Contains(err.Error(), "not running") && !strings.Contains(strings.ToLower(err.Error()), "executable file not found") {
			msg = err.Error()
		}
		writeErr(w, http.StatusConflict, errors.New(msg))
		return
	}
	writeJSON(w, map[string]any{"path": p, "files": parseListing(out)})
}

// ctrDownload copies a path of the container to this computer: a file as
// itself, a folder as a .tar.
func (s *Server) ctrDownload(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		Path string `json:"path"`
		Dest string `json:"dest"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&req)
	p, err := cleanCtrPath(req.Path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if !filepath.IsAbs(req.Dest) {
		writeErr(w, http.StatusBadRequest, errors.New("escolha onde salvar"))
		return
	}
	eng, ok := s.reqEngine(w, r, h)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Minute)
	defer cancel()
	rc, err := eng.ArchiveGet(ctx, r.PathValue("id"), p)
	if err != nil {
		writeErr(w, engineStatus(err), err)
		return
	}
	defer rc.Close()
	tmp := req.Dest + ".parcial"
	n, kind, err := saveArchive(rc, tmp)
	if err == nil {
		err = os.Rename(tmp, req.Dest)
	}
	if err != nil {
		os.Remove(tmp)
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]any{"path": req.Dest, "bytes": n, "kind": kind})
}

// saveArchive writes the tar of one path: the file itself when it is a
// single regular file, else the whole tar.
func saveArchive(rc io.Reader, dest string) (int64, string, error) {
	br := bufio.NewReaderSize(rc, 1<<20)
	// Peek the first header without losing the stream.
	head, _ := br.Peek(64 << 10)
	tr := tar.NewReader(bytes.NewReader(head))
	hdr, err := tr.Next()
	if err != nil {
		return 0, "", fmt.Errorf("resposta inesperada do engine: %v", err)
	}
	f, err := os.Create(dest)
	if err != nil {
		return 0, "", err
	}
	var n int64
	if hdr.Typeflag == tar.TypeReg {
		t := tar.NewReader(br)
		if _, err = t.Next(); err == nil {
			n, err = io.Copy(f, t)
		}
		if err == nil {
			_ = os.Chmod(dest, (fs.FileMode(hdr.Mode).Perm()&0o755)|0o600)
		}
	} else {
		n, err = io.Copy(f, br)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	kind := "file"
	if hdr.Typeflag != tar.TypeReg {
		kind = "tar"
	}
	return n, kind, err
}

// ctrUpload copies a file or folder of this computer into a folder of the
// container.
func (s *Server) ctrUpload(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		Src string `json:"src"`
		Dir string `json:"dir"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&req)
	dir, err := cleanCtrPath(req.Dir)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if !filepath.IsAbs(req.Src) {
		writeErr(w, http.StatusBadRequest, errors.New("escolha o arquivo a enviar"))
		return
	}
	src, err := filepath.EvalSymlinks(req.Src)
	var st fs.FileInfo
	if err == nil {
		st, err = os.Stat(src)
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("escolha o arquivo a enviar"))
		return
	}
	eng, ok := s.reqEngine(w, r, h)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Minute)
	defer cancel()
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(applog.Safe("enviar arquivos ao container", func() error { return tarPath(pw, src, filepath.Base(req.Src), st) }))
	}()
	if err := eng.ArchivePut(ctx, r.PathValue("id"), dir, pr); err != nil {
		pr.CloseWithError(err)
		writeErr(w, engineStatus(err), err)
		return
	}
	writeJSON(w, map[string]any{"path": path.Join(dir, filepath.Base(req.Src))})
}

// tarPath writes a file or a folder (recursively) as a tar.
func tarPath(w io.Writer, src, base string, st fs.FileInfo) error {
	tw := tar.NewWriter(w)
	add := func(p string, info fs.FileInfo, name string) error {
		link := ""
		if info.Mode()&fs.ModeSymlink != 0 {
			link, _ = os.Readlink(p)
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(name)
		hdr.Uid, hdr.Gid, hdr.Uname, hdr.Gname = 0, 0, "", ""
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = io.Copy(tw, f)
			return err
		}
		return nil
	}
	if !st.IsDir() {
		if err := add(src, st, base); err != nil {
			return err
		}
		return tw.Close()
	}
	err := filepath.Walk(src, func(p string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		return add(p, info, filepath.Join(base, rel))
	})
	if err != nil {
		return err
	}
	return tw.Close()
}

func (s *Server) ctrDelete(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	p, err := cleanCtrPath(r.URL.Query().Get("path"))
	if err != nil || p == "/" {
		writeErr(w, http.StatusBadRequest, errors.New("caminho inválido"))
		return
	}
	eng, ok := s.reqEngine(w, r, h)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	out, code, err := eng.Exec(ctx, r.PathValue("id"), []string{"rm", "-rf", "--", p}, nil)
	if err == nil && code != 0 {
		err = errors.New(strings.TrimSpace(out))
	}
	if err != nil && strings.Contains(err.Error(), "executable file not found") {
		err = errors.New("o container não tem o comando rm")
	}
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

/* live resources */

var memSizeRe = regexp.MustCompile(`(?i)^\s*([0-9]+(?:\.[0-9]+)?)\s*([kmgt]?)i?b?\s*$`)

// parseSize reads 512m, 2g, 1.5G, 1048576.
func parseSize(s string) (int64, error) {
	m := memSizeRe.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("tamanho inválido: %q (use 512m, 2g…)", s)
	}
	v, _ := strconv.ParseFloat(m[1], 64)
	mult := map[string]float64{"": 1, "k": 1 << 10, "m": 1 << 20, "g": 1 << 30, "t": 1 << 40}[strings.ToLower(m[2])]
	return int64(v * mult), nil
}

func (s *Server) ctrResources(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		CPUs        string `json:"cpus"`
		Memory      string `json:"memory"`
		Reservation string `json:"reservation"`
		Pids        int64  `json:"pids"`
		Restart     string `json:"restart"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var res engine.Resources
	if c := strings.TrimSpace(req.CPUs); c != "" {
		v, err := strconv.ParseFloat(strings.ReplaceAll(c, ",", "."), 64)
		if err != nil || v <= 0 || v > 1024 {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("CPUs inválido: %q (use 0.5, 2…)", c))
			return
		}
		res.NanoCPUs = int64(v * 1e9)
	}
	if m := strings.TrimSpace(req.Memory); m != "" {
		v, err := parseSize(m)
		if err != nil || v < 6<<20 {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("memória inválida: %q (mínimo 6m)", m))
			return
		}
		res.Memory, res.MemorySwap = v, 2*v
	}
	if m := strings.TrimSpace(req.Reservation); m != "" {
		v, err := parseSize(m)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		res.MemoryReservation = v
	}
	res.PidsLimit = req.Pids
	switch req.Restart {
	case "", "no", "always", "unless-stopped", "on-failure":
		res.Restart = req.Restart
	default:
		writeErr(w, http.StatusBadRequest, fmt.Errorf("política de reinício inválida: %s", req.Restart))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	ref, err := s.engineFor(ctx, h, r.URL.Query().Get("rt"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	eng, id := ref.Engine, r.PathValue("id")
	var warn []string
	switch ref.Runtime {
	case "podman":
		if res.Restart != "" {
			if v, verr := eng.Version(ctx); verr == nil && !versionNewer(v.Version, "5.0.99") {
				err = fmt.Errorf("o Podman %s não muda a política de reinício sem recriar (só a partir do 5.1); use Editar e recriar para isso", v.Version)
				break
			}
		}
		err = eng.UpdatePodman(ctx, id, res)
	default: // docker, and nerdctl/k3s through the shim
		warn, err = eng.Update(ctx, id, res)
	}
	if err != nil {
		status := http.StatusBadGateway
		if !errors.As(err, new(*engine.APIError)) {
			status = http.StatusBadRequest
		}
		writeErr(w, status, err)
		return
	}
	if res.Restart != "" && ref.Runtime == "podman" {
		// Older Podman answers OK without applying it: check.
		if got, gerr := eng.RestartPolicy(ctx, id); gerr == nil && got != res.Restart && !(got == "" && res.Restart == "no") {
			warn = append(warn, "o Podman não aplicou a política de reinício ("+got+")")
		}
	}
	writeJSON(w, map[string]any{"warnings": warn, "runtime": ref.Runtime})
}

/* commit */

var refRe = regexp.MustCompile(`^[a-z0-9]+([._/:-][a-zA-Z0-9]+)*$`)

func (s *Server) ctrCommit(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		Ref     string `json:"ref"`
		Comment string `json:"comment"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&req)
	req.Ref = strings.TrimSpace(req.Ref)
	if !refRe.MatchString(req.Ref) {
		writeErr(w, http.StatusBadRequest, errors.New("nome de imagem inválido (ex.: meu-app:snapshot)"))
		return
	}
	eng, ok := s.reqEngine(w, r, h)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	id, err := eng.Commit(ctx, r.PathValue("id"), req.Ref, req.Comment)
	if err != nil {
		writeErr(w, engineStatus(err), err)
		return
	}
	writeJSON(w, map[string]string{"id": id, "ref": req.Ref})
}

/* events */

type eventView struct {
	Time   int64  `json:"time"`
	Type   string `json:"type"`
	Action string `json:"action"`
	ID     string `json:"id"`
	Name   string `json:"name"`
	Detail string `json:"detail,omitempty"`
}

// noisyEvent: exec and health checks fill the history and say little.
func noisyEvent(action string) bool {
	return strings.HasPrefix(action, "exec_") || strings.HasPrefix(action, "health_status") || action == "top" || action == "attach" || action == "resize" || action == "archive-path" || action == "extract-to-dir"
}

func (s *Server) ctrEvents(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	eng, ok := s.reqEngine(w, r, h)
	if !ok {
		return
	}
	q := r.URL.Query()
	window, _ := strconv.ParseInt(q.Get("since"), 10, 64)
	if window <= 0 {
		window = 24 * 3600
	}
	now := time.Now().Unix()
	filters := map[string][]string{}
	if c := q.Get("container"); c != "" {
		filters["container"] = []string{c}
	}
	if t := q.Get("type"); t != "" {
		filters["type"] = []string{t}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	all := q.Get("all") == "1"
	evs, err := eng.EventsBetween(ctx, now-window, now, filters, func(e engine.Event) bool { return all || !noisyEvent(e.Action) })
	if err != nil && len(evs) == 0 {
		writeErr(w, engineStatus(err), err)
		return
	}
	out := []eventView{}
	for i := len(evs) - 1; i >= 0 && len(out) < 1000; i-- {
		e := evs[i]
		v := eventView{Time: e.Time, Type: e.Type, Action: e.Action, ID: e.Actor.ID, Name: e.Actor.Attributes["name"]}
		if e.TimeNano > 0 {
			v.Time = e.TimeNano / 1e6 // ms
		} else {
			v.Time = e.Time * 1000
		}
		a := e.Actor.Attributes
		switch {
		case a["exitCode"] != "":
			v.Detail = "código de saída " + a["exitCode"]
		case a["signal"] != "":
			v.Detail = "sinal " + a["signal"]
		case e.Type == "container" && a["image"] != "":
			v.Detail = a["image"]
		case e.Type == "network" && a["container"] != "":
			v.Detail = "container " + shortID(a["container"])
		}
		if v.Name == "" {
			v.Name = shortID(e.Actor.ID)
		}
		out = append(out, v)
	}
	writeJSON(w, out)
}

func shortID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
