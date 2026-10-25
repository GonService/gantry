package server

// The equipment of a network lab inside a project package (.gantrylab):
// the configuration of each IOL node (NVRAM) and, when asked, the disks of
// the VMs, in the folder .gantrylab/ of the package. Importing the package
// puts them back in the nodes before the lab goes up.

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gantry/internal/hosts"
	"gantry/internal/labs"
	"gantry/internal/vms"
)

const labPackDir = ".gantrylab"

// packNode: the name of a node in a package, as a compose allows it (R1,
// SW01, pc-1): it becomes a folder name, so no slash and no leading dot.
var packNode = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)

type labPackNode struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"` // iol, dyn or vm
	NVRAM bool   `json:"nvram,omitempty"`
	Disk  bool   `json:"disk,omitempty"`
}

type labPackManifest struct {
	Format  int           `json:"format"`
	Lab     string        `json:"lab"`
	Created int64         `json:"created"`
	Nodes   []labPackNode `json:"nodes"`
}

type labPackOpts struct {
	NVRAM bool // the configuration of the IOL nodes
	Disks bool // the disks of the VMs
	Write bool // write memory on the running nodes first
}

// collectLabEquipment copies what the package carries of the nodes to tmp
// (nvram/<node>.tar, discos/<node>.qcow2) and returns the manifest; nil
// when there is nothing to carry.
func (s *Server) collectLabEquipment(ctx context.Context, h *hosts.Host, l labs.Lab, o labPackOpts, tmp string, out io.Writer) (*labPackManifest, error) {
	man := &labPackManifest{Format: 1, Lab: l.ID, Created: time.Now().Unix()}
	for _, v := range stateNodes(l) {
		n := labPackNode{Name: v.Name, Kind: nodeKind(v)}
		if o.Write && v.State == "running" {
			if r, err := vms.Run(ctx, h, "console-save", vmEnv(l.ID, v.Name)); err == nil {
				fmt.Fprintln(out, strings.TrimSpace(r))
			}
		}
		safe := safeName.ReplaceAllString(v.Name, "_")
		switch {
		case n.Kind != "vm" && o.NVRAM:
			if err := os.MkdirAll(filepath.Join(tmp, "nvram"), 0o700); err != nil {
				return nil, err
			}
			if _, err := s.fetchNVRAM(ctx, h, l.ID, v.Name, filepath.Join(tmp, "nvram", safe+".tar")); err != nil {
				return nil, fmt.Errorf("nó %s: %v", v.Name, err)
			}
			n.NVRAM = true
		case n.Kind == "vm" && o.Disks:
			if err := os.MkdirAll(filepath.Join(tmp, "discos"), 0o700); err != nil {
				return nil, err
			}
			r, err := vms.Run(ctx, h, "export", vmEnv(l.ID, v.Name))
			if err != nil {
				return nil, fmt.Errorf("VM %s: %v", v.Name, err)
			}
			lines := strings.Split(strings.TrimSpace(r), "\n")
			src := strings.TrimSpace(lines[len(lines)-1])
			f, err := os.Create(filepath.Join(tmp, "discos", safe+".qcow2"))
			if err != nil {
				return nil, err
			}
			_, err = s.copyFromHost(ctx, h, src, f)
			f.Close()
			_, _ = vms.Run(context.Background(), h, "export-clean", vmEnv(l.ID, v.Name))
			if err != nil {
				return nil, fmt.Errorf("VM %s: %v", v.Name, err)
			}
			n.Disk = true
		default:
			continue
		}
		man.Nodes = append(man.Nodes, n)
	}
	if len(man.Nodes) == 0 {
		return nil, nil
	}
	b, _ := json.MarshalIndent(man, "", "  ")
	return man, os.WriteFile(filepath.Join(tmp, "manifest.json"), b, 0o600)
}

// withLabExtras copies the project package src (.tar.gz) to dst adding the
// files of dir (the equipment) under .gantrylab/.
func withLabExtras(src io.Reader, dst io.Writer, dir string) error {
	gin, err := gzip.NewReader(src)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gin)
	gz := gzip.NewWriter(dst)
	tw := tar.NewWriter(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := io.Copy(tw, tr); err != nil {
			return err
		}
	}
	err = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || p == dir {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = labPackDir + "/" + filepath.ToSlash(rel)
		if info.IsDir() {
			hdr.Name += "/"
			return tw.WriteHeader(hdr)
		}
		hdr.Mode = 0o600
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
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

// applyLabExtras puts the equipment of an unpacked package (dir/.gantrylab)
// in the nodes of project on the host, then removes the folder.
func (s *Server) applyLabExtras(ctx context.Context, h *hosts.Host, dir, project string) (string, error) {
	base := path.Join(dir, labPackDir)
	b, err := h.ReadFile(ctx, path.Join(base, "manifest.json"))
	if err != nil {
		return "", nil // a plain project package
	}
	defer func() { _, _ = h.Run(context.Background(), "", []string{"rm", "-rf", base}, nil) }()
	var man labPackManifest
	if err := json.Unmarshal(b, &man); err != nil {
		return "", fmt.Errorf("pacote: manifest do lab inválido: %v", err)
	}
	var log strings.Builder
	for _, n := range man.Nodes {
		if !packNode.MatchString(n.Name) {
			return log.String(), fmt.Errorf("pacote: nó com nome inválido (%q)", n.Name)
		}
		// A project of the same name already here keeps its nodes.
		if o, _ := h.Run(ctx, "", []string{"sh", "-c", `d="${XDG_STATE_HOME:-$HOME/.local/state}/gantry/vms/$1/$2"; { [ -f "$d/iol" ] || [ -f "$d/dyn" ] || [ -f "$d/vpcs" ] || [ -f "$d/disk.qcow2" ]; } && echo sim`, "sh", project, n.Name}, nil); strings.TrimSpace(o) == "sim" {
			log.WriteString(fmt.Sprintf("%s: já existe nesta máquina (projeto %s); a configuração do pacote não foi aplicada\n", n.Name, project))
			continue
		}
		safe := safeName.ReplaceAllString(n.Name, "_")
		var o string
		switch {
		case n.NVRAM:
			o, err = vms.Run(ctx, h, "nvram-unpack", vmEnv(project, n.Name, "GV_FILE", path.Join(base, "nvram", safe+".tar")))
		case n.Disk:
			o, err = vms.Run(ctx, h, "import", vmEnv(project, n.Name, "GV_FILE", path.Join(base, "discos", safe+".qcow2")))
		default:
			continue
		}
		log.WriteString(strings.TrimSpace(o) + "\n")
		if err != nil {
			return log.String(), fmt.Errorf("%s: %v", n.Name, err)
		}
	}
	return log.String(), nil
}
