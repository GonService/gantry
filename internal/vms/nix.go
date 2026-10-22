package vms

// VMs with nix: (Gantry NixOS images). Their configuration is a NixOS
// module of the project plus the parameters of the instance, delivered as
// /etc/nixos/lab.nix and /etc/nixos/gantry-params.json: by cloud-init
// (write_files) on the first boot and over SSH afterwards. The image applies
// them with nixos-rebuild (service gantry-apply) and writes the result to
// /var/lib/gantry/status.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/goccy/go-yaml"

	"gantry/internal/composex"
)

const (
	NixParamsPath = "/etc/nixos/gantry-params.json"
	NixModulePath = "/etc/nixos/lab.nix"
	// NixStatusPath holds "applying <hash>", "ok <hash>" or "failed <hash>".
	NixStatusPath = "/var/lib/gantry/status"
	// NixLogPath is the output of the last nixos-rebuild.
	NixLogPath = "/var/lib/gantry/last-apply.log"
	// NixModuleMax is the largest module Gantry delivers.
	NixModuleMax = 256 << 10
)

// NixFiles are the two files of a VM with nix:, byte for byte as they land
// in /etc/nixos: the parameters (indented JSON) and the module.
func NixFiles(v *composex.VM) (params, module []byte, err error) {
	if v.Nix == nil {
		return nil, nil, nil
	}
	if v.Nix.ReadErr != "" {
		return nil, nil, fmt.Errorf("x-vms.%s.nix.module: %s", v.Name, v.Nix.ReadErr)
	}
	mod := strings.ReplaceAll(v.Nix.Content, "\r\n", "\n")
	if len(mod) > NixModuleMax {
		return nil, nil, fmt.Errorf("x-vms.%s.nix.module: %s tem mais de %d KB", v.Name, v.Nix.Module, NixModuleMax>>10)
	}
	for _, r := range mod {
		if (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f || r == 0xfeff {
			return nil, nil, fmt.Errorf("x-vms.%s.nix.module: %s tem um caractere de controle (%U) que o cloud-init não aceita", v.Name, v.Nix.Module, r)
		}
	}
	p := map[string]any{}
	for k, x := range v.Nix.Params {
		p[k] = x
	}
	if _, ok := p["hostname"]; !ok {
		p["hostname"] = v.Name
	}
	if v.Metrics {
		p["metrics"] = true // the base module turns the node_exporter on
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("x-vms.%s.nix.params: %v", v.Name, err)
	}
	return append(b, '\n'), []byte(mod), nil
}

// NixHash is the hash gantry-apply computes inside the VM over the module
// and the parameters (sha256 of lab.nix followed by gantry-params.json).
func NixHash(params, module []byte) string {
	h := sha256.New()
	h.Write(module)
	h.Write(params)
	return hex.EncodeToString(h.Sum(nil))
}

// NixHashOf is NixHash of a VM's files ("" for a VM without nix:).
func NixHashOf(v *composex.VM) (string, error) {
	if v.Nix == nil {
		return "", nil
	}
	params, module, err := NixFiles(v)
	if err != nil {
		return "", err
	}
	return NixHash(params, module), nil
}

// nixPlaceholder marks where a file's content goes in the cloud-config.
func nixPlaceholder(i int) string { return fmt.Sprintf("__GANTRY_NIX_FILE_%d__", i) }

// nixWriteFiles adds the two files to the cloud-config's write_files,
// after the user's own (one list, not two keys). Their content goes in as
// placeholders, filled by literalBlocks after the YAML is written.
func nixWriteFiles(cfg map[string]any, params, module []byte) [][]byte {
	wf, _ := cfg["write_files"].([]any)
	contents := [][]byte{params, module}
	for i, p := range []string{NixParamsPath, NixModulePath} {
		wf = append(wf, yaml.MapSlice{
			{Key: "path", Value: p},
			{Key: "permissions", Value: "0644"},
			{Key: "content", Value: nixPlaceholder(i)},
		})
	}
	cfg["write_files"] = wf
	return contents
}

// literalBlocks puts each file in place of its placeholder as a literal
// block with a fixed indentation indicator (|2), every line indented, so a
// line that starts with spaces, a quote or a colon goes through unchanged.
// The chomping keeps the file's ending: "+" keeps every final line break,
// "-" says there is none.
func literalBlocks(doc string, contents [][]byte) (string, error) {
	lines := strings.Split(doc, "\n")
	for i, c := range contents {
		key := "content: " + nixPlaceholder(i)
		at := -1
		for j, l := range lines {
			if strings.HasSuffix(l, key) {
				at = j
				break
			}
		}
		if at < 0 {
			return "", fmt.Errorf("não achei onde pôr o arquivo %d no cloud-config", i)
		}
		l := lines[at]
		col := len(l) - len(key) // where the key starts: the mapping's indentation
		text := string(c)
		chomp := "-"
		if strings.HasSuffix(text, "\n") {
			chomp = "+"
			text = strings.TrimSuffix(text, "\n")
		}
		out := []string{l[:col] + "content: |2" + chomp}
		if text != "" || chomp == "+" {
			ind := strings.Repeat(" ", col+2)
			for _, b := range strings.Split(text, "\n") {
				if b == "" {
					out = append(out, "")
				} else {
					out = append(out, ind+b)
				}
			}
		}
		lines = append(lines[:at], append(out, lines[at+1:]...)...)
	}
	return strings.Join(lines, "\n"), nil
}
