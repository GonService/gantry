package main

import (
	"crypto/tls"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSelfSignedKeptAndRenewed(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	now := time.Now()
	names := []string{"localhost", "127.0.0.1", "gantry-teste", "192.0.2.10"}
	if err := ensureSelfSigned(dir, cert, key, names, now); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(cert)
	if st, _ := os.Stat(key); st.Mode().Perm() != 0o600 {
		t.Errorf("a chave devia ser 0600, é %v", st.Mode().Perm())
	}
	pair, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	if fp := fingerprint(pair.Certificate[0]); len(strings.Split(fp, ":")) != 32 {
		t.Errorf("impressão digital estranha: %s", fp)
	}

	// Same names: the same certificate (the fingerprint does not change).
	if err := ensureSelfSigned(dir, cert, key, names, now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(cert); string(again) != string(first) {
		t.Error("o certificado mudou sem motivo")
	}
	// A new address on the network: a new certificate with it.
	if err := ensureSelfSigned(dir, cert, key, append(names, "192.0.2.20"), now); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(cert); string(again) == string(first) {
		t.Error("um endereço novo devia gerar outro certificado")
	}
	// Close to expiring: renewed.
	mid, _ := os.ReadFile(cert)
	if err := ensureSelfSigned(dir, cert, key, append(names, "192.0.2.20"), now.Add(810*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(cert); string(again) == string(mid) {
		t.Error("um certificado perto de vencer devia ser renovado")
	}
}

func TestServerTLSOwnCertificate(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	if err := ensureSelfSigned(dir, cert, key, []string{"localhost"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	cfg, fp, err := serverTLS(t.TempDir(), cert, key)
	if err != nil || cfg == nil || fp == "" {
		t.Fatalf("certificado próprio: %v %v %q", cfg, err, fp)
	}
	if _, _, err := serverTLS(t.TempDir(), cert, filepath.Join(dir, "falta.pem")); err == nil {
		t.Error("uma chave que não existe devia dar erro")
	}
}

func TestLinkHost(t *testing.T) {
	if got := linkHost("127.0.0.1:7777"); got != "127.0.0.1:7777" {
		t.Errorf("loopback: %s", got)
	}
	if got := linkHost("0.0.0.0:7777"); strings.HasPrefix(got, "0.0.0.0") && lanIP() != "" {
		t.Errorf("0.0.0.0 devia virar o IP da rede: %s", got)
	}
}
