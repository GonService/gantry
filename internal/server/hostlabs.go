package server

import (
	"context"
	"encoding/json"
	"path"
	"slices"
	"sync"
	"time"

	"gantry/internal/hosts"
	"gantry/internal/labs"
	"gantry/internal/vms"
)

// Projects with VMs that another Gantry made on the machine (the one
// installed on it, or the Gantry of a colleague) have no containers with
// Compose labels to give them away: this one only knew the projects it
// made itself. Now it asks the machine which projects have VMs and where
// their compose files are, and adopts the ones it does not know.

const hostLabsEvery = time.Minute

var hostLabsSeen = struct {
	sync.Mutex
	at map[string]time.Time
}{at: map[string]time.Time{}}

// adoptHostLabs looks, at most once a minute per host, for projects with VMs
// on the machine that this Gantry does not know, and remembers them.
func (s *Server) adoptHostLabs(ctx context.Context, h *hosts.Host) {
	if h.Kind == hosts.TCP {
		return
	}
	hostLabsSeen.Lock()
	if time.Since(hostLabsSeen.at[h.Name]) < hostLabsEvery {
		hostLabsSeen.Unlock()
		return
	}
	hostLabsSeen.at[h.Name] = time.Now()
	hostLabsSeen.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ps, states, err := vms.Projects(ctx, h)
	if err != nil || len(ps) == 0 {
		return
	}
	known := s.store.Known(h.Name)
	var others []map[string]map[string]labs.Known // the state.json of the machine's own Gantry
	loaded := false
	for _, p := range ps {
		if _, ok := known[p.Name]; ok {
			continue
		}
		k := labs.Known{Files: p.Files, VMs: p.VMs}
		if len(k.Files) == 0 {
			if !loaded {
				loaded = true
				for _, f := range states {
					b, err := h.ReadFile(ctx, f)
					if err != nil {
						continue
					}
					var st map[string]map[string]labs.Known
					if json.Unmarshal(b, &st) == nil {
						others = append(others, st)
					}
				}
			}
			if o, ok := fromOtherGantry(ctx, h, others, p.Name); ok {
				k.Files, k.Runtimes, k.Used = o.Files, o.Runtimes, o.Used
				for _, v := range o.VMs {
					if !slices.Contains(k.VMs, v) {
						k.VMs = append(k.VMs, v)
					}
				}
			}
		}
		// Without its compose file the project cannot be opened: it stays
		// out until the machine says where the file is.
		if len(k.Files) == 0 || !h.Exists(ctx, k.Files[0]) {
			continue
		}
		k.Dir = path.Dir(k.Files[0])
		s.store.Adopt(h.Name, p.Name, k)
	}
}

// fromOtherGantry finds the project in the state.json of a Gantry of the
// machine. That Gantry may know projects of other machines with the same
// name: the one whose compose file is on this machine wins.
func fromOtherGantry(ctx context.Context, h *hosts.Host, states []map[string]map[string]labs.Known, project string) (labs.Known, bool) {
	for _, st := range states {
		for _, byLab := range st {
			k, ok := byLab[project]
			if !ok || len(k.Files) == 0 {
				continue
			}
			if h.Exists(ctx, k.Files[0]) {
				return k, true
			}
		}
	}
	return labs.Known{}, false
}
