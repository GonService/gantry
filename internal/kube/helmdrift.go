package kube

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
)

// Drift of a Helm release: the objects as they run against the manifest
// Helm applied, only on the fields the manifest sets (the server's
// defaults and status are not changes), and who changed them.

// DriftChange is one field that differs.
type DriftChange struct {
	Path string `json:"path"`
	Want string `json:"want"` // as the manifest says ("" when absent there)
	Have string `json:"have"` // as it runs ("—" when absent in the cluster)
}

// DriftObject is one object of the manifest.
type DriftObject struct {
	Kind      string        `json:"kind"`
	Kid       string        `json:"kid,omitempty"` // the screen's kind id, when it has one
	Namespace string        `json:"namespace,omitempty"`
	Name      string        `json:"name"`
	State     string        `json:"state"` // same, changed, gone, unknown
	Changes   []DriftChange `json:"changes,omitempty"`
	Managers  []string      `json:"managers,omitempty"` // who wrote it besides Helm
	Error     string        `json:"error,omitempty"`
}

var kidOf = map[string]string{"Pod": "pods", "Deployment": "deployments", "StatefulSet": "statefulsets", "DaemonSet": "daemonsets", "ReplicaSet": "replicasets",
	"Job": "jobs", "CronJob": "cronjobs", "HorizontalPodAutoscaler": "hpas", "Service": "services", "Ingress": "ingresses", "NetworkPolicy": "networkpolicies",
	"ConfigMap": "configmaps", "Secret": "secrets", "ServiceAccount": "serviceaccounts", "PersistentVolumeClaim": "pvcs", "PersistentVolume": "pvs",
	"StorageClass": "storageclasses", "Namespace": "namespaces"}

// manifestDocs splits a multi-document manifest into objects.
func manifestDocs(m string) []map[string]any {
	var out []map[string]any
	for _, doc := range regexp.MustCompile(`(?m)^---[ \t]*$`).Split(m, -1) {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		var o map[string]any
		if err := yaml.Unmarshal([]byte(doc), &o); err != nil || o == nil || o["kind"] == nil {
			continue
		}
		out = append(out, o)
	}
	return out
}

// HelmDrift compares the latest revision of a release with the cluster.
// secrets false leaves the content of Secrets out (read-only mode).
func (k *Client) HelmDrift(ctx context.Context, ns, name string, secrets bool) ([]DriftObject, error) {
	d, err := k.HelmRelease(ctx, ns, name, true, 0)
	if err != nil {
		return nil, err
	}
	if d.ContentError != "" {
		return nil, fmt.Errorf("%s", d.ContentError)
	}
	out := []DriftObject{}
	for _, want := range manifestDocs(d.Manifest) {
		av, _ := want["apiVersion"].(string)
		kind, _ := want["kind"].(string)
		md, _ := want["metadata"].(map[string]any)
		oname, _ := md["name"].(string)
		ons, _ := md["namespace"].(string)
		o := DriftObject{Kind: kind, Kid: kidOf[kind], Name: oname}
		res, err := k.resourceFor(ctx, av, kind)
		if err != nil {
			o.State, o.Error = "unknown", err.Error()
			out = append(out, o)
			continue
		}
		if res.Namespaced {
			if ons == "" {
				ons = ns
			}
			o.Namespace = ons
		}
		p := "/apis/" + av
		if !strings.Contains(av, "/") {
			p = "/api/" + av
		}
		if res.Namespaced {
			p += "/namespaces/" + url.PathEscape(ons)
		}
		p += "/" + res.Name + "/" + url.PathEscape(oname)
		var live map[string]any
		if err := k.Get(ctx, p, nil, &live); err != nil {
			if NotFound(err) {
				o.State = "gone"
			} else {
				o.State, o.Error = "unknown", err.Error()
			}
			out = append(out, o)
			continue
		}
		if kind == "Secret" {
			secretStrings(want)
			if !secrets {
				delete(want, "data")
				delete(live, "data")
			}
		}
		delete(want, "status")
		var ch []DriftChange
		diffValue("", want, live, &ch)
		if len(ch) == 0 {
			o.State = "same"
		} else {
			o.State = "changed"
			if kind == "Secret" {
				for i := range ch {
					if strings.HasPrefix(ch[i].Path, "data.") {
						ch[i].Want, ch[i].Have = "(…)", "(diferente)"
					}
				}
			}
			o.Changes = ch
			o.Managers = managers(live)
		}
		out = append(out, o)
	}
	return out, nil
}

// secretStrings moves stringData into data (base64), as the API stores it.
func secretStrings(o map[string]any) {
	sd, _ := o["stringData"].(map[string]any)
	if len(sd) == 0 {
		return
	}
	data, _ := o["data"].(map[string]any)
	if data == nil {
		data = map[string]any{}
	}
	for k, v := range sd {
		data[k] = base64.StdEncoding.EncodeToString([]byte(fmt.Sprint(v)))
	}
	o["data"] = data
	delete(o, "stringData")
}

// managers are the writers of the object other than Helm, with when.
func managers(live map[string]any) []string {
	md, _ := live["metadata"].(map[string]any)
	mf, _ := md["managedFields"].([]any)
	var out []string
	for _, x := range mf {
		m, _ := x.(map[string]any)
		name, _ := m["manager"].(string)
		op, _ := m["operation"].(string)
		sub, _ := m["subresource"].(string)
		if name == "" || strings.EqualFold(name, "helm") || sub == "status" || op == "" {
			continue
		}
		when, _ := m["time"].(string)
		out = append(out, strings.TrimSpace(name+" ("+strings.ToLower(op)+") "+when))
	}
	sort.Strings(out)
	return out
}

