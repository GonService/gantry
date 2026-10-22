package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"gantry/internal/applog"
	"gantry/internal/kube"
)

// The cluster keeps the log of a container only while its pod exists: a
// "Reiniciar" (rollout restart), a new version, a deleted pod or a node that
// went away, and the log is gone. Gantry saves it first, in its own folder:
//   - before its own Reiniciar and before deleting a pod;
//   - when the watch of pods (open while someone looks at the cluster) sees
//     a pod start to terminate, for any reason.
// The logs tab of a pod lists the saved pods of the same workload.

const (
	podLogKeep     = 7 * 24 * time.Hour
	podLogMaxTotal = 300 << 20 // all saved pods
	podLogMaxCtr   = 4 << 20   // one run of one container
	podLogTail     = 20000
)

type savedCtr struct {
	Name     string `json:"name"`
	Previous bool   `json:"previous,omitempty"` // the run before the last one
	Bytes    int64  `json:"bytes"`
	file     string
}

type savedPod struct {
	Pod        string     `json:"pod"`
	NS         string     `json:"ns"`
	Workload   string     `json:"workload"`
	Node       string     `json:"node,omitempty"`
	Saved      int64      `json:"saved"` // unix ms
	Containers []savedCtr `json:"containers"`
	dir        string
}

var (
	podLogMu   sync.Mutex
	podLogSeen = map[string]time.Time{} // pod uid -> saved at (no second copy)
)

