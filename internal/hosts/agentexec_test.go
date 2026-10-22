//go:build !windows

package hosts

import (
	"context"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"gantry/internal/agent"
)

func TestAgentExec(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "a.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: agent.New(agent.Options{Version: "t"}).Handler()}
	go srv.Serve(ln)
	defer srv.Close()
	h := &Host{Kind: SSH, Agent: agent.NewClient(func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	})}
	h.SetAgentExec(true)
	ctx := WithEnv(context.Background(), map[string]string{"GV_X": "a b"})

	out, err := h.run(ctx, t.TempDir(), []string{"sh", "-c", `echo "$GV_X"; cat`}, []byte("entrada"), nil)
	if err != nil || out != "a b\nentrada" {
		t.Fatalf("%q %v", out, err)
	}
	if _, err := h.run(ctx, "", []string{"sh", "-c", "echo falhou >&2; exit 2"}, nil, nil); err == nil || err.Error() != "falhou" {
		t.Fatal(err)
	}
	rc, wait, err := h.Pipe(context.Background(), "printf 'um\\ndois\\n'")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	if string(b) != "um\ndois\n" || wait() != nil {
		t.Fatalf("%q", b)
	}
	rc, wait, _ = h.Pipe(context.Background(), "echo ruim >&2; exit 1")
	io.Copy(io.Discard, rc)
	if err := wait(); err == nil || !strings.Contains(err.Error(), "ruim") {
		t.Fatal(err)
	}
	if err := h.RunStdin(context.Background(), "cat >&2; exit 0", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	// Without the agent answering, a command that never started falls back
	// (here, to an SSH that cannot run: the error comes from there).
	h2 := &Host{Kind: SSH, Agent: agent.NewClient(func(ctx context.Context) (net.Conn, error) { return nil, io.EOF })}
	h2.SetAgentExec(true)
	if _, _, ok := h2.agentRun(context.Background(), "true", "", nil, nil); ok {
		t.Fatal("deveria ir por SSH")
	}
	// Installing the agent goes over SSH.
	if h.useAgent(WithoutAgent(context.Background())) {
		t.Fatal("WithoutAgent")
	}
}
