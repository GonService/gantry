package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func esc(s string) string { return url.PathEscape(s) }

type hostView struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Runtime string `json:"runtime"`
	OK      bool   `json:"ok"`
	Error   string `json:"error"`
	Machine string `json:"machine"`
}

type labCtr struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Service string `json:"service"`
	Image   string `json:"image"`
	State   string `json:"state"`
	Status  string `json:"status"`
	Runtime string `json:"runtime"`
}

type labView struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	WorkingDir string   `json:"workingDir"`
	Loose      bool     `json:"loose"`
	Running    int      `json:"running"`
	Total      int      `json:"total"`
	Containers []labCtr `json:"containers"`
	VMs        []struct {
		Name  string `json:"name"`
		State string `json:"state"`
		Accel string `json:"accel"`
	} `json:"vms"`
}

type clusterView struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Context string `json:"context"`
	Mode    string `json:"mode"`
}

// cluster finds a cluster by id, name or context.
func cluster(ctx context.Context, c *Client, which string) (clusterView, error) {
	var list []clusterView
	if err := c.JSON(ctx, http.MethodGet, "/clusters", nil, nil, &list); err != nil {
		return clusterView{}, err
	}
	for _, x := range list {
		if x.ID == which || strings.EqualFold(x.Name, which) || x.Context == which {
			return x, nil
		}
	}
	if which == "" && len(list) == 1 {
		return list[0], nil
	}
	var names []string
	for _, x := range list {
		names = append(names, x.Name)
	}
	return clusterView{}, fmt.Errorf("cluster %q not found (clusters: %s)", which, strings.Join(names, ", "))
}

// container finds a container of a host by id, name or compose service.
func container(ctx context.Context, c *Client, host, which string) (labCtr, string, error) {
	var labs []labView
	if err := c.JSON(ctx, http.MethodGet, "/hosts/"+esc(host)+"/labs", nil, nil, &labs); err != nil {
		return labCtr{}, "", err
	}
	var found []labCtr
	var in []string
	for _, l := range labs {
		for _, x := range l.Containers {
			if x.ID == which || strings.HasPrefix(x.ID, which) && len(which) >= 6 || x.Name == which || x.Service == which {
				found = append(found, x)
				in = append(in, l.Name)
			}
		}
	}
	switch len(found) {
	case 0:
		return labCtr{}, "", fmt.Errorf("no container %q on %s", which, host)
	case 1:
		return found[0], in[0], nil
	}
	var names []string
	for i, x := range found {
		names = append(names, x.Name+" ("+in[i]+")")
	}
	return labCtr{}, "", fmt.Errorf("%q is more than one container: %s; use the name", which, strings.Join(names, ", "))
}

var kindAlias = map[string]string{"pod": "pods", "po": "pods", "deploy": "deployments", "deployment": "deployments", "sts": "statefulsets", "statefulset": "statefulsets",
	"ds": "daemonsets", "daemonset": "daemonsets", "svc": "services", "service": "services", "ing": "ingresses", "ingress": "ingresses", "cm": "configmaps",
	"configmap": "configmaps", "ns": "namespaces", "namespace": "namespaces", "node": "nodes", "no": "nodes", "job": "jobs", "cronjob": "cronjobs", "cj": "cronjobs",
	"pvc": "pvcs", "persistentvolumeclaims": "pvcs", "pv": "pvs", "event": "events", "ev": "events", "hpa": "hpas", "rs": "replicasets", "replicaset": "replicasets",
	"secret": "secrets"}

func kindOf(k string) string {
	k = strings.ToLower(strings.TrimSpace(k))
	if a, ok := kindAlias[k]; ok {
		return a
	}
	return k
}

type kubeRow struct {
	Name      string   `json:"name"`
	Namespace string   `json:"namespace"`
	Cells     []string `json:"cells"`
	Problem   string   `json:"problem"`
}

func kubeList(ctx context.Context, c *Client, cl clusterView, kind, ns string) (string, error) {
	var res struct {
		Rows    []kubeRow `json:"rows"`
		Columns []string  `json:"columns"`
	}
	q := url.Values{}
	if ns != "" {
		q.Set("ns", ns)
	}
	if err := c.JSON(ctx, http.MethodGet, "/clusters/"+esc(cl.ID)+"/r/"+esc(kind), q, nil, &res); err != nil {
		return "", err
	}
	head := append([]string{"NAMESPACE", "NAME"}, res.Columns...)
	head = append(head, "PROBLEM")
	var rows [][]string
	for i, r := range res.Rows {
		if i >= 300 {
			break
		}
		row := append([]string{r.Namespace, r.Name}, r.Cells...)
		rows = append(rows, append(row, r.Problem))
	}
	more := ""
	if len(res.Rows) > 300 {
		more = fmt.Sprintf("\n(%d more not shown; filter by namespace)", len(res.Rows)-300)
	}
	return fmt.Sprintf("%s in cluster %s%s:\n%s%s", kind, cl.Name, map[bool]string{true: " (namespace " + ns + ")", false: ""}[ns != ""], table(head, rows), more), nil
}

