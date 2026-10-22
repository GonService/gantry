package kube

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
)

// Kind is a resource type Gantry shows.
type Kind struct {
	ID         string   `json:"id"`    // pods, deployments…
	Label      string   `json:"label"` // shown in the menu
	Group      string   `json:"group"` // menu section
	APIVersion string   `json:"apiVersion"`
	Resource   string   `json:"-"`
	Namespaced bool     `json:"namespaced"`
	Columns    []string `json:"columns"`
	Custom     bool     `json:"custom,omitempty"` // a CRD
	summary    func(o obj) Row
}

// Row is a resource as a line of a table.
type Row struct {
	Name      string   `json:"name"`
	Namespace string   `json:"namespace,omitempty"`
	Created   int64    `json:"created"`
	Cells     []string `json:"cells"`
	// Problem says, in words, what is wrong (empty when fine).
	Problem string `json:"problem,omitempty"`
	// Extra data the page uses (containers of a pod, replicas…).
	Info map[string]any `json:"info,omitempty"`

	labels map[string]any // to select rows in memory (not sent)
}

// Path is the API path of a kind (ns "" = all namespaces), or of one object.
func (k Kind) Path(ns, name string) string {
	base := "/api/" + k.APIVersion
	if strings.Contains(k.APIVersion, "/") {
		base = "/apis/" + k.APIVersion
	}
	if k.Namespaced && ns != "" {
		base += "/namespaces/" + url.PathEscape(ns)
	}
	base += "/" + k.Resource
	if name != "" {
		base += "/" + url.PathEscape(name)
	}
	return base
}

type obj map[string]any

func (o obj) get(path ...string) any {
	var cur any = map[string]any(o)
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	return cur
}
func (o obj) str(path ...string) string {
	v := o.get(path...)
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	return ""
}
func (o obj) num(path ...string) int {
	if f, ok := o.get(path...).(float64); ok {
		return int(f)
	}
	return 0
}
func (o obj) list(path ...string) []obj {
	arr, _ := o.get(path...).([]any)
	out := make([]obj, 0, len(arr))
	for _, a := range arr {
		if m, ok := a.(map[string]any); ok {
			out = append(out, obj(m))
		}
	}
	return out
}
func (o obj) mapOf(path ...string) map[string]any {
	m, _ := o.get(path...).(map[string]any)
	return m
}

func ts(s string) int64 {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0
	}
	return t.Unix()
}

func base(o obj) Row {
	return Row{Name: o.str("metadata", "name"), Namespace: o.str("metadata", "namespace"), Created: ts(o.str("metadata", "creationTimestamp")),
		labels: o.mapOf("metadata", "labels")}
}

// matches says whether a row has every label of sel.
func (r Row) matches(sel map[string]any) bool {
	if len(sel) == 0 {
		return false
	}
	for k, v := range sel {
		if fmt.Sprint(r.labels[k]) != fmt.Sprint(v) {
			return false
		}
	}
	return true
}

// PodStatus is the status kubectl shows for a pod, and whether it is a problem.
func PodStatus(o obj) (string, bool) {
	if o.get("metadata", "deletionTimestamp") != nil {
		return "Terminating", false
	}
	reason := o.str("status", "reason")
	status := o.str("status", "phase")
	if reason != "" {
		status = reason
	}
	for _, c := range o.list("status", "initContainerStatuses") {
		if w := c.str("state", "waiting", "reason"); w != "" && w != "PodInitializing" {
			return "Init:" + w, true
		}
		if t := c.mapOf("state", "terminated"); t != nil && c.num("state", "terminated", "exitCode") != 0 {
			return "Init:Error", true
		}
	}
	bad := false
	for _, c := range o.list("status", "containerStatuses") {
		if w := c.str("state", "waiting", "reason"); w != "" {
			status = w
			bad = w != "ContainerCreating" && w != "PodInitializing"
		} else if t := c.str("state", "terminated", "reason"); t != "" && status != "Succeeded" {
			status = t
			bad = t != "Completed"
		}
	}
	switch status {
	case "Failed", "Unknown", "Evicted", "OOMKilled", "Error":
		bad = true
	case "Pending":
		for _, cnd := range o.list("status", "conditions") {
			if cnd.str("type") == "PodScheduled" && cnd.str("status") == "False" {
				return "Pending: " + cnd.str("reason"), true
			}
		}
	}
	return status, bad
}

