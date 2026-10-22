package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// fixPath gives a Finder-launched app the PATH the user has in the terminal,
// plus the usual folders where engines install their CLIs.
func fixPath() {
	parts := []string{}
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		parts = append(parts, p)
	}

	if sh := os.Getenv("SHELL"); sh != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		out, err := exec.CommandContext(ctx, sh, "-l", "-c", "printf %s \"$PATH\"").Output()
		cancel()
		if err == nil {
			for _, p := range strings.Split(strings.TrimSpace(string(out)), ":") {
				add(p)
			}
		}
	}
	for _, p := range strings.Split(os.Getenv("PATH"), ":") {
		add(p)
	}
	home, _ := os.UserHomeDir()
	for _, p := range []string{
		"/opt/homebrew/bin",
		"/usr/local/bin",
		filepath.Join(home, ".orbstack/bin"),
		filepath.Join(home, ".docker/bin"),
		filepath.Join(home, ".rd/bin"),
		"/Applications/Docker.app/Contents/Resources/bin",
		"/opt/podman/bin",
		"/usr/bin", "/bin", "/usr/sbin", "/sbin",
	} {
		add(p)
	}
	_ = os.Setenv("PATH", strings.Join(parts, ":"))
}
