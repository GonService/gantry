package server

// Updating the app in one click (macOS): through Homebrew when it was
// installed by it, or by the zip of the release, checked (SHA256SUMS and,
// when the app is signed, the same Developer ID) before the installed app is
// touched; then the new one opens.

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

type ghAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// appBundle is the Gantry.app the process runs from ("" outside one).
func appBundle(exe string) string {
	i := strings.Index(exe, ".app/Contents/MacOS/")
	if i < 0 {
		return ""
	}
	return exe[:i+len(".app")]
}

// updateVia says how this app updates: brew (installed by Homebrew), zip
// (an app bundle on macOS) or "" (download it from the release page).
func updateVia(goos, exe string, caskrooms []string) string {
	if goos != "darwin" || appBundle(exe) == "" {
		return ""
	}
	for _, d := range caskrooms {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return "brew"
		}
	}
	return "zip"
}

// assetURL finds an asset of a release by name.
func assetURL(rel *ghRelease, name string) string {
	for _, a := range rel.Assets {
		if a.Name == name {
			return a.URL
		}
	}
	return ""
}

func download(ctx context.Context, url, dest string) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("User-Agent", "Gantry/"+Version)
	resp, err := (&http.Client{Timeout: 30 * time.Minute}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s respondeu %d", url, resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, io.LimitReader(resp.Body, 2<<30))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// downloadVerified downloads the zip of the version and checks it against
// the SHA256SUMS of the release.
func downloadVerified(ctx context.Context, rel *ghRelease, version, dir string) (string, error) {
	name := "Gantry-" + version + "-macos.zip"
	zu, su := assetURL(rel, name), assetURL(rel, "SHA256SUMS")
	if zu == "" || su == "" {
		return "", fmt.Errorf("a versão %s não tem %s e SHA256SUMS para baixar", version, name)
	}
	sums := filepath.Join(dir, "SHA256SUMS")
	if err := download(ctx, su, sums); err != nil {
		return "", err
	}
	want := ""
	f, err := os.Open(sums)
	if err != nil {
		return "", err
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if fl := strings.Fields(sc.Text()); len(fl) == 2 && strings.TrimPrefix(fl[1], "*") == name {
			want = strings.ToLower(fl[0])
		}
	}
	f.Close()
	if want == "" {
		return "", fmt.Errorf("SHA256SUMS não tem %s", name)
	}
	zip := filepath.Join(dir, name)
	if err := download(ctx, zu, zip); err != nil {
		return "", err
	}
	zf, err := os.Open(zip)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, err = io.Copy(h, zf)
	zf.Close()
	if err != nil {
		return "", err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return "", fmt.Errorf("o arquivo baixado não confere com SHA256SUMS (%s, esperado %s); nada foi trocado", got[:12], want[:12])
	}
	return zip, nil
}

var teamRe = regexp.MustCompile(`(?m)^TeamIdentifier=(\S+)`)

// teamOf is the Developer ID team that signed an app ("" when unsigned).
func teamOf(run func(name string, args ...string) (string, error), app string) string {
	out, err := run("codesign", "-dv", app)
	if err != nil {
		return ""
	}
	if m := teamRe.FindStringSubmatch(out); m != nil && m[1] != "not set" {
		return m[1]
	}
	return ""
}

// checkSignature: when the installed app is signed, the new one must be
// valid, accepted by Gatekeeper and from the same team.
func checkSignature(run func(name string, args ...string) (string, error), cur, next string) error {
	team := teamOf(run, cur)
	if team == "" {
		return nil // an unsigned app updates to the next one as it is
	}
	if out, err := run("codesign", "--verify", "--deep", "--strict", next); err != nil {
		return fmt.Errorf("a assinatura da versão nova não é válida: %s", strings.TrimSpace(out))
	}
	if got := teamOf(run, next); got != team {
		return fmt.Errorf("a versão nova foi assinada por outro desenvolvedor (%q, o instalado é %q); nada foi trocado", got, team)
	}
	if out, err := run("spctl", "-a", "-t", "exec", next); err != nil {
		return fmt.Errorf("o Gatekeeper recusou a versão nova: %s", strings.TrimSpace(out))
	}
	return nil
}

func runCmd(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

// relaunch opens the app again (the new one) and ends this process.
func relaunch(app string) {
	_ = exec.Command("sh", "-c", "sleep 2; open -n \"$1\"", "sh", app).Start()
	go func() {
		time.Sleep(800 * time.Millisecond)
		os.Exit(0)
	}()
}

// updateApp updates and reopens the app, as a job with live output.
func (s *Server) updateApp(w http.ResponseWriter, r *http.Request) {
	exe, _ := os.Executable()
	exe, _ = filepath.EvalSymlinks(exe)
	via := updateVia(runtime.GOOS, exe, brewCaskrooms)
	if via == "" {
		writeErr(w, http.StatusBadRequest, errors.New("esta instalação não atualiza sozinha; baixe a versão nova na página da versão"))
		return
	}
	app := appBundle(exe)
	id, err := s.startJob("app:update", "Atualizar o Gantry", func(ctx context.Context, out io.Writer) error {
		if via == "brew" {
			brew, err := exec.LookPath("brew")
			if err != nil {
				for _, p := range []string{"/opt/homebrew/bin/brew", "/usr/local/bin/brew"} {
					if _, e := os.Stat(p); e == nil {
						brew, err = p, nil
					}
				}
			}
			if err != nil {
				return errors.New("não achei o brew")
			}
			fmt.Fprintf(out, "\x1b[36m==>\x1b[0m brew upgrade --cask gonservice/tap/gantry\n")
			cmd := exec.CommandContext(ctx, brew, "upgrade", "--cask", "gonservice/tap/gantry")
			cmd.Stdout, cmd.Stderr = out, out
			cmd.Env = append(os.Environ(), "HOMEBREW_NO_AUTO_UPDATE=", "NONINTERACTIVE=1")
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("o brew não atualizou: %v", err)
			}
		} else {
			if err := s.updateFromZip(ctx, app, out); err != nil {
				return err
			}
		}
		fmt.Fprintf(out, "\n\x1b[1;32mAtualizado.\x1b[0m O Gantry reabre em instantes.\n")
		relaunch(app)
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, map[string]any{"id": id, "via": via})
}

// updateFromZip downloads, checks and puts the new app in place of app; the
// installed one comes back if anything fails.
func (s *Server) updateFromZip(ctx context.Context, app string, out io.Writer) error {
	versionMu.Lock()
	c := versionCache
	versionMu.Unlock()
	if !c.Newer {
		return errors.New("nenhuma versão nova conhecida; verifique de novo")
	}
	rel, err := latestRelease(ctx)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "gantry-update-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	fmt.Fprintf(out, "\x1b[36m==>\x1b[0m baixando o Gantry %s\n", c.Latest)
	zip, err := downloadVerified(ctx, rel, c.Latest, tmp)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "    SHA256 confere")
	if o, err := runCmd("ditto", "-x", "-k", zip, tmp); err != nil {
		return fmt.Errorf("não consegui abrir o zip: %s", o)
	}
	next := filepath.Join(tmp, "Gantry.app")
	if err := checkSignature(runCmd, app, next); err != nil {
		return err
	}
	fmt.Fprintln(out, "    assinatura confere")
	old := filepath.Join(filepath.Dir(app), ".Gantry.app.antes")
	_ = os.RemoveAll(old)
	if err := os.Rename(app, old); err != nil {
		return fmt.Errorf("não consegui trocar o app em %s: %v", filepath.Dir(app), err)
	}
	if o, err := runCmd("ditto", next, app); err != nil {
		_ = os.RemoveAll(app)
		_ = os.Rename(old, app)
		return fmt.Errorf("não consegui copiar a versão nova (%s); o app instalado voltou", strings.TrimSpace(o))
	}
	_ = os.RemoveAll(old)
	fmt.Fprintf(out, "\x1b[36m==>\x1b[0m %s trocado\n", app)
	return nil
}

// latestRelease is the newest release, with its assets: the newest of the
// release pages (the repository and the tap may differ while a version is
// being copied, or for versions published only in the tap).
func latestRelease(ctx context.Context) (*ghRelease, error) {
	urls := []string{releasesURL, tapReleasesURL}
	if e := os.Getenv("GANTRY_UPDATE_URL"); e != "" {
		urls = []string{e}
	}
	var last error = errors.New("nenhuma versão publicada encontrada")
	var best *ghRelease
	for _, u := range urls {
		rel, err := fetchRelease(ctx, u)
		if err != nil {
			last = err
			continue
		}
		if rel != nil && (best == nil || versionNewer(releaseVersion(rel), releaseVersion(best))) {
			best = rel
		}
	}
	if best != nil {
		return best, nil
	}
	return nil, last
}
