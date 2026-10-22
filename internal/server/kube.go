package server

// Kubernetes clusters: added by the user (from a kubeconfig, never by
// themselves) or created by Gantry (a k3s project), shown in the sidebar
// with their own pages. Each cluster has a mode that guards what changes
// it: "leitura" (only look) or "livre" (everything the account may do).

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gantry/internal/applog"
	"gantry/internal/kube"
)

type clusterRec struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Source  string `json:"source"` // kubeconfig (a file of the user), colado (Gantry's copy), gantry (a project)
	File    string `json:"file"`
	Context string `json:"context"`
	Mode    string `json:"mode"` // leitura, livre
	Lab     string `json:"lab,omitempty"`
	Added   int64  `json:"added"`
	// Gantry's clusters: the machine of the project and the API port
	// published there (reached through the SSH tunnel on servers).
	Machine string `json:"machine,omitempty"`
	Port    int    `json:"port,omitempty"`
}

var clusterMu sync.Mutex

func (s *Server) clustersFile() string { return filepath.Join(s.configDir(), "clusters.json") }

func (s *Server) clusters() []clusterRec {
	var list []clusterRec
	if b, err := os.ReadFile(s.clustersFile()); err == nil {
		_ = json.Unmarshal(b, &list)
	}
	for i := range list {
		// "confirmar" (type or click the cluster's name) existed until
		// 0.30: those clusters are free now.
		if list[i].Mode != "leitura" {
			list[i].Mode = "livre"
		}
	}
	return list
}

func (s *Server) saveClusters(list []clusterRec) error {
	sort.Slice(list, func(i, j int) bool { return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name) })
	b, _ := json.MarshalIndent(list, "", "  ")
	if err := os.MkdirAll(s.configDir(), 0o700); err != nil {
		return err
	}
	return os.WriteFile(s.clustersFile(), b, 0o600)
}

func (s *Server) clusterByID(id string) (clusterRec, bool) {
	clusterMu.Lock()
	defer clusterMu.Unlock()
	for _, c := range s.clusters() {
		if c.ID == id {
			return c, true
		}
	}
	return clusterRec{}, false
}

// kube clients, rebuilt when the kubeconfig file changes.
type kubeEntry struct {
	k      *kube.Client
	mod    time.Time
	server string
}

var (
	kubeMu      sync.Mutex
	kubeClients = map[string]kubeEntry{}
)

// forgetKubeClient drops the client of a cluster (and its watches).
func forgetKubeClient(id string) {
	kubeMu.Lock()
	e, ok := kubeClients[id]
	delete(kubeClients, id)
	kubeMu.Unlock()
	if ok {
		e.k.StopWatches()
	}
}

func (s *Server) kubeClient(c clusterRec) (*kube.Client, error) {
	path := kube.Expand(c.File)
	st, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("não achei o kubeconfig %s", c.File)
	}
	server := ""
	if c.Source == "gantry" && c.Machine != "" && c.Machine != "local" {
		h := s.machineHost(c.Machine)
		if h == nil {
			return nil, fmt.Errorf("a máquina do cluster %s não está mais no Gantry", c.Name)
		}
		if h.Remote() && !h.Wanted() {
			return nil, fmt.Errorf("abra a máquina %s para conectar ao cluster", h.Name)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		addr, err := h.LocalAddr(ctx, "127.0.0.1:"+strconv.Itoa(c.Port))
		cancel()
		if err != nil {
			return nil, err
		}
		server = "https://" + addr
	}
	kubeMu.Lock()
	e, ok := kubeClients[c.ID]
	kubeMu.Unlock()
	if ok && e.mod.Equal(st.ModTime()) && e.server == server {
		return e.k, nil
	}
	if ok {
		e.k.StopWatches() // the file or the address changed
	}
	cfg, err := kube.Load(path)
	if err != nil {
		return nil, err
	}
	if server != "" {
		cfg.SetServer(server, "127.0.0.1")
	}
	k, err := cfg.NewClient(c.Context)
	if err != nil {
		return nil, err
	}
	s.watchPodLeaving(c.ID, k)
	kubeMu.Lock()
	kubeClients[c.ID] = kubeEntry{k, st.ModTime(), server}
	kubeMu.Unlock()
	return k, nil
}

func validMode(m string) bool { return m == "leitura" || m == "livre" }

/* adding and listing */

func (s *Server) listClusters(w http.ResponseWriter, r *http.Request) {
	clusterMu.Lock()
	list := s.clusters()
	clusterMu.Unlock()
	out := make([]clusterView, 0, len(list))
	for _, c := range list {
		v := clusterView{clusterRec: c}
		v.Plugin, v.PluginHint = missingPlugin(c)
		out = append(out, v)
	}
	writeJSON(w, out)
}

