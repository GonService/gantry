package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gantry/internal/metrics"
)

// Client talks to an agent through a dial function (the forwarded socket).
type Client struct {
	http   *http.Client
	stream *http.Client
	tr     *http.Transport
	dial   func(ctx context.Context) (net.Conn, error)
}

// CloseIdle drops kept-alive connections, so the next request dials again
// (after the way to reach the agent changed).
func (c *Client) CloseIdle() { c.tr.CloseIdleConnections() }

func NewClient(dial func(ctx context.Context) (net.Conn, error)) *Client {
	tr := &http.Transport{
		DialContext:        func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx) },
		MaxIdleConns:       4,
		IdleConnTimeout:    30 * time.Second,
		DisableCompression: true,
	}
	return &Client{http: &http.Client{Transport: tr, Timeout: 20 * time.Second}, stream: &http.Client{Transport: tr}, tr: tr, dial: dial}
}

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	u := "http://agent" + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
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
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("agente: %s", strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) Info(ctx context.Context) (Info, error) {
	var i Info
	err := c.get(ctx, "/v1/info", nil, &i)
	return i, err
}

func (c *Client) Metrics(ctx context.Context, since int64, lab string, max int) (metrics.Result, error) {
	var r metrics.Result
	q := url.Values{"since": {fmt.Sprint(since)}, "max": {fmt.Sprint(max)}}
	if lab != "" {
		q.Set("lab", lab)
	}
	err := c.get(ctx, "/v1/metrics", q, &r)
	return r, err
}

// Watch calls fn with each changed path until ctx ends or the stream breaks.
func (c *Client) Watch(ctx context.Context, paths []string, fn func(path string)) error {
	q := url.Values{"path": paths}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://agent/v1/watch?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := c.stream.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("agente: %s", strings.TrimSpace(string(b)))
	}
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if p, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			fn(p)
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		return err
	}
	return ctx.Err()
}
