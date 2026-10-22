package engine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// RegistryAuth, when set, gives the login of a registry (docker.io,
// ghcr.io, registry.exemplo.com:5000) for pulls and pushes.
var RegistryAuth func(server string) (user, pass string, ok bool)

// RegistryOf is the registry an image reference points to.
func RegistryOf(ref string) string {
	first, _, found := strings.Cut(ref, "/")
	if !found || (!strings.ContainsAny(first, ".:") && first != "localhost") {
		return "docker.io"
	}
	return first
}

func authHeader(ref string) map[string]string {
	if RegistryAuth == nil {
		return nil
	}
	server := RegistryOf(ref)
	user, pass, ok := RegistryAuth(server)
	if !ok {
		return nil
	}
	if server == "docker.io" {
		server = "https://index.docker.io/v1/"
	}
	b, _ := json.Marshal(map[string]string{"username": user, "password": pass, "serveraddress": server})
	return map[string]string{"X-Registry-Auth": base64.URLEncoding.EncodeToString(b)}
}

// CheckLogin asks the engine to validate a registry login.
func (c *Client) CheckLogin(ctx context.Context, server, user, pass string) error {
	if server == "docker.io" {
		server = "https://index.docker.io/v1/"
	}
	resp, err := c.request(ctx, http.MethodPost, "/auth", nil, map[string]string{"username": user, "password": pass, "serveraddress": server})
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// streamJSON reads the JSON lines of a push, load or similar operation,
// passing what they say to progress and failing on the first error.
func streamJSON(r io.Reader, progress func(string)) error {
	dec := json.NewDecoder(r)
	last := ""
	for {
		var m struct {
			Status, Progress, ID, Error, Stream string
			ErrorDetail                         struct{ Message string } `json:"errorDetail"`
		}
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if m.Error != "" {
			return errors.New(m.Error)
		}
		if progress == nil {
			continue
		}
		line := strings.TrimSpace(m.Stream)
		if line == "" {
			line = strings.TrimSpace(m.ID + " " + m.Status)
		}
		if line != "" && line != last && m.Progress == "" {
			progress(line)
		}
		last = line
	}
}

// Push sends an image to its registry.
func (c *Client) Push(ctx context.Context, ref string, progress func(string)) error {
	image, tag := ref, ""
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		image, tag = ref[:i], ref[i+1:]
	}
	q := url.Values{}
	if tag != "" {
		q.Set("tag", tag)
	}
	hdr := authHeader(image)
	if hdr == nil {
		// The engine wants the header even for registries without login.
		hdr = map[string]string{"X-Registry-Auth": base64.URLEncoding.EncodeToString([]byte("{}"))}
	}
	resp, err := c.requestH(ctx, http.MethodPost, "/images/"+image+"/push", q, nil, hdr)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return streamJSON(resp.Body, progress)
}

// HistoryEntry is one layer of an image, newest first.
type HistoryEntry struct {
	ID        string   `json:"id"`
	Created   int64    `json:"created"`
	CreatedBy string   `json:"createdBy"`
	Size      int64    `json:"size"`
	Comment   string   `json:"comment"`
	Tags      []string `json:"tags"`
}

func (c *Client) ImageHistory(ctx context.Context, id string) ([]HistoryEntry, error) {
	var raw []struct {
		ID        string `json:"Id"`
		Created   int64
		CreatedBy string
		Size      int64
		Comment   string
		Tags      []string
	}
	if err := c.getJSON(ctx, "/images/"+url.PathEscape(id)+"/history", nil, &raw); err != nil {
		return nil, err
	}
	out := make([]HistoryEntry, 0, len(raw))
	for _, r := range raw {
		out = append(out, HistoryEntry{ID: r.ID, Created: r.Created, CreatedBy: r.CreatedBy, Size: r.Size, Comment: r.Comment, Tags: r.Tags})
	}
	return out, nil
}

