//go:build !darwin || !cgo

package cvm

import "errors"

const hasRunner = false

// RunMachine needs Apple's Virtualization framework (macOS, built with cgo).
func RunMachine(name string) error {
	return errors.New("a Container VM só roda no macOS (Apple Virtualization)")
}
