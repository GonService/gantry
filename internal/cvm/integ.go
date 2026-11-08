package cvm

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
)

// How the Mac's terminal reaches the machine's engines: Docker contexts, a
// Podman connection, small commands in ~/.gantry/bin (that run the CLIs in
// the machine, in the same folder), that folder in the PATH, and the
// /var/run/docker.sock link some tools look for.

//go:embed shim.sh.tmpl
var shimTmpl string

var shimT = template.Must(template.New("shim").Parse(shimTmpl))

// BinDir is where the commands that run in the machine are.
func BinDir() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".gantry", "bin")
}

// ContextNames are the Docker contexts of a machine (Docker and Podman).
func ContextNames(name string) (string, string) {
	if name == DefaultName {
		return "gantry", "gantry-podman"
	}
	return name, name + "-podman"
}

// Integration is the state of each piece.
type Integration struct {
	DockerContext string   `json:"dockerContext"`        // name, when it exists
	PodmanContext string   `json:"podmanContext"`        // Docker context to the Podman socket
	PodmanConn    bool     `json:"podmanConn"`           // podman system connection
	PodmanCLI     bool     `json:"podmanCli"`            // podman on the Mac
	DockerCLI     string   `json:"dockerCli"`            // the docker the Mac runs ("" none, or a path)
	Path          bool     `json:"path"`                 // ~/.gantry/bin in the shell's rc
	RC            string   `json:"rc"`                   // that file
	Link          bool     `json:"link"`                 // /var/run/docker.sock is ours
	LinkOther     string   `json:"linkOther,omitempty"`  // where it points otherwise
	Shims         []string `json:"shims"`                // commands in ~/.gantry/bin
	CurrentCtx    string   `json:"currentContext"`       // docker's current context
	DockerSock    string   `json:"dockerSock,omitempty"` // sockets on the Mac
	PodmanSock    string   `json:"podmanSock,omitempty"`
	BinDir        string   `json:"binDir"`
	Commands      []string `json:"commands"` // ready to paste
}

func dockerHome() string {
	if d := os.Getenv("DOCKER_CONFIG"); d != "" {
		return d
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".docker")
}

func ctxDir(ctxName string) string {
	h := sha256.Sum256([]byte(ctxName))
	return filepath.Join(dockerHome(), "contexts", "meta", hex.EncodeToString(h[:]))
}

func ctxExists(ctxName, sock string) bool {
	b, err := os.ReadFile(filepath.Join(ctxDir(ctxName), "meta.json"))
	if err != nil {
		return false
	}
	var m struct {
		Endpoints map[string]struct{ Host string }
	}
	return json.Unmarshal(b, &m) == nil && m.Endpoints["docker"].Host == "unix://"+sock
}

// writeContext creates a Docker context the same way docker context create
// does (works without the docker CLI).
func writeContext(ctxName, sock, desc string) error {
	meta := map[string]any{
		"Name":      ctxName,
		"Metadata":  map[string]string{"Description": desc},
		"Endpoints": map[string]any{"docker": map[string]any{"Host": "unix://" + sock, "SkipTLSVerify": false}},
	}
	b, _ := json.Marshal(meta)
	if err := os.MkdirAll(ctxDir(ctxName), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(ctxDir(ctxName), "meta.json"), b, 0o644)
}

func currentContext() string {
	b, err := os.ReadFile(filepath.Join(dockerHome(), "config.json"))
	if err != nil {
		return ""
	}
	var c struct {
		CurrentContext string `json:"currentContext"`
	}
	_ = json.Unmarshal(b, &c)
	return c.CurrentContext
}

func rcFile() string {
	h, _ := os.UserHomeDir()
	sh := filepath.Base(os.Getenv("SHELL"))
	if sh == "bash" {
		return filepath.Join(h, ".bash_profile")
	}
	return filepath.Join(h, ".zshrc")
}

const rcStart, rcEnd = "# >>> gantry >>>", "# <<< gantry <<<"

func rcBlock() string {
	return rcStart + "\n# Comandos que rodam na Container VM (docker, podman, nerdctl); os do\n# Mac, quando existem, vêm antes.\nexport PATH=\"$PATH:$HOME/.gantry/bin\"\n" + rcEnd + "\n"
}

func hasRC() bool {
	b, _ := os.ReadFile(rcFile())
	return bytes.Contains(b, []byte(rcStart))
}

func setRC(on bool) error {
	p := rcFile()
	b, _ := os.ReadFile(p)
	s := string(b)
	i, j := strings.Index(s, rcStart), strings.Index(s, rcEnd)
	if i >= 0 && j > i {
		s = s[:i] + strings.TrimPrefix(s[j+len(rcEnd):], "\n")
	}
	if on {
		if s != "" && !strings.HasSuffix(s, "\n") {
			s += "\n"
		}
		s += "\n" + rcBlock()
	}
	return os.WriteFile(p, []byte(s), 0o644)
}

