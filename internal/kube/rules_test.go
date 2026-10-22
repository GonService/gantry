package kube

import "testing"

func TestPermTable(t *testing.T) {
	rows := PermTableFrom([]Rule{
		{Verbs: []string{"get", "list"}, APIGroups: []string{""}, Resources: []string{"pods", "pods/log"}},
		{Verbs: []string{"*"}, APIGroups: []string{"apps"}, Resources: []string{"deployments", "deployments/scale"}},
		{Verbs: []string{"get"}, APIGroups: []string{""}, Resources: []string{"secrets"}, ResourceNames: []string{"um"}},
		{Verbs: []string{"create"}, APIGroups: []string{"*"}, Resources: []string{"*/exec"}},
	})
	by := map[string]PermRow{}
	for _, r := range rows {
		by[r.Kind] = r
	}
	p := by["pods"]
	if !p.Verbs["get"] || !p.Verbs["list"] || p.Verbs["delete"] || !p.Verbs["logs"] || !p.Verbs["exec"] || p.Verbs["portforward"] {
		t.Fatalf("pods %+v", p)
	}
	d := by["deployments"]
	if !d.Verbs["delete"] || !d.Verbs["patch"] || !d.Verbs["scale"] {
		t.Fatalf("deployments %+v", d)
	}
	if s := by["secrets"]; s.Verbs["get"] || !s.Some["get"] {
		t.Fatalf("secrets %+v", s)
	}
	if by["services"].Verbs["get"] {
		t.Fatal("services")
	}
}
