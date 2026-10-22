package server

// The ports of a running VM, changed without restarting it: the QEMU
// monitor adds or removes the forward, and the compose gets the new list.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"gantry/internal/composex"
	"gantry/internal/hosts"
	"gantry/internal/vms"
)

// portItem writes a port in the short syntax of the compose.
func portItem(p composex.Port) string {
	s := p.Host + ":" + p.Container
	if p.HostIP != "" {
		ip := p.HostIP
		if strings.Contains(ip, ":") {
			ip = "[" + ip + "]"
		}
		s = ip + ":" + s
	}
	if p.Proto != "" && p.Proto != "tcp" {
		s += "/" + p.Proto
	}
	return strconv.Quote(s)
}

func (s *Server) vmPorts(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		Op   string `json:"op"`   // add, remove
		Port string `json:"port"` // as in the compose: [ip:]host:vm[/proto]
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil || (req.Op != "add" && req.Op != "remove") {
		writeErr(w, http.StatusBadRequest, errors.New("informe op (add ou remove) e a porta"))
		return
	}
	want := composex.ParsePort(strings.TrimSpace(req.Port))
	hp, err1 := strconv.Atoi(want.Host)
	gp, err2 := strconv.Atoi(want.Container)
	if err1 != nil || err2 != nil || hp < 1 || hp > 65535 || gp < 1 || gp > 65535 || (want.Proto != "tcp" && want.Proto != "udp") {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("porta inválida %q; use porta-do-host:porta-da-VM, como 8080:80 (ou 8080:80/udp)", req.Port))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	l, name, ok := s.vmContext(w, r, h)
	if !ok {
		return
	}
	s.composeEdit.Lock()
	defer s.composeEdit.Unlock()
	p, _, err := s.loadProject(ctx, h, l.Files)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	v := p.VM(name)
	if v == nil {
		writeErr(w, http.StatusNotFound, errors.New("VM não encontrada no compose"))
		return
	}
	if v.NoManagement {
		writeErr(w, http.StatusBadRequest, errors.New("esta VM não tem a placa de gerência (management: false): não publica portas"))
		return
	}
	same := func(a composex.Port) bool {
		return a.Host == want.Host && a.Proto == want.Proto && (a.HostIP == want.HostIP || req.Op == "remove" && want.HostIP == "")
	}
	var list []composex.Port
	found := composex.Port{}
	for _, x := range v.Ports {
		if same(x) {
			found = x
			if req.Op == "remove" {
				continue
			}
		}
		list = append(list, x)
	}
	if req.Op == "add" {
		if found.Host != "" {
			writeErr(w, http.StatusConflict, fmt.Errorf("a porta %s do host já está nesta VM", want.Host))
			return
		}
		list = append(list, want)
		found = want
	} else if found.Host == "" {
		writeErr(w, http.StatusNotFound, fmt.Errorf("a VM não publica a porta %s", want.Host))
		return
	}
	// The hash the VM runs with, before: if it was up to date, it stays so.
	net, _ := s.projectNet(ctx, h, l.ID, p)
	oldEnv, _ := vms.Env(l.ID, v, net)
	// The new compose, checked before anything changes.
	items := make([]string, len(list))
	for i, x := range list {
		items[i] = portItem(x)
	}
	val := "[" + strings.Join(items, ", ") + "]"
	if len(list) == 0 {
		val = ""
	}
	texts := map[string]string{}
	file := ""
	for _, f := range l.Files {
		b, err := h.ReadFile(ctx, f)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err)
			return
		}
		texts[f] = string(b)
		lines := strings.Split(texts[f], "\n")
		if start, end := findBlock(lines, "x-vms", name); start >= 0 && file == "" {
			file = f
			if at := fieldLine(lines, start, end, "ports"); at >= 0 && strings.Contains(strings.Join(lines[at:end], "\n"), "${") {
				writeErr(w, http.StatusBadRequest, errors.New("as portas desta VM usam variáveis (${…}); mude no arquivo"))
				return
			}
		}
	}
	if file == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("a VM %s não está nos arquivos do projeto", name))
		return
	}
	out, ok := setBlockField(texts[file], "x-vms", name, "ports", val)
	if !ok {
		writeErr(w, http.StatusBadRequest, errors.New("não consegui mudar as portas no compose"))
		return
	}
	texts[file] = out
	env := map[string]string{}
	if b, err := h.ReadFile(ctx, path.Join(path.Dir(l.Files[0]), ".env")); err == nil {
		env = composex.ParseEnv(b)
	}
	var docs [][]byte
	for _, f := range l.Files {
		docs = append(docs, composex.Interpolate([]byte(texts[f]), env))
	}
	if _, err := composex.Parse(docs...); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// Live first: a port in use on the host fails here, with nothing written.
	rule := found.Proto + ":" + found.HostIP + ":" + found.Host + ":" + found.Container
	o, err := vms.Run(ctx, h, "port-live", vmEnv(l.ID, name, "GV_OP", req.Op, "GV_PORT", rule))
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	if err := h.WriteFile(ctx, file, []byte(texts[file])); err != nil {
		writeErr(w, http.StatusBadGateway, errors.New("não consegui gravar "+file+": "+err.Error()))
		return
	}
	// The VM is now as the file says: its hash too (only if it was in sync).
	var running string
	for _, x := range l.VMs {
		if x.Name == name {
			running = x.Spec
		}
	}
	if running != "" && running == oldEnv["GV_HASH"] {
		if np, _, err := s.loadProject(ctx, h, l.Files); err == nil && np.VM(name) != nil {
			if newEnv, err := vms.Env(l.ID, np.VM(name), net); err == nil {
				_, _ = vms.Run(ctx, h, "hash-set", vmEnv(l.ID, name, "GV_HASH", newEnv["GV_HASH"], "GV_BASEHASH", newEnv["GV_BASEHASH"]))
			}
		}
	}
	writeJSON(w, map[string]any{"ok": true, "output": strings.TrimSpace(o)})
}
