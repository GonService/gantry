package config

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

// WSLExe is the wsl.exe to run; GANTRY_WSL_EXE replaces it (tests).
func WSLExe() string {
	if p := os.Getenv("GANTRY_WSL_EXE"); p != "" {
		return p
	}
	return "wsl.exe"
}

// WSLAvailable tells whether this machine has WSL.
func WSLAvailable() bool {
	if os.Getenv("GANTRY_WSL_EXE") != "" {
		return true
	}
	if runtime.GOOS != "windows" {
		return false
	}
	_, err := exec.LookPath("wsl.exe")
	return err == nil
}

// WSLDistros lists the installed distributions (not Docker Desktop's own).
func WSLDistros(ctx context.Context) []string {
	if !WSLAvailable() {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, WSLExe(), "-l", "-q").Output()
	if err != nil {
		return nil
	}
	var res []string
	for _, l := range strings.Split(DecodeWSL(out), "\n") {
		l = strings.TrimSpace(strings.Trim(l, "\x00\r"))
		if l == "" || strings.HasPrefix(strings.ToLower(l), "docker-desktop") {
			continue
		}
		res = append(res, l)
	}
	return res
}

// DecodeWSL reads wsl.exe output, which is UTF-16LE on Windows.
func DecodeWSL(b []byte) string {
	if len(b) >= 2 && (b[1] == 0 || (b[0] == 0xff && b[1] == 0xfe)) {
		if b[0] == 0xff && b[1] == 0xfe {
			b = b[2:]
		}
		u := make([]uint16, 0, len(b)/2)
		for i := 0; i+1 < len(b); i += 2 {
			u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
		}
		return string(utf16.Decode(u))
	}
	return string(b)
}

var (
	wslMu    sync.Mutex
	wslAt    time.Time
	wslFound []string
)

// wslDistros is WSLDistros cached for a minute (Detect runs often).
func wslDistros() []string {
	wslMu.Lock()
	defer wslMu.Unlock()
	if time.Since(wslAt) > time.Minute {
		wslFound = WSLDistros(context.Background())
		wslAt = time.Now()
	}
	return wslFound
}