// kubeconfigContexts lists the contexts of a kubeconfig (path, or the
// default one) so the user can pick what to add.
func (s *Server) kubeconfigContexts(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
		YAML string `json:"yaml"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&req)
	var cfg *kube.Config
	var err error
	path := ""
	if strings.TrimSpace(req.YAML) != "" {
		cfg, err = kube.Parse([]byte(req.YAML), "")
	} else {
		path = req.Path
		if path == "" {
			path = kube.DefaultPath()
		}
		cfg, err = kube.Load(path)
		if errors.Is(err, os.ErrNotExist) {
			err = fmt.Errorf("não achei um kubeconfig em %s; escolha o arquivo ou cole o conteúdo", path)
		}
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	clusterMu.Lock()
	have := map[string]bool{}
	for _, c := range s.clusters() {
		if kube.Expand(c.File) == kube.Expand(path) {
			have[c.Context] = true
		}
	}
	clusterMu.Unlock()
	type ctxView struct {
		kube.ContextInfo
		Added bool `json:"added,omitempty"`
	}
	var out []ctxView
	for _, c := range cfg.ContextList() {
		out = append(out, ctxView{c, have[c.Name]})
	}
	writeJSON(w, map[string]any{"path": path, "contexts": out})
}

func (s *Server) addCluster(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path    string `json:"path"`
		YAML    string `json:"yaml"`
		Context string `json:"context"`
		Name    string `json:"name"`
		Mode    string `json:"mode"`
		Folder  string `json:"folder"`
		// Trust: the user saw the command and files of a pasted kubeconfig.
		Trust bool `json:"trust"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Mode == "" {
		req.Mode = "livre"
	}
	if !validMode(req.Mode) {
		writeErr(w, http.StatusBadRequest, errors.New("modo inválido"))
		return
	}
	c := clusterRec{ID: randomID(8), Context: req.Context, Mode: req.Mode, Added: time.Now().Unix()}
	if strings.TrimSpace(req.YAML) != "" {
		// A pasted kubeconfig: Gantry keeps its own copy of that context.
		cfg, err := kube.Parse([]byte(req.YAML), "")
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if c.Context == "" {
			c.Context = cfg.CurrentContext
		}
		for _, ci := range cfg.ContextList() {
			if ci.Name == c.Context && (ci.Exec != "" || len(ci.Files) > 0) && !req.Trust {
				what := []string{}
				if ci.Exec != "" {
					what = append(what, "roda o comando "+ci.Exec)
				}
				if len(ci.Files) > 0 {
					what = append(what, "lê "+strings.Join(ci.Files, ", "))
				}
				writeErr(w, http.StatusPreconditionFailed, fmt.Errorf("este kubeconfig %s neste computador; confirme que confia nele", strings.Join(what, " e ")))
				return
			}
		}
		one, err := cfg.Extract(c.Context)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		dir := filepath.Join(s.configDir(), "clusters")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		c.File = filepath.Join(dir, c.ID+".yaml")
		if err := os.WriteFile(c.File, one, 0o600); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		c.Source = "colado"
	} else {
		c.File = req.Path
		if c.File == "" {
			c.File = kube.DefaultPath()
		}
		if _, err := kube.Load(c.File); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		c.Source = "kubeconfig"
	}
	c.Name = strings.TrimSpace(req.Name)
	if c.Name == "" {
		c.Name = c.Context
	}
	// It must answer before it goes to the list.
	k, err := s.kubeClient(c)
	if err == nil {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		_, err = k.Version(ctx)
		cancel()
	}
	if err != nil {
		if c.Source == "colado" {
			os.Remove(c.File)
		}
		writeErr(w, http.StatusBadRequest, fmt.Errorf("o cluster não respondeu: %v", err))
		return
	}
	clusterMu.Lock()
	list := append(s.clusters(), c)
	err = s.saveClusters(list)
	clusterMu.Unlock()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if req.Folder != "" {
		groupsMu.Lock()
		d := s.loadGroups()
		if d.find(req.Folder) >= 0 {
			d.Items["cluster:"+c.ID] = req.Folder
			_ = s.saveGroups(d)
		}
		groupsMu.Unlock()
	}
	writeJSON(w, c)
}

