package server

// Port-forward: a local TCP port (127.0.0.1 only) that leads to a port of
// a pod, or of a Service (a ready pod behind it, chosen again for each
// connection). Like kubectl port-forward, any program of this computer
// can use the port while it is open.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"gantry/internal/applog"
	"gantry/internal/kube"
)

// pfInfo is what the screens see of a port-forward.
type pfInfo struct {
	ID      string `json:"id"`
	Cluster string `json:"cluster"`
	NS      string `json:"ns"`
	Kind    string `json:"kind"` // pods, services
	Name    string `json:"name"`
	Port    int    `json:"port"`
	Local   int    `json:"local"`
	Created int64  `json:"created"`
	Conns   int32  `json:"conns"` // open now
	Total   int64  `json:"total"` // since it was opened
	LastErr string `json:"lastError,omitempty"`
}

type portForward struct {
	pfInfo
	conns atomic.Int32
	total atomic.Int64

	k   *kube.Client
	ln  net.Listener
	mu  sync.Mutex
	cs  map[net.Conn]bool
	end context.CancelFunc
}

var (
	pfMu sync.Mutex
	pfs  = map[string]*portForward{}
)

func (f *portForward) close() {
	f.end()
	f.ln.Close()
	f.mu.Lock()
	for c := range f.cs {
		c.Close()
	}
	f.mu.Unlock()
}

func (f *portForward) serve(ctx context.Context) {
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		go func() { defer applog.Recover("server/kubepf.go:75"); f.handle(ctx, c) }()
	}
}

func (f *portForward) handle(ctx context.Context, c net.Conn) {
	f.conns.Add(1)
	f.total.Add(1)
	f.mu.Lock()
	f.cs[c] = true
	f.mu.Unlock()
	defer func() {
		c.Close()
		f.mu.Lock()
		delete(f.cs, c)
		f.mu.Unlock()
		f.conns.Add(-1)
	}()
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	pod, port := f.Name, f.Port
	var err error
	if f.Kind == "services" {
		pod, port, err = f.k.ServiceTarget(dctx, f.NS, f.Name, f.Port)
	}
	var pc *kube.PortConn
	if err == nil {
		pc, err = f.k.PortForward(dctx, f.NS, pod, port)
	}
	cancel()
	if err != nil {
		f.setErr(err.Error())
		return
	}
	defer pc.Close()
	// The local program may stop sending before the answer comes (it
	// half-closes): the pod's side goes on until it ends, for up to a minute.
	up, down := make(chan struct{}), make(chan struct{})
	go func() { defer applog.Recover("server/kubepf.go:111"); _, _ = io.Copy(pc, c); close(up) }()
	go func() { defer applog.Recover("server/kubepf.go:112"); _, _ = io.Copy(c, pc); close(down) }()
	select {
	case <-down:
	case <-ctx.Done():
	case <-up:
		select {
		case <-down:
		case <-ctx.Done():
		case <-time.After(time.Minute):
		}
	}
	if e := pc.Err(); e != "" {
		f.setErr(e)
	}
}

func (f *portForward) setErr(s string) {
	f.mu.Lock()
	f.LastErr = s
	f.mu.Unlock()
	log.Printf("port-forward %s/%s:%d: %s", f.NS, f.Name, f.Port, s)
}

func (f *portForward) view() pfInfo {
	f.mu.Lock()
	v := f.pfInfo
	f.mu.Unlock()
	v.Conns, v.Total = f.conns.Load(), f.total.Load()
	return v
}

// clusterForward opens a port-forward.
func (s *Server) clusterForward(w http.ResponseWriter, r *http.Request, c clusterRec, k *kube.Client) {
	if c.Mode == "leitura" {
		writeErr(w, http.StatusForbidden, errors.New("este cluster está em modo só leitura: encaminhar portas não é permitido"))
		return
	}
	if !guard(w, r, c) {
		return
	}
	var req struct {
		NS    string `json:"ns"`
		Kind  string `json:"kind"`
		Name  string `json:"name"`
		Port  int    `json:"port"`
		Local int    `json:"local"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil || req.NS == "" || req.Name == "" || req.Port <= 0 || req.Port > 65535 || req.Local < 0 || req.Local > 65535 {
		writeErr(w, http.StatusBadRequest, errors.New("informe o pod ou Service e a porta"))
		return
	}
	if req.Kind != "services" {
		req.Kind = "pods"
	}
	// Check the target now, so a typo is an error here and not later.
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if req.Kind == "services" {
		if _, _, err := k.ServiceTarget(ctx, req.NS, req.Name, req.Port); err != nil {
			writeErr(w, kubeStatus(err), err)
			return
		}
	} else {
		pk, _ := kube.KindByID("pods")
		if err := k.Get(ctx, pk.Path(req.NS, req.Name), nil, &map[string]any{}); err != nil {
			writeErr(w, kubeStatus(err), err)
			return
		}
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", req.Local))
	if err != nil && req.Local != 0 {
		writeErr(w, http.StatusConflict, fmt.Errorf("a porta %d desta máquina já está em uso; escolha outra ou deixe em branco", req.Local))
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	fctx, end := context.WithCancel(context.Background())
	f := &portForward{pfInfo: pfInfo{ID: hex.EncodeToString(b), Cluster: c.ID, NS: req.NS, Kind: req.Kind, Name: req.Name, Port: req.Port,
		Local: ln.Addr().(*net.TCPAddr).Port, Created: time.Now().Unix()}, k: k, ln: ln, cs: map[net.Conn]bool{}, end: end}
	pfMu.Lock()
	pfs[f.ID] = f
	pfMu.Unlock()
	go func() { defer applog.Recover("server/kubepf.go:198"); f.serve(fctx) }()
	writeJSON(w, f.view())
}

func (s *Server) listForwards(w http.ResponseWriter, r *http.Request) {
	cluster := r.URL.Query().Get("cluster")
	pfMu.Lock()
	out := []pfInfo{}
	for _, f := range pfs {
		if cluster == "" || f.Cluster == cluster {
			out = append(out, f.view())
		}
	}
	pfMu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Created < out[j].Created })
	writeJSON(w, out)
}

func (s *Server) closeForward(w http.ResponseWriter, r *http.Request) {
	pfMu.Lock()
	f, ok := pfs[r.PathValue("fid")]
	delete(pfs, r.PathValue("fid"))
	pfMu.Unlock()
	if !ok {
		writeErr(w, http.StatusNotFound, errors.New("encaminhamento não encontrado"))
		return
	}
	f.close()
	writeJSON(w, map[string]bool{"ok": true})
}

// closeForwards stops the port-forwards of a cluster (removed, or its
// client changed).
func closeForwards(clusterID string, keep *kube.Client) {
	pfMu.Lock()
	defer pfMu.Unlock()
	for id, f := range pfs {
		if f.Cluster == clusterID && f.k != keep {
			f.close()
			delete(pfs, id)
		}
	}
}
