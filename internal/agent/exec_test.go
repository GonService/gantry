//go:build !windows

package agent

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExec(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "a.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	a := New(Options{Version: "t"})
	srv := &http.Server{Handler: a.Handler()}
	go srv.Serve(ln)
	defer srv.Close()
	c := NewClient(func(ctx context.Context) (net.Conn, error) { var d net.Dialer; return d.DialContext(ctx, "unix", sock) })
	ctx := context.Background()

	var out, errb bytes.Buffer
	code, err := c.Exec(ctx, `echo "$X $(pwd)"; echo erro >&2; cat; exit 3`, os.TempDir(), []string{"X=oi"}, strings.NewReader("da entrada\n"), &out, &errb)
	if err != nil || code != 3 {
		t.Fatal(code, err)
	}
	if !strings.Contains(out.String(), "oi ") || !strings.Contains(out.String(), "da entrada") || errb.String() != "erro\n" {
		t.Fatalf("out %q err %q", out.String(), errb.String())
	}
	// A big output comes whole.
	out.Reset()
	if code, err := c.Exec(ctx, "head -c 3000000 /dev/zero", "", nil, nil, &out, nil); err != nil || code != 0 || out.Len() != 3000000 {
		t.Fatal(code, err, out.Len())
	}
	// Ending the context kills the command (and its children).
	mark := filepath.Join(t.TempDir(), "vivo")
	cctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = c.Exec(cctx, "sh -c 'sleep 30; touch "+mark+"'", "", nil, nil, nil, nil)
	if err == nil || time.Since(start) > 5*time.Second {
		t.Fatal("não parou", err)
	}
	time.Sleep(300 * time.Millisecond)
	// Many at once: no session limit.
	errs := make(chan error, 30)
	for i := 0; i < 30; i++ {
		go func() {
			var o bytes.Buffer
			_, err := c.Exec(ctx, "sleep 0.3; echo ok", "", nil, nil, &o, nil)
			if err == nil && o.String() != "ok\n" {
				err = os.ErrInvalid
			}
			errs <- err
		}()
	}
	for i := 0; i < 30; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(mark); err == nil {
		t.Fatal("o filho do comando cancelado continuou")
	}
}

func TestExecBackground(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "a.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: New(Options{Version: "t"}).Handler()}
	go srv.Serve(ln)
	defer srv.Close()
	c := NewClient(func(ctx context.Context) (net.Conn, error) { var d net.Dialer; return d.DialContext(ctx, "unix", sock) })
	// A program left behind with the output open does not hold the command.
	start := time.Now()
	var out bytes.Buffer
	code, err := c.Exec(context.Background(), "sleep 20 & echo ok; exit 4", "", nil, nil, &out, nil)
	if err != nil || code != 4 || out.String() != "ok\n" || time.Since(start) > 8*time.Second {
		t.Fatal(code, err, out.String(), time.Since(start))
	}
}
