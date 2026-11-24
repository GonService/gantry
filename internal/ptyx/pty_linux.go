package ptyx

import (
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// open makes a new pseudo-terminal: the master side and the path of the
// slave (/dev/pts/N).
func open() (*os.File, string, error) {
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, "", err
	}
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		unix.Close(fd)
		return nil, "", err
	}
	n, err := unix.IoctlGetUint32(fd, unix.TIOCGPTN)
	if err != nil {
		unix.Close(fd)
		return nil, "", err
	}
	return os.NewFile(uintptr(fd), "/dev/ptmx"), "/dev/pts/" + strconv.Itoa(int(n)), nil
}
