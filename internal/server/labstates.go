package server

// The states of a network lab: the configuration of every node saved
// together (a snapshot of each VM, the NVRAM of each IOL node), to repeat
// an exercise; and starting the nodes again from nothing.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gantry/internal/hosts"
	"gantry/internal/labs"
	"gantry/internal/vms"
)

var stateNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,47}$`)

// vmStateDir is the folder of a node on the host (created if missing).
func vmStateDir(ctx context.Context, h *hosts.Host, lab, node string) (string, error) {
	dir, err := h.Run(ctx, "", []string{"sh", "-c", `d="${XDG_STATE_HOME:-$HOME/.local/state}/gantry/vms/$1/$2"; mkdir -p "$d" && printf '%s' "$d"`, "sh", lab, node}, nil)
	return strings.TrimSpace(dir), err
}

// fetchNVRAM copies the configuration files of an IOL node to dest (a tar).
func (s *Server) fetchNVRAM(ctx context.Context, h *hosts.Host, lab, node, dest string) (int64, error) {
	o, err := vms.Run(ctx, h, "nvram-pack", vmEnv(lab, node))
	if err != nil {
		return 0, err
	}
	lines := strings.Split(strings.TrimSpace(o), "\n")
	src := strings.TrimSpace(lines[len(lines)-1])
	f, err := os.Create(dest)
	if err != nil {
		return 0, err
	}
	n, err := s.copyFromHost(ctx, h, src, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	_, _ = h.Run(context.Background(), "", []string{"rm", "-f", src}, nil)
	return n, err
}

// placeNVRAM puts the configuration files (a tar) in an IOL node of the
// host; a running node is restarted with them.
func (s *Server) placeNVRAM(ctx context.Context, h *hosts.Host, lab, node string, r io.Reader) (string, error) {
	dir, err := vmStateDir(ctx, h, lab, node)
	if err != nil {
		return "", err
	}
	tmp := dir + "/nvram.import.tar"
	if err := s.copyToHost(ctx, h, r, tmp); err != nil {
		return "", err
	}
	return vms.Run(ctx, h, "nvram-unpack", vmEnv(lab, node, "GV_FILE", tmp))
}

type labState struct {
	Name  string           `json:"name"`
	When  int64            `json:"when"`  // the newest node's copy
	Nodes map[string]int64 `json:"nodes"` // node -> when it was saved
}

type stateNode struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"` // iol, dyn or vm
	Running bool   `json:"running"`
}

// stateNodes are the nodes of the lab that exist on the machine.
func stateNodes(l labs.Lab) []labs.VM {
	var out []labs.VM
	for _, v := range l.VMs {
		if v.State != "missing" {
			out = append(out, v)
		}
	}
	return out
}

func nodeKind(v labs.VM) string {
	switch v.Accel {
	case "iol":
		return "iol"
	case "dynamips":
		return "dyn"
	case "vpcs":
		return "vpcs"
	}
	return "vm"
}

// progAccel: a node that is a program keeping its configuration in NVRAM
// files (IOL, Dynamips), not a VM with a disk.
func progAccel(accel string) bool { return accel == "iol" || accel == "dynamips" }

func (s *Server) listLabStates(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	l, err := s.findLab(ctx, h, r.PathValue("lab"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	by := map[string]*labState{}
	nodes := []stateNode{}
	for _, v := range stateNodes(l) {
		nodes = append(nodes, stateNode{Name: v.Name, Kind: nodeKind(v), Running: v.State == "running"})
		o, err := vms.Run(ctx, h, "state-list", vmEnv(l.ID, v.Name))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(strings.TrimSpace(o), "\n") {
			f := strings.Fields(line)
			if len(f) < 1 {
				continue
			}
			var when int64
			if len(f) > 1 {
				when, _ = strconv.ParseInt(f[1], 10, 64)
			}
			st := by[f[0]]
			if st == nil {
				st = &labState{Name: f[0], Nodes: map[string]int64{}}
				by[f[0]] = st
			}
			st.Nodes[v.Name] = when
			if when > st.When {
				st.When = when
			}
		}
	}
	out := []labState{}
	for _, st := range by {
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].When > out[j].When })
	writeJSON(w, map[string]any{"states": out, "nodes": nodes})
}

