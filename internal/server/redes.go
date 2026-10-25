package server

// The lab networks (x-redes): segments where many ports meet, like a switch,
// with an optional subnet and DHCP. The page lists them with the addresses
// their DHCP handed out, creates, changes and removes them; the relay of the
// machine picks the change up live.

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

// splitFlow splits the inside of a YAML flow map ("a: 1, b: [x, y]") at its
// top-level commas.
func splitFlow(s string) []string {
	var out []string
	depth, q, from := 0, byte(0), 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case q != 0:
			if c == q {
				q = 0
			}
		case c == '"' || c == '\'':
			q = c
		case c == '[' || c == '{':
			depth++
		case c == ']' || c == '}':
			depth--
		case c == ',' && depth == 0:
			out = append(out, strings.TrimSpace(s[from:i]))
			from = i + 1
		}
	}
	if t := strings.TrimSpace(s[from:]); t != "" {
		out = append(out, t)
	}
	return out
}

// flowSet writes field: value in the flow map at the end of line ("  lan:
// {subnet: …}"), replacing it or adding it; an empty value removes it. ok
// is false when the line has no flow map.
func flowSet(line, field, value string) (string, bool) {
	i := strings.Index(line, ":")
	if i < 0 {
		return line, false
	}
	rest := strings.TrimSpace(line[i+1:])
	comment := ""
	if !strings.HasPrefix(rest, "{") {
		return line, false
	}
	j := strings.LastIndex(rest, "}")
	if j < 0 {
		return line, false
	}
	comment = strings.TrimSpace(rest[j+1:])
	var items []string
	done := false
	for _, it := range splitFlow(rest[1:j]) {
		k := strings.TrimSpace(strings.SplitN(it, ":", 2)[0])
		if k == field {
			done = true
			if value != "" {
				items = append(items, field+": "+value)
			}
			continue
		}
		items = append(items, it)
	}
	if !done && value != "" {
		items = append(items, field+": "+value)
	}
	out := line[:i+1] + " {" + strings.Join(items, ", ") + "}"
	if comment != "" {
		out += " " + comment
	}
	return out, true
}

// setRedeField writes a field of a network, in its flow map or its block.
func setRedeField(src, name, field, value string) (string, bool) {
	lines := strings.Split(src, "\n")
	start, _ := findBlock(lines, "x-redes", name)
	if start < 0 {
		return src, false
	}
	if l, ok := flowSet(lines[start], field, value); ok {
		lines[start] = l
		return strings.Join(lines, "\n"), true
	}
	return setBlockField(src, "x-redes", name, field, value)
}

// redeFlow is a network as one line of YAML (the drawing's position kept).
func redeFlow(r *composex.Rede) string {
	var items []string
	if r.Subnet != "" {
		items = append(items, "subnet: "+r.Subnet)
	}
	if r.Gateway != "" {
		items = append(items, "gateway: "+r.Gateway)
	}
	if r.DHCP {
		if r.DHCPFrom != "" && r.DHCPTo != "" {
			items = append(items, "dhcp: "+r.DHCPFrom+"-"+r.DHCPTo)
		} else {
			items = append(items, "dhcp: true")
		}
	}
	if len(r.DNS) > 0 {
		items = append(items, "dns: ["+strings.Join(r.DNS, ", ")+"]")
	}
	if len(r.Pos) == 2 {
		items = append(items, fmt.Sprintf("pos: [%d, %d]", r.Pos[0], r.Pos[1]))
	}
	return "{" + strings.Join(items, ", ") + "}"
}

