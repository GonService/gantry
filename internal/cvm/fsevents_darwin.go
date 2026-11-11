//go:build darwin && cgo

package cvm

/*
#cgo LDFLAGS: -framework CoreServices
#include <stdlib.h>
#include <stdint.h>
#include <CoreServices/CoreServices.h>
#include <dispatch/dispatch.h>
// Exported from fsevents_export_darwin.go. The _cgo_export.h header only
// exists for .c files, so it is declared here.
extern void gantryFSEvent(uintptr_t id, char *p, unsigned int flags);

static void gantryCallback(ConstFSEventStreamRef ref, void *info, size_t n, void *paths,
		const FSEventStreamEventFlags flags[], const FSEventStreamEventId ids[]) {
	char **p = (char **)paths;
	for (size_t i = 0; i < n; i++) {
		gantryFSEvent((uintptr_t)info, p[i], (unsigned int)flags[i]);
	}
}

static FSEventStreamRef gantryWatch(uintptr_t id, char **paths, int n) {
	CFMutableArrayRef arr = CFArrayCreateMutable(NULL, n, &kCFTypeArrayCallBacks);
	for (int i = 0; i < n; i++) {
		CFStringRef s = CFStringCreateWithCString(NULL, paths[i], kCFStringEncodingUTF8);
		if (s) {
			CFArrayAppendValue(arr, s);
			CFRelease(s);
		}
	}
	FSEventStreamContext ctx = {0, (void *)id, NULL, NULL, NULL};
	FSEventStreamRef st = FSEventStreamCreate(NULL, gantryCallback, &ctx, arr,
		kFSEventStreamEventIdSinceNow, 0.15,
		kFSEventStreamCreateFlagFileEvents | kFSEventStreamCreateFlagNoDefer);
	CFRelease(arr);
	if (st == NULL) {
		return NULL;
	}
	FSEventStreamSetDispatchQueue(st, (dispatch_queue_t)dispatch_get_global_queue(DISPATCH_QUEUE_PRIORITY_DEFAULT, 0));
	if (!FSEventStreamStart(st)) {
		FSEventStreamInvalidate(st);
		FSEventStreamRelease(st);
		return NULL;
	}
	return st;
}

static void gantryUnwatch(FSEventStreamRef st) {
	FSEventStreamStop(st);
	FSEventStreamInvalidate(st);
	FSEventStreamRelease(st);
}
*/
import "C"

import (
	"errors"
	"sync"
	"unsafe"
)

// FSEvents of macOS: one stream per watch, on a dispatch queue (no run
// loop). The kernel coalesces by folder, so watching the home folder is
// cheap.

var fsev struct {
	sync.Mutex
	next uintptr
	fn   map[uintptr]func(string, uint32)
}

func fsevCall(id uintptr, p string, flags uint32) {
	fsev.Lock()
	fn := fsev.fn[id]
	fsev.Unlock()
	if fn != nil {
		fn(p, flags)
	}
}

// watchPaths calls fn for each change under paths until the returned stop.
func watchPaths(paths []string, fn func(path string, flags uint32)) (func(), error) {
	if len(paths) == 0 {
		return nil, errors.New("nada para observar")
	}
	fsev.Lock()
	if fsev.fn == nil {
		fsev.fn = map[uintptr]func(string, uint32){}
	}
	fsev.next++
	id := fsev.next
	fsev.fn[id] = fn
	fsev.Unlock()

	n := len(paths)
	arr := C.malloc(C.size_t(n) * C.size_t(unsafe.Sizeof(uintptr(0))))
	cs := unsafe.Slice((**C.char)(arr), n)
	for i, p := range paths {
		cs[i] = C.CString(p)
	}
	st := C.gantryWatch(C.uintptr_t(id), (**C.char)(arr), C.int(n))
	for i := range cs {
		C.free(unsafe.Pointer(cs[i]))
	}
	C.free(arr)
	drop := func() {
		fsev.Lock()
		delete(fsev.fn, id)
		fsev.Unlock()
	}
	if st == nil {
		drop()
		return nil, errors.New("o macOS não abriu o FSEvents")
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			C.gantryUnwatch(st)
			drop()
		})
	}, nil
}
