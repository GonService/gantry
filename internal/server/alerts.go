package server

import (
	"context"
	"net/http"
	"time"

	"gantry/internal/hosts"
)

// alerts lists the alerts of the Prometheus found for the host (in a
// project or configured in Observabilidade). No Prometheus, no alerts: the
// feature only shows when someone has rules.
func (s *Server) alerts(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	c, ep, err := s.obsClient(ctx, h, "prometheus")
	if err != nil {
		writeJSON(w, map[string]any{"prometheus": false, "alerts": []any{}})
		return
	}
	list, err := c.Alerts(ctx)
	if err != nil {
		writeJSON(w, map[string]any{"prometheus": true, "lab": ep.Lab, "error": err.Error(), "alerts": []any{}})
		return
	}
	writeJSON(w, map[string]any{"prometheus": true, "lab": ep.Lab, "alerts": list})
}
