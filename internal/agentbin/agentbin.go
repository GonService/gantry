// Package agentbin holds the gantry-agent binaries the app installs on
// servers. `make agents` builds them into bin/ before the app is compiled.
package agentbin

import (
	"bytes"
	"compress/gzip"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"
)

//go:embed bin
var files embed.FS

func name(arch string) string { return "bin/gantry-agent-linux-" + arch + ".gz" }

// Arches lists the Linux architectures this build can install.
func Arches() []string {
	var out []string
	ents, _ := fs.ReadDir(files, "bin")
	for _, e := range ents {
		n := e.Name()
		if a, ok := strings.CutPrefix(n, "gantry-agent-linux-"); ok && strings.HasSuffix(a, ".gz") {
			out = append(out, strings.TrimSuffix(a, ".gz"))
		}
	}
	sort.Strings(out)
	return out
}

// Binary returns the agent for a Linux architecture (amd64, arm64).
func Binary(arch string) ([]byte, error) {
	b, err := files.ReadFile(name(arch))
	if err != nil {
		have := Arches()
		if len(have) == 0 {
			return nil, fmt.Errorf("este build do Gantry não inclui o agente; compile com make (que roda make agents)")
		}
		return nil, fmt.Errorf("este build não tem o agente para linux/%s (tem: %s)", arch, strings.Join(have, ", "))
	}
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	return io.ReadAll(zr)
}

// LinuxArch maps `uname -m` to a Go architecture name.
func LinuxArch(uname string) (string, bool) {
	switch strings.TrimSpace(uname) {
	case "x86_64", "amd64":
		return "amd64", true
	case "aarch64", "arm64":
		return "arm64", true
	}
	return "", false
}
