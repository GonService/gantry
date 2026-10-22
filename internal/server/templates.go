package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"sort"

	"gantry/internal/composex"
	"gantry/internal/i18n"
	"gantry/internal/templates"
)

func (s *Server) tpl() *templates.Store {
	s.tplOnce.Do(func() {
		s.tpls = templates.Open(filepath.Join(filepath.Dir(s.cfg.Path()), "templates.json"))
	})
	return s.tpls
}

func (s *Server) listTemplates(w http.ResponseWriter, r *http.Request) {
	list := s.tpl().List()
	for i := range list {
		list[i] = builtinInLang(list[i])
	}
	writeJSON(w, list)
}

// builtinInLang: the names of Gantry's own templates in the app's language
// (the user's templates keep the name the user gave).
func builtinInLang(t templates.Template) templates.Template {
	if t.Builtin {
		t.Name = i18n.T(t.Name)
	}
	return t
}

func (s *Server) getTemplate(w http.ResponseWriter, r *http.Request) {
	t, ok := s.tpl().Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, errors.New("modelo não encontrado"))
		return
	}
	writeJSON(w, builtinInLang(t))
}

func (s *Server) saveTemplate(w http.ResponseWriter, r *http.Request) {
	var t templates.Template
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&t); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	t.ID = r.PathValue("id") // empty on POST: a new one
	if _, err := composex.Parse(composex.Interpolate([]byte(t.YAML), defaultsOf(t.YAML))); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("YAML inválido: "+err.Error()))
		return
	}
	saved, err := s.tpl().Save(t)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, templates.ErrBuiltin) {
			status = http.StatusForbidden
		}
		writeErr(w, status, err)
		return
	}
	writeJSON(w, saved)
}

func (s *Server) deleteTemplate(w http.ResponseWriter, r *http.Request) {
	if err := s.tpl().Delete(r.PathValue("id")); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// defaultsOf fills each variable with its default, or a placeholder, so a
// template can be checked before anyone gives the values.
func defaultsOf(yaml string) map[string]string {
	env := map[string]string{}
	for _, v := range composex.Vars([]byte(yaml)) {
		env[v.Name] = v.Default
		if env[v.Name] == "" {
			env[v.Name] = "x"
		}
	}
	return env
}

// checkTemplate validates a YAML and says what it creates, for the editor.
func (s *Server) checkTemplate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		YAML string            `json:"yaml"`
		Env  map[string]string `json:"env"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	vars := composex.Vars([]byte(req.YAML))
	if vars == nil {
		vars = []composex.Var{}
	}
	env := defaultsOf(req.YAML)
	for k, v := range req.Env {
		env[k] = v
	}
	p, err := composex.Parse(composex.Interpolate([]byte(req.YAML), env))
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error(), "vars": vars})
		return
	}
	type svc struct {
		Name    string   `json:"name"`
		Image   string   `json:"image"`
		Runtime string   `json:"runtime,omitempty"`
		Ports   []string `json:"ports"`
	}
	var svcs []svc
	for _, n := range p.Names {
		x := p.Services[n]
		v := svc{Name: n, Image: x.Image, Runtime: p.RuntimeOf(n, ""), Ports: []string{}}
		if x.Build && v.Image == "" {
			v.Image = "(build)"
		}
		for _, pt := range x.Ports {
			if pt.Host != "" {
				v.Ports = append(v.Ports, pt.Host+"→"+pt.Container)
			} else {
				v.Ports = append(v.Ports, pt.Container)
			}
		}
		svcs = append(svcs, v)
	}
	vms := []map[string]any{}
	for _, v := range p.VMs {
		vms = append(vms, map[string]any{"name": v.Name, "image": v.Image, "ports": v.Ports})
	}
	nets := []string{}
	for k := range p.Networks {
		nets = append(nets, k)
	}
	sort.Strings(nets)
	if svcs == nil {
		svcs = []svc{}
	}
	writeJSON(w, map[string]any{"ok": len(svcs)+len(vms) > 0, "error": map[bool]string{true: "", false: "o modelo não tem serviços nem VMs"}[len(svcs)+len(vms) > 0],
		"vars": vars, "services": svcs, "vms": vms, "networks": nets, "mixed": p.Gantry, "uses": p.Uses})
}

// convertRun turns docker/podman/nerdctl run commands into a compose file.
func (s *Server) convertRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Commands string `json:"commands"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 256<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	c, err := composex.FromCommands(req.Commands)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, c)
}
