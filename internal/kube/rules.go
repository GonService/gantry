package kube

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// What the account may do in a namespace: the rules the API server gives
// (SelfSubjectRulesReview), as a table of kinds and verbs.

// Rule is one resource rule of the account.
type Rule struct {
	Verbs         []string `json:"verbs"`
	APIGroups     []string `json:"apiGroups"`
	Resources     []string `json:"resources"`
	ResourceNames []string `json:"resourceNames,omitempty"`
}

// PermRow is what the account may do with one kind.
type PermRow struct {
	Kind  string          `json:"kind"`
	Label string          `json:"label"`
	Verbs map[string]bool `json:"verbs"`
	// Some: allowed only for some objects (resourceNames).
	Some map[string]bool `json:"some,omitempty"`
}

// PermTable is the table for a namespace, with who the account is.
type PermTable struct {
	Namespace  string    `json:"namespace"`
	User       string    `json:"user,omitempty"`
	Groups     []string  `json:"groups,omitempty"`
	Incomplete bool      `json:"incomplete"`
	Rows       []PermRow `json:"rows"`
	Rules      []Rule    `json:"rules"`
}

// PermVerbs are the columns of the table; the extra ones are subresources.
var PermVerbs = []string{"get", "list", "watch", "create", "update", "patch", "delete", "exec", "logs", "portforward", "debug", "scale"}

var permSub = map[string]struct{ kind, sub, verb string }{
	"exec":        {"pods", "exec", "create"},
	"logs":        {"pods", "log", "get"},
	"portforward": {"pods", "portforward", "create"},
	"debug":       {"pods", "ephemeralcontainers", "patch"},
}

func has(list []string, v string) bool {
	for _, x := range list {
		if x == "*" || x == v {
			return true
		}
	}
	return false
}

// allows tells whether a rule allows verb on group/resource[/sub].
func (r Rule) allows(group, resource, sub, verb string) bool {
	if !has(r.Verbs, verb) || !has(r.APIGroups, group) {
		return false
	}
	want := resource
	if sub != "" {
		want += "/" + sub
	}
	for _, x := range r.Resources {
		if x == "*" || x == want || (sub != "" && x == "*/"+sub) || (sub == "" && x == resource) {
			return true
		}
	}
	return false
}

// PermTableFrom builds the table from the rules.
func PermTableFrom(rules []Rule) []PermRow {
	var rows []PermRow
	for _, k := range Kinds {
		group := ""
		if i := strings.Index(k.APIVersion, "/"); i > 0 {
			group = k.APIVersion[:i]
		}
		row := PermRow{Kind: k.ID, Label: k.Label, Verbs: map[string]bool{}, Some: map[string]bool{}}
		check := func(col, res, sub, verb string) {
			for _, r := range rules {
				if r.allows(group, res, sub, verb) {
					if len(r.ResourceNames) == 0 {
						row.Verbs[col] = true
						delete(row.Some, col)
						return
					}
					row.Some[col] = true
				}
			}
		}
		for _, v := range PermVerbs[:7] {
			check(v, k.Resource, "", v)
		}
		for col, s := range permSub {
			if s.kind == k.ID {
				check(col, "pods", s.sub, s.verb)
			}
		}
		if k.ID == "deployments" || k.ID == "statefulsets" || k.ID == "replicasets" {
			check("scale", k.Resource, "scale", "patch")
		}
		if len(row.Some) == 0 {
			row.Some = nil
		}
		rows = append(rows, row)
	}
	return rows
}

// Permissions reads the rules of the account in ns and who it is.
func (k *Client) Permissions(ctx context.Context, ns string) (*PermTable, error) {
	if ns == "" {
		ns = "default"
	}
	body := map[string]any{"apiVersion": "authorization.k8s.io/v1", "kind": "SelfSubjectRulesReview", "spec": map[string]any{"namespace": ns}}
	resp, err := k.Do(ctx, http.MethodPost, "/apis/authorization.k8s.io/v1/selfsubjectrulesreviews", nil, body, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Status struct {
			ResourceRules []Rule `json:"resourceRules"`
			Incomplete    bool   `json:"incomplete"`
		} `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	t := &PermTable{Namespace: ns, Incomplete: out.Status.Incomplete, Rules: out.Status.ResourceRules, Rows: PermTableFrom(out.Status.ResourceRules)}
	if t.Rules == nil {
		t.Rules = []Rule{}
	}
	// Who: SelfSubjectReview (Kubernetes 1.28+); older clusters just skip it.
	if r2, err := k.Do(ctx, http.MethodPost, "/apis/authentication.k8s.io/v1/selfsubjectreviews", nil, map[string]any{"apiVersion": "authentication.k8s.io/v1", "kind": "SelfSubjectReview"}, ""); err == nil {
		var who struct {
			Status struct {
				UserInfo struct {
					Username string   `json:"username"`
					Groups   []string `json:"groups"`
				} `json:"userInfo"`
			} `json:"status"`
		}
		if json.NewDecoder(r2.Body).Decode(&who) == nil {
			t.User, t.Groups = who.Status.UserInfo.Username, who.Status.UserInfo.Groups
		}
		r2.Body.Close()
	}
	return t, nil
}