func (s *Server) editCluster(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name *string `json:"name"`
		Mode *string `json:"mode"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req)
	clusterMu.Lock()
	defer clusterMu.Unlock()
	list := s.clusters()
	for i := range list {
		if list[i].ID != r.PathValue("id") {
			continue
		}
		if req.Name != nil && strings.TrimSpace(*req.Name) != "" {
			list[i].Name = strings.TrimSpace(*req.Name)
		}
		if req.Mode != nil {
			if !validMode(*req.Mode) {
				writeErr(w, http.StatusBadRequest, errors.New("modo inválido"))
				return
			}
			if *req.Mode == "leitura" && list[i].Mode != "leitura" {
				// Read-only: the open port-forwards (which let writes through) close.
				closeForwards(list[i].ID, nil)
			}
			list[i].Mode = *req.Mode
		}
		if err := s.saveClusters(list); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, list[i])
		return
	}
	writeErr(w, http.StatusNotFound, errors.New("cluster não encontrado"))
}

// forgetArgoTokens deletes the Argo CD server tokens the Argo CD screens
// (removed from Gantry) kept in the keychain, one per cluster.
// Once per computer (a marker file in the config folder).
func (s *Server) forgetArgoTokens() {
	mark := filepath.Join(s.configDir(), ".argocd-esquecido")
	if _, err := os.Stat(mark); err == nil {
		return
	}
	st := s.secretStore()
	for _, c := range s.clusters() {
		if v, err := st.Get("argocd:" + c.ID); err == nil && v != "" {
			_ = st.Delete("argocd:" + c.ID)
		}
	}
	_ = os.WriteFile(mark, nil, 0o600)
}

// removeCluster takes it out of Gantry; the cluster itself does not change.
func (s *Server) removeCluster(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	clusterMu.Lock()
	list := s.clusters()
	kept := list[:0]
	var gone *clusterRec
	for _, c := range list {
		if c.ID == id {
			c := c
			gone = &c
			continue
		}
		kept = append(kept, c)
	}
	err := s.saveClusters(kept)
	clusterMu.Unlock()
	if gone == nil {
		writeErr(w, http.StatusNotFound, errors.New("cluster não encontrado"))
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if gone.Source == "colado" || gone.Source == "gantry" {
		os.Remove(gone.File)
	}
	forgetKubeClient(id)
	dropSampler(id)
	closeTunnels(id, nil)
	s.forgetItem("cluster:" + id)
	writeJSON(w, map[string]bool{"ok": true})
}

/* the cluster pages */

type kubeHandler func(w http.ResponseWriter, r *http.Request, c clusterRec, k *kube.Client)

func (s *Server) withCluster(fn kubeHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, ok := s.clusterByID(r.PathValue("id"))
		if !ok {
			writeErr(w, http.StatusNotFound, errors.New("cluster não encontrado"))
			return
		}
		k, err := s.kubeClient(c)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err)
			return
		}
		touchSampler(c.ID, k) // CPU and memory history while the cluster is in use
		fn(w, r, c, k)
	}
}

// guard checks the cluster's mode before a change: read-only refuses.
func guard(w http.ResponseWriter, r *http.Request, c clusterRec) bool {
	if c.Mode != "leitura" {
		return true
	}
	writeErr(w, http.StatusForbidden, errors.New("este cluster está em modo só leitura; mude em Configuração › Clusters"))
	return false
}

func kubeStatus(err error) int {
	var ae *kube.APIError
	if errors.As(err, &ae) {
		return ae.Status
	}
	return http.StatusBadGateway
}

func (s *Server) clusterKinds(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, kube.Kinds)
}

// clusterOverview is the first page of a cluster.
func (s *Server) clusterOverview(w http.ResponseWriter, r *http.Request, c clusterRec, k *kube.Client) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	ns := r.URL.Query().Get("ns")
	ver, err := k.Version(ctx)
	if err != nil {
		writeErr(w, kubeStatus(err), err)
		return
	}
	out := map[string]any{"version": ver, "server": k.Server, "mode": c.Mode}
	var mu sync.Mutex
	var wg sync.WaitGroup
	list := func(id, ns string) {
		defer wg.Done()
		kind, _ := kube.KindByID(id)
		rows, err := k.ListCached(ctx, kind, ns) // followed by watch
		if id == "events" && err == nil {
			// Only alerts on the overview.
			var warn []kube.Row
			for _, e := range rows {
				if len(e.Cells) > 0 && e.Cells[0] == "Warning" {
					warn = append(warn, e)
				}
			}
			rows = warn
		}
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			out[id+"Error"] = err.Error()
			return
		}
		out[id] = rows
	}
	for _, id := range []string{"nodes", "namespaces"} {
		wg.Add(1)
		go func() { defer applog.Recover("server/kube.go:481"); list(id, "") }()
	}
	for _, id := range []string{"pods", "deployments", "statefulsets", "daemonsets", "jobs", "services", "events"} {
		wg.Add(1)
		go func() { defer applog.Recover("server/kube.go:485"); list(id, ns) }()
	}
	var usage map[string]kube.Usage
	wg.Add(1)
	go func() {
		defer applog.Recover("server/kube.go:489")
		defer wg.Done()
		u := k.NodeUsage(ctx)
		mu.Lock()
		usage = u
		mu.Unlock()
	}()
	wg.Wait()
	out["nodeUsage"] = usage
	// Only warnings, newest first, for the overview.
	if evs, ok := out["events"].([]kube.Row); ok {
		var warn []kube.Row
		for _, e := range evs {
			if e.Problem != "" && len(warn) < 30 {
				warn = append(warn, e)
			}
		}
		out["events"] = warn
	}
	writeJSON(w, out)
}

func (s *Server) clusterList(w http.ResponseWriter, r *http.Request, c clusterRec, k *kube.Client) {
	kind, ok := k.KindFor(r.Context(), r.PathValue("kind"))
	if !ok {
		writeErr(w, http.StatusNotFound, errors.New("tipo desconhecido"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	ns := r.URL.Query().Get("ns")
	var rows []kube.Row
	var err error
	if sel := r.URL.Query().Get("sel"); sel != "" {
		rows, err = k.List(ctx, kind, ns, sel)
	} else {
		rows, err = k.ListCached(ctx, kind, ns) // followed by watch
	}
	if err != nil {
		writeErr(w, kubeStatus(err), err)
		return
	}
	resp := map[string]any{"rows": rows, "columns": kind.Columns, "can": k.PermsFor(ctx, kind, ns)}
	switch kind.ID {
	case "pods":
		resp["usage"] = k.PodUsage(ctx, ns)
		// Why a pod is stuck: the latest warning about it.
		ek, _ := kube.KindByID("events")
		if evs, err := k.ListCached(ctx, ek, ns); err == nil {
			why := map[string]string{}
			for _, e := range evs { // newest first
				if len(e.Cells) < 4 || e.Cells[0] != "Warning" || !strings.HasPrefix(e.Cells[2], "Pod/") {
					continue
				}
				key := e.Namespace + "/" + strings.TrimPrefix(e.Cells[2], "Pod/")
				if _, ok := why[key]; !ok {
					why[key] = e.Cells[1] + ": " + e.Cells[3]
				}
			}
			for i := range rows {
				if w, ok := why[rows[i].Namespace+"/"+rows[i].Name]; ok && rows[i].Problem != "" && !strings.Contains(rows[i].Problem, ":") {
					rows[i].Problem += " · " + w
				}
			}
		}
	case "nodes":
		resp["usage"] = k.NodeUsage(ctx)
	}
	writeJSON(w, resp)
}

func objNS(r *http.Request) string {
	if ns := r.PathValue("ns"); ns != "-" {
		return ns
	}
	return ""
}

func (s *Server) clusterYAML(w http.ResponseWriter, r *http.Request, c clusterRec, k *kube.Client) {
	kind, ok := k.KindFor(r.Context(), r.PathValue("kind"))
	if !ok {
		writeErr(w, http.StatusNotFound, errors.New("tipo desconhecido"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if r.Method == http.MethodPut {
		if !guard(w, r, c) {
			return
		}
		var req struct {
			YAML string `json:"yaml"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if err := k.Replace(ctx, kind, objNS(r), r.PathValue("name"), req.YAML); err != nil {
			writeErr(w, kubeStatus(err), err)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
		return
	}
	y, err := k.YAML(ctx, kind, objNS(r), r.PathValue("name"), kind.ID == "secrets" && c.Mode == "leitura")
	if err != nil {
		writeErr(w, kubeStatus(err), err)
		return
	}
	writeJSON(w, map[string]string{"yaml": y})
}

