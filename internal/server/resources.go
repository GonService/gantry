package server

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"gantry/internal/engine"
	"gantry/internal/hosts"
	"gantry/internal/i18n"
)

// Images, volumes and networks of a host, with which containers use them.

type resourceView struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Detail  string   `json:"detail,omitempty"`
	Size    int64    `json:"size,omitempty"`
	Created int64    `json:"created,omitempty"`
	UsedBy  []string `json:"usedBy"`
	System  bool     `json:"system,omitempty"`
	Lab     string   `json:"lab,omitempty"`
}

var systemNetworks = map[string]bool{"bridge": true, "host": true, "none": true, "podman": true}

func (s *Server) listResources(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	eng, ok := s.reqEngine(w, r, h)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	cs, err := eng.Containers(ctx)
	if err != nil {
		writeErr(w, engineStatus(err), err)
		return
	}
	out := []resourceView{}
	switch kind := r.PathValue("kind"); kind {
	case "images":
		imgs, err := eng.Images(ctx)
		if err != nil {
			writeErr(w, engineStatus(err), err)
			return
		}
		for _, im := range imgs {
			used := []string{}
			for _, c := range cs {
				if strings.TrimPrefix(c.ImageID, "sha256:") == strings.TrimPrefix(im.ID, "sha256:") {
					used = append(used, c.Name())
				}
			}
			tags := []string{}
			for _, t := range im.RepoTags {
				if t != "<none>:<none>" {
					tags = append(tags, t)
				}
			}
			if len(tags) == 0 {
				out = append(out, resourceView{ID: im.ID, Name: i18n.T("<sem tag>"), Size: im.Size, Created: im.Created, UsedBy: used})
				continue
			}
			// One row per tag; removing a row removes that tag, and the
			// image goes away with its last tag.
			for _, t := range tags {
				v := resourceView{ID: t, Name: t, Size: im.Size, Created: im.Created, UsedBy: used}
				if id := strings.TrimPrefix(im.ID, "sha256:"); len(id) >= 12 && !strings.ContainsAny(id, "/:") {
					v.Detail = id[:12]
				}
				if len(tags) > 1 {
					v.Detail += " · " + strconv.Itoa(len(tags)) + " tags"
				}
				out = append(out, v)
			}
		}
	case "volumes":
		vols, err := eng.Volumes(ctx)
		if err != nil {
			writeErr(w, engineStatus(err), err)
			return
		}
		for _, vol := range vols {
			v := resourceView{ID: vol.Name, Name: vol.Name, Detail: vol.Mountpoint, UsedBy: []string{}, Lab: vol.Labels["com.docker.compose.project"]}
			if t, err := time.Parse(time.RFC3339, vol.CreatedAt); err == nil {
				v.Created = t.Unix()
			}
			for _, c := range cs {
				for _, m := range c.Mounts {
					if m.Type == "volume" && m.Name == vol.Name {
						v.UsedBy = append(v.UsedBy, c.Name())
						break
					}
				}
			}
			out = append(out, v)
		}
	case "networks":
		nets, err := eng.Networks(ctx)
		if err != nil {
			writeErr(w, engineStatus(err), err)
			return
		}
		for _, n := range nets {
			v := resourceView{ID: n.ID, Name: n.Name, Detail: n.Driver, UsedBy: []string{}, System: systemNetworks[n.Name], Lab: n.Labels["com.docker.compose.project"]}
			for _, ip := range n.IPAM.Config {
				if ip.Subnet != "" {
					v.Detail += " · " + ip.Subnet
				}
			}
			for _, c := range cs {
				if _, ok := c.NetworkSettings.Networks[n.Name]; ok {
					v.UsedBy = append(v.UsedBy, c.Name())
				}
			}
			out = append(out, v)
		}
	default:
		writeErr(w, http.StatusNotFound, errors.New("tipo desconhecido: "+kind))
		return
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, out)
}

func (s *Server) removeResource(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	eng, ok := s.reqEngine(w, r, h)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	id := r.PathValue("id")
	var err error
	switch r.PathValue("kind") {
	case "images":
		err = eng.RemoveImage(ctx, id, r.URL.Query().Get("force") == "1")
	case "volumes":
		err = eng.RemoveVolume(ctx, id)
	case "networks":
		err = eng.RemoveNetwork(ctx, id)
	default:
		err = errors.New("tipo desconhecido")
	}
	if err != nil {
		writeErr(w, engineStatus(err), err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) pruneResources(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	eng, ok := s.reqEngine(w, r, h)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	freed, n, err := eng.Prune(ctx, r.PathValue("kind"))
	if err != nil {
		var ae *engine.APIError
		if !errors.As(err, &ae) {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeErr(w, engineStatus(err), err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "removed": n, "freed": freed})
}