// putRede writes (or, with flow "", removes) the entry of a network: in
// place when it is there, else at the end of x-redes (made at the end of
// the first file when no file has it).
func putRede(texts map[string]string, files []string, name, flow string) {
	for _, f := range files {
		lines := strings.Split(texts[f], "\n")
		if start, end := findBlock(lines, "x-redes", name); start >= 0 {
			var res []string
			res = append(res, lines[:start]...)
			if flow != "" {
				res = append(res, strings.Repeat(" ", indentOf(lines[start]))+name+": "+flow)
			}
			res = append(res, lines[end:]...)
			texts[f] = strings.Join(res, "\n")
			return
		}
	}
	if flow == "" {
		return
	}
	for _, f := range files {
		lines := strings.Split(texts[f], "\n")
		sec := -1
		for i, l := range lines {
			if strings.HasPrefix(l, "x-redes:") {
				sec = i
				lines[i] = "x-redes:"
			}
		}
		if sec < 0 {
			continue
		}
		end, child := len(lines), 2
		for i := sec + 1; i < len(lines); i++ {
			t := strings.TrimSpace(lines[i])
			if t == "" || strings.HasPrefix(t, "#") {
				continue
			}
			if indentOf(lines[i]) == 0 {
				end = i
				break
			}
			if child == 2 && i == sec+1 {
				child = indentOf(lines[i])
			}
		}
		for end > sec+1 && strings.TrimSpace(lines[end-1]) == "" {
			end--
		}
		res := append([]string{}, lines[:end]...)
		res = append(res, strings.Repeat(" ", child)+name+": "+flow)
		res = append(res, lines[end:]...)
		texts[f] = strings.Join(res, "\n")
		return
	}
	f := files[0]
	texts[f] = strings.TrimRight(texts[f], "\n") + "\n\nx-redes:\n  " + name + ": " + flow + "\n"
}

// setIfaces writes the ports of a node (interfaces: of a VM, x-interfaces:
// of a container).
func setIfaces(texts map[string]string, files []string, node string, list []composex.VMIface) error {
	for _, f := range files {
		lines := strings.Split(texts[f], "\n")
		top, field := "x-vms", "interfaces"
		if start, _ := findBlock(lines, "x-vms", node); start < 0 {
			if start, _ := findBlock(lines, "services", node); start < 0 {
				continue
			}
			top, field = "services", "x-interfaces"
		}
		if out, ok := setBlockField(texts[f], top, node, field, ifacesFlow(list)); ok {
			texts[f] = out
			return nil
		}
	}
	return fmt.Errorf("%s não está nos arquivos do projeto", node)
}

// editLab changes the files of a lab: edit gets their texts, the result
// must still be a valid project before anything is written; then the
// relay gets the new table.
func (s *Server) editLab(ctx context.Context, h *hosts.Host, labID string, sync bool, edit func(p *composex.Project, files []string, texts map[string]string) error) (map[string]any, error) {
	l, err := s.findLab(ctx, h, labID)
	if err != nil {
		return nil, err
	}
	if len(l.Files) == 0 {
		return nil, errors.New("projeto sem arquivo compose")
	}
	s.composeEdit.Lock()
	defer s.composeEdit.Unlock()
	p, _, err := s.loadProject(ctx, h, l.Files)
	if err != nil {
		return nil, err
	}
	texts := map[string]string{}
	for _, f := range l.Files {
		b, err := h.ReadFile(ctx, f)
		if err != nil {
			return nil, err
		}
		texts[f] = string(b)
	}
	if err := edit(p, l.Files, texts); err != nil {
		return nil, err
	}
	env := map[string]string{}
	if b, err := h.ReadFile(ctx, path.Join(path.Dir(l.Files[0]), ".env")); err == nil {
		env = composex.ParseEnv(b)
	}
	var docs [][]byte
	for _, f := range l.Files {
		docs = append(docs, composex.Interpolate([]byte(texts[f]), env))
	}
	if _, err := composex.Parse(docs...); err != nil {
		return nil, err
	}
	for _, f := range l.Files {
		if err := h.WriteFile(ctx, f, []byte(texts[f])); err != nil {
			return nil, errors.New("não consegui gravar " + f + ": " + err.Error())
		}
	}
	resp := map[string]any{"ok": true, "live": false}
	if !sync {
		return resp, nil
	}
	if p, _, err := s.loadProject(ctx, h, l.Files); err == nil && vms.HasRelay(p) && (len(l.VMs) > 0 || len(p.Taps) > 0) {
		if vms.RelayMap(ctx, h, l.ID) != "" {
			out, err := vms.SyncRelay(ctx, h, l.ID, p)
			if err != nil {
				resp["error"] = fmt.Sprintf("a mudança foi salva no compose, mas não consegui aplicá-la ao vivo: %v", err)
			} else {
				resp["live"], resp["output"] = true, strings.TrimSpace(out)
			}
		}
	}
	return resp, nil
}