func podSummary(o obj) Row {
	r := base(o)
	status, bad := PodStatus(o)
	ready, total, restarts := 0, 0, 0
	var containers []string
	var ports []int
	for _, c := range o.list("spec", "containers") {
		containers = append(containers, c.str("name"))
		for _, p := range c.list("ports") {
			ports = append(ports, p.num("containerPort"))
		}
	}
	for _, c := range o.list("status", "containerStatuses") {
		total++
		if c.get("ready") == true {
			ready++
		}
		restarts += c.num("restartCount")
	}
	if total == 0 {
		total = len(containers)
	}
	r.Cells = []string{fmt.Sprintf("%d/%d", ready, total), status, strconv.Itoa(restarts), o.str("spec", "nodeName")}
	if bad {
		r.Problem = status
		if msg := problemMessage(o); msg != "" {
			r.Problem += ": " + msg
		}
	} else if restarts > 5 && status == "Running" {
		r.Problem = fmt.Sprintf("reiniciou %d vezes", restarts)
	} else if (status == "ContainerCreating" || status == "Pending" || status == "PodInitializing") && r.Created > 0 && time.Since(time.Unix(r.Created, 0)) > 3*time.Minute {
		r.Problem = "parado em " + status + " há " + age(r.Created)
	}
	r.Info = map[string]any{"containers": containers, "status": status, "ports": ports}
	return r
}

func problemMessage(o obj) string {
	for _, c := range o.list("status", "containerStatuses") {
		if m := c.str("state", "waiting", "message"); m != "" {
			return firstLine(m)
		}
		if m := c.str("state", "terminated", "message"); m != "" {
			return firstLine(m)
		}
	}
	for _, cnd := range o.list("status", "conditions") {
		if cnd.str("status") == "False" && cnd.str("message") != "" {
			return firstLine(cnd.str("message"))
		}
	}
	return ""
}

func firstLine(s string) string {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "(combined from similar events):"))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

func replicasSummary(o obj, ready int) Row {
	r := base(o)
	want := o.num("spec", "replicas")
	if o.get("spec", "replicas") == nil {
		want = 1
	}
	r.Cells = []string{fmt.Sprintf("%d/%d", ready, want), strconv.Itoa(o.num("status", "updatedReplicas")), strconv.Itoa(o.num("status", "availableReplicas")), images(o)}
	if ready < want {
		r.Problem = fmt.Sprintf("%d de %d prontos", ready, want)
	}
	r.Info = map[string]any{"replicas": want, "selector": o.mapOf("spec", "selector", "matchLabels")}
	return r
}

func images(o obj) string {
	var out []string
	for _, c := range o.list("spec", "template", "spec", "containers") {
		out = append(out, c.str("image"))
	}
	return strings.Join(out, ", ")
}

