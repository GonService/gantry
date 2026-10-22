package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSSHHosts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".ssh", "conf.d"), 0o700)
	os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte(`Include conf.d/*
Host *
  ServerAliveInterval 30
Host lab homelab
  HostName 192.0.2.10
  User ana
Host vm-teste
  HostName=127.0.0.1
  Port 2222
Host 10.0.*
  User root
`), 0o600)
	os.WriteFile(filepath.Join(home, ".ssh", "conf.d", "trabalho"), []byte("Host bastion\n  HostName bastion.example.com\n  User ops\n"), 0o600)
	got := SSHHosts()
	want := []SSHHost{
		{Alias: "bastion", HostName: "bastion.example.com", User: "ops"},
		{Alias: "lab", HostName: "192.0.2.10", User: "ana"},
		{Alias: "homelab", HostName: "192.0.2.10", User: "ana"},
		{Alias: "vm-teste", HostName: "127.0.0.1", Port: "2222"},
	}
	if len(got) != len(want) {
		t.Fatalf("%+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%d: %+v != %+v", i, got[i], want[i])
		}
	}
}
