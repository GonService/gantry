//go:build linux || darwin

package ptyx

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// A command on the pseudo-terminal sees a tty with the size given, reads
// what is typed and follows a resize.
func TestStart(t *testing.T) {
	s, err := Start(exec.Command("sh", "-c", "tty >/dev/null && echo TTY; stty size; read l; echo got:$l; sleep 0.3; stty size"), 100, 30)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var out bytes.Buffer
	done := make(chan struct{})
	go func() { _, _ = io.Copy(&out, s); close(done) }()
	time.Sleep(200 * time.Millisecond)
	_, _ = s.Write([]byte("oi\n"))
	_ = s.Resize(context.Background(), 120, 40)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
	got := strings.ReplaceAll(out.String(), "\r", "")
	for _, want := range []string{"TTY\n", "30 100\n", "got:oi\n", "40 120\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("sem %q em %q", want, got)
		}
	}
}
