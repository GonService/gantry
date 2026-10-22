package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/goccy/go-yaml"

	"gantry/internal/applog"
	"gantry/internal/engine"
	"gantry/internal/hosts"
)

// Image updates: the app asks the registries (from this computer) which
// tags exist for the images of a project and points out newer versions of
// the same kind (same prefix and suffix: 16-alpine → 17-alpine, v2.53.0 →
// v2.55.1). Pinned versions only change when the user says so.

type imageRef struct {
	Registry string // host, e.g. registry-1.docker.io
	Repo     string // e.g. library/nginx
	Tag      string
}

func parseImageRef(ref string) (imageRef, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.Contains(ref, "@") || strings.Contains(ref, "${") {
		return imageRef{}, false
	}
	name, tag := ref, "latest"
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		name, tag = ref[:i], ref[i+1:]
	}
	reg := "registry-1.docker.io"
	parts := strings.SplitN(name, "/", 2)
	if len(parts) == 2 && (strings.ContainsAny(parts[0], ".:") || parts[0] == "localhost") {
		reg, name = parts[0], parts[1]
		if reg == "docker.io" || reg == "index.docker.io" {
			reg = "registry-1.docker.io"
		}
	}
	if reg == "registry-1.docker.io" && !strings.Contains(name, "/") {
		name = "library/" + name
	}
	if reg == "localhost" {
		return imageRef{}, false // a local image (podman names them so): nothing to compare with
	}
	return imageRef{Registry: reg, Repo: name, Tag: tag}, true
}

// tagShape splits a tag into prefix, numbers and suffix: "v2.53.0" →
// ("v", [2 53 0], ""), "16-alpine" → ("", [16], "-alpine"),
// "RELEASE.2025-04-22T22-12-26Z" → ("RELEASE.", [2025 4 22 22 12 26], "Z").
var tagRe = regexp.MustCompile(`^([A-Za-z]*[._-]?)((?:\d+)(?:[.\-T]\d+)*)(.*)$`)

type tagVer struct {
	Prefix, Suffix string
	Nums           []int
	Sep            string // separators between the numbers, so the shape matches
}

func parseTag(t string) (tagVer, bool) {
	m := tagRe.FindStringSubmatch(t)
	if m == nil {
		return tagVer{}, false
	}
	var v tagVer
	v.Prefix, v.Suffix = m[1], m[3]
	num := ""
	for _, r := range m[2] {
		if r >= '0' && r <= '9' {
			num += string(r)
			continue
		}
		n, _ := strconv.Atoi(num)
		v.Nums = append(v.Nums, n)
		v.Sep += string(r)
		num = ""
	}
	n, _ := strconv.Atoi(num)
	v.Nums = append(v.Nums, n)
	// Pre-releases are not offered.
	low := strings.ToLower(v.Suffix)
	for _, w := range []string{"rc", "beta", "alpha", "dev", "nightly", "snapshot", "preview"} {
		if strings.Contains(low, w) {
			return tagVer{}, false
		}
	}
	return v, true
}

func (a tagVer) sameShape(b tagVer) bool {
	return a.Prefix == b.Prefix && a.Suffix == b.Suffix && a.Sep == b.Sep && len(a.Nums) == len(b.Nums)
}

func (a tagVer) less(b tagVer) bool {
	for i := range a.Nums {
		if a.Nums[i] != b.Nums[i] {
			return a.Nums[i] < b.Nums[i]
		}
	}
	return false
}

type imageUpdate struct {
	Service string `json:"service"`
	Image   string `json:"image"`
	Current string `json:"current"`
	Pinned  bool   `json:"pinned"`          // the tag is a version (not latest, main…)
	Minor   string `json:"minor,omitempty"` // newest of the same major
	Major   string `json:"major,omitempty"` // newest overall, when another major
	Var     string `json:"var,omitempty"`   // the .env variable that sets the tag, if any
	Error   string `json:"error,omitempty"`
	// Floating tags (latest, main…): the registry has another image under
	// the same tag than the one on the machine.
	Newer   bool `json:"newer,omitempty"`
	Checked bool `json:"checked,omitempty"` // the comparison was possible
}