type redeLease struct {
	Rede string `json:"rede"`
	MAC  string `json:"mac"`
	IP   string `json:"ip"`
	Node string `json:"node"`
	Port string `json:"port"`
}

type redeInfo struct {
	*composex.Rede
	Server string `json:"server,omitempty"` // the DHCP's own address
	Ports  []struct {
		Node string   `json:"node"`
		Port int      `json:"port"`
		IPs  []string `json:"ips,omitempty"`
		DHCP bool     `json:"dhcp,omitempty"`
	} `json:"ports"`
}

// listRedes: the networks of the lab, the ports on each and the DHCP leases.
func (s *Server) listRedes(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	l, err := s.findLab(ctx, h, r.PathValue("lab"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	p, _, err := s.loadProject(ctx, h, l.Files)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	out := struct {
		Redes  []redeInfo  `json:"redes"`
		Leases []redeLease `json:"leases"`
	}{Redes: redesOf(p), Leases: []redeLease{}}
	if len(p.RedeNames) > 0 {
		if txt, err := vms.Run(ctx, h, "dhcp-leases", map[string]string{"GV_PROJECT": l.ID}); err == nil {
			for _, line := range strings.Split(txt, "\n") {
				if f := strings.Fields(line); len(f) >= 5 && p.Redes[f[0]] != nil {
					out.Leases = append(out.Leases, redeLease{f[0], f[1], f[2], f[3], f[4]})
				}
			}
		}
	}
	writeJSON(w, out)
}

// saveRede creates or changes a network (renaming it moves its ports).
func (s *Server) saveRede(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		Name    string   `json:"name"`
		Subnet  string   `json:"subnet"`
		Gateway string   `json:"gateway"`
		DHCP    bool     `json:"dhcp"`
		Range   string   `json:"range"` // the DHCP pool, a-b ("" : the default one)
		DNS     []string `json:"dns"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	old := r.PathValue("rede")
	if req.Name == "" {
		req.Name = old
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	resp, err := s.editLab(ctx, h, r.PathValue("lab"), true, func(p *composex.Project, files []string, texts map[string]string) error {
		nr := &composex.Rede{Name: req.Name, Subnet: strings.TrimSpace(req.Subnet), Gateway: strings.TrimSpace(req.Gateway), DHCP: req.DHCP}
		for _, d := range req.DNS {
			if d = strings.TrimSpace(d); d != "" {
				nr.DNS = append(nr.DNS, d)
			}
		}
		if a, b, ok := strings.Cut(req.Range, "-"); ok && req.DHCP {
			nr.DHCPFrom, nr.DHCPTo = strings.TrimSpace(a), strings.TrimSpace(b)
		}
		cur := p.Redes[old]
		if cur != nil {
			nr.Pos = cur.Pos
		} else if old != req.Name {
			return fmt.Errorf("a rede %s não está no projeto", old)
		}
		if cur != nil && old != req.Name {
			if p.Redes[req.Name] != nil {
				return fmt.Errorf("já existe a rede %s", req.Name)
			}
			putRede(texts, files, old, "")
			for _, v := range p.Nodes() {
				moved := false
				list := append([]composex.VMIface{}, v.Interfaces...)
				for i := range list {
					if list[i].Net == old {
						list[i].Net, moved = req.Name, true
					}
				}
				if moved {
					if err := setIfaces(texts, files, v.Name, list); err != nil {
						return err
					}
				}
			}
		}
		putRede(texts, files, req.Name, redeFlow(nr))
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, resp)
}

// deleteRede removes a network; its ports are left with no cable.
func (s *Server) deleteRede(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	name := r.PathValue("rede")
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	resp, err := s.editLab(ctx, h, r.PathValue("lab"), true, func(p *composex.Project, files []string, texts map[string]string) error {
		if p.Redes[name] == nil {
			return fmt.Errorf("a rede %s não está no projeto", name)
		}
		for _, v := range p.Nodes() {
			moved := false
			list := append([]composex.VMIface{}, v.Interfaces...)
			for i := range list {
				if list[i].Net == name {
					list[i], moved = composex.VMIface{}, true
				}
			}
			if moved {
				if err := setIfaces(texts, files, v.Name, list); err != nil {
					return err
				}
			}
		}
		putRede(texts, files, name, "")
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, resp)
}

// portSeen: how long ago (seconds) a port sent a frame through the cables'
// relay.
type portSeen struct {
	Node string `json:"node"`
	Port int    `json:"port"`
	Ago  int64  `json:"ago"`
}

// labLive: what changes by the second in the drawing, in one command on
// the machine: when each port last sent a frame (the link lights) and the
// addresses the networks' DHCP gave.
func (s *Server) labLive(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	id := r.PathValue("lab")
	if !labName.MatchString(id) {
		writeErr(w, http.StatusBadRequest, errors.New("lab inválido"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	txt, err := vms.Run(ctx, h, "lab-live", map[string]string{"GV_PROJECT": id})
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	leases := []redeLease{}
	var seen strings.Builder
	for _, line := range strings.Split(txt, "\n") {
		if rest, ok := strings.CutPrefix(line, "lease "); ok {
			if f := strings.Fields(rest); len(f) >= 5 {
				leases = append(leases, redeLease{f[0], f[1], f[2], f[3], f[4]})
			}
			continue
		}
		seen.WriteString(line + "\n")
	}
	writeJSON(w, map[string]any{"ports": parsePortsSeen(seen.String()), "leases": leases})
}

// parsePortsSeen reads the output of ports-seen: "now <epoch>" and then
// "node port <epoch>" lines (the host's clock on both).
func parsePortsSeen(txt string) []portSeen {
	out := []portSeen{}
	var now int64
	for _, line := range strings.Split(txt, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[0] == "now" {
			now, _ = strconv.ParseInt(f[1], 10, 64)
			continue
		}
		if len(f) != 3 || now == 0 {
			continue
		}
		port, err1 := strconv.Atoi(f[1])
		at, err2 := strconv.ParseInt(f[2], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		out = append(out, portSeen{f[0], port, max(0, now-at)})
	}
	return out
}

// redesOf: the networks of a project with the ports on each.
func redesOf(p *composex.Project) []redeInfo {
	out := []redeInfo{}
	for _, n := range p.RedeNames {
		ri := redeInfo{Rede: p.Redes[n]}
		if ri.DHCP {
			ri.Server = ri.DHCPServer()
		}
		for _, v := range p.Nodes() {
			for i, f := range v.Interfaces {
				if f.Net == n {
					ri.Ports = append(ri.Ports, struct {
						Node string   `json:"node"`
						Port int      `json:"port"`
						IPs  []string `json:"ips,omitempty"`
						DHCP bool     `json:"dhcp,omitempty"`
					}{v.Name, i, f.IPs, f.DHCP})
				}
			}
		}
		out = append(out, ri)
	}
	return out
}

// labDrawing: what the drawing of a lab shows, read once: the nodes with
// their cables and the networks (the leases come with the live state).
func (s *Server) labDrawing(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	l, err := s.findLab(ctx, h, r.PathValue("lab"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	p, _, err := s.loadProject(ctx, h, l.Files)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]any{"vms": cablesOf(p), "redes": redesOf(p)})
}
