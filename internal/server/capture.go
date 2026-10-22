package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"gantry/internal/applog"
	"gantry/internal/hosts"
	"gantry/internal/i18n"
	"gantry/internal/pcap"
	"gantry/internal/vms"
)

// Captures of the lab's cables: the relay copies the frames of a cable to
// a pcap file on the machine; the app shows them live, opens them in
// Wireshark (streamed) or downloads the file.

// capTarget is what a capture records: a port (its relay port) or a whole
// lab network (seg:NAME), and how to name it.
type capTarget struct {
	project, key, label, title string
}

// captureTarget reads what to capture from the query: node and port (the
// cable of that port, or the port on its network), or rede (a whole lab
// network).
func (s *Server) captureTarget(ctx context.Context, h *hosts.Host, labID string, r *http.Request) (capTarget, error) {
	q := r.URL.Query()
	l, err := s.findLab(ctx, h, labID)
	if err != nil {
		return capTarget{}, err
	}
	p, _, err := s.loadProject(ctx, h, l.Files)
	if err != nil {
		return capTarget{}, err
	}
	if rede := q.Get("rede"); rede != "" {
		if p.Redes[rede] == nil {
			return capTarget{}, fmt.Errorf("a rede %s não está no projeto", rede)
		}
		return capTarget{l.ID, "seg:" + rede, fmt.Sprintf(i18n.T("a rede %s (todas as portas nela)"), rede), rede}, nil
	}
	node := q.Get("node")
	port, err := strconv.Atoi(q.Get("port"))
	if node == "" || err != nil {
		return capTarget{}, errors.New("informe a VM (ou container) e a porta, ou a rede")
	}
	for _, v := range p.Nodes() {
		if v.Name != node {
			continue
		}
		if port < 0 || port >= len(v.Interfaces) {
			return capTarget{}, fmt.Errorf("%s não tem a porta %d", node, port)
		}
		f := v.Interfaces[port]
		switch {
		case f.Net != "":
			return capTarget{l.ID, strconv.Itoa(f.RPort), fmt.Sprintf(i18n.T("a porta %d de %s, na rede %s"), port, node, f.Net), node + " · " + f.Net}, nil
		case f.Link != "":
			return capTarget{l.ID, strconv.Itoa(f.RPort), fmt.Sprintf(i18n.T("o cabo %s (%s, porta %d)"), f.Link, node, port), f.Link}, nil
		}
		return capTarget{}, fmt.Errorf("a porta %d de %s não tem cabo", port, node)
	}
	return capTarget{}, fmt.Errorf("%s não está no projeto", node)
}

func captureStart(ctx context.Context, h *hosts.Host, project, key string) (string, error) {
	out, err := vms.Run(ctx, h, "capture-start", map[string]string{"GV_PROJECT": project, "GV_PORT": key})
	if err != nil {
		return "", err
	}
	f := strings.TrimSpace(out)
	if !strings.HasSuffix(f, ".pcap") {
		return "", fmt.Errorf("captura: resposta inesperada %q", f)
	}
	return f, nil
}

func captureStop(h *hosts.Host, project, file string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, _ = vms.Run(ctx, h, "capture-stop", map[string]string{"GV_PROJECT": project, "GV_FILE": file})
}

