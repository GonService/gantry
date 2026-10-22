//go:build !linux

package tap

import (
	"context"
	"errors"
)

// Run is Linux only (TAP interfaces).
func Run(context.Context, string, []Port, func(string, ...any)) error {
	return errors.New("as portas de cabo de containers só funcionam em Linux")
}
