package config

import (
	"os"
	"path/filepath"
	"testing"
	"unicode/utf16"
)

func TestDecodeWSL(t *testing.T) {
	// wsl.exe -l -q writes UTF-16LE (with or without BOM).
	enc := func(s string, bom bool) []byte {
		var b []byte
		if bom {
			b = []byte{0xff, 0xfe}
		}
		for _, u := range utf16.Encode([]rune(s)) {
			b = append(b, byte(u), byte(u>>8))
		}
		return b
	}
	for _, bom := range []bool{false, true} {
		if got := DecodeWSL(enc("Ubuntu\r\nDebian\r\n", bom)); got != "Ubuntu\r\nDebian\r\n" {
			t.Errorf("bom=%v: %q", bom, got)
		}
	}
	if got := DecodeWSL([]byte("Ubuntu\n")); got != "Ubuntu\n" {
		t.Errorf("utf-8: %q", got)
	}
}

func TestWSLDistros(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "wsl.exe")
	_ = os.WriteFile(exe, []byte("#!/bin/sh\nprintf 'Ubuntu\\ndocker-desktop-data\\nDebian\\n'\n"), 0o755)
	t.Setenv("GANTRY_WSL_EXE", exe)
	got := WSLDistros(t.Context())
	if len(got) != 2 || got[0] != "Ubuntu" || got[1] != "Debian" {
		t.Errorf("distros: %v", got)
	}
}