func age(sec int64) string {
	if sec == 0 {
		return ""
	}
	d := time.Since(time.Unix(sec, 0))
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// Kinds lists what Gantry shows, in menu order.
var Kinds = []Kind{
	{ID: "pods", Label: "Pods", Group: "Workloads", APIVersion: "v1", Resource: "pods", Namespaced: true,
		Columns: []string{"Prontos", "Estado", "Reinícios", "Nó"}, summary: podSummary},
	{ID: "deployments", Label: "Deployments", Group: "Workloads", APIVersion: "apps/v1", Resource: "deployments", Namespaced: true,
		Columns: []string{"Prontos", "Atualizados", "Disponíveis", "Imagens"}, summary: func(o obj) Row { return replicasSummary(o, o.num("status", "readyReplicas")) }},
	{ID: "statefulsets", Label: "StatefulSets", Group: "Workloads", APIVersion: "apps/v1", Resource: "statefulsets", Namespaced: true,
		Columns: []string{"Prontos", "Atualizados", "Disponíveis", "Imagens"}, summary: func(o obj) Row { return replicasSummary(o, o.num("status", "readyReplicas")) }},
	{ID: "daemonsets", Label: "DaemonSets", Group: "Workloads", APIVersion: "apps/v1", Resource: "daemonsets", Namespaced: true,
		Columns: []string{"Desejados", "Prontos", "Disponíveis", "Imagens"}, summary: func(o obj) Row {
			r := base(o)
			want, ready := o.num("status", "desiredNumberScheduled"), o.num("status", "numberReady")
			r.Cells = []string{strconv.Itoa(want), strconv.Itoa(ready), strconv.Itoa(o.num("status", "numberAvailable")), images(o)}
			if ready < want {
				r.Problem = fmt.Sprintf("%d de %d prontos", ready, want)
			}
			return r
		}},
	{ID: "replicasets", Label: "ReplicaSets", Group: "Workloads", APIVersion: "apps/v1", Resource: "replicasets", Namespaced: true,
		Columns: []string{"Prontos", "Atualizados", "Disponíveis", "Imagens"}, summary: func(o obj) Row { return replicasSummary(o, o.num("status", "readyReplicas")) }},
	{ID: "jobs", Label: "Jobs", Group: "Workloads", APIVersion: "batch/v1", Resource: "jobs", Namespaced: true,
		Columns: []string{"Concluídos", "Estado", "Duração"}, summary: func(o obj) Row {
			r := base(o)
			want := o.num("spec", "completions")
			if want == 0 {
				want = 1
			}
			st := "Rodando"
			for _, c := range o.list("status", "conditions") {
				if c.str("status") == "True" {
					switch c.str("type") {
					case "Complete":
						st = "Concluído"
					case "Failed":
						st = "Falhou"
						r.Problem = "Falhou: " + firstLine(c.str("message"))
					}
				}
			}
			dur := ""
			if s := ts(o.str("status", "startTime")); s > 0 {
				end := ts(o.str("status", "completionTime"))
				if end == 0 {
					end = time.Now().Unix()
				}
				dur = (time.Duration(end-s) * time.Second).String()
			}
			r.Cells = []string{fmt.Sprintf("%d/%d", o.num("status", "succeeded"), want), st, dur}
			return r
		}},
	{ID: "cronjobs", Label: "CronJobs", Group: "Workloads", APIVersion: "batch/v1", Resource: "cronjobs", Namespaced: true,
		Columns: []string{"Agenda", "Suspenso", "Ativos", "Última execução"}, summary: func(o obj) Row {
			r := base(o)
			susp := "não"
			if o.get("spec", "suspend") == true {
				susp = "sim"
			}
			last := ""
			if t := ts(o.str("status", "lastScheduleTime")); t > 0 {
				last = "há " + age(t)
			}
			r.Cells = []string{o.str("spec", "schedule"), susp, strconv.Itoa(len(o.list("status", "active"))), last}
			r.Info = map[string]any{"suspended": susp == "sim"}
			return r
		}},
	{ID: "hpas", Label: "Autoscalers (HPA)", Group: "Workloads", APIVersion: "autoscaling/v2", Resource: "horizontalpodautoscalers", Namespaced: true,
		Columns: []string{"Alvo", "Mín", "Máx", "Réplicas"}, summary: func(o obj) Row {
			r := base(o)
			r.Cells = []string{o.str("spec", "scaleTargetRef", "kind") + "/" + o.str("spec", "scaleTargetRef", "name"), o.str("spec", "minReplicas"), o.str("spec", "maxReplicas"), strconv.Itoa(o.num("status", "currentReplicas"))}
			return r
		}},
	{ID: "services", Label: "Services", Group: "Rede", APIVersion: "v1", Resource: "services", Namespaced: true,
		Columns: []string{"Tipo", "Cluster IP", "Externo", "Portas"}, summary: func(o obj) Row {
			r := base(o)
			var ports []string
			var pl []map[string]any
			for _, p := range o.list("spec", "ports") {
				s := p.str("port")
				if np := p.str("nodePort"); np != "" {
					s += ":" + np
				}
				ports = append(ports, s+"/"+p.str("protocol"))
				pl = append(pl, map[string]any{"port": p.num("port"), "name": p.str("name"), "protocol": p.str("protocol")})
			}
			var ext []string
			for _, i := range o.list("status", "loadBalancer", "ingress") {
				ext = append(ext, i.str("ip")+i.str("hostname"))
			}
			if e, ok := o.get("spec", "externalIPs").([]any); ok {
				for _, x := range e {
					ext = append(ext, fmt.Sprint(x))
				}
			}
			r.Cells = []string{o.str("spec", "type"), o.str("spec", "clusterIP"), strings.Join(ext, ","), strings.Join(ports, " ")}
			r.Info = map[string]any{"ports": pl}
			return r
		}},
	{ID: "ingresses", Label: "Ingresses", Group: "Rede", APIVersion: "networking.k8s.io/v1", Resource: "ingresses", Namespaced: true,
		Columns: []string{"Classe", "Hosts", "Endereço"}, summary: func(o obj) Row {
			r := base(o)
			var hosts []string
			for _, rule := range o.list("spec", "rules") {
				h := rule.str("host")
				if h == "" {
					h = "*"
				}
				hosts = append(hosts, h)
			}
			var addr []string
			for _, i := range o.list("status", "loadBalancer", "ingress") {
				addr = append(addr, i.str("ip")+i.str("hostname"))
			}
			r.Cells = []string{o.str("spec", "ingressClassName"), strings.Join(hosts, ", "), strings.Join(addr, ", ")}
			return r
		}},
	{ID: "endpoints", Label: "Endpoints", Group: "Rede", APIVersion: "v1", Resource: "endpoints", Namespaced: true,
		Columns: []string{"Endereços"}, summary: func(o obj) Row {
			r := base(o)
			n := 0
			for _, s := range o.list("subsets") {
				n += len(s.list("addresses"))
			}
			r.Cells = []string{strconv.Itoa(n)}
			return r
		}},
	{ID: "networkpolicies", Label: "Network Policies", Group: "Rede", APIVersion: "networking.k8s.io/v1", Resource: "networkpolicies", Namespaced: true,
		Columns: []string{"Pods"}, summary: func(o obj) Row {
			r := base(o)
			b, _ := json.Marshal(o.mapOf("spec", "podSelector", "matchLabels"))
			r.Cells = []string{string(b)}
			return r
		}},
	{ID: "configmaps", Label: "ConfigMaps", Group: "Configuração", APIVersion: "v1", Resource: "configmaps", Namespaced: true,
		Columns: []string{"Chaves"}, summary: func(o obj) Row {
			r := base(o)
			r.Cells = []string{strconv.Itoa(len(o.mapOf("data")) + len(o.mapOf("binaryData")))}
			return r
		}},
	{ID: "secrets", Label: "Secrets", Group: "Configuração", APIVersion: "v1", Resource: "secrets", Namespaced: true,
		Columns: []string{"Tipo", "Chaves"}, summary: func(o obj) Row {
			r := base(o)
			r.Cells = []string{o.str("type"), strconv.Itoa(len(o.mapOf("data")))}
			return r
		}},
	{ID: "serviceaccounts", Label: "Service Accounts", Group: "Configuração", APIVersion: "v1", Resource: "serviceaccounts", Namespaced: true,
		Columns: []string{"Secrets"}, summary: func(o obj) Row { r := base(o); r.Cells = []string{strconv.Itoa(len(o.list("secrets")))}; return r }},
	{ID: "pvcs", Label: "Volumes (PVC)", Group: "Armazenamento", APIVersion: "v1", Resource: "persistentvolumeclaims", Namespaced: true,
		Columns: []string{"Estado", "Volume", "Tamanho", "Classe"}, summary: func(o obj) Row {
			r := base(o)
			st := o.str("status", "phase")
			r.Cells = []string{st, o.str("spec", "volumeName"), o.str("status", "capacity", "storage"), o.str("spec", "storageClassName")}
			if st == "Pending" || st == "Lost" {
				r.Problem = st
			}
			return r
		}},
	{ID: "pvs", Label: "Volumes (PV)", Group: "Armazenamento", APIVersion: "v1", Resource: "persistentvolumes",
		Columns: []string{"Tamanho", "Estado", "Claim", "Classe"}, summary: func(o obj) Row {
			r := base(o)
			claim := ""
			if c := o.str("spec", "claimRef", "name"); c != "" {
				claim = o.str("spec", "claimRef", "namespace") + "/" + c
			}
			r.Cells = []string{o.str("spec", "capacity", "storage"), o.str("status", "phase"), claim, o.str("spec", "storageClassName")}
			return r
		}},
	{ID: "storageclasses", Label: "Storage Classes", Group: "Armazenamento", APIVersion: "storage.k8s.io/v1", Resource: "storageclasses",
		Columns: []string{"Provisionador", "Padrão"}, summary: func(o obj) Row {
			r := base(o)
			def := ""
			if o.str("metadata", "annotations", "storageclass.kubernetes.io/is-default-class") == "true" {
				def = "sim"
			}
			r.Cells = []string{o.str("provisioner"), def}
			return r
		}},
	{ID: "nodes", Label: "Nós", Group: "Cluster", APIVersion: "v1", Resource: "nodes",
		Columns: []string{"Estado", "Papéis", "Versão", "IP", "CPU", "Memória"}, summary: func(o obj) Row {
			r := base(o)
			st := "NotReady"
			for _, c := range o.list("status", "conditions") {
				if c.str("type") == "Ready" && c.str("status") == "True" {
					st = "Ready"
				}
			}
			if o.get("spec", "unschedulable") == true {
				st += ", sem agendamento"
			}
			var roles []string
			for k := range o.mapOf("metadata", "labels") {
				if r, ok := strings.CutPrefix(k, "node-role.kubernetes.io/"); ok {
					roles = append(roles, r)
				}
			}
			sort.Strings(roles)
			ip := ""
			for _, a := range o.list("status", "addresses") {
				if a.str("type") == "InternalIP" {
					ip = a.str("address")
				}
			}
			r.Cells = []string{st, strings.Join(roles, ","), o.str("status", "nodeInfo", "kubeletVersion"), ip, o.str("status", "capacity", "cpu"), o.str("status", "capacity", "memory")}
			if !strings.HasPrefix(st, "Ready") {
				r.Problem = "nó não está pronto"
			}
			r.Info = map[string]any{"unschedulable": o.get("spec", "unschedulable") == true}
			return r
		}},
	{ID: "namespaces", Label: "Namespaces", Group: "Cluster", APIVersion: "v1", Resource: "namespaces",
		Columns: []string{"Estado"}, summary: func(o obj) Row { r := base(o); r.Cells = []string{o.str("status", "phase")}; return r }},
	{ID: "events", Label: "Eventos", Group: "Cluster", APIVersion: "v1", Resource: "events", Namespaced: true,
		Columns: []string{"Tipo", "Motivo", "Objeto", "Mensagem", "Vezes", "Último"}, summary: func(o obj) Row {
			r := base(o)
			last := ts(o.str("lastTimestamp"))
			if last == 0 {
				last = ts(o.str("eventTime"))
			}
			if last == 0 {
				last = r.Created
			}
			count := o.num("count")
			if count == 0 {
				count = 1
			}
			r.Cells = []string{o.str("type"), o.str("reason"), o.str("involvedObject", "kind") + "/" + o.str("involvedObject", "name"), firstLine(o.str("message")), strconv.Itoa(count), "há " + age(last)}
			if o.str("type") == "Warning" {
				r.Problem = o.str("reason")
			}
			r.Created = last
			return r
		}},
}

// KindByID finds a kind.
func KindByID(id string) (Kind, bool) {
	for _, k := range Kinds {
		if k.ID == id {
			return k, true
		}
	}
	return Kind{}, false
}

// List reads the objects of a kind as table rows (ns "" = all).
func (k *Client) List(ctx context.Context, kind Kind, ns, selector string) ([]Row, error) {
	q := url.Values{}
	if selector != "" {
		q.Set("labelSelector", selector)
	}
	return k.ListQuery(ctx, kind, ns, q)
}

// ListQuery is List with any query (fieldSelector, limit…).
func (k *Client) ListQuery(ctx context.Context, kind Kind, ns string, q url.Values) ([]Row, error) {
	objs, _, err := k.listAll(ctx, kind, ns, q)
	if err != nil {
		return nil, err
	}
	out := make([]Row, 0, len(objs))
	for _, o := range objs {
		out = append(out, kind.summary(o))
	}
	sortRows(kind, out)
	return out, nil
}

// YAML returns an object as YAML, without the noise of managedFields.
// mask hides the values of a Secret.
func (k *Client) YAML(ctx context.Context, kind Kind, ns, name string, mask bool) (string, error) {
	resp, err := k.Do(ctx, http.MethodGet, kind.Path(ns, name), nil, nil, "")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(io.LimitReader(resp.Body, 32<<20))
	dec.UseNumber() // integers stay integers (no 3.0 or 1e+06 in the YAML)
	var o map[string]any
	if err := dec.Decode(&o); err != nil {
		return "", err
	}
	if md, ok := o["metadata"].(map[string]any); ok {
		delete(md, "managedFields")
	}
	if mask {
		// kubectl apply keeps the whole object (values too) in this annotation.
		if md, ok := o["metadata"].(map[string]any); ok {
			if an, ok := md["annotations"].(map[string]any); ok {
				if _, has := an["kubectl.kubernetes.io/last-applied-configuration"]; has {
					an["kubectl.kubernetes.io/last-applied-configuration"] = "(escondido no modo só leitura)"
				}
			}
		}
		for _, f := range []string{"data", "stringData"} {
			if m, ok := o[f].(map[string]any); ok {
				for key := range m {
					m[key] = "(escondido no modo só leitura)"
				}
			}
		}
	}
	b, err := yaml.MarshalWithOptions(numbers(o), yaml.IndentSequence(true))
	return string(b), err
}

// numbers turns json.Number into int64 (or float64) for the YAML encoder.
func numbers(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = numbers(e)
		}
	case []any:
		for i, e := range x {
			x[i] = numbers(e)
		}
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i
		}
		f, _ := x.Float64()
		return f
	}
	return v
}

