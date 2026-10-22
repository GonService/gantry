package server

// New versions of Gantry: the app asks the releases page now and then and
// tells the user; it never downloads or installs anything by itself.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The releases are published in the Gantry repository (tags vX.Y.Z) and
// copied to GonService/homebrew-tap (tags gantry-vX.Y.Z, next to the other
// apps there), where versions up to 0.47 were published only. The app asks
// both and takes the newest. GANTRY_UPDATE_URL points elsewhere (a mirror,
// a test server): a list of releases or a single one.
const (
	releasesURL    = "https://api.github.com/repos/GonService/gantry/releases/latest"
	tapReleasesURL = "https://api.github.com/repos/GonService/homebrew-tap/releases?per_page=50"
	tagPrefix      = "gantry-v"
)

// brewCaskrooms are where Homebrew keeps what it installed (Apple Silicon,
// Intel).
var brewCaskrooms = []string{"/opt/homebrew/Caskroom/gantry", "/usr/local/Caskroom/gantry"}

type versionInfo struct {
	Current string `json:"current"`
	Latest  string `json:"latest,omitempty"`
	Newer   bool   `json:"newer"`
	URL     string `json:"url,omitempty"`
	Date    string `json:"date,omitempty"`
	Notes   string `json:"notes,omitempty"`
	Error   string `json:"error,omitempty"`
	Checked int64  `json:"checked,omitempty"`
	// Brew is the command that updates an app installed by Homebrew.
	Brew string `json:"brew,omitempty"`
	// Via is how Atualizar agora updates this app: brew, zip or "" (it
	// cannot: the page of the release).
	Via string `json:"via,omitempty"`
}

type ghRelease struct {
	TagName     string    `json:"tag_name"`
	HTMLURL     string    `json:"html_url"`
	PublishedAt string    `json:"published_at"`
	Body        string    `json:"body"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	Assets      []ghAsset `json:"assets"`
}

var (
	versionMu    sync.Mutex
	versionCache versionInfo
)

// githubToken raises the GitHub API limit of the check (60 calls an hour
// without a login): GANTRY_UPDATE_TOKEN, GITHUB_TOKEN or the gh login of
// this computer.
func githubToken(ctx context.Context) string {
	for _, k := range []string{"GANTRY_UPDATE_TOKEN", "GITHUB_TOKEN", "GH_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	if gh, err := exec.LookPath("gh"); err == nil {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, gh, "auth", "token", "-h", "github.com").Output(); err == nil {
			return strings.TrimSpace(string(out))
		}
	}
	return ""
}

// versionNewer compares dotted versions (v0.24.1 > 0.24.0); a dev build
// is never behind.
func versionNewer(latest, current string) bool {
	parse := func(v string) []int {
		v = strings.TrimPrefix(strings.TrimSpace(v), "v")
		v, _, _ = strings.Cut(v, "-")
		var out []int
		for _, p := range strings.Split(v, ".") {
			n, err := strconv.Atoi(p)
			if err != nil {
				return nil
			}
			out = append(out, n)
		}
		return out
	}
	l, c := parse(latest), parse(current)
	if l == nil || c == nil {
		return false
	}
	for i := 0; i < len(l) || i < len(c); i++ {
		var a, b int
		if i < len(l) {
			a = l[i]
		}
		if i < len(c) {
			b = c[i]
		}
		if a != b {
			return a > b
		}
	}
	return false
}

func checkLatest(ctx context.Context) versionInfo {
	v := versionInfo{Current: Version, Checked: time.Now().Unix()}
	if exe, err := os.Executable(); err == nil {
		exe, _ = filepath.EvalSymlinks(exe)
		v.Via = updateVia(runtime.GOOS, exe, brewCaskrooms)
	}
	for _, d := range brewCaskrooms {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			v.Brew = "brew upgrade --cask gonservice/tap/gantry"
		}
	}
	rel, err := latestRelease(ctx)
	if err != nil && rel == nil && err.Error() == "nenhuma versão publicada encontrada" {
		err = nil
	}
	if err != nil {
		v.Error = err.Error()
		return v
	}
	if rel == nil {
		v.Error = "nenhuma versão publicada encontrada"
		return v
	}
	v.Latest = releaseVersion(rel)
	v.URL = rel.HTMLURL
	v.Date = rel.PublishedAt
	notes := strings.TrimSpace(rel.Body)
	if len(notes) > 1500 {
		notes = notes[:1500] + "…"
	}
	v.Notes = notes
	v.Newer = !rel.Prerelease && versionNewer(v.Latest, Version)
	return v
}

// releaseVersion is the version of a release, from its tag (vX.Y.Z here,
// gantry-vX.Y.Z in the tap).
func releaseVersion(r *ghRelease) string {
	return strings.TrimPrefix(strings.TrimPrefix(r.TagName, tagPrefix), "v")
}

// fetchRelease reads a release page: a list (the newest release of Gantry
// in it; other apps share the tap) or a single release. nil: none there.
func fetchRelease(ctx context.Context, u string) (*ghRelease, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "Gantry/"+Version)
	if strings.HasPrefix(u, "https://api.github.com/") {
		if t := githubToken(ctx); t != "" {
			req.Header.Set("Authorization", "Bearer "+t)
		}
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("a página de versões respondeu %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if t := strings.TrimSpace(string(body)); strings.HasPrefix(t, "[") {
		var list []ghRelease
		if err := json.Unmarshal(body, &list); err != nil {
			return nil, err
		}
		// Newest first, as GitHub lists them; pre-releases are skipped.
		for i := range list {
			r := list[i]
			if !r.Draft && !r.Prerelease && strings.HasPrefix(r.TagName, tagPrefix) {
				return &r, nil
			}
		}
		return nil, nil
	}
	var r ghRelease
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	if r.TagName == "" || r.Draft {
		return nil, errors.New("resposta sem versão")
	}
	return &r, nil
}

// latestVersion answers from the cache: 6 h after a good answer, 30 min
// after a failure; ?force=1 asks again.
func (s *Server) latestVersion(w http.ResponseWriter, r *http.Request) {
	versionMu.Lock()
	c := versionCache
	versionMu.Unlock()
	age := time.Since(time.Unix(c.Checked, 0))
	fresh := c.Checked > 0 && ((c.Error == "" && age < 6*time.Hour) || (c.Error != "" && age < 30*time.Minute))
	if !fresh || r.URL.Query().Get("force") == "1" {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		c = checkLatest(ctx)
		cancel()
		versionMu.Lock()
		versionCache = c
		versionMu.Unlock()
	}
	writeJSON(w, c)
}

// openRelease opens the page of the latest release in the browser.
func (s *Server) openRelease(w http.ResponseWriter, r *http.Request) {
	versionMu.Lock()
	u := versionCache.URL
	versionMu.Unlock()
	if !strings.HasPrefix(u, "https://") {
		writeErr(w, http.StatusNotFound, errors.New("nenhuma versão nova conhecida"))
		return
	}
	if err := openURL(u); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]string{"url": u})
}
