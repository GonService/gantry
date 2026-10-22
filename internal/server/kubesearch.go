package server

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"gantry/internal/kube"
)

func (s *Server) clusterSearch(w http.ResponseWriter, r *http.Request, c clusterRec, k *kube.Client) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	if r.URL.Query().Get("novo") == "1" {
		k.ForgetSearch()
	}
	writeJSON(w, k.Search(ctx, kube.ParseQuery(r.URL.Query().Get("q")), limit))
}

func (s *Server) clusterPerms(w http.ResponseWriter, r *http.Request, c clusterRec, k *kube.Client) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	t, err := k.Permissions(ctx, r.URL.Query().Get("ns"))
	if err != nil {
		writeErr(w, kubeStatus(err), err)
		return
	}
	writeJSON(w, map[string]any{"table": t, "verbs": kube.PermVerbs})
}
