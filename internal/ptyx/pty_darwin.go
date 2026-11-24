package ptyx

import (
	"bytes"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// open makes a new pseudo-terminal: the master side and the path of the
// slave (/dev/ttysNNN), with grantpt and unlockpt done by their ioctls.
func open() (*os.File, string, error) {
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, "", err
	}
	fail := func(err error) (*os.File, string, error) {
		unix.Close(fd)
		return nil, "", err
	}
	if err := ioctl(fd, unix.TIOCPTYGRANT, 0); err != nil {
		return fail(err)
	}
	if err := ioctl(fd, unix.TIOCPTYUNLK, 0); err != nil {
		return fail(err)
	}
	var buf [128]byte
	if err := ioctl(fd, unix.TIOCPTYGNAME, uintptr(unsafe.Pointer(&buf[0]))); err != nil {
		return fail(err)
	}
	name := string(buf[:bytes.IndexByte(buf[:], 0)])
	return os.NewFile(uintptr(fd), "/dev/ptmx"), name, nil
}

func ioctl(fd int, req uint, arg uintptr) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(req), arg); e != 0 {
		return e
	}
	return nil
}
