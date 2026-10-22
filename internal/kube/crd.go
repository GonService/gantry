package kube

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Custom resources (CRDs): each one becomes a Kind with ID "crd.<plural>.<group>"
// (the name of the CRD) and the columns its authors chose for kubectl get
// (additionalPrinterColumns).

const crdTTL = 2 * time.Minute

type crdCache struct {
	kinds []Kind
	at    time.Time
}

// CRDs lists the custom resources of the cluster (kept for a while).
func (k *Client) CRDs(ctx context.Context) ([]Kind, error) {
	discMu.Lock()
	c := k.crds
	discMu.Unlock()
	if c != nil && time.Since(c.at) < crdTTL {
		return c.kinds, nil
	}
	var l struct {
		Items []obj `json:"items"`
	}
	var out []Kind
	if err := k.Get(ctx, "/apis/apiextensions.k8s.io/v1/customresourcedefinitions", nil, &l); err != nil {
		// Many accounts may not list the CRDs (a cluster-wide read) but do
		// see the API groups (discovery, open to every user): the custom
		// resources come from there, without the kubectl columns. Without
		// this, Argo CD's screens and Customizados were missing.
		d, derr := k.discoverCustom(ctx)
		if derr != nil || len(d) == 0 {
			return nil, err
		}
		out = d
	} else {
		for _, o := range l.Items {
			if kd, ok := crdKind(o); ok {
				out = append(out, kd)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return out[i].Label < out[j].Label
	})
	discMu.Lock()
	k.crds = &crdCache{kinds: out, at: time.Now()}
	discMu.Unlock()
	return out, nil
}

func crdKind(o obj) (Kind, bool) {
	group := o.str("spec", "group")
	plural := o.str("spec", "names", "plural")
	if group == "" || plural == "" {
		return Kind{}, false
	}
	// The version kubectl uses: the storage one, else the first served.
	var ver obj
	for _, v := range o.list("spec", "versions") {
		if v.get("served") == true && (ver == nil || v.get("storage") == true) {
			ver = v
		}
	}
	if ver == nil {
		return Kind{}, false
	}
	type col struct {
		name, typ, path string
	}
	var cols []col
	for _, c := range ver.list("additionalPrinterColumns") {
		if c.num("priority") > 0 {
			continue // kubectl shows these only with -o wide
		}
		name := c.str("name")
		if strings.EqualFold(name, "age") && c.str("type") == "date" {
			continue // the table already has Idade
		}
		cols = append(cols, col{name, c.str("type"), c.str("jsonPath")})
	}
	if len(cols) > 5 {
		cols = cols[:5]
	}
	kd := Kind{
		ID:         "crd." + plural + "." + group,
		Label:      o.str("spec", "names", "kind"),
		Group:      group,
		APIVersion: group + "/" + ver.str("name"),
		Resource:   plural,
		Namespaced: o.str("spec", "scope") == "Namespaced",
		Custom:     true,
	}
	for _, c := range cols {
		kd.Columns = append(kd.Columns, c.name)
	}
	kd.summary = func(x obj) Row {
		r := base(x)
		for _, c := range cols {
			r.Cells = append(r.Cells, printerValue(JSONPath(x, c.path), c.typ))
		}
		// A Ready condition that is not True is worth a look.
		for _, cond := range x.list("status", "conditions") {
			if cond.str("type") == "Ready" && cond.str("status") == "False" {
				r.Problem = firstLine(strings.TrimSpace(cond.str("reason") + ": " + cond.str("message")))
			}
		}
		if r.Cells == nil {
			r.Cells = []string{}
		}
		return r
	}
	return kd, true
}

// KindFor resolves a kind id: a built-in one or a custom resource.
func (k *Client) KindFor(ctx context.Context, id string) (Kind, bool) {
	if kd, ok := KindByID(id); ok {
		return kd, true
	}
	if !strings.HasPrefix(id, "crd.") {
		return Kind{}, false
	}
	list, err := k.CRDs(ctx)
	if err != nil {
		return Kind{}, false
	}
	for _, kd := range list {
		if kd.ID == id {
			return kd, true
		}
	}
	// Just installed? Look again once.
	discMu.Lock()
	k.crds = nil
	discMu.Unlock()
	if list, err = k.CRDs(ctx); err == nil {
		for _, kd := range list {
			if kd.ID == id {
				return kd, true
			}
		}
	}
	return Kind{}, false
}

func mapKey(cur []any, key string) []any {
	var next []any
	for _, c := range cur {
		if m, ok := c.(map[string]any); ok {
			if v, ok := m[key]; ok {
				next = append(next, v)
			}
		}
	}
	return next
}

func printerValue(v any, typ string) string {
	if v == nil {
		return ""
	}
	switch typ {
	case "date":
		if s, ok := v.(string); ok {
			if t := ts(s); t > 0 {
				return age(time.Now().Unix() - t)
			}
		}
	case "integer":
		if f, ok := v.(float64); ok {
			return strconv.FormatInt(int64(f), 10)
		}
	}
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	case []any:
		var parts []string
		for _, e := range x {
			parts = append(parts, printerValue(e, ""))
		}
		return strings.Join(parts, ", ")
	default:
		return fmt.Sprint(x)
	}
}

