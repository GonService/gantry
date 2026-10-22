// Package obs talks to the observability stack next to a host: Prometheus
// for metrics, Loki for log history and Grafana for dashboards. They are
// found among the host's containers or set by hand, and reached through the
// host (over SSH for servers), so a Prometheus on 127.0.0.1 of a server works.
package obs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"gantry/internal/engine"
)

// Endpoint is one service and where it came from.
type Endpoint struct {
	URL       string `json:"url"`
	Source    string `json:"source"` // config or container
	Container string `json:"container,omitempty"`
	Lab       string `json:"lab,omitempty"`
}

// Kinds are the services Gantry knows.
var Kinds = []string{"prometheus", "loki", "grafana"}

// LabelPath on a container tells the sub path its web UI is served from.
const LabelPath = "gantry.path"

// Detect looks for Prometheus, Loki and Grafana among running containers
// that publish their usual port.
func Detect(cs []engine.Container) map[string]Endpoint {
	out := map[string]Endpoint{}
	for _, c := range cs {
		if c.State != "running" {
			continue
		}
		kind, port := kindOf(c.Image)
		if kind == "" {
			continue
		}
		if _, ok := out[kind]; ok {
			continue
		}
		for _, p := range c.Ports {
			if p.PrivatePort != port || p.PublicPort == 0 {
				continue
			}
			ip := p.IP
			if ip == "" || ip == "0.0.0.0" || ip == "::" {
				ip = "127.0.0.1"
			}
			out[kind] = Endpoint{
				// A service behind a sub path (Grafana at /grafana) says so in a label.
				URL:    "http://" + net.JoinHostPort(ip, strconv.Itoa(p.PublicPort)) + strings.TrimRight(c.Labels[LabelPath], "/"),
				Source: "container", Container: c.Name(), Lab: c.Labels["com.docker.compose.project"],
			}
			break
		}
	}
	return out
}

func kindOf(image string) (string, int) {
	i := strings.ToLower(image)
	if j := strings.LastIndex(i, "@"); j > 0 {
		i = i[:j]
	}
	name := i
	if j := strings.LastIndex(name, ":"); j > strings.LastIndex(name, "/") {
		name = name[:j]
	}
	base := name[strings.LastIndex(name, "/")+1:]
	switch {
	case base == "prometheus" || base == "prometheus-linux-amd64" || base == "prometheus-linux-arm64":
		return "prometheus", 9090
	case base == "loki":
		return "loki", 3100
	case base == "grafana" || base == "grafana-oss" || base == "grafana-enterprise":
		return "grafana", 3000
	}
	return "", 0
}

// Client calls one service through a dial function of the host.
type Client struct {
	base  *url.URL
	token string
	http  *http.Client
}

// NewClient reaches rawURL by dialing through dial (the host's machine).
func NewClient(rawURL, token string, dial func(ctx context.Context, addr string) (net.Conn, error)) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(rawURL, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("endereço inválido: %q (use http://host:porta)", rawURL)
	}
	addr := u.Host
	if u.Port() == "" {
		if u.Scheme == "https" {
			addr += ":443"
		} else {
			addr += ":80"
		}
	}
	tr := &http.Transport{
		DialContext:     func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx, addr) },
		MaxIdleConns:    4,
		IdleConnTimeout: 30 * time.Second,
	}
	return &Client{base: u, token: token, http: &http.Client{Transport: tr, Timeout: 30 * time.Second}}, nil
}

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	u := *c.base
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			return ue.Err
		}
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(b, &e)
		msg := e.Error + e.Message
		if msg == "" {
			msg = strings.TrimSpace(string(b))
			if len(msg) > 300 {
				msg = msg[:300]
			}
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	if out == nil {
		return nil
	}
	if s, ok := out.(*string); ok {
		*s = string(b)
		return nil
	}
	return json.Unmarshal(b, out)
}

/* Prometheus */

type PromSeries struct {
	Labels map[string]string `json:"labels"`
	Points [][2]float64      `json:"points"` // unix seconds, value
}

// PromVersion checks Prometheus and returns its version.
func (c *Client) PromVersion(ctx context.Context) (string, error) {
	var r struct {
		Data struct {
			Version string `json:"version"`
		} `json:"data"`
	}
	err := c.get(ctx, "/api/v1/status/buildinfo", nil, &r)
	return r.Data.Version, err
}

// Alert is a rule of Prometheus that is firing (or about to).
type Alert struct {
	Name     string            `json:"name"`
	State    string            `json:"state"` // firing or pending
	Severity string            `json:"severity,omitempty"`
	Summary  string            `json:"summary,omitempty"`
	Since    time.Time         `json:"since"`
	Labels   map[string]string `json:"labels"`
}

// Alerts returns the alerts that are firing or pending.
func (c *Client) Alerts(ctx context.Context) ([]Alert, error) {
	var r struct {
		Data struct {
			Alerts []struct {
				Labels      map[string]string `json:"labels"`
				Annotations map[string]string `json:"annotations"`
				State       string            `json:"state"`
				ActiveAt    time.Time         `json:"activeAt"`
			} `json:"alerts"`
		} `json:"data"`
	}
	if err := c.get(ctx, "/api/v1/alerts", nil, &r); err != nil {
		return nil, err
	}
	out := []Alert{}
	for _, a := range r.Data.Alerts {
		sum := a.Annotations["summary"]
		if sum == "" {
			sum = a.Annotations["description"]
		}
		out = append(out, Alert{Name: a.Labels["alertname"], State: a.State, Severity: a.Labels["severity"], Summary: sum, Since: a.ActiveAt, Labels: a.Labels})
	}
	return out, nil
}

