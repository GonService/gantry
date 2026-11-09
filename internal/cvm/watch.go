package cvm

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Hot reload: virtiofs does not tell the machine when a file changes on
// the Mac, so watchers in containers (vite, nodemon, air…) see nothing.
// The app watches the shared folders with FSEvents and, for each change,
// sets the file's mtime in the machine to the value it already has: no
// real change, but the guest's kernel raises the inotify event the
// containers wait for. The echo of that (FSEvents sees the utime) is
// dropped because the mtime is the one just sent.

// touchLoop runs in the machine, reading "path<TAB>seconds.nanos" lines.
const touchLoop = `t=$(printf '\t'); while IFS= read -r l; do p=${l%"$t"*}; m=${l##*"$t"}; touch -c -m -d "@$m" -- "$p" 2>/dev/null; done`

// skipDirs are changes nobody waits for (and the machine's own files).
var skipDirs = []string{"/Library/", "/.git/", "/.Trash/", "/.cache/", "/.gantry/", "/node_modules/.cache/", "/.DS_Store", "/.Spotlight-V100/", "/.fseventsd/"}

func skipPath(p string) bool {
	for _, d := range skipDirs {
		if strings.Contains(p+"/", d) {
			return true
		}
	}
	return false
}

type watchState struct {
	stop  func()
	quit  chan struct{}
	mu    sync.Mutex
	queue map[string]bool
	sent  map[string]time.Time
}

var watches struct {
	sync.Mutex
	m map[string]*watchState
}

// Watching tells whether the changes of the Mac are going into the machine.
func Watching(name string) bool {
	watches.Lock()
	defer watches.Unlock()
	return watches.m[name] != nil
}

// StartWatch begins forwarding the changes of the shared folders (once).
func StartWatch(name string) error {
	s, err := Load(name)
	if err != nil {
		return err
	}
	if !s.Watch {
		StopWatch(name)
		return nil
	}
	watches.Lock()
	if watches.m == nil {
		watches.m = map[string]*watchState{}
	}
	if watches.m[name] != nil {
		watches.Unlock()
		return nil
	}
	w := &watchState{quit: make(chan struct{}), queue: map[string]bool{}, sent: map[string]time.Time{}}
	watches.m[name] = w
	watches.Unlock()

	home, _ := os.UserHomeDir()
	paths := []string{home}
	for _, sh := range s.Shares {
		paths = append(paths, sh.Path)
	}
	stop, err := watchPaths(paths, func(p string, _ uint32) {
		if skipPath(p) {
			return
		}
		w.mu.Lock()
		if len(w.queue) < 50000 {
			w.queue[p] = true
		}
		w.mu.Unlock()
	})
	if err != nil {
		watches.Lock()
		delete(watches.m, name)
		watches.Unlock()
		return err
	}
	w.stop = stop
	go w.pump(name)
	return nil
}

// StopWatch ends the forwarding of a machine.
func StopWatch(name string) {
	watches.Lock()
	w := watches.m[name]
	delete(watches.m, name)
	watches.Unlock()
	if w != nil {
		if w.stop != nil {
			w.stop()
		}
		close(w.quit)
	}
}

// pump keeps an ssh into the machine running the touch loop and feeds it
// the queued changes in small batches.
func (w *watchState) pump(name string) {
	for {
		select {
		case <-w.quit:
			return
		default:
		}
		if err := w.session(name); err != nil {
			slog.Debug("Container VM: avisos de alteração", "máquina", name, "erro", err)
		}
		select {
		case <-w.quit:
			return
		case <-time.After(3 * time.Second):
		}
	}
}

func (w *watchState) session(name string) error {
	args := append([]string{"-T", "-o", "BatchMode=yes"}, SSHArgs(name)...)
	args = append(args, "--", "sudo sh -c "+shQuote(touchLoop))
	cmd := exec.Command("ssh", args...)
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	cmd.Stdout = io.Discard
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	bw := bufio.NewWriter(in)
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-w.quit:
			in.Close()
			_ = cmd.Process.Kill()
			return nil
		case err := <-done:
			return err
		case <-tick.C:
		}
		w.mu.Lock()
		batch := w.queue
		w.queue = map[string]bool{}
		w.mu.Unlock()
		if len(batch) == 0 {
			continue
		}
		for p := range batch {
			if strings.ContainsAny(p, "\t\n") {
				continue
			}
			fi, err := os.Lstat(p)
			if err != nil || !(fi.Mode().IsRegular() || fi.IsDir()) {
				continue
			}
			mt := fi.ModTime()
			if last, ok := w.sent[p]; ok && last.Equal(mt) {
				continue // the echo of our own utime
			}
			w.sent[p] = mt
			fmt.Fprintf(bw, "%s\t%d.%09d\n", p, mt.Unix(), mt.Nanosecond())
		}
		if len(w.sent) > 100000 {
			w.sent = map[string]time.Time{}
		}
		if err := bw.Flush(); err != nil {
			_ = cmd.Process.Kill()
			return err
		}
	}
}

// shQuote quotes a string for the remote shell.
func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