// metadata fields the server owns: never a change.
var serverMeta = map[string]bool{"creationTimestamp": true, "resourceVersion": true, "uid": true, "generation": true, "managedFields": true, "selfLink": true}

// diffValue walks what the manifest sets and notes where the cluster differs.
func diffValue(path string, want, have any, out *[]DriftChange) {
	if want == nil {
		return // null in a template: nothing asked
	}
	switch w := want.(type) {
	case map[string]any:
		h, _ := have.(map[string]any)
		if h == nil {
			*out = append(*out, DriftChange{Path: path, Want: short(w), Have: "—"})
			return
		}
		keys := make([]string, 0, len(w))
		for k := range w {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if path == "" && (k == "apiVersion" || k == "kind") {
				continue // the object was read by them
			}
			if path == "metadata" && serverMeta[k] {
				continue
			}
			if path == "metadata.annotations" && strings.HasPrefix(k, "meta.helm.sh/") {
				continue
			}
			sub := k
			if path != "" {
				sub = path + "." + k
			}
			hv, ok := h[k]
			if !ok {
				if isEmpty(w[k]) {
					continue
				}
				*out = append(*out, DriftChange{Path: sub, Want: short(w[k]), Have: "—"})
				continue
			}
			diffValue(sub, w[k], hv, out)
		}
	case []any:
		h, _ := have.([]any)
		if key := listKey(w); key != "" {
			// Lists of named items (containers, ports, env…): by the key.
			byKey := map[string]any{}
			for _, x := range h {
				if m, ok := x.(map[string]any); ok {
					byKey[fmt.Sprint(m[key])] = x
				}
			}
			for _, x := range w {
				m := x.(map[string]any)
				id := fmt.Sprint(m[key])
				sub := fmt.Sprintf("%s[%s=%s]", path, key, id)
				hv, ok := byKey[id]
				if !ok {
					*out = append(*out, DriftChange{Path: sub, Want: short(m), Have: "—"})
					continue
				}
				diffValue(sub, x, hv, out)
			}
			for _, x := range h {
				if m, ok := x.(map[string]any); ok {
					id := fmt.Sprint(m[key])
					found := false
					for _, y := range w {
						if fmt.Sprint(y.(map[string]any)[key]) == id {
							found = true
						}
					}
					if !found {
						*out = append(*out, DriftChange{Path: fmt.Sprintf("%s[%s=%s]", path, key, id), Want: "—", Have: short(m)})
					}
				}
			}
			return
		}
		if len(w) != len(h) {
			*out = append(*out, DriftChange{Path: path, Want: short(w), Have: short(h)})
			return
		}
		for i := range w {
			diffValue(fmt.Sprintf("%s[%d]", path, i), w[i], h[i], out)
		}
	default:
		if !sameScalar(path, want, have) {
			*out = append(*out, DriftChange{Path: path, Want: short(want), Have: short(have)})
		}
	}
}

func isEmpty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case map[string]any:
		return len(x) == 0
	case []any:
		return len(x) == 0
	case string:
		return x == ""
	}
	return false
}

// listKey: the field that names the items of a list, when all have it.
func listKey(l []any) string {
	if len(l) == 0 {
		return ""
	}
	for _, key := range []string{"name", "containerPort", "mountPath", "port", "key", "ip"} {
		ok := true
		for _, x := range l {
			m, isMap := x.(map[string]any)
			if !isMap || m[key] == nil {
				ok = false
				break
			}
		}
		if ok {
			return key
		}
	}
	return ""
}

func num(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case uint64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil
	}
	return 0, false
}

var quantityRe = regexp.MustCompile(`^([0-9.]+)(m|k|Ki|M|Mi|G|Gi|T|Ti|P|Pi|E|Ei)?$`)

var quantityMul = map[string]float64{"": 1, "m": 1e-3, "k": 1e3, "Ki": 1 << 10, "M": 1e6, "Mi": 1 << 20, "G": 1e9, "Gi": 1 << 30, "T": 1e12, "Ti": 1 << 40, "P": 1e15, "Pi": 1 << 50, "E": 1e18, "Ei": 1 << 60}

// quantity reads a Kubernetes quantity (500m, 1Gi, 0.5).
func quantity(v any) (float64, bool) {
	s := strings.TrimSpace(fmt.Sprint(v))
	m := quantityRe.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	f, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	return f * quantityMul[m[2]], true
}

func sameScalar(path string, a, b any) bool {
	if fmt.Sprint(a) == fmt.Sprint(b) {
		return true
	}
	if x, ok := num(a); ok {
		if y, ok := num(b); ok {
			return x == y
		}
	}
	// Quantities the server writes in its own form (cpu 0.5 → 500m).
	if strings.Contains(path, "resources.") || strings.Contains(path, "storage") {
		if x, ok := quantity(a); ok {
			if y, ok := quantity(b); ok {
				return math.Abs(x-y) <= 1e-9*math.Max(1, math.Abs(x))
			}
		}
	}
	return false
}

func short(v any) string {
	switch x := v.(type) {
	case string:
		if len(x) > 120 {
			return x[:117] + "…"
		}
		return x
	case nil:
		return "null"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	b = bytes.TrimSpace(b)
	if len(b) > 120 {
		return string(b[:117]) + "…"
	}
	return string(b)
}
