package kube

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"gantry/internal/applog"
)

// Perms says which actions of the screens the account may do. The cluster
// answers each question (SelfSubjectAccessReview); answers are kept for a
// couple of minutes. When the cluster cannot answer, the action is shown
// (the API refuses it anyway if it was not allowed).
type Perms struct {
	Update      bool `json:"update"`      // Editar (replace)
	Delete      bool `json:"delete"`      // Apagar
	Patch       bool `json:"patch"`       // escalar, reiniciar, suspender, isolar, voltar versão
	Scale       bool `json:"scale"`       // Escalar (subresource scale)
	Exec        bool `json:"exec"`        // Shell
	Logs        bool `json:"logs"`        // Logs
	PortForward bool `json:"portforward"` // Encaminhar porta
	CreateJobs  bool `json:"createJobs"`  // Executar agora (CronJob)
	Proxy       bool `json:"proxy"`       // Abrir (Service)
	Secrets     bool `json:"secrets"`     // Ver valores
	Evict       bool `json:"evict"`       // Esvaziar nó
	Debug       bool `json:"debug"`       // Debug (container efêmero)
}

type permKey struct{ ns, group, resource, sub, verb string }

type permAnswer struct {
	ok bool
	at time.Time
}

var permMu sync.Mutex

const permTTL = 2 * time.Minute

// Can asks whether the account may verb resource/sub in ns ("" = all).
func (k *Client) Can(ctx context.Context, ns, group, resource, sub, verb string) bool {
	key := permKey{ns, group, resource, sub, verb}
	permMu.Lock()
	if k.perms == nil {
		k.perms = map[permKey]permAnswer{}
	}
	a, ok := k.perms[key]
	permMu.Unlock()
	if ok && time.Since(a.at) < permTTL {
		return a.ok
	}
	body := map[string]any{"apiVersion": "authorization.k8s.io/v1", "kind": "SelfSubjectAccessReview",
		"spec": map[string]any{"resourceAttributes": map[string]any{"namespace": ns, "verb": verb, "group": group, "resource": resource, "subresource": sub}}}
	resp, err := k.Do(ctx, http.MethodPost, "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews", nil, body, "")
	if err != nil {
		return true // no answer: show it
	}
	defer resp.Body.Close()
	var out struct {
		Status struct {
			Allowed bool `json:"allowed"`
		} `json:"status"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil {
		return true
	}
	permMu.Lock()
	k.perms[key] = permAnswer{out.Status.Allowed, time.Now()}
	permMu.Unlock()
	return out.Status.Allowed
}

// PermsFor answers, at once, what the screens of kind in ns need.
func (k *Client) PermsFor(ctx context.Context, kind Kind, ns string) Perms {
	group := ""
	if i := strings.Index(kind.APIVersion, "/"); i > 0 {
		group = kind.APIVersion[:i]
	}
	if !kind.Namespaced {
		ns = ""
	} else if ns == "" {
		// Every namespace at once: an account with rights in some
		// namespaces only would get "no". Here that is "not sure", so the
		// buttons show; the details of an object ask for its namespace.
		return Perms{Update: true, Delete: true, Patch: true, Scale: true, Exec: true, Logs: true, PortForward: true,
			CreateJobs: true, Proxy: true, Secrets: true, Evict: true, Debug: true}
	}
	var p Perms
	type q struct {
		dst                        *bool
		group, resource, sub, verb string
	}
	qs := []q{
		{&p.Update, group, kind.Resource, "", "update"},
		{&p.Delete, group, kind.Resource, "", "delete"},
		{&p.Patch, group, kind.Resource, "", "patch"},
	}
	switch kind.ID {
	case "deployments", "statefulsets", "replicasets":
		qs = append(qs, q{&p.Scale, group, kind.Resource, "scale", "patch"})
	}
	switch kind.ID {
	case "pods":
		qs = append(qs, q{&p.Exec, "", "pods", "exec", "create"}, q{&p.Logs, "", "pods", "log", "get"}, q{&p.PortForward, "", "pods", "portforward", "create"},
			q{&p.Debug, "", "pods", "ephemeralcontainers", "patch"})
	case "services":
		qs = append(qs, q{&p.Proxy, "", "services", "proxy", "get"}, q{&p.PortForward, "", "pods", "portforward", "create"})
	case "cronjobs":
		qs = append(qs, q{&p.CreateJobs, "batch", "jobs", "", "create"})
	case "secrets":
		qs = append(qs, q{&p.Secrets, "", "secrets", "", "get"})
	case "nodes":
		qs = append(qs, q{&p.Evict, "", "pods", "eviction", "create"})
	case "deployments":
		qs = append(qs, q{&p.Logs, "", "pods", "log", "get"})
	}
	var wg sync.WaitGroup
	for _, x := range qs {
		wg.Add(1)
		go func(x q) {
			defer applog.Recover("kube/rbac.go:120")
			defer wg.Done()
			*x.dst = k.Can(ctx, ns, x.group, x.resource, x.sub, x.verb)
		}(x)
	}
	wg.Wait()
	return p
}
