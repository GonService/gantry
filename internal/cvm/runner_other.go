//go:build !darwin || !cgo || !cvmrunner

package cvm

import "errors"

// RunMachine needs Apple's Virtualization framework: it is in the helper
// (cmd/gantry-vm, built for macOS with cgo and the cvmrunner tag).
func RunMachine(name string) error {
	return errors.New("a Container VM só roda no macOS, pelo gantry-vm (Apple Virtualization)")
}