// newerTags picks, among tags, the newest of the same shape as cur: in the
// same major and overall.
func newerTags(cur string, tags []string) (minor, major string) {
	cv, ok := parseTag(cur)
	if !ok {
		return "", ""
	}
	var bestMinor, bestMajor *tagVer
	var nameMinor, nameMajor string
	for _, t := range tags {
		v, ok := parseTag(t)
		if !ok || !cv.sameShape(v) || !cv.less(v) {
			continue
		}
		if v.Nums[0] == cv.Nums[0] && len(cv.Nums) > 1 {
			if bestMinor == nil || bestMinor.less(v) {
				bestMinor, nameMinor = &v, t
			}
		} else if v.Nums[0] != cv.Nums[0] {
			if bestMajor == nil || bestMajor.less(v) {
				bestMajor, nameMajor = &v, t
			}
		}
	}
	return nameMinor, nameMajor
}

var (
	tagCacheMu sync.Mutex
	tagCache   = map[string]struct {
		at   time.Time
		tags []string
		err  error
	}{}
	registryScheme = "https" // tests use http
	registryClient = &http.Client{Timeout: 20 * time.Second}
)

// registryTags lists the tags of an image (cached for an hour).
func registryTags(ctx context.Context, ref imageRef) ([]string, error) {
	key := ref.Registry + "/" + ref.Repo
	tagCacheMu.Lock()
	if c, ok := tagCache[key]; ok && time.Since(c.at) < time.Hour {
		tagCacheMu.Unlock()
		return c.tags, c.err
	}
	tagCacheMu.Unlock()
	tags, err := fetchTags(ctx, ref)
	if err != nil {
		return tags, err // not cached: a timeout or a closed page is not an answer
	}
	tagCacheMu.Lock()
	tagCache[key] = struct {
		at   time.Time
		tags []string
		err  error
	}{time.Now(), tags, err}
	tagCacheMu.Unlock()
	return tags, err
}

var bearerRe = regexp.MustCompile(`(\w+)="([^"]*)"`)

