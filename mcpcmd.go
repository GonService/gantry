package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"gantry/internal/mcp"
)

// runMCP is gantry mcp: the MCP server (stdio) of the Gantry that is open,
// for an MCP client (an AI assistant), configured as:
//
//	{"mcpServers": {"gantry": {"command": "/Applications/Gantry.app/Contents/MacOS/gantry", "args": ["mcp"]}}}
func runMCP(dir string, argv []string) {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	cfg := fs.String("config", filepath.Join(dir, "config.json"), "the config file of the Gantry to use")
	_ = fs.Parse(argv)
	info, err := mcp.ReadInfo(filepath.Dir(*cfg))
	if err != nil {
		fmt.Fprintln(os.Stderr, "gantry mcp:", err)
		os.Exit(1)
	}
	s := &mcp.Server{
		Client:  &mcp.Client{Base: info.URL, Token: info.Token, HTTP: &http.Client{Timeout: 3 * time.Minute}},
		Write:   info.Write,
		Version: version,
	}
	if err := s.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "gantry mcp:", err)
		os.Exit(1)
	}
}
