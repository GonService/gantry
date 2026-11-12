package cvm

import (
	"fmt"
	"os"

	"github.com/lima-vm/go-qcow2reader"
	"github.com/lima-vm/go-qcow2reader/convert"
)

// qcow2ToRaw writes the disk of a .qcow2 as a raw, sparse file (the
// Virtualization framework only takes raw disks), as Lima does, without
// qemu-img on the Mac.
func qcow2ToRaw(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	img, err := qcow2reader.Open(in)
	if err != nil {
		return fmt.Errorf("qcow2: %v", err)
	}
	defer img.Close()
	if err := img.Readable(); err != nil {
		return fmt.Errorf("qcow2: %v", err)
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if err := out.Truncate(img.Size()); err != nil {
		out.Close()
		return err
	}
	if err := convert.Convert(out, img, convert.Options{}); err != nil {
		out.Close()
		_ = os.Remove(dst)
		return fmt.Errorf("qcow2 para raw: %v", err)
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
