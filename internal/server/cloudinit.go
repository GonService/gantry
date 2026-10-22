package server

// The cloud-init of the VMs while a project is made: what each VM will get
// (the cloud-config Gantry builds) and the part the user writes, which
// goes to a file of the project (cloud-init: ./<vm>.cloud-init.yaml).

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"

	"github.com/goccy/go-yaml"

	"gantry/internal/composex"
	"gantry/internal/vms"
)

type cloudInitVM struct {
	Name string `json:"name"`
	// Extra is the cloud-config the user writes for the VM (merged into
	// what Gantry builds), File where it is ("" when inline or none).
	Extra string `json:"extra"`
	File  string `json:"file,omitempty"`
	// UserData is the whole cloud-config the VM gets on its first boot.
	UserData string `json:"userData,omitempty"`
	Error    string `json:"error,omitempty"`
	// Installer: a VM booted from an ISO has no cloud-init.
	Installer bool `json:"installer,omitempty"`
}

// projectFile finds a file referenced by the compose among the files of
// the project being made.
func projectFile(files map[string]string, ref string) (string, bool) {
	c, ok := files[path.Clean(strings.TrimPrefix(ref, "./"))]
	return c, ok
}

func cloudInitPreview(compose, env string, files map[string]string) ([]cloudInitVM, error) {
	p, err := composex.Parse(composex.Interpolate([]byte(compose), composex.ParseEnv([]byte(env))))
	if err != nil {
		return nil, err
	}
	out := []cloudInitVM{}
	for _, v := range p.VMs {
		x := cloudInitVM{Name: v.Name, File: v.CloudInitFile, Installer: v.ISO != "" || v.NoCloudInit}
		if v.CloudInitFile != "" {
			if c, ok := projectFile(files, v.CloudInitFile); ok {
				v.CloudInit = c
			} else {
				v.CloudInitErr = fmt.Sprintf("%s não está entre os arquivos do projeto", v.CloudInitFile)
			}
		}
		x.Extra = v.CloudInit
		if v.Nix != nil {
			if c, ok := projectFile(files, v.Nix.Module); ok {
				v.Nix.Content = c
			} else {
				v.Nix.ReadErr = fmt.Sprintf("%s não está entre os arquivos do projeto", v.Nix.Module)
			}
		}
		if !x.Installer {
			ud, err := vms.UserData(v)
			if err != nil {
				x.Error = err.Error()
			} else {
				x.UserData = strings.ReplaceAll(ud, "__GANTRY_PUBKEY__", "<gantry-ssh-key>")
			}
		}
		out = append(out, x)
	}
	return out, nil
}

// cloudInit answers what each VM of a compose will get from cloud-init.
func (s *Server) cloudInit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Compose string            `json:"compose"`
		Env     string            `json:"env"`
		Files   map[string]string `json:"files"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	list, err := cloudInitPreview(req.Compose, req.Env, req.Files)
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("compose inválido: %v", err))
		return
	}
	writeJSON(w, list)
}

// cloudInitEdit puts the user's cloud-config of a VM in its file and
// points the compose to it (an empty text takes the field out).
func (s *Server) cloudInitEdit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Compose string `json:"compose"`
		VM      string `json:"vm"`
		Text    string `json:"text"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	p, err := composex.Parse([]byte(req.Compose))
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("compose inválido: %v", err))
		return
	}
	v := p.VM(req.VM)
	if v == nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("a VM %s não está no compose", req.VM))
		return
	}
	text := strings.TrimRight(strings.ReplaceAll(req.Text, "\r\n", "\n"), " \n\t")
	if text != "" {
		var cfg map[string]any
		if err := yaml.Unmarshal([]byte(text), &cfg); err != nil {
			msg, _, _ := strings.Cut(err.Error(), "\n")
			writeErr(w, http.StatusBadRequest, fmt.Errorf("cloud-init de %s: %v", req.VM, msg))
			return
		}
		if cfg == nil && strings.TrimLeft(text, "#") == text {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("cloud-init de %s: use chave: valor (cloud-config)", req.VM))
			return
		}
		text += "\n"
	}
	file := v.CloudInitFile
	if file == "" {
		file = "./" + req.VM + ".cloud-init.yaml"
	}
	value := file
	if text == "" {
		value = ""
	}
	compose, ok := setBlockField(req.Compose, "x-vms", req.VM, "cloud-init", value)
	if !ok {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("não achei a VM %s no compose", req.VM))
		return
	}
	writeJSON(w, map[string]any{"compose": compose, "file": path.Clean(strings.TrimPrefix(file, "./")), "content": text})
}