// labCaptureLive starts a capture and sends one line per frame (SSE), until
// the window closes; then the capture stops (the file stays, for Baixar).
func (s *Server) labCaptureLive(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	ctx := r.Context()
	tg, err := s.captureTarget(ctx, h, r.PathValue("lab"), r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	project := tg.project
	file, err := captureStart(ctx, h, project, tg.key)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	defer captureStop(h, project, file)
	send, ok := startSSE(w)
	if !ok {
		return
	}
	out := b64Writer{send}
	send("arquivo", file)
	fmt.Fprintf(out, "\x1b[2m— %s —\x1b[0m\n", fmt.Sprintf(i18n.T("capturando %s; arquivo %s"), tg.label, file))
	rc, wait, err := h.Pipe(ctx, "exec tail -c +1 -f "+shellQuote(file))
	if err != nil {
		sendEnd(send, err.Error())
		return
	}
	defer func() {
		rc.Close()
		_ = wait()
	}()
	pr, err := pcap.NewReader(rc)
	if err != nil {
		if ctx.Err() == nil {
			sendEnd(send, err.Error())
		}
		return
	}
	n := 0
	for {
		f, err := pr.Next()
		if err != nil {
			if ctx.Err() == nil {
				sendEnd(send, err.Error())
			}
			return
		}
		n++
		fmt.Fprintf(out, "\x1b[2m%s\x1b[0m %5d  %s\n", f.Time.Format("15:04:05.000"), n, pcap.Describe(f.Data))
	}
}

func validCapture(f, lab string) bool {
	return strings.HasSuffix(f, ".pcap") && strings.Contains(f, "/gantry/vms/"+lab+"/capture/") && !strings.Contains(f, "..")
}

// labCaptureSave copies a capture file of the machine to this computer.
func (s *Server) labCaptureSave(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		Path string `json:"path"`
		Dest string `json:"dest"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&req); err != nil || !validCapture(req.Path, r.PathValue("lab")) {
		writeErr(w, http.StatusBadRequest, errors.New("arquivo de captura inválido"))
		return
	}
	if !filepath.IsAbs(req.Dest) {
		writeErr(w, http.StatusBadRequest, errors.New("escolha onde salvar a captura"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	b, err := h.ReadFile(ctx, req.Path)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if err := os.WriteFile(req.Dest, b, 0o644); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]any{"path": req.Dest, "bytes": len(b)})
}

// labCaptureFile downloads a capture file of the project.
func (s *Server) labCaptureFile(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	f := r.URL.Query().Get("path")
	if !validCapture(f, r.PathValue("lab")) {
		writeErr(w, http.StatusBadRequest, errors.New("arquivo de captura inválido"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	b, err := h.ReadFile(ctx, f)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.tcpdump.pcap")
	w.Header().Set("Content-Disposition", `attachment; filename="`+path.Base(f)+`"`)
	w.Write(b)
}

// wiresharkPath is the Wireshark of this computer, if any.
func wiresharkPath() string {
	if p, err := exec.LookPath("wireshark"); err == nil {
		return p
	}
	if runtime.GOOS == "darwin" {
		for _, p := range []string{"/Applications/Wireshark.app/Contents/MacOS/Wireshark", os.Getenv("HOME") + "/Applications/Wireshark.app/Contents/MacOS/Wireshark"} {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	return ""
}

// labCaptureWireshark opens Wireshark on this computer reading the capture
// live (the pcap streamed from the machine); closing Wireshark stops it.
func (s *Server) labCaptureWireshark(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	ws := wiresharkPath()
	if ws == "" {
		writeErr(w, http.StatusPreconditionFailed, errors.New("o Wireshark não está instalado neste computador (no Mac: brew install --cask wireshark); use Baixar .pcap"))
		return
	}
	tg, err := s.captureTarget(r.Context(), h, r.PathValue("lab"), r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	project := tg.project
	file, err := captureStart(r.Context(), h, project, tg.key)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	rc, wait, err := h.Pipe(ctx, "exec tail -c +1 -f "+shellQuote(file))
	if err != nil {
		cancel()
		captureStop(h, project, file)
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	cmd := exec.Command(ws, "-k", "-i", "-", "-o", "gui.window_title:"+tg.title)
	cmd.Stdin = rc
	if err := cmd.Start(); err != nil {
		cancel()
		rc.Close()
		captureStop(h, project, file)
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	go func() {
		defer applog.Recover("server/capture.go:wireshark")
		_ = cmd.Wait()
		cancel()
		rc.Close()
		_ = wait()
		captureStop(h, project, file)
	}()
	writeJSON(w, map[string]any{"ok": true, "file": file})
}
