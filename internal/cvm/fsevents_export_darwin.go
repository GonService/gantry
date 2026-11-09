//go:build darwin && cgo

package cvm

/*
#include <stdint.h>
*/
import "C"

//export gantryFSEvent
func gantryFSEvent(id C.uintptr_t, p *C.char, flags C.uint) {
	fsevCall(uintptr(id), C.GoString(p), uint32(flags))
}
