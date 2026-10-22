//go:build windows

package agent

import (
	"context"
	"io"
)

func runShell(ctx context.Context, st execStart, stdin io.Reader, stdout, stderr io.Writer) (int, string) {
	return 127, "o agente não roda comandos no Windows"
}