// Replace saves an edited YAML over the object.
func (k *Client) Replace(ctx context.Context, kind Kind, ns, name, doc string) error {
	return k.replace(ctx, kind, ns, name, doc, false)
}

// ValidateReplace is Replace in dry run: the cluster checks the object
// (schema, unknown fields, admission) and keeps nothing.
func (k *Client) ValidateReplace(ctx context.Context, kind Kind, ns, name, doc string) error {
	return k.replace(ctx, kind, ns, name, doc, true)
}

func (k *Client) replace(ctx context.Context, kind Kind, ns, name, doc string, dry bool) error {
	var o map[string]any
	if err := yaml.Unmarshal([]byte(doc), &o); err != nil {
		return fmt.Errorf("YAML inválido: %v", err)
	}
	md, _ := o["metadata"].(map[string]any)
	if md == nil || fmt.Sprint(md["name"]) != name {
		return fmt.Errorf("o nome do objeto (metadata.name) precisa continuar %s", name)
	}
	resp, err := k.Do(ctx, http.MethodPut, kind.Path(ns, name), dryRun(dry, nil), o, "")
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// dryRun adds the dry run and the strict check of fields to a request.
func dryRun(dry bool, q url.Values) url.Values {
	if !dry {
		return q
	}
	if q == nil {
		q = url.Values{}
	}
	q.Set("dryRun", "All")
	q.Set("fieldValidation", "Strict")
	return q
}

// Delete removes an object.
func (k *Client) Delete(ctx context.Context, kind Kind, ns, name string) error {
	resp, err := k.Do(ctx, http.MethodDelete, kind.Path(ns, name), nil, nil, "")
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// Patch merges a JSON patch into an object.
func (k *Client) Patch(ctx context.Context, kind Kind, ns, name string, patch any) error {
	resp, err := k.Do(ctx, http.MethodPatch, kind.Path(ns, name), nil, patch, "application/merge-patch+json")
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// Scale sets the replicas of a deployment, statefulset or replicaset.
func (k *Client) Scale(ctx context.Context, kind Kind, ns, name string, replicas int) error {
	resp, err := k.Do(ctx, http.MethodPatch, kind.Path(ns, name)+"/scale", nil, map[string]any{"spec": map[string]any{"replicas": replicas}}, "application/merge-patch+json")
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// Restart rolls a workload out again, as kubectl rollout restart.
func (k *Client) Restart(ctx context.Context, kind Kind, ns, name string) error {
	return k.Patch(ctx, kind, ns, name, map[string]any{"spec": map[string]any{"template": map[string]any{"metadata": map[string]any{
		"annotations": map[string]any{"kubectl.kubernetes.io/restartedAt": time.Now().Format(time.RFC3339)}}}}})
}

// TriggerCronJob creates a Job from a CronJob now (kubectl create job --from).
func (k *Client) TriggerCronJob(ctx context.Context, ns, name string) (string, error) {
	cj, _ := KindByID("cronjobs")
	var o obj
	if err := k.Get(ctx, cj.Path(ns, name), nil, &o); err != nil {
		return "", err
	}
	suffix := fmt.Sprintf("-manual-%d", time.Now().Unix()%100000)
	base := name
	if len(base)+len(suffix) > 63 {
		base = strings.TrimRight(base[:63-len(suffix)], "-.")
	}
	jobName := base + suffix
	tmplMeta, _ := o.get("spec", "jobTemplate", "metadata").(map[string]any)
	labels, _ := tmplMeta["labels"].(map[string]any)
	annotations, _ := tmplMeta["annotations"].(map[string]any)
	if annotations == nil {
		annotations = map[string]any{}
	}
	annotations["cronjob.kubernetes.io/instantiate"] = "manual"
	job := map[string]any{
		"apiVersion": "batch/v1", "kind": "Job",
		"metadata": map[string]any{"name": jobName, "namespace": ns, "labels": labels,
			"annotations":     annotations,
			"ownerReferences": []any{map[string]any{"apiVersion": "batch/v1", "kind": "CronJob", "name": name, "uid": o.str("metadata", "uid")}}},
		"spec": o.get("spec", "jobTemplate", "spec"),
	}
	jk, _ := KindByID("jobs")
	resp, err := k.Do(ctx, http.MethodPost, jk.Path(ns, ""), nil, job, "")
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	return jobName, nil
}

// LogOptions narrow a pod log read.
type LogOptions struct {
	Container  string
	Follow     bool
	Tail       int // < 0: everything
	Since      int // seconds
	Timestamps bool
	Previous   bool
	// SinceTime (RFC 3339) wins over Since.
	SinceTime string
	// LimitBytes stops the read after so many bytes (0: no limit).
	LimitBytes int
}

// Logs streams the log of a pod's container.
func (k *Client) Logs(ctx context.Context, ns, pod string, o LogOptions) (io.ReadCloser, error) {
	q := url.Values{}
	if o.Container != "" {
		q.Set("container", o.Container)
	}
	if o.Follow {
		q.Set("follow", "true")
	}
	if o.Tail >= 0 {
		q.Set("tailLines", strconv.Itoa(o.Tail))
	}
	if o.SinceTime != "" {
		q.Set("sinceTime", o.SinceTime)
	} else if o.Since > 0 {
		q.Set("sinceSeconds", strconv.Itoa(o.Since))
	}
	if o.Timestamps {
		q.Set("timestamps", "true")
	}
	if o.Previous {
		q.Set("previous", "true")
	}
	if o.LimitBytes > 0 {
		q.Set("limitBytes", strconv.Itoa(o.LimitBytes))
	}
	resp, err := k.Do(ctx, http.MethodGet, "/api/v1/namespaces/"+url.PathEscape(ns)+"/pods/"+url.PathEscape(pod)+"/log", q, nil, "")
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// ContainerState is where one container of a pod is now.
type ContainerState struct {
	ID       string // changes when the container restarts
	State    string // running, waiting or terminated ("" before it was created)
	Reason   string
	PodDone  bool // the pod finished (Succeeded or Failed): no new container comes
	Restarts int
	ExitCode int
	// Over: it ended and the restart policy of the pod does not start it again.
	Over bool
}

// ContainerStateOf reads the state of a container of a pod; container ""
// is the one the API picks (the default-container annotation, else the
// first one).
func (k *Client) ContainerStateOf(ctx context.Context, ns, pod, container string) (ContainerState, error) {
	var o obj
	if err := k.Get(ctx, "/api/v1/namespaces/"+url.PathEscape(ns)+"/pods/"+url.PathEscape(pod), nil, &o); err != nil {
		return ContainerState{}, err
	}
	if container == "" {
		container = o.str("metadata", "annotations", "kubectl.kubernetes.io/default-container")
	}
	if container == "" {
		if cs := o.list("spec", "containers"); len(cs) > 0 {
			container = cs[0].str("name")
		}
	}
	phase := o.str("status", "phase")
	st := ContainerState{PodDone: phase == "Succeeded" || phase == "Failed"}
	for _, list := range []string{"containerStatuses", "initContainerStatuses", "ephemeralContainerStatuses"} {
		for _, c := range o.list("status", list) {
			if c.str("name") != container {
				continue
			}
			st.ID = c.str("containerID")
			st.Restarts = c.num("restartCount")
			for _, s := range []string{"running", "waiting", "terminated"} {
				if m := c.mapOf("state", s); m != nil {
					st.State = s
					st.Reason = c.str("state", s, "reason")
				}
			}
			if st.State == "terminated" {
				st.ExitCode = c.num("state", "terminated", "exitCode")
				switch o.str("spec", "restartPolicy") {
				case "Never":
					st.Over = true
				case "OnFailure":
					st.Over = st.ExitCode == 0
				}
				if list == "initContainerStatuses" && st.ExitCode == 0 {
					st.Over = true // an init container that did its job…
					for _, ic := range o.list("spec", "initContainers") {
						if ic.str("name") == container && ic.str("restartPolicy") == "Always" {
							st.Over = false // …unless it is a sidecar
						}
					}
				}
			}
			return st, nil
		}
	}
	return st, nil
}

// Usage is CPU (millicores) and memory (bytes) from metrics-server.
type Usage struct {
	CPU int64 `json:"cpu"`
	Mem int64 `json:"mem"`
}

func (k *Client) usage(ctx context.Context, path string, pods bool) map[string]Usage {
	out := map[string]Usage{}
	var l struct {
		Items []obj `json:"items"`
	}
	if err := k.Get(ctx, path, nil, &l); err != nil {
		return out
	}
	for _, it := range l.Items {
		var u Usage
		if pods {
			for _, c := range it.list("containers") {
				u.CPU += ParseCPU(c.str("usage", "cpu"))
				u.Mem += ParseMem(c.str("usage", "memory"))
			}
			out[it.str("metadata", "namespace")+"/"+it.str("metadata", "name")] = u
		} else {
			u.CPU, u.Mem = ParseCPU(it.str("usage", "cpu")), ParseMem(it.str("usage", "memory"))
			out[it.str("metadata", "name")] = u
		}
	}
	return out
}

// ParseCPU reads a CPU quantity in millicores (250m, 2, 1500000n).
func ParseCPU(s string) int64 {
	switch {
	case strings.HasSuffix(s, "n"):
		v, _ := strconv.ParseInt(strings.TrimSuffix(s, "n"), 10, 64)
		return v / 1e6
	case strings.HasSuffix(s, "u"):
		v, _ := strconv.ParseInt(strings.TrimSuffix(s, "u"), 10, 64)
		return v / 1e3
	case strings.HasSuffix(s, "m"):
		v, _ := strconv.ParseInt(strings.TrimSuffix(s, "m"), 10, 64)
		return v
	}
	f, _ := strconv.ParseFloat(s, 64)
	return int64(f * 1000)
}

// ParseMem reads a memory quantity in bytes (128Mi, 1Gi, 2G, 1000Ki).
func ParseMem(s string) int64 {
	mult := map[string]float64{"Ki": 1 << 10, "Mi": 1 << 20, "Gi": 1 << 30, "Ti": 1 << 40, "k": 1e3, "K": 1e3, "M": 1e6, "G": 1e9, "T": 1e12}
	for _, suf := range []string{"Ki", "Mi", "Gi", "Ti", "k", "K", "M", "G", "T"} {
		if strings.HasSuffix(s, suf) {
			f, _ := strconv.ParseFloat(strings.TrimSuffix(s, suf), 64)
			return int64(f * mult[suf])
		}
	}
	f, _ := strconv.ParseFloat(s, 64)
	return int64(f)
}

// HelmRelease is a release installed with Helm 3 (read from its secrets).
type HelmRelease struct {
	Name       string `json:"name"`
	Namespace  string `json:"namespace"`
	Revision   int    `json:"revision"`
	Status     string `json:"status"`
	Chart      string `json:"chart"`
	AppVersion string `json:"appVersion"`
	Updated    int64  `json:"updated"`
}

// HelmReleases lists the latest revision of each release in ns ("" = all).
func (k *Client) HelmReleases(ctx context.Context, ns string) ([]HelmRelease, error) {
	sk, _ := KindByID("secrets")
	var l struct {
		Items []obj `json:"items"`
	}
	if err := k.Get(ctx, sk.Path(ns, ""), url.Values{"labelSelector": {"owner=helm"}}, &l); err != nil {
		return nil, err
	}
	latest := map[string]HelmRelease{}
	for _, it := range l.Items {
		lb := it.mapOf("metadata", "labels")
		rev, _ := strconv.Atoi(fmt.Sprint(lb["version"]))
		h := HelmRelease{Name: fmt.Sprint(lb["name"]), Namespace: it.str("metadata", "namespace"), Revision: rev, Status: fmt.Sprint(lb["status"]), Updated: ts(it.str("metadata", "creationTimestamp"))}
		if data := it.str("data", "release"); data != "" {
			if rel := decodeRelease(data); rel != nil {
				h.Chart = rel.str("chart", "metadata", "name") + "-" + rel.str("chart", "metadata", "version")
				h.AppVersion = rel.str("chart", "metadata", "appVersion")
			}
		}
		key := h.Namespace + "/" + h.Name
		if cur, ok := latest[key]; !ok || h.Revision > cur.Revision {
			latest[key] = h
		}
	}
	out := make([]HelmRelease, 0, len(latest))
	for _, h := range latest {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Namespace+out[i].Name < out[j].Namespace+out[j].Name })
	return out, nil
}

// decodeRelease opens a Helm release: base64 (the Secret) of base64 of gzip of JSON.
func decodeRelease(s string) obj {
	o, _ := openRelease(s)
	return o
}

// openRelease is decodeRelease with the reason when it can't: what the
// screen says instead of an empty release.
func openRelease(s string) (obj, error) {
	if s == "" {
		return nil, errors.New("o Secret da release não tem o campo release")
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("o campo release não é base64: %w", err)
	}
	// Helm writes base64 of the gzip; accept the gzip (or the JSON) directly too.
	if d, err := base64.StdEncoding.DecodeString(string(b)); err == nil {
		b = d
	}
	if len(b) > 2 && b[0] == 0x1f && b[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			return nil, fmt.Errorf("gzip da release inválido: %w", err)
		}
		b, err = io.ReadAll(io.LimitReader(zr, 64<<20))
		if err != nil {
			return nil, fmt.Errorf("gzip da release inválido: %w", err)
		}
	}
	var o obj
	if err := json.Unmarshal(b, &o); err != nil {
		return nil, fmt.Errorf("a release não é JSON: %w", err)
	}
	return o, nil
}

// HelmRevision is one revision of a release.
type HelmRevision struct {
	Revision    int    `json:"revision"`
	Status      string `json:"status"`
	Chart       string `json:"chart"`
	AppVersion  string `json:"appVersion"`
	Updated     int64  `json:"updated"`
	Description string `json:"description,omitempty"`
}

// HelmDetail is a release: its history and, of one revision (the latest
// by default), the values given at install/upgrade, the chart's default
// values, the notes and the manifest.
type HelmDetail struct {
	Name      string         `json:"name"`
	Namespace string         `json:"namespace"`
	History   []HelmRevision `json:"history"`
	// Revision is the one Values, Defaults, Notes and Manifest are of.
	Revision int            `json:"revision,omitempty"`
	Values   map[string]any `json:"values,omitempty"`
	Defaults map[string]any `json:"defaults,omitempty"`
	Notes    string         `json:"notes,omitempty"`
	Manifest string         `json:"manifest,omitempty"`
	// ContentError says why the content of the revision could not be read.
	ContentError string `json:"contentError,omitempty"`
}

// HelmRelease reads one release. withContent false leaves values, notes
// and manifest out (they often hold passwords). rev picks the revision of
// the content; 0 is the latest.
func (k *Client) HelmRelease(ctx context.Context, ns, name string, withContent bool, rev int) (*HelmDetail, error) {
	sk, _ := KindByID("secrets")
	var l struct {
		Items []obj `json:"items"`
	}
	if err := k.Get(ctx, sk.Path(ns, ""), url.Values{"labelSelector": {"owner=helm,name=" + name}}, &l); err != nil {
		return nil, err
	}
	if len(l.Items) == 0 {
		return nil, &APIError{Status: http.StatusNotFound, Message: "release " + name + " não encontrada"}
	}
	d := &HelmDetail{Name: name, Namespace: ns, History: []HelmRevision{}}
	data := map[int]string{}
	latest := -1
	for _, it := range l.Items {
		lb := it.mapOf("metadata", "labels")
		n, _ := strconv.Atoi(fmt.Sprint(lb["version"]))
		hr := HelmRevision{Revision: n, Status: fmt.Sprint(lb["status"]), Updated: ts(it.str("metadata", "creationTimestamp"))}
		raw := it.str("data", "release")
		if rel := decodeRelease(raw); rel != nil {
			hr.Chart = rel.str("chart", "metadata", "name") + "-" + rel.str("chart", "metadata", "version")
			hr.AppVersion = rel.str("chart", "metadata", "appVersion")
			hr.Description = rel.str("info", "description")
		}
		d.History = append(d.History, hr)
		data[n] = raw
		if n > latest {
			latest = n
		}
	}
	sort.Slice(d.History, func(i, j int) bool { return d.History[i].Revision > d.History[j].Revision })
	if rev == 0 {
		rev = latest
	}
	if _, ok := data[rev]; !ok {
		return nil, &APIError{Status: http.StatusNotFound, Message: fmt.Sprintf("a release %s não tem a revisão %d (o Helm guarda só as últimas)", name, rev)}
	}
	if withContent {
		d.Revision = rev
		rel, err := openRelease(data[rev])
		if err != nil {
			d.ContentError = err.Error()
		} else {
			d.Values = rel.mapOf("config")
			d.Defaults = rel.mapOf("chart", "values")
			d.Notes = rel.str("info", "notes")
			d.Manifest = rel.str("manifest")
			if d.Manifest == "" && d.Values == nil && d.Notes == "" {
				d.ContentError = fmt.Sprintf("a release não traz values, notas nem manifest (campos: %s)", strings.Join(keysOf(rel), ", "))
			}
		}
	}
	return d, nil
}

func keysOf(o obj) []string {
	out := make([]string, 0, len(o))
	for k := range o {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
