package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"gantry/internal/i18n"
)

// The language of the app: chosen in Settings, or the system's (the page
// sends the one it uses in X-Gantry-Lang, or lang= in an EventSource).
// Gantry has one user, so the language is the app's, not the request's.

func (s *Server) useLang(r *http.Request) {
	if l := s.cfg.Lang(); l != "" {
		i18n.Set(l)
		return
	}
	l := r.Header.Get("X-Gantry-Lang")
	if l == "" {
		l = r.URL.Query().Get("lang")
	}
	if l == "" {
		l = r.Header.Get("Accept-Language")
	}
	i18n.Set(l)
}

// translateEvent puts the data of an SSE event (JSON or text) in English.
func translateEvent(data string) string {
	if len(data) > 0 && data[0] == '{' {
		var x any
		if json.Unmarshal([]byte(data), &x) == nil {
			if b, err := json.Marshal(i18n.JSON(x)); err == nil {
				return string(b)
			}
		}
		return data
	}
	return i18n.T(data)
}

// language: GET and PUT /api/idioma — "pt", "en" or "" (the system's).
func (s *Server) language(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPut {
		var req struct {
			Language string `json:"language"`
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&req)
		if req.Language != "" && req.Language != "pt" && req.Language != "en" {
			writeErr(w, http.StatusBadRequest, errors.New("idioma inválido"))
			return
		}
		if err := s.cfg.SetLang(req.Language); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if req.Language != "" {
			i18n.Set(req.Language)
		}
	}
	writeJSON(w, map[string]string{"language": s.cfg.Lang(), "using": i18n.Lang()})
}
