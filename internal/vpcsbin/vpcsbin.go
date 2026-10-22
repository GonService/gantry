// Package vpcsbin holds the VPCS (Virtual PC Simulator, GNS3, BSD license:
// bin/COPYING) that Gantry puts on a machine whose lab has VPCS nodes and
// that has no vpcs of its own. tools/vpcs/build.sh builds it.
package vpcsbin

import (
	"bytes"
	"compress/gzip"
	"embed"
	"fmt"
	"io"
)

//go:embed bin
var files embed.FS

// Version of the VPCS carried.
const Version = "0.8.5"

// Binary returns VPCS for a Linux architecture.
func Binary(arch string) ([]byte, error) {
	b, err := files.ReadFile("bin/vpcs-linux-" + arch + ".gz")
	if err != nil {
		return nil, fmt.Errorf("o Gantry não traz o VPCS para linux/%s; instale o vpcs nessa máquina", arch)
	}
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	return io.ReadAll(zr)
}