func fetchTags(ctx context.Context, ref imageRef) ([]string, error) {
	next := schemeFor(ref.Registry) + "://" + ref.Registry + "/v2/" + ref.Repo + "/tags/list?n=1000"
	authz := ""
	var all []string
	for page := 0; next != "" && page < 10; page++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, next, nil)
		if authz != "" {
			req.Header.Set("Authorization", authz)
		}
		resp, err := registryClient.Do(req)
		if err != nil {
			return all, err
		}
		if resp.StatusCode == http.StatusUnauthorized && authz == "" {
			ch := resp.Header.Get("WWW-Authenticate")
			resp.Body.Close()
			if authz, err = registryAuthHeader(ctx, ch, ref.Registry); err != nil {
				return all, err
			}
			page--
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return all, fmt.Errorf("%s respondeu %d", ref.Registry, resp.StatusCode)
		}
		var body struct {
			Tags []string `json:"tags"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&body)
		link := resp.Header.Get("Link")
		resp.Body.Close()
		if err != nil {
			return all, err
		}
		all = append(all, body.Tags...)
		next = ""
		if i, j := strings.Index(link, "<"), strings.Index(link, ">"); i >= 0 && j > i {
			u, err := url.Parse(link[i+1 : j])
			if err == nil {
				next = schemeFor(ref.Registry) + "://" + ref.Registry + u.RequestURI()
			}
		}
	}
	return all, nil
}

// schemeFor: registries on this computer speak plain HTTP, as the engines
// allow for them.
func schemeFor(registry string) string {
	host := registry
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	if host == "localhost" || strings.HasPrefix(host, "127.") {
		return "http"
	}
	return registryScheme
}

// registryLoginFor is the saved login of a registry host, if any.
func registryLoginFor(registry string) (string, string, bool) {
	if engine.RegistryAuth == nil {
		return "", "", false
	}
	if registry == "registry-1.docker.io" {
		registry = "docker.io"
	}
	return engine.RegistryAuth(registry)
}

// registryAuthHeader answers a 401 of a registry: a bearer token
// (anonymous, or with the saved login) or basic auth with the saved login.
func registryAuthHeader(ctx context.Context, challenge, registry string) (string, error) {
	user, pass, ok := registryLoginFor(registry)
	if strings.HasPrefix(strings.ToLower(challenge), "basic") {
		if !ok {
			return "", errors.New("o registro pede login (cadastre em Imagens → Mais → Registros)")
		}
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass)), nil
	}
	if !ok {
		user, pass = "", ""
	}
	tok, err := registryToken(ctx, challenge, user, pass)
	if err != nil {
		return "", err
	}
	return "Bearer " + tok, nil
}

// registryToken gets a pull token from the realm the registry pointed to,
// anonymous unless a login is given.
func registryToken(ctx context.Context, challenge, user, pass string) (string, error) {
	if !strings.HasPrefix(strings.ToLower(challenge), "bearer ") {
		return "", errors.New("o registro pede login")
	}
	params := map[string]string{}
	for _, m := range bearerRe.FindAllStringSubmatch(challenge, -1) {
		params[m[1]] = m[2]
	}
	if params["realm"] == "" {
		return "", errors.New("o registro não disse onde pegar o token")
	}
	q := url.Values{}
	for _, k := range []string{"service", "scope"} {
		if params[k] != "" {
			q.Set(k, params[k])
		}
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, params["realm"]+"?"+q.Encode(), nil)
	if user != "" {
		if req.URL.Scheme != "https" && schemeFor(req.URL.Host) != "http" {
			return "", errors.New("o registro pediu o login por uma conexão sem criptografia; recusei")
		}
		req.SetBasicAuth(user, pass)
	}
	resp, err := registryClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token do registro: %d", resp.StatusCode)
	}
	var t struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&t); err != nil {
		return "", err
	}
	if t.Token == "" {
		t.Token = t.AccessToken
	}
	return t.Token, nil
}

var (
	digestCacheMu sync.Mutex
	digestCache   = map[string]struct {
		at     time.Time
		digest string
	}{}
)

// manifestDigest is the digest the registry gives for a tag (the index of
// a multi-platform image, as the engines record when they pull).
func manifestDigest(ctx context.Context, ref imageRef) (string, error) {
	key := ref.Registry + "/" + ref.Repo + ":" + ref.Tag
	digestCacheMu.Lock()
	if c, ok := digestCache[key]; ok && time.Since(c.at) < 30*time.Minute {
		digestCacheMu.Unlock()
		return c.digest, nil
	}
	digestCacheMu.Unlock()
	u := schemeFor(ref.Registry) + "://" + ref.Registry + "/v2/" + ref.Repo + "/manifests/" + ref.Tag
	authz := ""
	for try := 0; try < 2; try++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
		req.Header.Set("Accept", strings.Join([]string{
			"application/vnd.oci.image.index.v1+json",
			"application/vnd.docker.distribution.manifest.list.v2+json",
			"application/vnd.docker.distribution.manifest.v2+json",
			"application/vnd.oci.image.manifest.v1+json",
		}, ", "))
		if authz != "" {
			req.Header.Set("Authorization", authz)
		}
		resp, err := registryClient.Do(req)
		if err != nil {
			return "", err
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized && authz == "" {
			if authz, err = registryAuthHeader(ctx, resp.Header.Get("WWW-Authenticate"), ref.Registry); err != nil {
				return "", err
			}
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("%s respondeu %d", ref.Registry, resp.StatusCode)
		}
		d := resp.Header.Get("Docker-Content-Digest")
		if d == "" {
			return "", errors.New("o registro não informou o digest")
		}
		digestCacheMu.Lock()
		digestCache[key] = struct {
			at     time.Time
			digest string
		}{time.Now(), d}
		digestCacheMu.Unlock()
		return d, nil
	}
	return "", errors.New("o registro recusou o acesso")
}

// floatingNewer says whether the registry has another image under the tag
// than the one on the machine (checked=false when it cannot tell).
func floatingNewer(ctx context.Context, eng *engine.Client, image string, ref imageRef) (newer, checked bool, err error) {
	local, err := eng.ImageDigests(ctx, image)
	if err != nil || len(local) == 0 {
		return false, false, nil // not pulled yet, or built here
	}
	remote, err := manifestDigest(ctx, ref)
	if err != nil {
		return false, false, err
	}
	for _, d := range local {
		if strings.HasSuffix(d, "@"+remote) {
			return false, true, nil
		}
	}
	return true, true, nil
}

var floating = map[string]bool{"latest": true, "main": true, "master": true, "stable": true, "edge": true, "nightly": true, "alpine": true, "slim": true}

// labUpdates checks the images of a project.
func (s *Server) labUpdates(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	l, err := s.findLab(ctx, h, r.PathValue("lab"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if len(l.Files) == 0 {
		writeJSON(w, []imageUpdate{})
		return
	}
	ctx = s.withSecrets(ctx, h, l.ID)
	p, _, err := s.loadProject(ctx, h, l.Files)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	written := s.rawImages(ctx, h, l.Files)
	force := r.URL.Query().Get("force") == "1"
	out := []imageUpdate{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, name := range p.Names {
		sv := p.Services[name]
		if sv == nil || sv.Image == "" {
			continue
		}
		ref, ok := parseImageRef(sv.Image)
		if !ok {
			continue
		}
		u := imageUpdate{Service: name, Image: sv.Image, Current: ref.Tag, Var: tagVar(written[name])}
		_, isVer := parseTag(ref.Tag)
		u.Pinned = isVer && !floating[ref.Tag]
		wg.Add(1)
		if force {
			tagCacheMu.Lock()
			delete(tagCache, ref.Registry+"/"+ref.Repo)
			tagCacheMu.Unlock()
			digestCacheMu.Lock()
			delete(digestCache, ref.Registry+"/"+ref.Repo+":"+ref.Tag)
			digestCacheMu.Unlock()
		}
		go func(u imageUpdate, ref imageRef) {
			defer applog.Recover("server/updates.go:459")
			defer wg.Done()
			if u.Pinned {
				tags, err := registryTags(ctx, ref)
				if err != nil {
					u.Error = err.Error()
				} else {
					u.Minor, u.Major = newerTags(ref.Tag, tags)
				}
			} else if h.Engine != nil {
				newer, checked, err := floatingNewer(ctx, h.Engine, u.Image, ref)
				u.Newer, u.Checked = newer, checked
				if err != nil {
					u.Error = err.Error()
				}
			}
			mu.Lock()
			out = append(out, u)
			mu.Unlock()
		}(u, ref)
	}
	wg.Wait()
	sort.Slice(out, func(i, j int) bool { return out[i].Service < out[j].Service })
	writeJSON(w, out)
}

// rawImages are the image: fields of each service as written (before the
// variables are filled in), from the project's files in order.
func (s *Server) rawImages(ctx context.Context, h *hosts.Host, files []string) map[string]string {
	out := map[string]string{}
	for _, f := range files {
		b, err := h.ReadFile(ctx, f)
		if err != nil {
			continue
		}
		var doc struct {
			Services map[string]struct {
				Image string `yaml:"image"`
			} `yaml:"services"`
		}
		if yaml.Unmarshal(b, &doc) != nil {
			continue
		}
		for n, sv := range doc.Services {
			if sv.Image != "" {
				out[n] = sv.Image
			}
		}
	}
	return out
}

var tagVarRe = regexp.MustCompile(`:\$\{([A-Za-z_][A-Za-z0-9_]*)(?::?-[^}]*)?\}$`)

// tagVar is the variable that sets the tag of a written image
// (nginx:${VERSAO_NGINX:-1.27}), if any.
func tagVar(written string) string {
	if m := tagVarRe.FindStringSubmatch(written); m != nil {
		return m[1]
	}
	return ""
}

// setImageTag changes the tag of a service's image: in the .env when a
// variable sets it, otherwise in the compose file.
func (s *Server) setImageTag(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		Service string `json:"service"`
		Tag     string `json:"tag"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`).MatchString(req.Tag) {
		writeErr(w, http.StatusBadRequest, errors.New("tag inválida"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	l, err := s.findLab(ctx, h, r.PathValue("lab"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	ctx = s.withSecrets(ctx, h, l.ID)
	p, _, err := s.loadProject(ctx, h, l.Files)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	sv := p.Services[req.Service]
	if sv == nil {
		writeErr(w, http.StatusNotFound, errors.New("serviço não encontrado"))
		return
	}
	ref, ok := parseImageRef(sv.Image)
	if !ok {
		writeErr(w, http.StatusBadRequest, errors.New("imagem sem versão para trocar"))
		return
	}
	written := s.rawImages(ctx, h, l.Files)[req.Service]
	if v := tagVar(written); v != "" && l.WorkingDir != "" {
		envPath := path.Join(l.WorkingDir, ".env")
		b, err := readEnvFile(ctx, h, envPath)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err)
			return
		}
		lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
		if len(lines) == 1 && lines[0] == "" {
			lines = nil
		}
		set := false
		for i, ln := range lines {
			if m := envLine.FindStringSubmatch(ln); m != nil && m[1] == v {
				lines[i], set = v+"="+req.Tag, true
			}
		}
		if !set {
			lines = append(lines, v+"="+req.Tag)
		}
		if err := h.WriteFile(ctx, envPath, []byte(strings.Join(lines, "\n")+"\n")); err != nil {
			writeErr(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "file": ".env", "var": v})
		return
	}
	// A literal tag: the service's image: line in one of the files.
	if written == "" || !strings.HasSuffix(written, ":"+ref.Tag) {
		writeErr(w, http.StatusBadRequest, errors.New("a versão desta imagem não está escrita no compose"))
		return
	}
	newImage := strings.TrimSuffix(written, ref.Tag) + req.Tag
	for _, f := range l.Files {
		b, err := h.ReadFile(ctx, f)
		if err != nil {
			continue
		}
		lines := strings.Split(string(b), "\n")
		if i := imageLine(lines, req.Service, written); i >= 0 {
			// Plain text, not a regexp replacement: the image may have $.
			lines[i] = strings.Replace(lines[i], written, newImage, 1)
			if err := h.WriteFile(ctx, f, []byte(strings.Join(lines, "\n"))); err != nil {
				writeErr(w, http.StatusBadGateway, err)
				return
			}
			writeJSON(w, map[string]any{"ok": true, "file": path.Base(f)})
			return
		}
	}
	writeErr(w, http.StatusBadRequest, errors.New("não achei a linha image: deste serviço para trocar"))
}

// imageLine finds the image: line of one service in a compose file (only
// in that service's block), or -1.
func imageLine(lines []string, service, image string) int {
	indentOf := func(l string) int { return len(l) - len(strings.TrimLeft(l, " ")) }
	inServices, svcIndent := false, -1
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		ind := indentOf(l)
		if ind == 0 {
			inServices, svcIndent = strings.HasPrefix(t, "services:"), -1
			continue
		}
		if !inServices {
			continue
		}
		if svcIndent >= 0 && ind <= svcIndent {
			svcIndent = -1
		}
		if svcIndent < 0 {
			k := strings.Trim(strings.TrimSuffix(strings.SplitN(t, ":", 2)[0], ":"), `"'`)
			if k == service && strings.HasSuffix(strings.SplitN(t, "#", 2)[0], ":") || k == service && strings.TrimSpace(strings.SplitN(t, ":", 2)[1]) == "" {
				svcIndent = ind
			}
			continue
		}
		if strings.HasPrefix(t, "image:") && strings.Contains(t, image) {
			v := strings.Trim(strings.TrimSpace(strings.SplitN(strings.TrimPrefix(t, "image:"), "#", 2)[0]), `"'`)
			if v == image {
				return i
			}
		}
	}
	return -1
}
