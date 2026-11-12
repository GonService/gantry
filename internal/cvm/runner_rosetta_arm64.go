//go:build darwin && cgo && cvmrunner && arm64

package cvm

import (
	"log"

	"github.com/Code-Hex/vz/v3"
)

// rosettaShare is the Rosetta folder, when Rosetta is installed. The
// Rosetta API of Virtualization exists only on Apple silicon.
func rosettaShare(lg *log.Logger) vz.DirectoryShare {
	if vz.LinuxRosettaDirectoryShareAvailability() != vz.LinuxRosettaAvailabilityInstalled {
		lg.Printf("Rosetta não está instalado no Mac (softwareupdate --install-rosetta)")
		return nil
	}
	ros, err := vz.NewLinuxRosettaDirectoryShare()
	if err != nil {
		lg.Printf("Rosetta: %v", err)
		return nil
	}
	return ros
}