func compact(v any, max int) string {
	b, _ := json.MarshalIndent(v, "", " ")
	return tail(string(b), max)
}

func allTools() []Tool {
	return []Tool{
		{
			Name: "list_hosts", Description: "List the machines (hosts) Gantry manages: local Docker/Podman, servers over SSH, and whether each is connected.",
			InputSchema: schema(nil, map[string]string{}),
			run: func(ctx context.Context, c *Client, a args) (string, error) {
				var hs []hostView
				if err := c.JSON(ctx, http.MethodGet, "/hosts", nil, nil, &hs); err != nil {
					return "", err
				}
				var rows [][]string
				for _, h := range hs {
					st := "connected"
					if !h.OK {
						st = "not connected"
						if h.Error != "" {
							st += ": " + h.Error
						}
					}
					rows = append(rows, []string{h.Name, h.Kind, h.Runtime, st})
				}
				return table([]string{"HOST", "KIND", "RUNTIME", "STATE"}, rows), nil
			},
		},
		{
			Name: "list_labs", Description: "List the projects (labs: a compose file with containers, VMs and network devices) of a host, with how many containers run.",
			InputSchema: schema([]string{"host"}, map[string]string{"host": "host name, from list_hosts"}),
			run: func(ctx context.Context, c *Client, a args) (string, error) {
				if err := a.need("host"); err != nil {
					return "", err
				}
				var labs []labView
				if err := c.JSON(ctx, http.MethodGet, "/hosts/"+esc(a.str("host"))+"/labs", nil, nil, &labs); err != nil {
					return "", err
				}
				var rows [][]string
				for _, l := range labs {
					name := l.Name
					if l.Loose {
						name += " (containers outside projects)"
					}
					vms := ""
					if len(l.VMs) > 0 {
						on := 0
						for _, v := range l.VMs {
							if v.State == "running" {
								on++
							}
						}
						vms = fmt.Sprintf("%d/%d", on, len(l.VMs))
					}
					rows = append(rows, []string{l.ID, name, fmt.Sprintf("%d/%d", l.Running, l.Total), vms, l.WorkingDir})
				}
				return table([]string{"ID", "NAME", "CONTAINERS UP", "VMS UP", "FOLDER"}, rows), nil
			},
		},
		{
			Name: "lab_status", Description: "Status of one project: each container and VM with its state, and whether the running project differs from its compose file (drift).",
			InputSchema: schema([]string{"host", "lab"}, map[string]string{"host": "host name", "lab": "project id, from list_labs"}),
			run: func(ctx context.Context, c *Client, a args) (string, error) {
				if err := a.need("host", "lab"); err != nil {
					return "", err
				}
				host, id := a.str("host"), a.str("lab")
				var labs []labView
				if err := c.JSON(ctx, http.MethodGet, "/hosts/"+esc(host)+"/labs", nil, nil, &labs); err != nil {
					return "", err
				}
				for _, l := range labs {
					if l.ID != id && l.Name != id {
						continue
					}
					var b strings.Builder
					fmt.Fprintf(&b, "Project %s on %s (%s): %d of %d containers running\n\n", l.Name, host, l.WorkingDir, l.Running, l.Total)
					var rows [][]string
					for _, x := range l.Containers {
						rows = append(rows, []string{x.Name, x.Service, x.Image, x.State, x.Status})
					}
					b.WriteString(table([]string{"CONTAINER", "SERVICE", "IMAGE", "STATE", "STATUS"}, rows))
					if len(l.VMs) > 0 {
						var vr [][]string
						for _, v := range l.VMs {
							vr = append(vr, []string{v.Name, v.State, v.Accel})
						}
						b.WriteString("\n" + table([]string{"VM", "STATE", "ACCEL"}, vr))
					}
					var d struct {
						Services []struct {
							Service string `json:"service"`
							Kind    string `json:"kind"`
							State   string `json:"state"`
						} `json:"services"`
					}
					if !l.Loose && c.JSON(ctx, http.MethodGet, "/hosts/"+esc(host)+"/labs/"+esc(l.ID)+"/drift", nil, nil, &d) == nil {
						var dr []string
						for _, s := range d.Services {
							if s.State != "ok" {
								dr = append(dr, s.Kind+" "+s.Service+": "+s.State)
							}
						}
						if len(dr) == 0 {
							b.WriteString("\nThe project runs as its compose file says.\n")
						} else {
							b.WriteString("\nDiffers from the compose file (bring it up to apply): " + strings.Join(dr, "; ") + "\n")
						}
					}
					return b.String(), nil
				}
				return "", fmt.Errorf("no project %q on %s", id, host)
			},
		},
		{
			Name: "container_logs", Description: "The last lines of the logs of a container.",
			InputSchema: schema([]string{"host", "container"}, map[string]string{"host": "host name", "container": "container name, id or compose service", "tail": "(number) how many lines (default 200)", "grep": "only lines with this text"}),
			run: func(ctx context.Context, c *Client, a args) (string, error) {
				if err := a.need("host", "container"); err != nil {
					return "", err
				}
				x, _, err := container(ctx, c, a.str("host"), a.str("container"))
				if err != nil {
					return "", err
				}
				q := url.Values{"follow": {"0"}, "tail": {strconv.Itoa(a.num("tail", 200))}}
				if x.Runtime != "" {
					q.Set("rt", x.Runtime)
				}
				if g := a.str("grep"); g != "" {
					q.Set("grep", g)
				}
				return c.Stream(ctx, "/hosts/"+esc(a.str("host"))+"/containers/"+esc(x.ID)+"/logs", q, 60<<10)
			},
		},
		{
			Name: "list_clusters", Description: "List the Kubernetes clusters Gantry has, with their context and whether they are read-only.",
			InputSchema: schema(nil, map[string]string{}),
			run: func(ctx context.Context, c *Client, a args) (string, error) {
				var list []clusterView
				if err := c.JSON(ctx, http.MethodGet, "/clusters", nil, nil, &list); err != nil {
					return "", err
				}
				var rows [][]string
				for _, x := range list {
					mode := "read-write"
					if x.Mode == "leitura" {
						mode = "read-only"
					}
					rows = append(rows, []string{x.ID, x.Name, x.Context, mode})
				}
				return table([]string{"ID", "NAME", "CONTEXT", "MODE"}, rows), nil
			},
		},
		{
			Name: "kube_get",
			Description: "Kubernetes objects of a cluster: without name, a table of the kind (pods show their status, restarts and problems such as CrashLoopBackOff); with name, the object, its events and a diagnosis of why its pods do not run. " +
				"Kinds: pods, deployments, statefulsets, daemonsets, jobs, cronjobs, services, ingresses, configmaps, pvcs, nodes, namespaces, events, or a custom resource id. Secrets are never shown.",
			InputSchema: schema([]string{"kind"}, map[string]string{"cluster": "cluster name or id (optional when there is only one)", "kind": "kind, plural or short (pods, deploy, svc…)", "namespace": "namespace (empty: all)", "name": "object name (for one object)"}),
			run: func(ctx context.Context, c *Client, a args) (string, error) {
				if err := a.need("kind"); err != nil {
					return "", err
				}
				kind := kindOf(a.str("kind"))
				if kind == "secrets" {
					return "", fmt.Errorf("Secrets are not available to assistants")
				}
				cl, err := cluster(ctx, c, a.str("cluster"))
				if err != nil {
					return "", err
				}
				ns, name := a.str("namespace"), a.str("name")
				if name == "" {
					return kubeList(ctx, c, cl, kind, ns)
				}
				if ns == "" {
					ns = "-"
				}
				var d map[string]any
				if err := c.JSON(ctx, http.MethodGet, "/clusters/"+esc(cl.ID)+"/r/"+esc(kind)+"/"+esc(ns)+"/"+esc(name)+"/detalhe", nil, nil, &d); err != nil {
					return "", err
				}
				if o, ok := d["object"].(map[string]any); ok {
					if md, ok := o["metadata"].(map[string]any); ok {
						delete(md, "managedFields")
						if an, ok := md["annotations"].(map[string]any); ok {
							delete(an, "kubectl.kubernetes.io/last-applied-configuration")
						}
					}
				}
				var b strings.Builder
				if diag, ok := d["diagnosis"]; ok && diag != nil {
					b.WriteString("Diagnosis:\n" + compact(diag, 8<<10) + "\n\n")
				}
				if ev, ok := d["events"].([]any); ok && len(ev) > 0 {
					b.WriteString("Events:\n" + compact(ev, 8<<10) + "\n\n")
				}
				if p, ok := d["pods"]; ok && p != nil {
					b.WriteString("Pods:\n" + compact(p, 6<<10) + "\n\n")
				}
				b.WriteString("Object:\n" + compact(d["object"], 24<<10))
				return b.String(), nil
			},
		},
		{
			Name: "kube_logs", Description: "The last lines of the logs of a pod, or of the pods of a Deployment, StatefulSet, DaemonSet or Job (up to 5 pods).",
			InputSchema: schema([]string{"namespace", "name"}, map[string]string{"cluster": "cluster name or id", "namespace": "namespace", "name": "pod or workload name", "kind": "pods (default), deployments, statefulsets, daemonsets or jobs", "container": "container (default: all of the pod)", "tail": "(number) lines per pod (default 100)", "previous": "\"true\" for the previous run of a pod that restarted"}),
			run: func(ctx context.Context, c *Client, a args) (string, error) {
				if err := a.need("namespace", "name"); err != nil {
					return "", err
				}
				cl, err := cluster(ctx, c, a.str("cluster"))
				if err != nil {
					return "", err
				}
				ns, name, kind := a.str("namespace"), a.str("name"), kindOf(a.str("kind"))
				if kind == "" {
					kind = "pods"
				}
				pods := []string{name}
				if kind != "pods" {
					var res struct {
						Pods []struct {
							Name string `json:"name"`
						} `json:"pods"`
					}
					if err := c.JSON(ctx, http.MethodGet, "/clusters/"+esc(cl.ID)+"/varios/"+esc(ns)+"/"+esc(kind)+"/"+esc(name)+"/pods", nil, nil, &res); err != nil {
						return "", err
					}
					pods = nil
					for i, p := range res.Pods {
						if i < 5 {
							pods = append(pods, p.Name)
						}
					}
					if len(pods) == 0 {
						return fmt.Sprintf("%s %s has no pods", kind, name), nil
					}
				}
				q := url.Values{"follow": {"0"}, "tail": {strconv.Itoa(a.num("tail", 100))}}
				if ct := a.str("container"); ct != "" {
					q.Set("container", ct)
				}
				if a.str("previous") == "true" {
					q.Set("previous", "1")
				}
				var b strings.Builder
				for _, p := range pods {
					text, err := c.Stream(ctx, "/clusters/"+esc(cl.ID)+"/pods/"+esc(ns)+"/"+esc(p)+"/logs", q, 40<<10)
					if err != nil {
						text = "[" + err.Error() + "]"
					}
					if len(pods) > 1 {
						fmt.Fprintf(&b, "=== %s ===\n", p)
					}
					b.WriteString(text)
					if !strings.HasSuffix(text, "\n") {
						b.WriteString("\n")
					}
				}
				return b.String(), nil
			},
		},
		{
			Name: "kube_events", Description: "The recent events of a cluster (warnings first come from failing pods, scheduling and image pulls).",
			InputSchema: schema(nil, map[string]string{"cluster": "cluster name or id", "namespace": "namespace (empty: all)"}),
			run: func(ctx context.Context, c *Client, a args) (string, error) {
				cl, err := cluster(ctx, c, a.str("cluster"))
				if err != nil {
					return "", err
				}
				return kubeList(ctx, c, cl, "events", a.str("namespace"))
			},
		},
		{
			Name: "helm_release", Description: "Helm releases of a cluster; with name, the release history (values and manifests are never returned).",
			InputSchema: schema(nil, map[string]string{"cluster": "cluster name or id", "namespace": "namespace of the release", "name": "release name"}),
			run: func(ctx context.Context, c *Client, a args) (string, error) {
				cl, err := cluster(ctx, c, a.str("cluster"))
				if err != nil {
					return "", err
				}
				if a.str("name") == "" {
					var list []struct {
						Name, Namespace, Chart, AppVersion, Status string
						Revision                                   int
					}
					if err := c.JSON(ctx, http.MethodGet, "/clusters/"+esc(cl.ID)+"/helm", nil, nil, &list); err != nil {
						return "", err
					}
					var rows [][]string
					for _, r := range list {
						rows = append(rows, []string{r.Namespace, r.Name, r.Chart, r.AppVersion, strconv.Itoa(r.Revision), r.Status})
					}
					return table([]string{"NAMESPACE", "RELEASE", "CHART", "APP", "REVISION", "STATUS"}, rows), nil
				}
				if err := a.need("namespace"); err != nil {
					return "", err
				}
				var d struct {
					Release struct {
						History []map[string]any `json:"history"`
					} `json:"release"`
				}
				if err := c.JSON(ctx, http.MethodGet, "/clusters/"+esc(cl.ID)+"/helm/"+esc(a.str("namespace"))+"/"+esc(a.str("name")), nil, nil, &d); err != nil {
					return "", err
				}
				return "Release " + a.str("name") + " history:\n" + compact(d.Release.History, 16<<10), nil
			},
		},
		// Changes: listed only with writing on in Gantry's settings.
		{
			Name: "lab_up", write: true, Description: "Bring a project up (create or update its containers and VMs as the compose file says).",
			InputSchema: schema([]string{"host", "lab"}, map[string]string{"host": "host name", "lab": "project id"}),
			run:         labAction("up"),
		},
		{
			Name: "lab_down", write: true, Description: "Remove the containers and networks of a project (named volumes and files stay; its VMs are deleted).",
			InputSchema: schema([]string{"host", "lab"}, map[string]string{"host": "host name", "lab": "project id"}),
			run:         labAction("down"),
		},
		{
			Name: "container_restart", write: true, Description: "Restart a container.",
			InputSchema: schema([]string{"host", "container"}, map[string]string{"host": "host name", "container": "container name, id or compose service"}),
			run: func(ctx context.Context, c *Client, a args) (string, error) {
				if err := a.need("host", "container"); err != nil {
					return "", err
				}
				x, _, err := container(ctx, c, a.str("host"), a.str("container"))
				if err != nil {
					return "", err
				}
				q := url.Values{}
				if x.Runtime != "" {
					q.Set("rt", x.Runtime)
				}
				var res map[string]any
				if err := c.JSON(ctx, http.MethodPost, "/hosts/"+esc(a.str("host"))+"/containers/"+esc(x.ID)+"/restart", q, map[string]any{}, &res); err != nil {
					return "", err
				}
				return "container " + x.Name + " restarted", nil
			},
		},
		{
			Name: "kube_scale", write: true, Description: "Scale a Deployment, StatefulSet or DaemonSet to a number of replicas (refused on read-only clusters).",
			InputSchema: schema([]string{"kind", "namespace", "name", "replicas"}, map[string]string{"cluster": "cluster name or id", "kind": "deployments or statefulsets", "namespace": "namespace", "name": "name", "replicas": "(number) replicas"}),
			run: func(ctx context.Context, c *Client, a args) (string, error) {
				if err := a.need("kind", "namespace", "name", "replicas"); err != nil {
					return "", err
				}
				return kubeAction(ctx, c, a, "escalar", map[string]any{"replicas": a.num("replicas", -1)})
			},
		},
		{
			Name: "kube_restart", write: true, Description: "Restart a Deployment, StatefulSet or DaemonSet (rolling restart; refused on read-only clusters).",
			InputSchema: schema([]string{"kind", "namespace", "name"}, map[string]string{"cluster": "cluster name or id", "kind": "deployments, statefulsets or daemonsets", "namespace": "namespace", "name": "name"}),
			run: func(ctx context.Context, c *Client, a args) (string, error) {
				if err := a.need("kind", "namespace", "name"); err != nil {
					return "", err
				}
				return kubeAction(ctx, c, a, "reiniciar", map[string]any{})
			},
		},
	}
}

