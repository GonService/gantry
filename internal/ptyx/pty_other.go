//go:build !linux && !darwin

package ptyx

import (
	"context"
	"os/exec"
)

// Start has no pseudo-terminal to give here.
func Start(*exec.Cmd, int, int) (*Session, error) { return nil, ErrUnsupported }

// Resize is never reached: there is no Session here.
func (s *Session) Resize(context.Context, int, int) error { return ErrUnsupported }
