package server

// The cables of a network lab: the extra interfaces of each VM and what
// each one connects to (a port of another VM, a bridge of the machine, or
// nothing). The page reads them from the compose and saves them back,
// rewriting only the interfaces: line of each VM.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"gantry/internal/composex"
	"gantry/internal/hosts"
	"gantry/internal/vms"
)

type cableVM struct {
	Name       string             `json:"name"`
	Interfaces []composex.VMIface `json:"interfaces"`
	NIC        string             `json:"nic,omitempty"`
	Management bool               `json:"management"`
	Kind       string             `json:"kind,omitempty"`
	Pos        []int              `json:"pos,omitempty"`
	Platform   string             `json:"platform,omitempty"` // a Dynamips router's model
	Ports      []string           `json:"ports,omitempty"`    // a Dynamips router's port names (its adapters)
	Icon       string             `json:"icon"`               // how it is drawn
	IconSet    bool               `json:"iconSet,omitempty"`  // chosen in the file (not the default)
	VPCSAddr   string             `json:"vpcsAddr,omitempty"` // a VPCS's address in its startup.vpc
}

// vpcsAddr: the address a VPCS gets from its startup configuration (the
// last "ip" line wins, as in VPCS): "10.0.0.10/24 gw 10.0.0.1", "DHCP", or
// "" when there is none.
func vpcsAddr(cfg string) string {
	out := ""
	for _, l := range strings.Split(cfg, "\n") {
		f := strings.Fields(l)
		if len(f) < 2 || f[0] != "ip" {
			continue
		}
		a := f[1:]
		if a[0] == "dhcp" {
			out = "DHCP"
			continue
		}
		if net.ParseIP(strings.SplitN(a[0], "/", 2)[0]) == nil {
			continue
		}
		addr, rest := a[0], a[1:]
		// "ip 10.0.0.10 255.255.255.0 10.0.0.1" or "ip 10.0.0.10 24 10.0.0.1"
		if !strings.Contains(addr, "/") && len(rest) > 0 {
			if m := net.ParseIP(rest[0]); m != nil && m.To4() != nil && strings.HasPrefix(rest[0], "255.") {
				ones, _ := net.IPMask(m.To4()).Size()
				addr += "/" + strconv.Itoa(ones)
				rest = rest[1:]
			} else if n, err := strconv.Atoi(rest[0]); err == nil && n > 0 && n <= 32 {
				addr += "/" + rest[0]
				rest = rest[1:]
			}
		}
		if len(rest) > 0 && net.ParseIP(rest[0]) != nil {
			addr += " gw " + rest[0]
		}
		out = addr
	}
	return out
}

func (s *Server) listCables(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
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
	out := []cableVM{}
	for _, v := range p.Nodes() {
		ifs := v.Interfaces
		if ifs == nil {
			ifs = []composex.VMIface{}
		}
		c := cableVM{Name: v.Name, Interfaces: ifs, NIC: v.NIC, Management: !v.NoManagement, Kind: v.Kind, Pos: v.Pos, Platform: v.Platform, Icon: composex.IconOf(v), IconSet: v.Icon != ""}
		if v.Kind == "dyn" {
			c.Ports = vms.DynPortNames(v)
		}
		if v.Kind == "vpcs" {
			c.VPCSAddr = vpcsAddr(v.Config)
		}
		out = append(out, c)
	}
	writeJSON(w, out)
}

