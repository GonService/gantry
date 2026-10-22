package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gantry/internal/i18n"
	"gantry/internal/kube"
)

// Logs of many pods at once (as stern does): the pods of a workload, of a
// service or of a label selector, each line with the pod's name in its
// color. New pods join while it runs; pods that go say so.

const multiLogMax = 30

var multiLogPoll = 3 * time.Second

// multiColors: ANSI colors readable on the dark terminal.
var multiColors = []string{"36", "33", "35", "32", "34", "91", "96", "93", "95", "92", "94", "31"}

// prefixWriter puts the pod's name in front of each whole line and writes
// the line in one piece (the streams of the pods run at the same time).
type prefixWriter struct {
	mu     *sync.Mutex
	out    io.Writer
	prefix []byte
	buf    []byte
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	p.buf = append(p.buf, b...)
	var chunk []byte
	for {
		i := bytes.IndexByte(p.buf, '\n')
		if i < 0 {
			break
		}
		chunk = append(chunk, p.prefix...)
		chunk = append(chunk, p.buf[:i+1]...)
		p.buf = p.buf[i+1:]
	}
	if len(p.buf) > 64<<10 { // a huge line without newline: out now
		chunk = append(append(append(chunk, p.prefix...), p.buf...), '\n')
		p.buf = nil
	}
	if len(chunk) == 0 {
		return len(b), nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, err := p.out.Write(chunk); err != nil {
		return 0, err
	}
	return len(b), nil
}

type multiStream struct {
	pod, container string
	cancel         context.CancelFunc
}

func (s *Server) clusterMultiLogs(w http.ResponseWriter, r *http.Request, c clusterRec, k *kube.Client) {
	ns := r.PathValue("ns")
	q := r.URL.Query()
	ctx := r.Context()
	sel := q.Get("selector")
	what := sel
	if sel == "" {
		kind, ok := kube.KindByID(r.PathValue("kind"))
		if !ok {
			writeErr(w, http.StatusBadRequest, errors.New("tipo desconhecido"))
			return
		}
		var err error
		sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		sel, err = k.SelectorOf(sctx, kind, ns, r.PathValue("name"))
		cancel()
		if err != nil {
			writeErr(w, kubeStatus(err), err)
			return
		}
		what = r.PathValue("name")
	}
	only := map[string]bool{}
	for _, p := range strings.Split(q.Get("pods"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			only[p] = true
		}
	}
	o := podLogOptions(r, true)
	o.Follow = true
	ctr := q.Get("container")
	send, ok := startSSE(w)
	if !ok {
		return
	}
	var mu sync.Mutex
	out := newLineFilter(b64Writer{send}, q.Get("grep"))
	note := func(msg string) {
		mu.Lock()
		defer mu.Unlock()
		out.Write([]byte("\x1b[2m— " + msg + " —\x1b[0m\n"))
	}
	streams := map[string]*multiStream{}
	colors := map[string]string{}
	var wg sync.WaitGroup
	defer func() {
		for _, st := range streams {
			st.cancel()
		}
		wg.Wait()
	}()
	start := func(pod kube.PodRef, first bool) {
		cts := pod.Containers
		if ctr != "" {
			cts = []string{ctr}
		}
		col, ok := colors[pod.Name]
		if !ok {
			col = multiColors[len(colors)%len(multiColors)]
			colors[pod.Name] = col
		}
		for _, ct := range cts {
			key := pod.Name + "/" + ct
			if streams[key] != nil {
				continue
			}
			label := pod.Name
			if len(pod.Containers) > 1 && ctr == "" {
				label += "/" + ct
			}
			pw := &prefixWriter{mu: &mu, out: out, prefix: []byte("\x1b[" + col + "m" + label + "\x1b[0m │ ")}
			sctx, cancel := context.WithCancel(ctx)
			streams[key] = &multiStream{pod: pod.Name, container: ct, cancel: cancel}
			po := o
			po.Container = ct
			if !first {
				// A pod that came later: its log from the start.
				po.Tail, po.Since, po.SinceTime = -1, 0, ""
			}
			f := &podLogFollower{k: k, ns: ns, pod: pod.Name, o: po, out: pw,
				notice: func(msg string) { pw.Write([]byte("\x1b[2m— " + i18n.T(msg) + " —\x1b[0m\n")) }}
			wg.Add(1)
			go func() {
				defer wg.Done()
				if msg := f.run(sctx); msg != "" && sctx.Err() == nil {
					pw.Write([]byte("\x1b[2m— " + i18n.T(msg) + " —\x1b[0m\n"))
				}
			}()
		}
	}
	first := true
	var lastTotal int
	for {
		lctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		pods, err := k.PodsBySelector(lctx, ns, sel)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			if first {
				sendEnd(send, err.Error())
				return
			}
		} else {
			if len(only) > 0 {
				var keep []kube.PodRef
				for _, p := range pods {
					if only[p.Name] {
						keep = append(keep, p)
					}
				}
				pods = keep
			}
			total := len(pods)
			if len(pods) > multiLogMax {
				pods = pods[:multiLogMax]
			}
			if first {
				if total == 0 {
					note(i18n.T(fmt.Sprintf("nenhum pod de %s por enquanto; esperando", what)))
				} else {
					names := make([]string, 0, len(pods))
					for _, p := range pods {
						names = append(names, p.Name)
					}
					note(i18n.T(fmt.Sprintf("logs de %d pods: %s", len(pods), strings.Join(names, ", "))))
				}
			}
			if total > multiLogMax && total != lastTotal {
				note(i18n.T(fmt.Sprintf("mostrando %d de %d pods; escolha quais nos filtros", multiLogMax, total)))
			}
			lastTotal = total
			now := map[string]bool{}
			for _, p := range pods {
				now[p.Name] = true
				known := false
				for _, st := range streams {
					if st.pod == p.Name {
						known = true
						break
					}
				}
				if !known {
					if !first {
						note(i18n.T(fmt.Sprintf("entrou o pod %s", p.Name)))
					}
					start(p, first)
				}
			}
			var gone []string
			for key, st := range streams {
				if !now[st.pod] {
					st.cancel()
					delete(streams, key)
					gone = append(gone, st.pod)
				}
			}
			sort.Strings(gone)
			for i, g := range gone {
				if i == 0 || gone[i-1] != g {
					note(i18n.T(fmt.Sprintf("saiu o pod %s", g)))
				}
			}
		}
		first = false
		if !sleepOrDone(ctx, multiLogPoll) {
			return
		}
	}
}

// clusterMultiPods lists the pods a multi log would follow (for the filter).
func (s *Server) clusterMultiPods(w http.ResponseWriter, r *http.Request, c clusterRec, k *kube.Client) {
	ns := r.PathValue("ns")
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	sel := r.URL.Query().Get("selector")
	if sel == "" {
		kind, ok := kube.KindByID(r.PathValue("kind"))
		if !ok {
			writeErr(w, http.StatusBadRequest, errors.New("tipo desconhecido"))
			return
		}
		var err error
		if sel, err = k.SelectorOf(ctx, kind, ns, r.PathValue("name")); err != nil {
			writeErr(w, kubeStatus(err), err)
			return
		}
	}
	pods, err := k.PodsBySelector(ctx, ns, sel)
	if err != nil {
		writeErr(w, kubeStatus(err), err)
		return
	}
	writeJSON(w, map[string]any{"selector": sel, "pods": pods, "max": strconv.Itoa(multiLogMax)})
}
