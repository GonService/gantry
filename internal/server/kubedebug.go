package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"gantry/internal/kube"
)

// Debug containers: an ephemeral container with tools (netshoot) in a
// running pod, to look inside images without a shell.

func (s *Server) clusterDebugList(w http.ResponseWriter, r *http.Request, c clusterRec, k *kube.Client) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	list, err := k.DebugContainers(ctx, r.PathValue("ns"), r.PathValue("pod"))
	if err != nil {
		writeErr(w, kubeStatus(err), err)
		return
	}
	writeJSON(w, map[string]any{"containers": list, "images": kube.DebugImages})
}

func (s *Server) clusterDebug(w http.ResponseWriter, r *http.Request, c clusterRec, k *kube.Client) {
	if c.Mode == "leitura" {
		writeErr(w, http.StatusForbidden, errors.New("este cluster está em modo só leitura: o debug nos pods fica desligado"))
		return
	}
	// In the query: the terminal of the app opens a session with a bare POST.
	q := r.URL.Query()
	req := struct{ Target, Image, Attach string }{q.Get("target"), q.Get("image"), q.Get("attach")}
	ns, pod := r.PathValue("ns"), r.PathValue("pod")
	if !kubeNameRe.MatchString(ns) || !kubeNameRe.MatchString(pod) {
		writeErr(w, http.StatusBadRequest, errors.New("nome de pod ou namespace inválido"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
	defer cancel()
	name := req.Attach
	if name == "" {
		var err error
		if name, err = k.AddDebugContainer(ctx, ns, pod, req.Target, req.Image); err != nil {
			st := kubeStatus(err)
			var ae *kube.APIError
			if !errors.As(err, &ae) {
				st = http.StatusConflict
			}
			writeErr(w, st, err)
			return
		}
	}
	ex, err := k.Attach(ctx, ns, pod, name)
	if err != nil {
		writeErr(w, kubeStatus(err), err)
		return
	}
	writeJSON(w, map[string]string{"id": s.addTerm(ex), "name": name})
}