// clusterSecret returns a Secret's values (base64), only when asked.
// clusterHelmRelease is the details of a release.
func (s *Server) clusterHelmRelease(w http.ResponseWriter, r *http.Request, c clusterRec, k *kube.Client) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	rev, _ := strconv.Atoi(r.URL.Query().Get("rev"))
	// Values and manifest often hold passwords: never to the assistants.
	d, err := k.HelmRelease(ctx, r.PathValue("ns"), r.PathValue("name"), c.Mode != "leitura" && !fromMCP(r), rev)
	if err != nil {
		writeErr(w, kubeStatus(err), err)
		return
	}
	writeJSON(w, map[string]any{"release": d, "helm": helmPath() != ""})
}

// clusterHelmDrift compares the objects of the release with the manifest
// Helm applied (the content of Secrets only outside read-only mode).
func (s *Server) clusterHelmDrift(w http.ResponseWriter, r *http.Request, c clusterRec, k *kube.Client) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	objs, err := k.HelmDrift(ctx, r.PathValue("ns"), r.PathValue("name"), c.Mode != "leitura")
	if err != nil {
		writeErr(w, kubeStatus(err), err)
		return
	}
	writeJSON(w, map[string]any{"objects": objs})
}

// kubeNameRe is a DNS name (releases, namespaces): never an option of a command.
var kubeNameRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)