// ImageSave streams images as a tar (the docker save format).
func (c *Client) ImageSave(ctx context.Context, refs ...string) (io.ReadCloser, error) {
	resp, err := c.request(ctx, http.MethodGet, "/images/get", url.Values{"names": refs}, nil)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// ImageLoad loads images from a tar made by save.
func (c *Client) ImageLoad(ctx context.Context, tarball io.Reader, progress func(string)) error {
	resp, err := c.request(ctx, http.MethodPost, "/images/load", url.Values{"quiet": {"0"}}, tarball)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return streamJSON(resp.Body, progress)
}

// Commit makes an image of a container's current state.
func (c *Client) Commit(ctx context.Context, id, ref, comment string) (string, error) {
	repo, tag := ref, "latest"
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		repo, tag = ref[:i], ref[i+1:]
	}
	q := url.Values{"container": {id}, "repo": {repo}, "tag": {tag}, "pause": {"1"}}
	if comment != "" {
		q.Set("comment", comment)
	}
	resp, err := c.request(ctx, http.MethodPost, "/commit", q, map[string]any{})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// Resources are the limits that change on a running container. Zero
// values are left as they are.
type Resources struct {
	NanoCPUs          int64  `json:"NanoCpus,omitempty"`
	CPUShares         int64  `json:"CpuShares,omitempty"`
	Memory            int64  `json:"Memory,omitempty"`
	MemorySwap        int64  `json:"MemorySwap,omitempty"`
	MemoryReservation int64  `json:"MemoryReservation,omitempty"`
	PidsLimit         int64  `json:"PidsLimit,omitempty"`
	Restart           string `json:"-"`
}

// Update changes the limits (and restart policy) of a container in place.
func (c *Client) Update(ctx context.Context, id string, r Resources) ([]string, error) {
	body := map[string]any{}
	b, _ := json.Marshal(r)
	_ = json.Unmarshal(b, &body)
	if r.Restart != "" {
		body["RestartPolicy"] = map[string]any{"Name": r.Restart}
	}
	resp, err := c.request(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/update", nil, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct{ Warnings []string }
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out.Warnings, nil
}

// EventsBetween returns the newest events (up to 5000) of a time window
// (Unix seconds), optionally narrowed by filters (container=…, type=…) and
// by keep.
func (c *Client) EventsBetween(ctx context.Context, since, until int64, filters map[string][]string, keep func(Event) bool) ([]Event, error) {
	q := url.Values{"since": {fmt.Sprint(since)}, "until": {fmt.Sprint(until)}}
	if len(filters) > 0 {
		f, _ := json.Marshal(filters)
		q.Set("filters", string(f))
	}
	resp, err := c.request(ctx, http.MethodGet, "/events", q, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	var out []Event
	for {
		var e Event
		if err := dec.Decode(&e); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return out, err
		}
		if keep != nil && !keep(e) {
			continue
		}
		out = append(out, e)
		if len(out) > 10000 {
			out = append(out[:0], out[len(out)-5000:]...)
		}
	}
	if len(out) > 5000 {
		out = out[len(out)-5000:]
	}
	return out, nil
}

// ImageDigests are the registry digests an image was pulled with (empty
// for images built or loaded here).
func (c *Client) ImageDigests(ctx context.Context, ref string) ([]string, error) {
	var out struct{ RepoDigests []string }
	err := c.getJSON(ctx, "/images/"+ref+"/json", nil, &out)
	return out.RepoDigests, err
}

// UpdatePodman changes the limits of a Podman container through its own
// API (Podman's Docker-compatible API has no update). The restart policy
// is only sent when restart is set (Podman 5.1 or newer apply it).
func (c *Client) UpdatePodman(ctx context.Context, id string, r Resources) error {
	body := map[string]any{}
	if r.NanoCPUs > 0 {
		body["cpu"] = map[string]any{"quota": r.NanoCPUs / 10000, "period": 100000}
	}
	mem := map[string]any{}
	if r.Memory > 0 {
		mem["limit"] = r.Memory
		if r.MemorySwap > 0 {
			mem["swap"] = r.MemorySwap
		}
	}
	if r.MemoryReservation > 0 {
		mem["reservation"] = r.MemoryReservation
	}
	if len(mem) > 0 {
		body["memory"] = mem
	}
	if r.PidsLimit > 0 {
		body["pids"] = map[string]any{"limit": r.PidsLimit}
	}
	q := url.Values{}
	if r.Restart != "" {
		q.Set("restartPolicy", r.Restart)
	}
	resp, err := c.request(ctx, http.MethodPost, "/v4.0.0/libpod/containers/"+url.PathEscape(id)+"/update", q, body)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// RestartPolicy is the restart policy a container has now.
func (c *Client) RestartPolicy(ctx context.Context, id string) (string, error) {
	var i struct {
		HostConfig struct{ RestartPolicy struct{ Name string } }
	}
	err := c.getJSON(ctx, "/containers/"+url.PathEscape(id)+"/json", nil, &i)
	return i.HostConfig.RestartPolicy.Name, err
}
