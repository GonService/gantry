//go:build darwin && cgo && cvmrunner && !arm64

package cvm

import (
	"log"

	"github.com/Code-Hex/vz/v3"
)

// rosettaShare: an Intel Mac runs amd64 natively and has no Rosetta for
// Linux.
func rosettaShare(*log.Logger) vz.DirectoryShare { return nil }