func helmPath() string {
	p, _ := exec.LookPath("helm")
	return p
}

// clusterHelmRollback runs helm rollback (the helm of this computer) with a
// one-context copy of the cluster's kubeconfig.
func (s *Server) clusterHelmRollback(w http.ResponseWriter, r *http.Request, c clusterRec, k *kube.Client) {
	var req struct {
		Revision int `json:"revision"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil || req.Revision <= 0 {
		writeErr(w, http.StatusBadRequest, errors.New("informe a revisão"))
		return
	}
	if !kubeNameRe.MatchString(r.PathValue("name")) || !kubeNameRe.MatchString(r.PathValue("ns")) {
		writeErr(w, http.StatusBadRequest, errors.New("nome de release ou namespace inválido"))
		return
	}
	if !guard(w, r, c) {
		return
	}
	helm := helmPath()
	if helm == "" {
		writeErr(w, http.StatusPreconditionFailed, errors.New("o helm não está instalado nesta máquina (no Mac: brew install helm)"))
		return
	}
	cfg, err := kube.Load(kube.Expand(c.File))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if c.Source == "gantry" {
		cfg.SetServer(k.Server, "127.0.0.1") // the address the app reaches now
	}
	one, err := cfg.Extract(c.Context)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	tmp, err := os.CreateTemp("", "gantry-kubeconfig-*.yaml")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer os.Remove(tmp.Name())
	_ = tmp.Chmod(0o600)
	_, err = tmp.Write(one)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, helm, "rollback", "--namespace", r.PathValue("ns"), "--kubeconfig", tmp.Name(), "--", r.PathValue("name"), strconv.Itoa(req.Revision))
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		writeErr(w, http.StatusBadGateway, errors.New("helm rollback: "+msg))
		return
	}
	writeJSON(w, map[string]any{"ok": true, "output": strings.TrimSpace(string(out))})
}

// clusterCRDs lists the custom resources of the cluster (for the menu).
func (s *Server) clusterCRDs(w http.ResponseWriter, r *http.Request, c clusterRec, k *kube.Client) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	list, err := k.CRDs(ctx)
	if err != nil {
		// No permission to list CRDs is common: the menu just has none.
		writeJSON(w, map[string]any{"kinds": []kube.Kind{}, "error": err.Error()})
		return
	}
	if list == nil {
		list = []kube.Kind{}
	}
	writeJSON(w, map[string]any{"kinds": list})
}

// clusterDetail is the details panel of an object (Secret values never come).
func (s *Server) clusterDetail(w http.ResponseWriter, r *http.Request, c clusterRec, k *kube.Client) {
	kind, ok := k.KindFor(r.Context(), r.PathValue("kind"))
	if !ok {
		writeErr(w, http.StatusNotFound, errors.New("tipo desconhecido"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	d, err := k.Detail(ctx, kind, objNS(r), r.PathValue("name"))
	if err != nil {
		writeErr(w, kubeStatus(err), err)
		return
	}
	var hist []kubePoint
	switch kind.ID {
	case "pods", "nodes", "deployments", "statefulsets", "daemonsets", "replicasets", "jobs", "services":
		hist = kubeHistory(c.ID, kind.ID, objNS(r), r.PathValue("name"), d.Pods)
	}
	writeJSON(w, struct {
		*kube.Detail
		History []kubePoint `json:"history"`
		Step    int         `json:"step"`
	}{d, hist, int(kubeSampleEvery / time.Second)})
}

func (s *Server) clusterSecret(w http.ResponseWriter, r *http.Request, c clusterRec, k *kube.Client) {
	if c.Mode == "leitura" {
		writeErr(w, http.StatusForbidden, errors.New("este cluster está em modo só leitura: os valores dos Secrets não são mostrados"))
		return
	}
	kind, _ := kube.KindByID("secrets")
	var o struct {
		Data map[string]string `json:"data"`
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := k.Get(ctx, kind.Path(objNS(r), r.PathValue("name")), nil, &o); err != nil {
		writeErr(w, kubeStatus(err), err)
		return
	}
	if o.Data == nil {
		o.Data = map[string]string{}
	}
	writeJSON(w, o.Data)
}

func (s *Server) clusterDelete(w http.ResponseWriter, r *http.Request, c clusterRec, k *kube.Client) {
	kind, ok := k.KindFor(r.Context(), r.PathValue("kind"))
	if !ok {
		writeErr(w, http.StatusNotFound, errors.New("tipo desconhecido"))
		return
	}
	if !guard(w, r, c) {
		return
	}
	if kind.ID == "pods" {
		pctx, pcancel := context.WithTimeout(r.Context(), 40*time.Second)
		if p, _, err := k.PodLeaving(pctx, objNS(r), r.PathValue("name")); err == nil {
			s.keepPodLogs(pctx, c.ID, k, p)
		}
		pcancel()
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := k.Delete(ctx, kind, objNS(r), r.PathValue("name")); err != nil {
		writeErr(w, kubeStatus(err), err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// clusterAction: scale, restart, trigger (a CronJob now), suspend, cordon.
func (s *Server) clusterAction(w http.ResponseWriter, r *http.Request, c clusterRec, k *kube.Client) {
	kind, ok := k.KindFor(r.Context(), r.PathValue("kind"))
	if !ok {
		writeErr(w, http.StatusNotFound, errors.New("tipo desconhecido"))
		return
	}
	var req struct {
		Replicas *int `json:"replicas"`
		On       bool `json:"on"`
		Revision int  `json:"revision"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req)
	if !guard(w, r, c) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	ns, name := objNS(r), r.PathValue("name")
	var err error
	res := map[string]any{"ok": true}
	switch act := r.PathValue("acao"); {
	case act == "escalar" && req.Replicas != nil && *req.Replicas >= 0 && *req.Replicas <= 1000:
		err = k.Scale(ctx, kind, ns, name, *req.Replicas)
	case act == "reiniciar" && (kind.ID == "deployments" || kind.ID == "statefulsets" || kind.ID == "daemonsets"):
		// The old pods go with their logs: saved first.
		s.keepWorkloadLogs(c.ID, k, ns, map[string]string{"deployments": "Deployment", "statefulsets": "StatefulSet", "daemonsets": "DaemonSet"}[kind.ID]+"/"+name)
		err = k.Restart(ctx, kind, ns, name)
	case act == "executar" && kind.ID == "cronjobs":
		var job string
		job, err = k.TriggerCronJob(ctx, ns, name)
		res["job"] = job
	case act == "suspender" && kind.ID == "cronjobs":
		err = k.Patch(ctx, kind, ns, name, map[string]any{"spec": map[string]any{"suspend": req.On}})
	case act == "voltar" && kind.ID == "deployments" && req.Revision > 0:
		err = k.Undo(ctx, ns, name, req.Revision)
	case act == "esvaziar" && kind.ID == "nodes":
		// A node with many pods takes longer than an ordinary action.
		dctx, dcancel := context.WithTimeout(r.Context(), 5*time.Minute)
		var dr *kube.DrainResult
		dr, err = k.Drain(dctx, name)
		dcancel()
		res["drain"] = dr
	case act == "isolar" && kind.ID == "nodes":
		err = k.Patch(ctx, kind, "", name, map[string]any{"spec": map[string]any{"unschedulable": req.On}})
	default:
		writeErr(w, http.StatusBadRequest, errors.New("ação inválida para este tipo"))
		return
	}
	if err != nil {
		writeErr(w, kubeStatus(err), err)
		return
	}
	writeJSON(w, res)
}

