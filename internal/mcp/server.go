package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client calls the API of the open Gantry with the assistants' token.
type Client struct {
	Base  string
	Token string
	HTTP  *http.Client
}

func (c *Client) do(ctx context.Context, method, path string, q url.Values, body any) (*http.Response, error) {
	u := strings.TrimRight(c.Base, "/") + "/api" + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Gantry did not answer (%v); is it open?", err)
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		var e struct {
			Error string `json:"error"`
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			return nil, errors.New(e.Error)
		}
		return nil, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return resp, nil
}

func (c *Client) JSON(ctx context.Context, method, path string, q url.Values, body, out any) error {
	resp, err := c.do(ctx, method, path, q, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

// Stream reads a log (server-sent events: base64 chunks, then "end") into
// text, up to max bytes.
func (c *Client) Stream(ctx context.Context, path string, q url.Values, max int) (string, error) {
	resp, err := c.do(ctx, http.MethodGet, path, q, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	event := ""
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(line[6:])
		case strings.HasPrefix(line, "data:"):
			data := strings.TrimSpace(line[5:])
			if event == "end" {
				var e struct {
					Message string `json:"message"`
				}
				_ = json.Unmarshal([]byte(data), &e)
				if e.Message != "" && !strings.Contains(e.Message, "fim do log") && !strings.Contains(e.Message, "end of log") {
					out.WriteString("\n[" + e.Message + "]")
				}
				return tail(out.String(), max), nil
			}
			if event == "" {
				if b, err := base64.StdEncoding.DecodeString(data); err == nil {
					out.Write(b)
				}
			}
		case line == "":
			event = ""
		}
	}
	return tail(out.String(), max), sc.Err()
}

func tail(s string, max int) string {
	if max > 0 && len(s) > max {
		s = "…" + s[len(s)-max:]
	}
	return s
}

// Tool is one tool of the server.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	write       bool
	run         func(ctx context.Context, c *Client, a args) (string, error)
}

type args map[string]any

func (a args) str(k string) string {
	switch v := a[k].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}

func (a args) need(keys ...string) error {
	var miss []string
	for _, k := range keys {
		if a.str(k) == "" {
			miss = append(miss, k)
		}
	}
	if len(miss) > 0 {
		return fmt.Errorf("missing %s", strings.Join(miss, ", "))
	}
	return nil
}

func (a args) num(k string, def int) int {
	if v, ok := a[k].(float64); ok {
		return int(v)
	}
	if n, err := strconv.Atoi(a.str(k)); err == nil {
		return n
	}
	return def
}

func schema(required []string, props map[string]string) map[string]any {
	p := map[string]any{}
	for k, d := range props {
		typ := "string"
		if strings.HasPrefix(d, "(number)") {
			typ, d = "integer", strings.TrimSpace(strings.TrimPrefix(d, "(number)"))
		}
		p[k] = map[string]any{"type": typ, "description": d}
	}
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": p, "required": required}
}

// Tools are the tools; the ones that change things only when write is on.
func Tools(write bool) []Tool {
	var out []Tool
	for _, t := range allTools() {
		if !t.write || write {
			out = append(out, t)
		}
	}
	return out
}

// Server answers MCP (JSON-RPC 2.0, one message per line) on in/out.
type Server struct {
	Client  *Client
	Write   bool
	Version string
}

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve runs until in ends.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	enc := json.NewEncoder(out)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var req rpcReq
		if err := json.Unmarshal(line, &req); err != nil {
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": nil, "error": rpcErr{-32700, "parse error"}})
			continue
		}
		res, rerr := s.handle(ctx, req)
		if len(req.ID) == 0 {
			continue // a notification
		}
		msg := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		if rerr != nil {
			msg["error"] = rerr
		} else {
			msg["result"] = res
		}
		if err := enc.Encode(msg); err != nil {
			return err
		}
	}
	return sc.Err()
}

func (s *Server) handle(ctx context.Context, req rpcReq) (any, *rpcErr) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		v := p.ProtocolVersion
		if v == "" {
			v = "2025-06-18"
		}
		return map[string]any{
			"protocolVersion": v,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "gantry", "version": s.Version},
			"instructions":    "Gantry manages containers (Docker, Podman), VMs and network labs (projects of a compose file) on the user's machines, and Kubernetes clusters. Start with list_hosts or list_clusters. Secrets and Helm values are never returned.",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": Tools(s.Write)}, nil
	case "tools/call":
		var p struct {
			Name      string `json:"name"`
			Arguments args   `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcErr{-32602, "invalid params"}
		}
		for _, t := range Tools(s.Write) {
			if t.Name != p.Name {
				continue
			}
			cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			if p.Arguments == nil {
				p.Arguments = args{}
			}
			text, err := t.run(cctx, s.Client, p.Arguments)
			if err != nil {
				return map[string]any{"content": []map[string]any{{"type": "text", "text": err.Error()}}, "isError": true}, nil
			}
			return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}, nil
		}
		for _, t := range allTools() {
			if t.Name == p.Name {
				return map[string]any{"content": []map[string]any{{"type": "text", "text": "this tool changes things and the access of assistants is read-only (Settings › General › Assistants (MCP))"}}, "isError": true}, nil
			}
		}
		return nil, &rpcErr{-32602, "unknown tool " + p.Name}
	}
	if strings.HasPrefix(req.Method, "notifications/") {
		return nil, nil
	}
	return nil, &rpcErr{-32601, "method not found: " + req.Method}
}

// table writes rows as aligned text.
func table(head []string, rows [][]string) string {
	if len(rows) == 0 {
		return "(none)"
	}
	w := make([]int, len(head))
	for i, h := range head {
		w[i] = len(h)
	}
	for _, r := range rows {
		for i := range head {
			if i < len(r) && len([]rune(r[i])) > w[i] {
				w[i] = len([]rune(r[i]))
			}
		}
	}
	var b strings.Builder
	line := func(r []string) {
		for i := range head {
			c := ""
			if i < len(r) {
				c = r[i]
			}
			b.WriteString(c)
			if i < len(head)-1 {
				b.WriteString(strings.Repeat(" ", w[i]-len([]rune(c))+2))
			}
		}
		b.WriteString("\n")
	}
	line(head)
	for _, r := range rows {
		line(r)
	}
	return b.String()
}