func (s *Server) podLogDir(clusterID string) string {
	return filepath.Join(s.configDir(), "podlogs", podLogName(clusterID))
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]`)

func podLogName(s string) string {
	s = unsafeChars.ReplaceAllString(s, "_")
	if s == "" || s == "." || s == ".." {
		s = "_"
	}
	return s
}

// keepPodLogs saves the logs of a pod that is going away.
func (s *Server) keepPodLogs(ctx context.Context, clusterID string, k *kube.Client, p kube.LeavingPod) {
	if p.Name == "" || p.NS == "" {
		return
	}
	key := clusterID + "|" + p.UID
	if p.UID == "" {
		key = clusterID + "|" + p.NS + "/" + p.Name
	}
	podLogMu.Lock()
	if t, ok := podLogSeen[key]; ok && time.Since(t) < time.Hour {
		podLogMu.Unlock()
		return
	}
	podLogSeen[key] = time.Now()
	for k, t := range podLogSeen {
		if time.Since(t) > time.Hour {
			delete(podLogSeen, k)
		}
	}
	podLogMu.Unlock()

	dir := filepath.Join(s.podLogDir(clusterID), podLogName(p.NS), podLogName(p.Name))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	sp := savedPod{Pod: p.Name, NS: p.NS, Workload: p.Workload, Node: p.Node, Saved: time.Now().UnixMilli()}
	// What a former save of the same pod name (a StatefulSet's db-0) kept is
	// replaced: the saved pod is the one that just went.
	old, _ := filepath.Glob(filepath.Join(dir, "*.log"))
	for _, f := range old {
		os.Remove(f)
	}
	for _, c := range p.Containers {
		if !c.Started {
			continue
		}
		runs := []bool{false}
		if c.Restarts > 0 {
			runs = append(runs, true)
		}
		for _, prev := range runs {
			name := podLogName(c.Name)
			if prev {
				name += ".anterior"
			}
			f := filepath.Join(dir, name+".log")
			n, err := savePodLog(ctx, k, p, c.Name, prev, f)
			if err != nil || n == 0 {
				os.Remove(f)
				continue
			}
			sp.Containers = append(sp.Containers, savedCtr{Name: c.Name, Previous: prev, Bytes: n})
		}
	}
	if len(sp.Containers) == 0 {
		os.Remove(filepath.Join(dir, "pod.json"))
		os.Remove(dir)
		return
	}
	b, _ := json.Marshal(sp)
	_ = os.WriteFile(filepath.Join(dir, "pod.json"), b, 0o600)
	s.prunePodLogs()
}

func savePodLog(ctx context.Context, k *kube.Client, p kube.LeavingPod, ctr string, prev bool, path string) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	rc, err := k.Logs(ctx, p.NS, p.Name, kube.LogOptions{Container: ctr, Tail: podLogTail, Timestamps: true, Previous: prev, LimitBytes: podLogMaxCtr})
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	// The log may hold secrets like any log: it stays on this computer, in
	// Gantry's folder, readable only by the user.
	return io.Copy(f, io.LimitReader(rc, podLogMaxCtr))
}

// keepWorkloadLogs saves the logs of the pods of a workload (before a restart).
func (s *Server) keepWorkloadLogs(clusterID string, k *kube.Client, ns, workload string) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	pods, err := k.PodsOfWorkload(ctx, ns, workload)
	if err != nil {
		return
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for _, p := range pods {
		wg.Add(1)
		sem <- struct{}{}
		go func(p kube.LeavingPod) {
			defer applog.Recover("podlogs workload")
			defer wg.Done()
			defer func() { <-sem }()
			s.keepPodLogs(ctx, clusterID, k, p)
		}(p)
	}
	wg.Wait()
}

// watchPodLeaving makes the client's watch of pods save the pods that go.
func (s *Server) watchPodLeaving(clusterID string, k *kube.Client) {
	k.SetPodLeaving(func(p kube.LeavingPod) {
		defer applog.Recover("podlogs watch")
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		s.keepPodLogs(ctx, clusterID, k, p)
	})
}

// savedPods lists what is saved for a cluster, newest first.
func (s *Server) savedPods(clusterID string) []savedPod {
	var out []savedPod
	metas, _ := filepath.Glob(filepath.Join(s.podLogDir(clusterID), "*", "*", "pod.json"))
	for _, m := range metas {
		b, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		var sp savedPod
		if json.Unmarshal(b, &sp) != nil {
			continue
		}
		sp.dir = filepath.Dir(m)
		out = append(out, sp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Saved > out[j].Saved })
	return out
}

// prunePodLogs drops saves older than a week, and the oldest past the size cap.
func (s *Server) prunePodLogs() {
	root := filepath.Join(s.configDir(), "podlogs")
	clusters, _ := os.ReadDir(root)
	type entry struct {
		dir   string
		saved int64
		size  int64
	}
	var all []entry
	for _, c := range clusters {
		for _, sp := range s.savedPods(c.Name()) {
			var size int64
			for _, ct := range sp.Containers {
				size += ct.Bytes
			}
			all = append(all, entry{sp.dir, sp.Saved, size})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].saved > all[j].saved })
	var total int64
	for _, e := range all {
		total += e.size
		if time.Since(time.UnixMilli(e.saved)) > podLogKeep || total > podLogMaxTotal {
			os.RemoveAll(e.dir)
		}
	}
}

// clusterSavedPods: GET …/podlogs?ns=&pod= — the saved pods of the workload
// of that pod (which may be gone too).
func (s *Server) clusterSavedPods(w http.ResponseWriter, r *http.Request, c clusterRec, k *kube.Client) {
	ns, pod := r.URL.Query().Get("ns"), r.URL.Query().Get("pod")
	saved := s.savedPods(c.ID)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	workload, err := k.WorkloadOfPod(ctx, ns, pod)
	if err != nil {
		for _, sp := range saved {
			if sp.NS == ns && sp.Pod == pod {
				workload = sp.Workload
				break
			}
		}
	}
	out := []savedPod{}
	for _, sp := range saved {
		if sp.NS != ns || (workload != "" && sp.Workload != workload) || (workload == "" && sp.Pod != pod) {
			continue
		}
		out = append(out, sp)
	}
	writeJSON(w, map[string]any{"workload": workload, "pods": out})
}

var logStamp = regexp.MustCompile(`^\d{4}-\d\d-\d\dT[0-9:.]+(?:Z|[+-]\d\d:\d\d) `)

// clusterSavedLog: GET …/podlogs/{ns}/{pod}/log?container=&previous=1 — a
// saved log, as the live one (SSE), with the same filter and timestamps.
func (s *Server) clusterSavedLog(w http.ResponseWriter, r *http.Request, c clusterRec, k *kube.Client) {
	q := r.URL.Query()
	dir := filepath.Join(s.podLogDir(c.ID), podLogName(r.PathValue("ns")), podLogName(r.PathValue("pod")))
	var sp savedPod
	b, err := os.ReadFile(filepath.Join(dir, "pod.json"))
	if err == nil {
		err = json.Unmarshal(b, &sp)
	}
	send, ok := startSSE(w)
	if !ok {
		return
	}
	if err != nil {
		sendEnd(send, "o Gantry não guardou o log deste pod (ou ele já passou de uma semana)")
		return
	}
	ctr, prev := q.Get("container"), q.Get("previous") == "1"
	var pick *savedCtr
	for i := range sp.Containers {
		x := &sp.Containers[i]
		if (ctr == "" || x.Name == ctr) && x.Previous == prev {
			pick = x
			break
		}
	}
	if pick == nil {
		sendEnd(send, "não há log guardado deste container neste pod")
		return
	}
	name := podLogName(pick.Name)
	if pick.Previous {
		name += ".anterior"
	}
	f, err := os.Open(filepath.Join(dir, name+".log"))
	if err != nil {
		sendEnd(send, err.Error())
		return
	}
	defer f.Close()
	stamps := q.Get("timestamps") == "1"
	out := newLineFilter(b64Writer{send}, q.Get("grep"))
	br := bufio.NewReaderSize(f, 64<<10)
	var buf []byte
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			if !stamps {
				if m := logStamp.Find(line); m != nil {
					line = line[len(m):]
				}
			}
			buf = append(buf, line...)
			if len(buf) > 32<<10 {
				out.Write(buf)
				buf = buf[:0]
			}
		}
		if err != nil {
			break
		}
		if r.Context().Err() != nil {
			return
		}
	}
	if len(buf) > 0 {
		out.Write(buf)
	}
	sendEnd(send, "log guardado pelo Gantry em "+time.UnixMilli(sp.Saved).Format("02/01 15:04")+" (o pod não existe mais no cluster)")
}

// podLogHint turns the cluster's answers about a missing log into words.
func podLogHint(err error) string {
	msg := err.Error()
	var ae *kube.APIError
	switch {
	case strings.Contains(msg, "previous terminated container"):
		return "este container não reiniciou neste pod, então não há execução anterior. Se o pod foi recriado (Reiniciar, nova versão), o log dos pods que saíram está em Pods anteriores"
	case errors.As(err, &ae) && ae.Status == http.StatusNotFound && strings.Contains(msg, "pods"):
		return "o pod não existe mais (foi recriado ou apagado). O log que o Gantry guardou dele está em Pods anteriores"
	}
	return msg
}