func (s *Server) clusterApply(w http.ResponseWriter, r *http.Request, c clusterRec, k *kube.Client) {
	var req struct {
		YAML string `json:"yaml"`
		NS   string `json:"ns"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if !guard(w, r, c) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	res, err := k.Apply(ctx, req.YAML, req.NS)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, res)
}

func (s *Server) clusterHelm(w http.ResponseWriter, r *http.Request, c clusterRec, k *kube.Client) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	rel, err := k.HelmReleases(ctx, r.URL.Query().Get("ns"))
	if err != nil {
		writeErr(w, kubeStatus(err), err)
		return
	}
	writeJSON(w, rel)
}

func podLogOptions(r *http.Request, follow bool) kube.LogOptions {
	q := r.URL.Query()
	o := kube.LogOptions{Container: q.Get("container"), Follow: follow, Tail: 300, Timestamps: q.Get("timestamps") == "1", Previous: q.Get("previous") == "1"}
	switch t := q.Get("tail"); t {
	case "":
	case "all":
		o.Tail = -1
	default:
		if n, err := strconv.Atoi(t); err == nil {
			o.Tail = n
		}
	}
	if n, err := strconv.Atoi(q.Get("since")); err == nil && n > 0 {
		o.Since = n
		if q.Get("tail") == "" {
			o.Tail = -1
		}
	}
	return o
}

func (s *Server) clusterLogs(w http.ResponseWriter, r *http.Request, c clusterRec, k *kube.Client) {
	o := podLogOptions(r, r.URL.Query().Get("follow") != "0")
	if o.Follow && !o.Previous {
		s.followPodLogs(w, r, k, o)
		return
	}
	rc, err := k.Logs(r.Context(), r.PathValue("ns"), r.PathValue("pod"), o)
	send, ok := startSSE(w)
	if !ok {
		if rc != nil {
			rc.Close()
		}
		return
	}
	if err != nil {
		// An EventSource drops the body of an error answer: the reason
		// (container still starting, wrong container…) goes as the end.
		sendEnd(send, podLogHint(err))
		return
	}
	defer rc.Close()
	out := newLineFilter(b64Writer{send}, r.URL.Query().Get("grep"))
	_, err = io.Copy(out, rc)
	if r.Context().Err() == nil {
		msg := "fim do log"
		if err != nil {
			msg = err.Error()
		}
		sendEnd(send, msg)
	}
}

func sendEnd(send func(string, string), msg string) {
	b, _ := json.Marshal(map[string]string{"message": msg})
	send("end", string(b))
}

func (s *Server) clusterSaveLogs(w http.ResponseWriter, r *http.Request, c clusterRec, k *kube.Client) {
	var req struct {
		Dest string `json:"dest"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&req)
	if !filepath.IsAbs(req.Dest) {
		writeErr(w, http.StatusBadRequest, errors.New("escolha onde salvar o log"))
		return
	}
	o := podLogOptions(r, false)
	if r.URL.Query().Get("tail") == "" {
		o.Tail = -1
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	rc, err := k.Logs(ctx, r.PathValue("ns"), r.PathValue("pod"), o)
	if err != nil {
		writeErr(w, kubeStatus(err), err)
		return
	}
	defer rc.Close()
	tmp := req.Dest + ".parcial"
	f, err := os.Create(tmp)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	cw := &countWriter{w: f}
	fw := newLineFilter(cw, r.URL.Query().Get("grep"))
	_, err = io.Copy(fw, rc)
	if lf, ok := fw.(*lineFilter); ok && err == nil && len(lf.pending) > 0 {
		lf.pending = append(lf.pending, '\n')
		_, err = lf.Write(nil)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, req.Dest)
	}
	if err != nil {
		os.Remove(tmp)
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]any{"path": req.Dest, "bytes": cw.n})
}