// ifacesFlow writes the ports as one line of YAML.
func ifacesFlow(list []composex.VMIface) string {
	if len(list) == 0 {
		return ""
	}
	var parts []string
	for _, f := range list {
		ip := ""
		switch {
		case f.DHCP:
			ip = ", ip: dhcp"
		case len(f.IPs) == 1:
			ip = ", ip: " + f.IPs[0]
		case len(f.IPs) > 1:
			ip = ", ip: [" + strings.Join(f.IPs, ", ") + "]"
		}
		switch {
		case f.Link != "" && ip == "":
			parts = append(parts, f.Link)
		case f.Link != "":
			parts = append(parts, "{link: "+f.Link+ip+"}")
		case f.Net != "":
			parts = append(parts, "{rede: "+f.Net+ip+"}")
		case f.Bridge != "":
			parts = append(parts, "{bridge: "+f.Bridge+"}")
		default:
			parts = append(parts, "none")
		}
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func (s *Server) saveCables(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		VMs map[string][]composex.VMIface `json:"vms"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	l, err := s.findLab(ctx, h, r.PathValue("lab"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if len(l.Files) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("projeto sem arquivo compose"))
		return
	}
	s.composeEdit.Lock()
	defer s.composeEdit.Unlock()
	texts := map[string]string{}
	for _, f := range l.Files {
		b, err := h.ReadFile(ctx, f)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err)
			return
		}
		texts[f] = string(b)
	}
	for vm, list := range req.VMs {
		done := false
		for _, f := range l.Files {
			lines := strings.Split(texts[f], "\n")
			// A VM (x-vms) or a container on cables (services: x-interfaces).
			top, field := "x-vms", "interfaces"
			if start, _ := findBlock(lines, "x-vms", vm); start < 0 {
				if start, _ := findBlock(lines, "services", vm); start < 0 {
					continue
				}
				top, field = "services", "x-interfaces"
			}
			out, ok := setBlockField(texts[f], top, vm, field, ifacesFlow(list))
			if ok {
				texts[f], done = out, true
				break
			}
		}
		if !done {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("a VM %s não está nos arquivos do projeto", vm))
			return
		}
	}
	// The new files must still make a valid project (each link with two
	// ends) before anything is written.
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
	for _, f := range l.Files {
		if err := h.WriteFile(ctx, f, []byte(texts[f])); err != nil {
			writeErr(w, http.StatusBadGateway, errors.New("não consegui gravar "+f+": "+err.Error()))
			return
		}
	}
	// The cables change live: the relay gets the new table. A port that
	// changed between a cable and a bridge still needs the VM restarted.
	resp := map[string]any{"ok": true, "live": false}
	if p, _, err := s.loadProject(ctx, h, l.Files); err == nil && vms.HasRelay(p) && (len(l.VMs) > 0 || len(p.Taps) > 0) {
		out, err := vms.SyncRelay(ctx, h, l.ID, p)
		if err != nil {
			resp["error"] = fmt.Sprintf("os cabos foram salvos no compose, mas não consegui trocá-los ao vivo: %v", err)
		} else {
			resp["live"], resp["output"] = true, strings.TrimSpace(out)
		}
	}
	writeJSON(w, resp)
}

// setPositions writes where each node is drawn (pos: of a VM, x-pos: of a
// container on cables) in the files of the project; a nil position
// removes it. Nodes not in the files are an error.
func setPositions(texts map[string]string, files []string, pos map[string][]int) error {
	for node, p := range pos {
		val := ""
		if len(p) == 2 {
			val = fmt.Sprintf("[%d, %d]", p[0], p[1])
		} else if p != nil {
			return fmt.Errorf("posição de %s: use [x, y]", node)
		}
		done := false
		for _, f := range files {
			if start, _ := findBlock(strings.Split(texts[f], "\n"), "x-redes", node); start >= 0 {
				// a lab network (its cloud on the drawing)
				if out, ok := setRedeField(texts[f], node, "pos", val); ok {
					texts[f], done = out, true
					break
				}
			}
		}
		for _, f := range files {
			if done {
				break
			}
			lines := strings.Split(texts[f], "\n")
			top, field := "x-vms", "pos"
			if start, _ := findBlock(lines, "x-vms", node); start < 0 {
				if start, _ := findBlock(lines, "services", node); start < 0 {
					continue
				}
				top, field = "services", "x-pos"
			}
			if out, ok := setBlockField(texts[f], top, node, field, val); ok {
				texts[f], done = out, true
				break
			}
		}
		if !done {
			return fmt.Errorf("%s não está nos arquivos do projeto", node)
		}
	}
	return nil
}

// savePositions keeps the drawing of the topology in the compose.
func (s *Server) savePositions(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		Pos map[string][]int `json:"pos"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	l, err := s.findLab(ctx, h, r.PathValue("lab"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	s.composeEdit.Lock()
	defer s.composeEdit.Unlock()
	texts := map[string]string{}
	for _, f := range l.Files {
		b, err := h.ReadFile(ctx, f)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err)
			return
		}
		texts[f] = string(b)
	}
	if err := setPositions(texts, l.Files, req.Pos); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// Never write a file that stopped being a valid compose.
	var docs [][]byte
	for _, f := range l.Files {
		docs = append(docs, composex.Interpolate([]byte(texts[f]), map[string]string{}))
	}
	if _, err := composex.Parse(docs...); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("a posição não foi salva: o arquivo ficaria inválido (%v)", err))
		return
	}
	for _, f := range l.Files {
		if err := h.WriteFile(ctx, f, []byte(texts[f])); err != nil {
			writeErr(w, http.StatusBadGateway, errors.New("não consegui gravar "+f+": "+err.Error()))
			return
		}
	}
	writeJSON(w, map[string]any{"ok": true})
}

// saveIcons writes the icon of nodes (icon: of a VM, x-icon: of a
// container); "" goes back to the default one.
func (s *Server) saveIcons(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		Icons map[string]string `json:"icons"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	resp, err := s.editLab(ctx, h, r.PathValue("lab"), false, func(p *composex.Project, files []string, texts map[string]string) error {
		for node, icon := range req.Icons {
			if icon != "" {
				ok := false
				for _, ic := range composex.Icons {
					ok = ok || ic == icon
				}
				if !ok {
					return fmt.Errorf("ícone desconhecido %q", icon)
				}
			}
			done := false
			for _, f := range files {
				lines := strings.Split(texts[f], "\n")
				top, field := "x-vms", "icon"
				if start, _ := findBlock(lines, "x-vms", node); start < 0 {
					if start, _ := findBlock(lines, "services", node); start < 0 {
						continue
					}
					top, field = "services", "x-icon"
				}
				if out, ok := setBlockField(texts[f], top, node, field, icon); ok {
					texts[f], done = out, true
					break
				}
			}
			if !done {
				return fmt.Errorf("%s não está nos arquivos do projeto", node)
			}
		}
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, resp)
}
