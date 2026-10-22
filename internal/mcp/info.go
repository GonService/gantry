// Package mcp is the MCP server of Gantry (gantry mcp): an assistant (an
// MCP client, such as a desktop AI assistant) reads and, when allowed, operates the
// labs, containers, VMs and clusters through the Gantry that is open, over
// its local API with a token of its own.
package mcp

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Info is what the open Gantry leaves for gantry mcp (mcp.json in its
// config folder, readable only by the user): where its API is, the token of
// the assistants and whether they may change things.
type Info struct {
	URL   string `json:"url"`
	Token string `json:"token"`
	Write bool   `json:"write"`
}

// InfoFile is mcp.json in the config folder.
func InfoFile(dir string) string { return filepath.Join(dir, "mcp.json") }

// WriteInfo writes mcp.json (0600).
func WriteInfo(dir string, i Info) error {
	b, _ := json.MarshalIndent(i, "", "  ")
	tmp := InfoFile(dir) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, InfoFile(dir))
}

// ReadInfo reads mcp.json; a missing file means the access is off or the
// app is closed.
func ReadInfo(dir string) (Info, error) {
	var i Info
	b, err := os.ReadFile(InfoFile(dir))
	if errors.Is(err, os.ErrNotExist) {
		return i, errors.New("Gantry is not open, or the access of assistants is off (Settings › General › Assistants (MCP))")
	}
	if err != nil {
		return i, err
	}
	err = json.Unmarshal(b, &i)
	return i, err
}