func (s *Server) clusterShell(w http.ResponseWriter, r *http.Request, c clusterRec, k *kube.Client) {
	if c.Mode == "leitura" {
		writeErr(w, http.StatusForbidden, errors.New("este cluster está em modo só leitura: o shell nos pods fica desligado"))
		return
	}
	if !guard(w, r, c) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	ex, err := k.Exec(ctx, r.PathValue("ns"), r.PathValue("pod"), r.URL.Query().Get("container"), shellCmd)
	if err != nil {
		writeErr(w, kubeStatus(err), err)
		return
	}
	writeJSON(w, map[string]string{"id": s.addTerm(ex)})
}

/* opening apps of the cluster: a local port proxies to the API server's
   proxy of the service (or pod), with the cluster's credentials. */

type kubeTunnel struct {
	url      string // http://127.0.0.1:port
	token    string // the session cookie's value
	cluster  string
	k        *kube.Client
	srv      *http.Server
	readOnly func() bool // the cluster is in leitura mode now

	mu    sync.Mutex
	codes map[string]time.Time // one-use codes of the links opened
}

const tunnelCodeTTL = 2 * time.Minute

// link is an address that opens the tunnel in a browser once.
func (t *kubeTunnel) link(p string) string {
	c := randomID(18)
	t.mu.Lock()
	if t.codes == nil {
		t.codes = map[string]time.Time{}
	}
	for k, at := range t.codes {
		if time.Since(at) > tunnelCodeTTL {
			delete(t.codes, k)
		}
	}
	t.codes[c] = time.Now()
	t.mu.Unlock()
	sep := "?"
	if strings.Contains(p, "?") {
		sep = "&"
	}
	return t.url + p + sep + "__gantry=" + c
}

func (t *kubeTunnel) useCode(c string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	at, ok := t.codes[c]
	delete(t.codes, c)
	return ok && time.Since(at) <= tunnelCodeTTL
}

var (
	tunnelMu sync.Mutex
	tunnels  = map[string]*kubeTunnel{}
)

// closeTunnels stops the tunnels of a cluster (removed, or its client
// changed: credentials or address).
func closeTunnels(clusterID string, keep *kube.Client) {
	closeForwards(clusterID, keep)
	tunnelMu.Lock()
	defer tunnelMu.Unlock()
	for key, t := range tunnels {
		if t.cluster == clusterID && t.k != keep {
			_ = t.srv.Close()
			delete(tunnels, key)
		}
	}
}