// JSONPath evaluates the subset of JSONPath that printer columns use:
// .a.b, .a[0].b, .a[*].b and .a[?(@.k=="v")].b (with == or !=).
func JSONPath(o map[string]any, path string) any {
	path = strings.TrimSpace(path)
	path = strings.TrimPrefix(strings.TrimSuffix(strings.TrimPrefix(path, "{"), "}"), "$")
	cur := []any{map[string]any(o)}
	for path != "" {
		switch {
		case strings.HasPrefix(path, "."):
			path = path[1:]
			// A key ends at an unescaped . or [ (labels have dots: a\.b/c).
			var key strings.Builder
			for path != "" && path[0] != '.' && path[0] != '[' {
				if path[0] == '\\' && len(path) > 1 {
					path = path[1:]
				}
				key.WriteByte(path[0])
				path = path[1:]
			}
			cur = mapKey(cur, key.String())
		case strings.HasPrefix(path, "["):
			end := strings.Index(path, "]")
			if end < 0 {
				return nil
			}
			sel := path[1:end]
			path = path[end+1:]
			if len(sel) >= 2 && (sel[0] == '\'' || sel[0] == '"') && sel[len(sel)-1] == sel[0] {
				cur = mapKey(cur, sel[1:len(sel)-1]) // ['key']
				if len(cur) == 0 {
					return nil
				}
				continue
			}
			var next []any
			for _, c := range cur {
				arr, ok := c.([]any)
				if !ok {
					continue
				}
				switch {
				case sel == "*":
					next = append(next, arr...)
				case strings.HasPrefix(sel, "?(@."):
					expr := strings.TrimSuffix(strings.TrimPrefix(sel, "?(@."), ")")
					op := "=="
					parts := strings.SplitN(expr, "==", 2)
					if len(parts) != 2 {
						op = "!="
						parts = strings.SplitN(expr, "!=", 2)
					}
					if len(parts) != 2 {
						continue
					}
					field := strings.TrimSpace(parts[0])
					want := strings.Trim(strings.TrimSpace(parts[1]), `"'`)
					for _, e := range arr {
						m, ok := e.(map[string]any)
						if !ok {
							continue
						}
						got := fmt.Sprint(m[field])
						if (op == "==") == (got == want) {
							next = append(next, e)
						}
					}
				default:
					i, err := strconv.Atoi(sel)
					if err != nil {
						continue
					}
					if i < 0 {
						i += len(arr)
					}
					if i >= 0 && i < len(arr) {
						next = append(next, arr[i])
					}
				}
			}
			cur = next
		default:
			return nil
		}
		if len(cur) == 0 {
			return nil
		}
	}
	if len(cur) == 1 {
		return cur[0]
	}
	return cur
}

// builtinGroup: API groups of Kubernetes itself (not custom resources).
func builtinGroup(g string) bool {
	switch g {
	case "", "apps", "batch", "autoscaling", "policy", "extensions", "events.k8s.io":
		return true
	}
	return strings.HasSuffix(g, ".k8s.io")
}

// discoverCustom lists the resources of the cluster's non-Kubernetes API
// groups (argoproj.io, cert-manager.io…) through discovery.
func (k *Client) discoverCustom(ctx context.Context) ([]Kind, error) {
	var groups struct {
		Groups []struct {
			Name      string `json:"name"`
			Preferred struct {
				GroupVersion string `json:"groupVersion"`
			} `json:"preferredVersion"`
		} `json:"groups"`
	}
	if err := k.Get(ctx, "/apis", nil, &groups); err != nil {
		return nil, err
	}
	var out []Kind
	for _, g := range groups.Groups {
		gv := g.Preferred.GroupVersion
		if builtinGroup(g.Name) || gv == "" {
			continue
		}
		var rl struct {
			Resources []struct {
				Name       string   `json:"name"`
				Kind       string   `json:"kind"`
				Namespaced bool     `json:"namespaced"`
				Verbs      []string `json:"verbs"`
			} `json:"resources"`
		}
		if err := k.Get(ctx, "/apis/"+gv, nil, &rl); err != nil {
			continue // an aggregated API that is down: skip it
		}
		for _, r := range rl.Resources {
			if strings.Contains(r.Name, "/") || !slicesContains(r.Verbs, "list") {
				continue // subresources (status, scale) and what cannot be listed
			}
			out = append(out, Kind{
				ID:         "crd." + r.Name + "." + g.Name,
				Label:      r.Kind,
				Group:      g.Name,
				APIVersion: gv,
				Resource:   r.Name,
				Namespaced: r.Namespaced,
				Custom:     true,
				summary: func(x obj) Row {
					row := base(x)
					row.Cells = []string{}
					return row
				},
			})
		}
	}
	return out, nil
}

func slicesContains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