// QueryRange runs a PromQL range query.
func (c *Client) QueryRange(ctx context.Context, q string, start, end time.Time, step time.Duration) ([]PromSeries, error) {
	if step < time.Second {
		step = time.Second
	}
	v := url.Values{
		"query": {q}, "start": {strconv.FormatInt(start.Unix(), 10)}, "end": {strconv.FormatInt(end.Unix(), 10)},
		"step": {strconv.Itoa(int(step / time.Second))},
	}
	var r struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				Values [][2]any          `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := c.get(ctx, "/api/v1/query_range", v, &r); err != nil {
		return nil, err
	}
	if r.Status != "success" {
		return nil, errors.New(r.Error)
	}
	out := make([]PromSeries, 0, len(r.Data.Result))
	for _, s := range r.Data.Result {
		ps := PromSeries{Labels: s.Metric, Points: make([][2]float64, 0, len(s.Values))}
		for _, p := range s.Values {
			t, _ := p[0].(float64)
			vs, _ := p[1].(string)
			f, err := strconv.ParseFloat(vs, 64)
			if err != nil {
				continue
			}
			ps.Points = append(ps.Points, [2]float64{t, f})
		}
		out = append(out, ps)
	}
	return out, nil
}

/* Loki */

type LogLine struct {
	T      int64             `json:"t"` // unix nanoseconds
	Labels map[string]string `json:"labels"`
	Line   string            `json:"line"`
}

func (c *Client) LokiReady(ctx context.Context) error {
	var s string
	return c.get(ctx, "/ready", nil, &s)
}

// LokiLabels lists the label names Loki knows.
func (c *Client) LokiLabels(ctx context.Context, since time.Time) ([]string, error) {
	var r struct {
		Data []string `json:"data"`
	}
	err := c.get(ctx, "/loki/api/v1/labels", url.Values{"start": {strconv.FormatInt(since.UnixNano(), 10)}}, &r)
	return r.Data, err
}

// LokiQuery returns up to limit lines, oldest first.
func (c *Client) LokiQuery(ctx context.Context, q string, start, end time.Time, limit int) ([]LogLine, error) {
	v := url.Values{
		"query": {q}, "start": {strconv.FormatInt(start.UnixNano(), 10)}, "end": {strconv.FormatInt(end.UnixNano(), 10)},
		"limit": {strconv.Itoa(limit)}, "direction": {"backward"},
	}
	var r struct {
		Data struct {
			Result []struct {
				Stream map[string]string `json:"stream"`
				Values [][2]string       `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := c.get(ctx, "/loki/api/v1/query_range", v, &r); err != nil {
		return nil, err
	}
	var out []LogLine
	for _, s := range r.Data.Result {
		for _, x := range s.Values {
			t, _ := strconv.ParseInt(x[0], 10, 64)
			out = append(out, LogLine{T: t, Labels: s.Stream, Line: x[1]})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].T < out[j].T })
	return out, nil
}

// ContainerSelector picks the LogQL stream selector for a container from
// the labels the log shipper uses (Promtail, Alloy and the Docker driver
// name them differently).
func ContainerSelector(labels []string, name, project, service string) (string, error) {
	have := map[string]bool{}
	for _, l := range labels {
		have[l] = true
	}
	q := strconv.Quote
	switch {
	case have["container"]:
		return "{container=~" + q("/?"+regexpQuote(name)) + "}", nil
	case have["container_name"]:
		return "{container_name=~" + q("/?"+regexpQuote(name)) + "}", nil
	case have["compose_service"] && service != "":
		sel := "compose_service=" + q(service)
		if have["compose_project"] && project != "" {
			sel = "compose_project=" + q(project) + ", " + sel
		}
		return "{" + sel + "}", nil
	case have["com_docker_compose_service"] && service != "":
		return "{com_docker_compose_service=" + q(service) + "}", nil
	}
	return "", errors.New("o Loki não tem um label de container (container, container_name ou compose_service); confira a coleta de logs (Promtail ou Alloy)")
}

func regexpQuote(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`\.+*?()|[]{}^$`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

/* Grafana */

type Dashboard struct {
	UID    string `json:"uid"`
	Title  string `json:"title"`
	URL    string `json:"url"` // path, e.g. /d/abc/slug
	Folder string `json:"folder,omitempty"`
}

func (c *Client) GrafanaVersion(ctx context.Context) (string, error) {
	var r struct {
		Version  string `json:"version"`
		Database string `json:"database"`
	}
	err := c.get(ctx, "/api/health", nil, &r)
	return r.Version, err
}

func (c *Client) Dashboards(ctx context.Context) ([]Dashboard, error) {
	var r []struct {
		UID         string `json:"uid"`
		Title       string `json:"title"`
		URL         string `json:"url"`
		FolderTitle string `json:"folderTitle"`
	}
	if err := c.get(ctx, "/api/search", url.Values{"type": {"dash-db"}, "limit": {"100"}}, &r); err != nil {
		return nil, err
	}
	out := make([]Dashboard, 0, len(r))
	for _, d := range r {
		out = append(out, Dashboard{UID: d.UID, Title: d.Title, URL: d.URL, Folder: d.FolderTitle})
	}
	return out, nil
}