func labAction(op string) func(ctx context.Context, c *Client, a args) (string, error) {
	return func(ctx context.Context, c *Client, a args) (string, error) {
		if err := a.need("host", "lab"); err != nil {
			return "", err
		}
		var res struct {
			OK      bool   `json:"ok"`
			Output  string `json:"output"`
			Error   string `json:"error"`
			Command string `json:"command"`
		}
		if err := c.JSON(ctx, http.MethodPost, "/hosts/"+esc(a.str("host"))+"/labs/"+esc(a.str("lab"))+"/"+op, nil, map[string]any{}, &res); err != nil {
			return "", err
		}
		out := tail(strings.TrimSpace(res.Output), 16<<10)
		if !res.OK {
			return "", fmt.Errorf("%s failed: %s\n%s", op, res.Error, out)
		}
		return op + " done (" + res.Command + ")\n" + out, nil
	}
}

func kubeAction(ctx context.Context, c *Client, a args, act string, body map[string]any) (string, error) {
	cl, err := cluster(ctx, c, a.str("cluster"))
	if err != nil {
		return "", err
	}
	kind := kindOf(a.str("kind"))
	var res map[string]any
	if err := c.JSON(ctx, http.MethodPost, "/clusters/"+esc(cl.ID)+"/r/"+esc(kind)+"/"+esc(a.str("namespace"))+"/"+esc(a.str("name"))+"/"+act, nil, body, &res); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s %s/%s: %s done", kind, a.str("namespace"), a.str("name"), map[string]string{"escalar": "scale", "reiniciar": "restart"}[act]), nil
}
