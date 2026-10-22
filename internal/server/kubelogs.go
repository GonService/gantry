package server

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"gantry/internal/i18n"
	"gantry/internal/kube"
)

// Following the log of a pod: the API server (or a load balancer, a proxy,
// the kubelet rotating the log file…) may end the stream while the
// container goes on. Then the log is read again from the last line seen,
// and a container that restarted is followed in its new run. The log only
// ends when the pod is gone or finished.

const (
	logRetryPause = 2 * time.Second
	logMaxFails   = 6
)

func (s *Server) followPodLogs(w http.ResponseWriter, r *http.Request, k *kube.Client, o kube.LogOptions) {
	send, ok := startSSE(w)
	if !ok {
		return
	}
	ctx := r.Context()
	f := &podLogFollower{
		k: k, ns: r.PathValue("ns"), pod: r.PathValue("pod"), o: o,
		out:    newLineFilter(b64Writer{send}, r.URL.Query().Get("grep")),
		notice: func(msg string) { b64Writer{send}.Write([]byte("\x1b[2m— " + i18n.T(msg) + " —\x1b[0m\n")) },
	}
	msg := f.run(ctx)
	if ctx.Err() == nil {
		sendEnd(send, msg)
	}
}

type podLogFollower struct {
	k       *kube.Client
	ns, pod string
	o       kube.LogOptions
	out     io.Writer
	notice  func(string)

	showTS bool
	last   time.Time // timestamp of the last line sent
	atLast int       // lines sent with that very timestamp
}

func (f *podLogFollower) run(ctx context.Context) string {
	f.showTS = f.o.Timestamps
	f.o.Timestamps = true // to know where to pick up again
	st, _ := f.k.ContainerStateOf(ctx, f.ns, f.pod, f.o.Container)
	cid := st.ID
	first := true
	fails := 0
	waiting := false
	for {
		started := time.Now()
		sent := 0
		rc, err := f.k.Logs(ctx, f.ns, f.pod, f.o)
		if err != nil && first {
			return podLogHint(err)
		}
		first = false
		if err == nil {
			sent, err = f.copy(rc)
			rc.Close()
		}
		if ctx.Err() != nil {
			return ""
		}
		if sent > 0 {
			fails = 0
			waiting = false
		}
		// Why did it end? Ask the pod.
		for {
			st, serr := f.k.ContainerStateOf(ctx, f.ns, f.pod, f.o.Container)
			if serr != nil {
				var ae *kube.APIError
				if errors.As(serr, &ae) && ae.Status == http.StatusNotFound {
					return "o pod foi apagado"
				}
				fails++
				if fails >= logMaxFails {
					return "perdi a conexão com o cluster: " + serr.Error()
				}
				if !sleepOrDone(ctx, logRetryPause*time.Duration(fails)) {
					return ""
				}
				continue
			}
			if st.ID != "" && cid != "" && st.ID != cid {
				// A new run of the container: its log from the start.
				f.notice("o container reiniciou")
				cid = st.ID
				f.o.SinceTime, f.o.Since, f.o.Tail = "", 0, -1
				f.last, f.atLast = time.Time{}, 0
				waiting = false
			}
			if cid == "" {
				cid = st.ID
			}
			if st.PodDone || st.Over {
				return "o container terminou"
			}
			if st.State == "running" {
				break
			}
			if !waiting {
				msg := "o container parou"
				if st.Reason != "" {
					msg += " (" + st.Reason + ")"
				}
				f.notice(msg + "; esperando ele voltar")
				waiting = true
			}
			if !sleepOrDone(ctx, logRetryPause) {
				return ""
			}
		}
		if err != nil || sent == 0 {
			if err != nil {
				fails++
				if fails >= logMaxFails {
					return "perdi a conexão com o cluster: " + err.Error()
				}
			}
			// A stream that ends at once and empty: not in a tight loop.
			if time.Since(started) < time.Second && !sleepOrDone(ctx, logRetryPause) {
				return ""
			}
		}
		// The same run goes on: read again from the last line seen.
		if !f.last.IsZero() {
			f.o.SinceTime = f.last.UTC().Format(time.RFC3339Nano)
			f.o.Since, f.o.Tail = 0, -1
		}
	}
}

// copy sends the lines of one stream, skipping the ones already sent
// (a stream read again from a timestamp repeats that second's lines).
func (f *podLogFollower) copy(rc io.Reader) (int, error) {
	br := bufio.NewReaderSize(rc, 64<<10)
	skipBefore, skipAt := f.last, f.atLast
	n := 0
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			t, rest := splitLogTS(line)
			send := true
			if !t.IsZero() && !skipBefore.IsZero() {
				switch {
				case t.Before(skipBefore):
					send = false
				case t.Equal(skipBefore) && skipAt > 0:
					skipAt--
					send = false
				}
			}
			if send {
				out := rest
				if f.showTS || t.IsZero() {
					out = line
				}
				if _, werr := f.out.Write(out); werr != nil {
					return n, werr
				}
				n++
				if !t.IsZero() {
					if t.Equal(f.last) {
						f.atLast++
					} else {
						f.last, f.atLast = t, 1
					}
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return n, nil
			}
			return n, err
		}
	}
}

// splitLogTS splits the timestamp the API puts at the start of each line.
func splitLogTS(line []byte) (time.Time, []byte) {
	i := bytes.IndexByte(line, ' ')
	if i < 20 || i > 40 {
		return time.Time{}, line
	}
	t, err := time.Parse(time.RFC3339Nano, string(line[:i]))
	if err != nil {
		return time.Time{}, line
	}
	return t, line[i+1:]
}

func sleepOrDone(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