// handler accepts only this computer's browser: the Host must be the
// tunnel's own address (no DNS rebinding) and the request must carry the
// cookie set on the first visit (the link has a one-use code), which is
// SameSite=Strict and named after this tunnel's port. With the cluster in
// leitura mode, only reads go through.
func (t *kubeTunnel) handler(rp http.Handler) http.Handler {
	host := strings.TrimPrefix(t.url, "http://")
	port := host[strings.LastIndex(host, ":"):]
	cookie := "gantry_tunel_" + strings.TrimPrefix(port, ":")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != host && r.Host != "localhost"+port {
			http.Error(w, "Gantry: endereço não permitido", http.StatusForbidden)
			return
		}
		if code := r.URL.Query().Get("__gantry"); code != "" {
			if !t.useCode(code) {
				http.Error(w, "Gantry: link já usado ou expirado; abra de novo pelo botão Abrir do Gantry", http.StatusForbidden)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: cookie, Value: t.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			q := r.URL.Query()
			q.Del("__gantry")
			u := *r.URL
			u.RawQuery = q.Encode()
			http.Redirect(w, r, u.RequestURI(), http.StatusFound)
			return
		}
		ck, err := r.Cookie(cookie)
		if err != nil || subtle.ConstantTimeCompare([]byte(ck.Value), []byte(t.token)) != 1 {
			http.Error(w, "Gantry: abra este endereço pelo botão Abrir do Gantry", http.StatusForbidden)
			return
		}
		if t.readOnly != nil && t.readOnly() && r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			http.Error(w, "Gantry: este cluster está em modo só leitura; só leituras passam pelo túnel", http.StatusForbidden)
			return
		}
		rp.ServeHTTP(w, r)
	})
}

func (s *Server) clusterOpen(w http.ResponseWriter, r *http.Request, c clusterRec, k *kube.Client) {
	var req struct {
		NS   string `json:"ns"`
		Kind string `json:"kind"` // services, pods
		Name string `json:"name"`
		Port int    `json:"port"`
		Path string `json:"path"`
		Only bool   `json:"only"` // return the address without opening the browser
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil || req.Name == "" || req.NS == "" || req.Port <= 0 || req.Port > 65535 {
		writeErr(w, http.StatusBadRequest, errors.New("informe o serviço e a porta"))
		return
	}
	if req.Kind != "pods" {
		req.Kind = "services"
	}
	up, err := url.Parse(k.Server)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	target := strings.TrimRight(up.EscapedPath(), "/") + fmt.Sprintf("/api/v1/namespaces/%s/%s/%s:%d/proxy", url.PathEscape(req.NS), req.Kind, url.PathEscape(req.Name), req.Port)
	key := c.ID + target
	closeTunnels(c.ID, k) // the ones of an older client (credentials or address changed)
	tunnelMu.Lock()
	t := tunnels[key]
	if t == nil {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			tunnelMu.Unlock()
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		rp := &httputil.ReverseProxy{
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.Out.URL.Scheme, pr.Out.URL.Host = up.Scheme, up.Host
				// The path stays escaped as it came (no %2F turning into /).
				pr.Out.URL.RawPath = target + pr.In.URL.EscapedPath()
				pr.Out.URL.Path, _ = url.PathUnescape(pr.Out.URL.RawPath)
				pr.Out.Host = up.Host
				pr.Out.Header.Del("Authorization")
				pr.Out.Header.Del("Cookie")
				if err := k.AuthorizeRequest(pr.Out); err != nil {
					log.Printf("túnel do cluster %s: %v", c.Name, err)
				}
			},
			Transport: k.Transport(),
			ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
				http.Error(w, "Gantry: o cluster não respondeu: "+err.Error(), http.StatusBadGateway)
			},
		}
		id := c.ID
		t = &kubeTunnel{url: "http://" + ln.Addr().String(), token: randomID(24), cluster: c.ID, k: k,
			readOnly: func() bool { cc, ok := s.clusterByID(id); return !ok || cc.Mode == "leitura" }}
		t.srv = &http.Server{Handler: t.handler(rp), ReadHeaderTimeout: 20 * time.Second}
		go func() { defer applog.Recover("server/kube.go:1092"); _ = t.srv.Serve(ln) }()
		tunnels[key] = t
	}
	tunnelMu.Unlock()
	u := t.link("/" + strings.TrimPrefix(req.Path, "/"))
	if !req.Only {
		if err := openURL(u); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, map[string]string{"url": u})
}

// clusterDisconnect: POST /api/clusters/{id}/desconectar — closes what is
// open with the cluster (the watches of the lists, the history of CPU and
// memory, the port-forwards). It connects again when opened.
func (s *Server) clusterDisconnect(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.clusterByID(id); !ok {
		writeErr(w, http.StatusNotFound, errors.New("cluster não encontrado"))
		return
	}
	closeForwards(id, nil)
	dropSampler(id)
	forgetKubeClient(id)
	writeJSON(w, map[string]bool{"ok": true})
}
