//go:build linux

package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const watchSupported = true

// watchFiles reports changes to the given files with inotify. It watches the
// parent folders, because editors such as Nvim save by writing a new file and
// renaming it over the old one.
func watchFiles(paths []string, out chan<- string) (func(), error) {
	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC | syscall.IN_NONBLOCK)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "inotify")
	const mask = syscall.IN_CLOSE_WRITE | syscall.IN_MOVED_TO | syscall.IN_MOVED_FROM |
		syscall.IN_CREATE | syscall.IN_DELETE | syscall.IN_ATTRIB
	dirs := map[int32]string{}
	want := map[string]bool{}
	for _, p := range paths {
		p = filepath.Clean(p)
		want[p] = true
		wd, err := syscall.InotifyAddWatch(fd, filepath.Dir(p), mask)
		if err != nil {
			f.Close()
			return nil, err
		}
		dirs[int32(wd)] = filepath.Dir(p)
	}

	var once sync.Once
	stop := func() { once.Do(func() { f.Close() }) }
	go func() {
		defer close(out)
		buf := make([]byte, 64<<10)
		// Saving a file fires several events; report each file once per burst.
		pending := map[string]bool{}
		var timer *time.Timer
		var tc <-chan time.Time
		reads := make(chan []byte)
		go func() {
			defer close(reads)
			for {
				n, err := f.Read(buf)
				if err != nil {
					return
				}
				reads <- append([]byte(nil), buf[:n]...)
			}
		}()
		for {
			select {
			case b, ok := <-reads:
				if !ok {
					return
				}
				for len(b) >= syscall.SizeofInotifyEvent {
					ev := (*syscall.InotifyEvent)(unsafe.Pointer(&b[0]))
					end := syscall.SizeofInotifyEvent + int(ev.Len)
					if end > len(b) {
						break
					}
					name := string(bytes.TrimRight(b[syscall.SizeofInotifyEvent:end], "\x00"))
					b = b[end:]
					if dir, ok := dirs[ev.Wd]; ok && name != "" {
						p := filepath.Join(dir, name)
						if want[p] {
							pending[p] = true
						}
					}
				}
				if len(pending) > 0 && timer == nil {
					timer = time.NewTimer(150 * time.Millisecond)
					tc = timer.C
				}
			case <-tc:
				timer, tc = nil, nil
				for p := range pending {
					select {
					case out <- p:
					default:
					}
				}
				pending = map[string]bool{}
			}
		}
	}()
	return stop, nil
}
