package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"html"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"gantry/internal/applog"
	"gantry/internal/hosts"
	"gantry/internal/obs"
)

// WebApp is a published port that answers HTTP: what the app offers to open.
type WebApp struct {
	Port   int    `json:"port"`
	Scheme string `json:"scheme"`           // http or https
	Path   string `json:"path,omitempty"`   // sub path the app lives in (label gantry.path)
	Title  string `json:"title,omitempty"`  // <title> of the page
	Status int    `json:"status,omitempty"` // HTTP status of the probe
	Owner  string `json:"owner"`            // container name, or "vm:<projeto>/<vm>"
	Lab    string `json:"lab,omitempty"`
}

type webCache struct {
	at   time.Time
	apps map[int]WebApp
}

var (
	webMu     sync.Mutex
	webCached = map[*hosts.Host]webCache{}
	titleRe   = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
)

// publishedPorts lists the ports published on the host's machine, by owner:
// containers of every engine of the machine and the VMs of the host's labs.
func (s *Server) publishedPorts(ctx context.Context, h *hosts.Host) map[int]WebApp {
	out := map[int]WebApp{}
	for _, e := range s.machineEngines(ctx, h) {
		cs, err := e.Engine.Containers(ctx)
		if err != nil {
			continue
		}
		for _, c := range cs {
			if c.State != "running" {
				continue
			}
			for _, p := range c.Ports {
				if p.PublicPort > 0 && (p.Type == "" || p.Type == "tcp") {
					if _, ok := out[p.PublicPort]; !ok {
						out[p.PublicPort] = WebApp{Port: p.PublicPort, Owner: c.Name(), Lab: c.Labels["com.docker.compose.project"], Path: strings.TrimRight(c.Labels[obs.LabelPath], "/")}
					}
				}
			}
		}
	}
	if h.Kind != hosts.TCP {
		if ls, err := s.labsOf(ctx, h); err == nil {
			for _, l := range ls {
				for _, vm := range l.VMs {
					if vm.State != "running" {
						continue
					}
					for _, p := range vm.Ports { // proto:ip:host:guest
						f := strings.Split(p, ":")
						if len(f) != 4 || f[0] != "tcp" {
							continue
						}
						if port, err := strconv.Atoi(f[2]); err == nil {
							out[port] = WebApp{Port: port, Owner: "vm:" + l.ID + "/" + vm.Name, Lab: l.ID}
						}
					}
				}
			}
		}
	}
	return out
}

// webApps probes every published port and keeps those that answer HTTP(S).
// Results are cached for half a minute per host.
func (s *Server) webApps(ctx context.Context, h *hosts.Host, fresh bool) map[int]WebApp {
	webMu.Lock()
	c, ok := webCached[h]
	webMu.Unlock()
	if ok && !fresh && time.Since(c.at) < 30*time.Second {
		return c.apps
	}
	ports := s.publishedPorts(ctx, h)
	apps := map[int]WebApp{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for port, app := range ports {
		wg.Add(1)
		go func() {
			defer applog.Recover("server/web.go:104")
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if probed, ok := probeWeb(ctx, h, app); ok {
				mu.Lock()
				apps[port] = probed
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	webMu.Lock()
	webCached[h] = webCache{at: time.Now(), apps: apps}
	webMu.Unlock()
	return apps
}

var probeClient = &http.Client{
	Timeout: 2 * time.Second,
	// Self-signed certificates are the norm in labs.
	Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, DisableKeepAlives: true},
	// A redirect still says "this is a web app"; don't follow it elsewhere.
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func probeWeb(ctx context.Context, h *hosts.Host, app WebApp) (WebApp, bool) {
	addr, err := h.LocalAddr(ctx, "127.0.0.1:"+strconv.Itoa(app.Port))
	if err != nil {
		return app, false
	}
	for _, scheme := range []string{"http", "https"} {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, scheme+"://"+addr+app.Path+"/", nil)
		resp, err := probeClient.Do(req)
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		// "Client sent an HTTP request to an HTTPS server" comes back as 400 over http.
		if scheme == "http" && resp.StatusCode == 400 && strings.Contains(strings.ToLower(string(b)), "https") {
			continue
		}
		app.Scheme, app.Status = scheme, resp.StatusCode
		if m := titleRe.FindSubmatch(b); m != nil {
			app.Title = strings.TrimSpace(html.UnescapeString(string(m[1])))
			if len(app.Title) > 80 {
				app.Title = app.Title[:80]
			}
		}
		return app, true
	}
	return app, false
}

// web answers the web apps published on the machine, by port.
func (s *Server) web(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	apps := s.webApps(ctx, h, r.URL.Query().Get("fresh") == "1")
	out := map[string]WebApp{}
	for p, a := range apps {
		out[strconv.Itoa(p)] = a
	}
	writeJSON(w, map[string]any{"apps": out})
}

// webOpen opens a published app in the browser of this computer, through
// the SSH tunnel when the host is a server.
func (s *Server) webOpen(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		Port int    `json:"port"`
		Path string `json:"path"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&req); err != nil || req.Port <= 0 || req.Port > 65535 {
		writeErr(w, http.StatusBadRequest, errors.New("porta inválida"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	scheme := "http"
	if a, ok := s.webApps(ctx, h, false)[req.Port]; ok {
		scheme = a.Scheme
		if req.Path == "" {
			req.Path = a.Path
		}
	}
	addr, err := h.LocalAddr(ctx, "127.0.0.1:"+strconv.Itoa(req.Port))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	if !strings.HasPrefix(req.Path, "/") {
		req.Path = "/" + req.Path
	}
	link := scheme + "://" + addr + req.Path
	if err := openURL(link); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]string{"url": link})
}
