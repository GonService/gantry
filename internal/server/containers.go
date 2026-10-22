package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"gantry/internal/engine"
	"gantry/internal/hosts"
)

// createWithPull creates a container, pulling the image first when asked
// or when the engine does not have it.
func createWithPull(ctx context.Context, eng *engine.Client, spec engine.ContainerSpec, always bool) (id string, pulled bool, err error) {
	if always {
		if err := eng.Pull(ctx, spec.Image, nil); err != nil {
			return "", false, fmt.Errorf("não consegui baixar %s: %v", spec.Image, err)
		}
		pulled = true
	}
	id, err = eng.Create(ctx, spec)
	var ae *engine.APIError
	if !pulled && errors.As(err, &ae) && ae.Status == http.StatusNotFound && strings.Contains(strings.ToLower(ae.Message), "image") {
		if perr := eng.Pull(ctx, spec.Image, nil); perr != nil {
			return "", false, fmt.Errorf("não consegui baixar %s: %v", spec.Image, perr)
		}
		pulled = true
		id, err = eng.Create(ctx, spec)
	}
	return id, pulled, err
}

// getContainer returns the full inspect plus the spec the edit form starts from.
func (s *Server) getContainer(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	eng, ok := s.reqEngine(w, r, h)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	raw, err := eng.InspectRaw(ctx, r.PathValue("id"))
	if err != nil {
		writeErr(w, engineStatus(err), err)
		return
	}
	spec, err := engine.SpecFromJSON(raw)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]any{"inspect": raw, "spec": spec})
}

// recreate replaces a container with a new one built from the spec, which
// is how Portainer edits containers. The old one is kept aside under a
// temporary name until the new one is up, and comes back on failure.
func (s *Server) recreate(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		Spec  engine.ContainerSpec `json:"spec"`
		Pull  bool                 `json:"pull"`
		Start *bool                `json:"start"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 256<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	spec := req.Spec
	spec.Image = strings.TrimSpace(spec.Image)
	if spec.Image == "" {
		writeErr(w, http.StatusBadRequest, errors.New("informe a imagem"))
		return
	}
	ref, err := s.engineFor(r.Context(), h, r.URL.Query().Get("rt"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	eng := ref.Engine
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Minute)
	defer cancel()
	id := r.PathValue("id")
	ins, err := eng.Inspect(ctx, id)
	if err != nil {
		writeErr(w, engineStatus(err), err)
		return
	}
	id = ins.ID // the path may hold the name, which moves to the new one
	oldName := strings.TrimPrefix(ins.Name, "/")
	if spec.Name == "" {
		spec.Name = oldName
	}
	running := ins.State.Running
	start := running
	if req.Start != nil {
		start = *req.Start
	}
	if req.Pull {
		if err := eng.Pull(ctx, spec.Image, nil); err != nil {
			writeErr(w, http.StatusBadGateway, fmt.Errorf("não consegui baixar %s: %v", spec.Image, err))
			return
		}
	}
	// nerdctl keeps the first name of a container that ran even after a
	// rename: there the old one is removed first and rebuilt from its own spec if the new one fails.
	if ref.Runtime == "nerdctl" || ref.Runtime == "k3s" {
		s.replace(ctx, w, eng, id, running, spec, start)
		return
	}
	aside := oldName + "-gantry-old"
	if running {
		if err := eng.Action(ctx, id, "stop"); err != nil {
			writeErr(w, engineStatus(err), fmt.Errorf("parar o container atual: %v", err))
			return
		}
	}
	if err := eng.Rename(ctx, id, aside); err != nil {
		if running {
			_ = eng.Action(ctx, id, "start")
		}
		writeErr(w, engineStatus(err), fmt.Errorf("renomear o container atual: %v", err))
		return
	}
	rollback := func(newID string, cause error) {
		if newID != "" {
			_ = eng.Remove(context.Background(), newID, true)
		}
		_ = eng.Rename(context.Background(), id, oldName)
		if running {
			_ = eng.Action(context.Background(), id, "start")
		}
		writeErr(w, engineStatus(cause), fmt.Errorf("%v (o container anterior foi mantido)", cause))
	}
	newID, _, err := createWithPull(ctx, eng, spec, false)
	if err != nil {
		rollback("", err)
		return
	}
	if start {
		if err := eng.Action(ctx, newID, "start"); err != nil {
			rollback(newID, fmt.Errorf("o novo container não iniciou: %v", err))
			return
		}
	}
	if err := eng.Remove(ctx, id, true); err != nil {
		writeErr(w, engineStatus(err), fmt.Errorf("o novo container está no ar, mas não consegui remover o anterior (%s): %v", aside, err))
		return
	}
	writeJSON(w, map[string]any{"ok": true, "id": newID})
}

func (s *Server) replace(ctx context.Context, w http.ResponseWriter, eng *engine.Client, id string, running bool, spec engine.ContainerSpec, start bool) {
	old, err := eng.Spec(ctx, id)
	if err != nil {
		writeErr(w, engineStatus(err), err)
		return
	}
	if running {
		if err := eng.Action(ctx, id, "stop"); err != nil {
			writeErr(w, engineStatus(err), fmt.Errorf("parar o container atual: %v", err))
			return
		}
	}
	if err := eng.Remove(ctx, id, true); err != nil {
		writeErr(w, engineStatus(err), fmt.Errorf("remover o container atual: %v", err))
		return
	}
	restore := func(newID string, cause error) {
		bg := context.Background()
		if newID != "" {
			_ = eng.Remove(bg, newID, true)
		}
		msg := "o container anterior foi recriado"
		if oid, err := eng.Create(bg, old); err != nil {
			msg = "não consegui recriar o anterior: " + err.Error()
		} else if running {
			_ = eng.Action(bg, oid, "start")
		}
		writeErr(w, engineStatus(cause), fmt.Errorf("%v (%s)", cause, msg))
	}
	newID, _, err := createWithPull(ctx, eng, spec, false)
	if err != nil {
		restore("", err)
		return
	}
	if start {
		if err := eng.Action(ctx, newID, "start"); err != nil {
			restore(newID, fmt.Errorf("o novo container não iniciou: %v", err))
			return
		}
	}
	writeJSON(w, map[string]any{"ok": true, "id": newID})
}

// netOp connects a container to a network or disconnects it.
func (s *Server) netOp(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		Network string `json:"network"`
		IPv4    string `json:"ipv4"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil || req.Network == "" {
		writeErr(w, http.StatusBadRequest, errors.New("informe a rede"))
		return
	}
	eng, ok := s.reqEngine(w, r, h)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	id := r.PathValue("id")
	var err error
	switch r.PathValue("op") {
	case "connect":
		err = eng.Connect(ctx, req.Network, id, strings.TrimSpace(req.IPv4))
	case "disconnect":
		err = eng.Disconnect(ctx, req.Network, id)
	default:
		writeErr(w, http.StatusNotFound, errors.New("operação desconhecida"))
		return
	}
	if err != nil {
		writeErr(w, engineStatus(err), err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
