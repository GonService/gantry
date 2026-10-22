package agent

// Commands over the agent connection: the app runs the shell lines it would
// run over SSH (vmctl, compose, tail -f of a capture…) through the agent
// instead, so they do not use SSH sessions (the server allows only a few
// per connection, MaxSessions, 10 by default) and work over the direct TLS
// connection too.
//
// The request is "GET /v1/exec" with "Upgrade: gantry-exec"; after the 101
// both sides send frames: a type byte, a 4-byte big-endian length, the
// data. The app sends the start (JSON), then stdin data and its end; the
// agent sends stdout, stderr and, last, the exit status.

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

const (
	frameStart  byte = 'S' // app → agent: execStart (JSON)
	frameStdin  byte = 'I' // app → agent: stdin data
	frameEOF    byte = 'E' // app → agent: stdin closed
	frameStdout byte = 'O' // agent → app
	frameStderr byte = 'R' // agent → app
	frameExit   byte = 'X' // agent → app: exit code (decimal), then " message" on a failure to start
)

const maxFrame = 1 << 20

type execStart struct {
	Line string   `json:"line"`          // run by sh -c
	Dir  string   `json:"dir,omitempty"` // working folder
	Env  []string `json:"env,omitempty"` // KEY=value added to the agent's
}

func writeFrame(w io.Writer, t byte, data []byte) error {
	var hdr [5]byte
	hdr[0] = t
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(data)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(data)
	return err
}

func readFrame(r io.Reader) (byte, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > maxFrame {
		return 0, nil, errors.New("quadro grande demais")
	}
	data := make([]byte, n)
	_, err := io.ReadFull(r, data)
	return hdr[0], data, err
}

// frameWriter sends what is written as frames of one type, one at a time
// on the shared connection.
type frameWriter struct {
	mu *sync.Mutex
	w  io.Writer
	t  byte
}

func (f frameWriter) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	total := len(p)
	for len(p) > 0 {
		n := len(p)
		if n > 64<<10 {
			n = 64 << 10
		}
		if err := writeFrame(f.w, f.t, p[:n]); err != nil {
			return 0, err
		}
		p = p[n:]
	}
	return total, nil
}

// exec serves /v1/exec: it takes over the connection and runs the command.
func (a *Agent) exec(w http.ResponseWriter, r *http.Request) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "gantry-exec") {
		http.Error(w, "use Upgrade: gantry-exec", http.StatusBadRequest)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "conexão sem hijack", http.StatusInternalServerError)
		return
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: gantry-exec\r\nConnection: Upgrade\r\n\r\n")
	if err := rw.Flush(); err != nil {
		return
	}
	var mu sync.Mutex
	out := bufio.NewWriter(conn)
	send := func(t byte, data []byte) {
		mu.Lock()
		defer mu.Unlock()
		_ = writeFrame(out, t, data)
		_ = out.Flush()
	}
	t, data, err := readFrame(rw)
	if err != nil || t != frameStart {
		send(frameExit, []byte("127 "+"esperava o início do comando"))
		return
	}
	var st execStart
	if err := json.Unmarshal(data, &st); err != nil || st.Line == "" {
		send(frameExit, []byte("127 "+"comando inválido"))
		return
	}
	// The command dies with the connection (the app closed it or went away).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stdinR, stdinW := io.Pipe()
	go func() {
		defer cancel()
		for {
			t, data, err := readFrame(rw)
			if err != nil {
				stdinW.CloseWithError(io.EOF)
				return
			}
			switch t {
			case frameStdin:
				if _, err := stdinW.Write(data); err != nil {
					return
				}
			case frameEOF:
				stdinW.Close()
			}
		}
	}()
	code, msg := runShell(ctx, st, stdinR, frameWriterFunc(send, frameStdout), frameWriterFunc(send, frameStderr))
	stdinR.Close()
	exit := strconv.Itoa(code)
	if msg != "" {
		exit += " " + msg
	}
	send(frameExit, []byte(exit))
}

type sendWriter struct {
	send func(byte, []byte)
	t    byte
}

func (s sendWriter) Write(p []byte) (int, error) {
	for off := 0; off < len(p); off += 64 << 10 {
		end := off + 64<<10
		if end > len(p) {
			end = len(p)
		}
		s.send(s.t, append([]byte(nil), p[off:end]...))
	}
	return len(p), nil
}

func frameWriterFunc(send func(byte, []byte), t byte) io.Writer { return sendWriter{send, t} }

// ErrUnreachable marks an Exec that never started the command: the caller
// may run it some other way.
var ErrUnreachable = errors.New("agente inacessível")

func unreachable(err error) error { return fmt.Errorf("%w: %v", ErrUnreachable, err) }

// Exec runs a shell line on the agent's machine: stdin (nil for none) goes
// to it, its output to stdout and stderr; it returns the exit code. An
// error means the command could not run (no agent, connection lost).
func (c *Client) Exec(ctx context.Context, line, dir string, env []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if c.dial == nil {
		return -1, unreachable(errors.New("agente sem conexão"))
	}
	conn, err := c.dial(ctx)
	if err != nil {
		return -1, unreachable(err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if _, err := io.WriteString(conn, "GET /v1/exec HTTP/1.1\r\nHost: agent\r\nConnection: Upgrade\r\nUpgrade: gantry-exec\r\n\r\n"); err != nil {
		return -1, unreachable(err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		return -1, unreachable(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		resp.Body.Close()
		return -1, unreachable(fmt.Errorf("agente: %s %s", resp.Status, strings.TrimSpace(string(b))))
	}
	var mu sync.Mutex
	start, _ := json.Marshal(execStart{Line: line, Dir: dir, Env: env})
	if err := writeFrame(conn, frameStart, start); err != nil {
		return -1, unreachable(err)
	}
	go func() {
		w := frameWriter{&mu, conn, frameStdin}
		if stdin != nil {
			_, _ = io.Copy(w, stdin)
		}
		mu.Lock()
		_ = writeFrame(conn, frameEOF, nil)
		mu.Unlock()
	}()
	for {
		t, data, err := readFrame(br)
		if err != nil {
			if ctx.Err() != nil {
				return -1, ctx.Err()
			}
			return -1, fmt.Errorf("agente: a conexão caiu durante o comando: %v", err)
		}
		switch t {
		case frameStdout:
			if stdout != nil {
				_, _ = stdout.Write(data)
			}
		case frameStderr:
			if stderr != nil {
				_, _ = stderr.Write(data)
			}
		case frameExit:
			s := string(data)
			num, msg, _ := strings.Cut(s, " ")
			code, err := strconv.Atoi(num)
			if err != nil {
				return -1, fmt.Errorf("agente: fim inválido %q", s)
			}
			if msg != "" {
				return code, errors.New(msg)
			}
			return code, nil
		}
	}
}
