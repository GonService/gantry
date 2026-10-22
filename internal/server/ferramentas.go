package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"gantry/internal/config"
	"gantry/internal/hosts"
	"gantry/internal/i18n"
	"gantry/internal/kube"
	"gantry/internal/tools"
)

type toolsCached struct {
	r   tools.Report
	err error
	at  time.Time
}

var toolsCache sync.Map // machine → toolsCached

const toolsTTL = 30 * time.Second

func localTools(ctx context.Context, fresh bool) (tools.Report, error) {
	if c, ok := toolsCache.Load("local"); ok && !fresh && time.Since(c.(toolsCached).at) < toolsTTL {
		return c.(toolsCached).r, c.(toolsCached).err
	}
	out, err := exec.CommandContext(ctx, "sh", "-c", tools.Script).Output()
	c := toolsCached{at: time.Now(), err: err}
	if err == nil {
		c.r = tools.Parse(string(out), true)
	}
	toolsCache.Store("local", c)
	return c.r, c.err
}

// machineTools are the tools of the host's machine (this computer for the
// local engines).
func machineTools(ctx context.Context, h *hosts.Host, fresh bool) (tools.Report, error) {
	switch h.Kind {
	case hosts.TCP:
		return tools.Report{}, errors.New("hosts tcp:// só dão acesso ao engine, não aos programas da máquina")
	case hosts.Unix:
		return localTools(ctx, fresh)
	}
	key := h.Machine()
	if c, ok := toolsCache.Load(key); ok && !fresh && time.Since(c.(toolsCached).at) < toolsTTL {
		return c.(toolsCached).r, c.(toolsCached).err
	}
	out, err := h.Run(ctx, "", []string{"sh", "-c", tools.Script}, nil)
	c := toolsCached{at: time.Now(), err: err}
	if err == nil {
		c.r = tools.Parse(out, false)
	}
	toolsCache.Store(key, c)
	return c.r, c.err
}

func (s *Server) hostTools(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	rep, err := machineTools(ctx, h, r.URL.Query().Get("fresh") == "1")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, toolsInLang(rep))
}

func (s *Server) localToolsHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	rep, err := localTools(ctx, r.URL.Query().Get("fresh") == "1")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, toolsInLang(rep))
}

// toolsInLang: the names of the tools are Gantry's words too (the JSON of
// the API leaves "name" alone, as it is the user's elsewhere).
func toolsInLang(rep tools.Report) tools.Report {
	out := rep
	out.Tools = append([]tools.Tool(nil), rep.Tools...)
	for i := range out.Tools {
		out.Tools[i].Name = i18n.T(out.Tools[i].Name)
	}
	return out
}

// installTool runs, on this computer, the Homebrew command of a missing tool
// (no sudo); the output shows in the operations panel.
func (s *Server) installTool(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	rep, err := localTools(ctx, true)
	cancel()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	var t *tools.Tool
	for i := range rep.Tools {
		if rep.Tools[i].ID == r.PathValue("id") {
			t = &rep.Tools[i]
		}
	}
	if t == nil || t.Have || !t.Brew || t.Install == "" {
		writeErr(w, http.StatusBadRequest, errors.New("esta ferramenta não se instala por aqui; use o comando mostrado no terminal"))
		return
	}
	cmdline := t.Install
	jid, err := s.startJob("local", "Instalar "+t.Name, func(ctx context.Context, out io.Writer) error {
		_, _ = out.Write([]byte("$ " + cmdline + "\n\n"))
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", cmdline)
		cmd.Env = append(os.Environ(), "HOMEBREW_NO_AUTO_UPDATE=1", "HOMEBREW_NO_ENV_HINTS=1", "NONINTERACTIVE=1")
		cmd.Stdout = out
		cmd.Stderr = out
		err := cmd.Run()
		toolsCache.Delete("local")
		if t.ID == "engine" || t.ID == "compose" {
			config.ForgetDetected()
			s.reload()
		}
		return err
	})
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, map[string]string{"id": jid})
}

// clusterView is a cluster as the list shows it: with the credential
// plugin it needs and this computer does not have.
type clusterView struct {
	clusterRec
	Plugin     string `json:"plugin,omitempty"`
	PluginHint string `json:"pluginHint,omitempty"`
}

type pluginCached struct {
	mod          time.Time
	context      string
	plugin, hint string
	at           time.Time
}

var pluginCache sync.Map // file → pluginCached

func missingPlugin(c clusterRec) (string, string) {
	file := kube.Expand(c.File)
	st, err := os.Stat(file)
	if err != nil {
		return "", ""
	}
	if v, ok := pluginCache.Load(file); ok {
		pc := v.(pluginCached)
		if pc.mod.Equal(st.ModTime()) && pc.context == c.Context && time.Since(pc.at) < time.Minute {
			return pc.plugin, pc.hint
		}
	}
	cfg, err := kube.Load(file)
	if err != nil {
		return "", ""
	}
	p, h := cfg.MissingPlugin(c.Context)
	pluginCache.Store(file, pluginCached{mod: st.ModTime(), context: c.Context, plugin: p, hint: h, at: time.Now()})
	return p, h
}

// sshConfigHosts lists the Host entries of ~/.ssh/config, marking the
// ones that are already servers in Gantry.
func (s *Server) sshConfigHosts(w http.ResponseWriter, r *http.Request) {
	type view struct {
		config.SSHHost
		Added bool `json:"added"`
	}
	s.mu.RLock()
	list := append([]*hosts.Host(nil), s.hosts...)
	s.mu.RUnlock()
	out := []view{}
	for _, h := range config.SSHHosts() {
		v := view{SSHHost: h}
		for _, x := range list {
			u := strings.TrimPrefix(x.URL, "ssh://")
			if i := strings.LastIndexByte(u, '@'); i >= 0 {
				u = u[i+1:]
			}
			u, _, _ = strings.Cut(u, ":")
			if x.Name == h.Alias || (strings.HasPrefix(x.URL, "ssh://") && (u == h.Alias || (h.HostName != "" && u == h.HostName))) {
				v.Added = true
			}
		}
		out = append(out, v)
	}
	writeJSON(w, out)
}

// progNeed is a program the machine lacks for what it is asked to run.
type progNeed struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Why     string `json:"why"`
	Install string `json:"install,omitempty"` // the command, when the system has a package
	Note    string `json:"note,omitempty"`
}

// programsMissing: of the tools ids asked (dynamips, python3), the ones the
// host's machine does not have; nothing when it cannot tell.
func programsMissing(ctx context.Context, h *hosts.Host, ids ...string) []progNeed {
	rep, err := machineTools(ctx, h, false)
	if err != nil {
		return nil
	}
	var out []progNeed
	for _, id := range ids {
		for _, t := range rep.Tools {
			if t.ID == id && !t.Have {
				out = append(out, progNeed{ID: t.ID, Name: i18n.T(t.Name), Why: i18n.T(t.Why), Install: t.Install, Note: i18n.T(t.Note)})
			}
		}
	}
	return out
}

// programsFor: the programs the kinds of equipment need (dyn: Dynamips; iol,
// dyn and vpcs: python3).
func programsFor(kinds map[string]bool) []string {
	var ids []string
	if kinds["iol"] || kinds["dyn"] || kinds["vpcs"] {
		ids = append(ids, "python3")
	}
	if kinds["dyn"] {
		ids = append(ids, "dynamips")
	}
	return ids
}