func lookOutside(bin string) string {
	// The docker of the Mac, not our command in ~/.gantry/bin.
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if d == BinDir() {
			continue
		}
		p := filepath.Join(d, bin)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p
		}
	}
	for _, d := range []string{"/opt/homebrew/bin", "/usr/local/bin"} {
		p := filepath.Join(d, bin)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// Inspect reports the integration of a machine.
func Inspect(name string) Integration {
	s, _ := Load(name)
	dctx, pctx := ContextNames(name)
	in := Integration{RC: rcFile(), BinDir: BinDir(), Shims: []string{}, CurrentCtx: currentContext()}
	in.DockerCLI = lookOutside("docker")
	in.PodmanCLI = lookOutside("podman") != ""
	if s.Docker {
		in.DockerSock = DockerSocket(name)
		if ctxExists(dctx, in.DockerSock) {
			in.DockerContext = dctx
		}
		in.Commands = append(in.Commands, "docker context use "+dctx, "export DOCKER_HOST=unix://"+in.DockerSock)
	}
	if s.Podman {
		in.PodmanSock = PodmanSocket(name)
		if ctxExists(pctx, in.PodmanSock) {
			in.PodmanContext = pctx
		}
		in.Commands = append(in.Commands, "export CONTAINER_HOST=unix://"+in.PodmanSock)
		if in.PodmanCLI {
			if out, err := exec.Command(lookOutside("podman"), "system", "connection", "list", "--format", "{{.Name}}").Output(); err == nil {
				for _, l := range strings.Fields(string(out)) {
					if l == dctx {
						in.PodmanConn = true
					}
				}
			}
		}
	}
	in.Path = hasRC()
	if t, err := os.Readlink("/var/run/docker.sock"); err == nil {
		if s.Docker && t == DockerSocket(name) {
			in.Link = true
		} else {
			in.LinkOther = t
		}
	}
	ents, _ := os.ReadDir(BinDir())
	for _, e := range ents {
		in.Shims = append(in.Shims, e.Name())
	}
	return in
}

// WriteShims (re)writes the commands of ~/.gantry/bin for the machine.
func WriteShims(name string) error {
	s, err := Load(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(BinDir(), 0o755); err != nil {
		return err
	}
	want := map[string]string{name: "bash -l"}
	if s.Docker || s.Podman {
		want["docker"] = "docker"
	}
	if s.Podman {
		want["podman"] = "sudo podman"
	}
	if s.Containerd {
		want["nerdctl"] = "sudo nerdctl"
	}
	if !s.Docker && s.Podman {
		want["docker"] = "sudo podman"
	}
	for bin, inner := range want {
		var b bytes.Buffer
		if err := shimT.Execute(&b, map[string]string{"Name": name, "Dir": Dir(name), "Inner": inner}); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(BinDir(), bin), b.Bytes(), 0o755); err != nil {
			return err
		}
	}
	return nil
}

// Wanted is what the user asks for in the Engines tab.
type Wanted struct {
	DockerContext *bool `json:"dockerContext"`
	PodmanContext *bool `json:"podmanContext"`
	PodmanConn    *bool `json:"podmanConn"`
	Path          *bool `json:"path"`
	Use           *bool `json:"use"` // make the Docker context the current one
}

// Apply changes the pieces asked for.
func Apply(ctx context.Context, name string, w Wanted) error {
	s, err := Load(name)
	if err != nil {
		return err
	}
	dctx, pctx := ContextNames(name)
	desc := "Container VM " + name + " (Gantry)"
	if w.DockerContext != nil && s.Docker {
		if *w.DockerContext {
			if err := writeContext(dctx, DockerSocket(name), desc); err != nil {
				return err
			}
		} else if ctxExists(dctx, DockerSocket(name)) {
			_ = os.RemoveAll(ctxDir(dctx))
		}
	}
	if w.PodmanContext != nil && s.Podman {
		if *w.PodmanContext {
			if err := writeContext(pctx, PodmanSocket(name), desc+" · Podman"); err != nil {
				return err
			}
		} else if ctxExists(pctx, PodmanSocket(name)) {
			_ = os.RemoveAll(ctxDir(pctx))
		}
	}
	if w.PodmanConn != nil && s.Podman {
		pm := lookOutside("podman")
		if pm == "" {
			return errors.New("o podman não está instalado no Mac; use a variável CONTAINER_HOST ou o comando podman de ~/.gantry/bin")
		}
		_ = exec.CommandContext(ctx, pm, "system", "connection", "remove", dctx).Run()
		if *w.PodmanConn {
			if out, err := exec.CommandContext(ctx, pm, "system", "connection", "add", dctx, "unix://"+PodmanSocket(name)).CombinedOutput(); err != nil {
				return fmt.Errorf("podman system connection add: %s", strings.TrimSpace(string(out)))
			}
		}
	}
	if w.Use != nil && *w.Use && s.Docker {
		if err := writeContext(dctx, DockerSocket(name), desc); err != nil {
			return err
		}
		if err := setCurrentContext(dctx); err != nil {
			return err
		}
	}
	if w.Path != nil {
		if *w.Path {
			if err := WriteShims(name); err != nil {
				return err
			}
		}
		if err := setRC(*w.Path); err != nil {
			return err
		}
	}
	return nil
}

func setCurrentContext(c string) error {
	p := filepath.Join(dockerHome(), "config.json")
	m := map[string]any{}
	if b, err := os.ReadFile(p); err == nil {
		if err := json.Unmarshal(b, &m); err != nil {
			return fmt.Errorf("%s ilegível: %v", p, err)
		}
	}
	m["currentContext"] = c
	b, _ := json.MarshalIndent(m, "", "\t")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600)
}

// LinkSocket points /var/run/docker.sock at the machine's Docker (on) or
// removes our link (off), asking macOS for the administrator password.
func LinkSocket(ctx context.Context, name string, on bool) error {
	sock := DockerSocket(name)
	var line string
	if on {
		line = "ln -sf '" + sock + "' /var/run/docker.sock"
	} else {
		if t, _ := os.Readlink("/var/run/docker.sock"); t != sock {
			return nil
		}
		line = "rm -f /var/run/docker.sock"
	}
	script := `do shell script "` + line + `" with administrator privileges with prompt "O Gantry quer apontar /var/run/docker.sock para a Container VM."`
	if out, err := exec.CommandContext(ctx, "osascript", "-e", script).CombinedOutput(); err != nil {
		return fmt.Errorf("osascript: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