// eachNode runs a vmctl command on the nodes, noting what each answered.
func eachNode(ctx context.Context, h *hosts.Host, l labs.Lab, nodes []labs.VM, cmd string, extra ...string) (string, error) {
	var b strings.Builder
	var failed []string
	for _, v := range nodes {
		o, err := vms.Run(ctx, h, cmd, vmEnv(l.ID, v.Name, extra...))
		if strings.TrimSpace(o) != "" {
			b.WriteString(strings.TrimSpace(o) + "\n")
		}
		if err != nil {
			b.WriteString(v.Name + ": " + err.Error() + "\n")
			failed = append(failed, v.Name)
		}
	}
	if len(failed) > 0 {
		return b.String(), fmt.Errorf("falhou em %s", strings.Join(failed, ", "))
	}
	return b.String(), nil
}

func (s *Server) saveLabState(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		Name  string `json:"name"`
		Write bool   `json:"write"` // write memory on the consoles first
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil || !stateNameRe.MatchString(req.Name) {
		writeErr(w, http.StatusBadRequest, errors.New("nome de estado inválido (use letras, números, ponto, - e _)"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Minute)
	defer cancel()
	l, err := s.findLab(ctx, h, r.PathValue("lab"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	nodes := stateNodes(l)
	if len(nodes) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("o lab ainda não tem VMs nem nós criados; suba o lab antes de salvar o estado"))
		return
	}
	var out strings.Builder
	if req.Write {
		var running []labs.VM
		for _, v := range nodes {
			if v.State == "running" {
				running = append(running, v)
			}
		}
		o, err := eachNode(ctx, h, l, running, "console-save")
		out.WriteString(o)
		if err != nil {
			out.WriteString("aviso: " + err.Error() + "\n")
		}
	}
	o, err := eachNode(ctx, h, l, nodes, "state-save", "GV_STATE", req.Name)
	out.WriteString(o)
	resp := map[string]any{"ok": err == nil, "output": out.String()}
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSON(w, resp)
}

// labStateAction restores or deletes a saved state on the nodes that have it.
func (s *Server) labStateAction(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	name := r.PathValue("name")
	if !stateNameRe.MatchString(name) {
		writeErr(w, http.StatusBadRequest, errors.New("nome de estado inválido"))
		return
	}
	cmd := "state-delete"
	if r.Method == http.MethodPost {
		cmd = "state-restore"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Minute)
	defer cancel()
	l, err := s.findLab(ctx, h, r.PathValue("lab"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	var with, without []labs.VM
	for _, v := range stateNodes(l) {
		o, _ := vms.Run(ctx, h, "state-list", vmEnv(l.ID, v.Name))
		has := false
		for _, line := range strings.Split(o, "\n") {
			if f := strings.Fields(line); len(f) > 0 && f[0] == name {
				has = true
			}
		}
		if has {
			with = append(with, v)
		} else {
			without = append(without, v)
		}
	}
	if len(with) == 0 {
		writeErr(w, http.StatusNotFound, fmt.Errorf("nenhum nó tem o estado %s", name))
		return
	}
	o, err := eachNode(ctx, h, l, with, cmd, "GV_STATE", name)
	if cmd == "state-restore" && len(without) > 0 {
		var names []string
		for _, v := range without {
			names = append(names, v.Name)
		}
		o += fmt.Sprintf("sem este estado (ficaram como estão): %s\n", strings.Join(names, ", "))
	}
	resp := map[string]any{"ok": err == nil, "output": o}
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSON(w, resp)
}

// zeroLab starts the IOL nodes again without configuration and, when asked,
// deletes the VMs (their disks come again from the image at the next up).
func (s *Server) zeroLab(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		VMs bool `json:"vms"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req)
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Minute)
	defer cancel()
	l, err := s.findLab(ctx, h, r.PathValue("lab"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	var iol, vm []labs.VM
	for _, v := range stateNodes(l) {
		if nodeKind(v) != "vm" {
			iol = append(iol, v)
		} else {
			vm = append(vm, v)
		}
	}
	o, err := eachNode(ctx, h, l, iol, "state-zero")
	if err == nil && req.VMs && len(vm) > 0 {
		var o2 string
		o2, err = eachNode(ctx, h, l, vm, "rm")
		o += o2
		if err == nil {
			o += "as VMs voltam da imagem quando o lab subir de novo\n"
		}
	}
	resp := map[string]any{"ok": err == nil, "output": o}
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSON(w, resp)
}
