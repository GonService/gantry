package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"time"

	"gantry/internal/i18n"
	"gantry/internal/kube"
)

// Checking a YAML while it is edited, as Lens does: the cluster itself
// answers (dry run, with the strict check of fields), so the problems are
// the ones a Save or an Apply would hit — unknown fields, required values,
// wrong types, the admission webhooks — with nothing changed.

type yamlProblem struct {
	Doc     int    `json:"doc"`             // which document of the text (from 0)
	Field   string `json:"field,omitempty"` // spec.template.spec.containers[0].image
	Message string `json:"message"`
}

var (
	fieldInMsg = regexp.MustCompile(`(?:unknown|duplicate) field "([^"]+)"`)
	// Server-side apply: "failed to create typed patch object (ns/x; v1,
	// Kind=ConfigMap): .foo: field not declared in schema", maybe several.
	ssaField = regexp.MustCompile(`(?m)(?:^|[\s(:])(\.[A-Za-z0-9_\-\[\]=.":/]+?): ([^\n]+)`)
	ssaHead  = regexp.MustCompile(`^failed to create typed patch object \([^)]*\):\s*`)
)

// problemsOf turns the cluster's refusal into problems by field.
func problemsOf(doc int, msg string, causes []kube.Cause) []yamlProblem {
	var out []yamlProblem
	for _, c := range causes {
		m := c.Message
		if m == "" {
			m = c.Reason
		}
		field := c.Field
		if field == "" {
			if f := fieldInMsg.FindStringSubmatch(m); f != nil {
				field = f[1]
			}
		}
		out = append(out, yamlProblem{Doc: doc, Field: field, Message: i18n.T(m)})
	}
	if len(out) == 0 {
		// The strict check says it all in the message: one problem per field.
		for _, f := range fieldInMsg.FindAllStringSubmatch(msg, -1) {
			out = append(out, yamlProblem{Doc: doc, Field: f[1], Message: f[0]})
		}
	}
	if len(out) == 0 && ssaHead.MatchString(msg) {
		for _, f := range ssaField.FindAllStringSubmatch(ssaHead.ReplaceAllString(msg, ""), -1) {
			out = append(out, yamlProblem{Doc: doc, Field: f[1], Message: f[2]})
		}
	}
	if len(out) == 0 && msg != "" {
		out = append(out, yamlProblem{Doc: doc, Message: i18n.T(msg)})
	}
	return out
}

// clusterValidate: POST /api/clusters/{id}/validar {yaml, ns, kind?, name?}
// — kind and name: the object being edited (a replace); without them, the
// documents of an Apply.
func (s *Server) clusterValidate(w http.ResponseWriter, r *http.Request, c clusterRec, k *kube.Client) {
	var req struct {
		YAML string `json:"yaml"`
		NS   string `json:"ns"`
		Kind string `json:"kind"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	problems := []yamlProblem{}
	if req.Kind != "" && req.Name != "" {
		kind, ok := k.KindFor(ctx, req.Kind)
		if !ok {
			writeErr(w, http.StatusNotFound, errors.New("tipo desconhecido"))
			return
		}
		ns := req.NS
		if ns == "-" {
			ns = ""
		}
		if err := k.ValidateReplace(ctx, kind, ns, req.Name, req.YAML); err != nil {
			var ae *kube.APIError
			if errors.As(err, &ae) {
				problems = problemsOf(0, ae.Message, ae.Causes)
			} else {
				problems = problemsOf(0, err.Error(), nil)
			}
		}
	} else {
		res, err := k.ValidateApply(ctx, req.YAML, req.NS)
		if err != nil {
			problems = problemsOf(0, err.Error(), nil)
		}
		for _, a := range res {
			if a.Error != "" {
				problems = append(problems, problemsOf(a.Doc, a.Error, a.Causes)...)
			}
		}
	}
	writeJSON(w, map[string]any{"ok": len(problems) == 0, "problems": problems})
}
