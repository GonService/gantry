package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// HTTPS for the browser version on the network: the user's certificate
// (-cert, -key) or one Gantry makes (-tls) and keeps in the config folder,
// for this computer's names and addresses. A made certificate is not
// trusted by the browser: the first time it asks, and the fingerprint
// printed at the start is what to compare.

// serverTLS is the TLS config and the SHA-256 fingerprint of the
// certificate.
func serverTLS(cfgDir, certFile, keyFile string) (*tls.Config, string, error) {
	if certFile == "" {
		dir := filepath.Join(cfgDir, "tls")
		certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
		if err := ensureSelfSigned(dir, certFile, keyFile, localNames(), time.Now()); err != nil {
			return nil, "", err
		}
	}
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, "", fmt.Errorf("certificado %s / chave %s: %v", certFile, keyFile, err)
	}
	if len(pair.Certificate) == 0 {
		return nil, "", errors.New("o arquivo do certificado está vazio")
	}
	return &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}, fingerprint(pair.Certificate[0]), nil
}

// fingerprint is the SHA-256 of a certificate as browsers show it
// (AB:CD:…).
func fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}

// localNames are the names and addresses the certificate covers: localhost,
// the computer's name and the addresses of its network interfaces.
func localNames() []string {
	names := []string{"localhost", "127.0.0.1", "::1"}
	if h, err := os.Hostname(); err == nil && h != "" {
		names = append(names, h)
		if !strings.Contains(h, ".") {
			names = append(names, h+".local")
		}
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && !n.IP.IsLinkLocalUnicast() {
				names = append(names, n.IP.String())
			}
		}
	}
	return names
}

// ensureSelfSigned keeps the made certificate: a new one only when there is
// none, it expires within 30 days or a name of this computer is missing in
// it (a new address on the network), so the fingerprint rarely changes.
func ensureSelfSigned(dir, certFile, keyFile string, names []string, now time.Time) error {
	if b, err := os.ReadFile(certFile); err == nil {
		if blk, _ := pem.Decode(b); blk != nil {
			if c, err := x509.ParseCertificate(blk.Bytes); err == nil && now.Add(30*24*time.Hour).Before(c.NotAfter) && covers(c, names) {
				if _, err := os.Stat(keyFile); err == nil {
					return nil
				}
			}
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return err
	}
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "Gantry", Organization: []string{"Gantry (certificado próprio)"}},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(825 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, n := range names {
		if ip := net.ParseIP(n); ip != nil {
			tpl.IPAddresses = append(tpl.IPAddresses, ip)
		} else {
			tpl.DNSNames = append(tpl.DNSNames, n)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := writePEM(keyFile, "EC PRIVATE KEY", kb); err != nil {
		return err
	}
	return writePEM(certFile, "CERTIFICATE", der)
}

// covers says whether the certificate has every name.
func covers(c *x509.Certificate, names []string) bool {
	for _, n := range names {
		if ip := net.ParseIP(n); ip != nil {
			found := false
			for _, x := range c.IPAddresses {
				if x.Equal(ip) {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		} else if c.VerifyHostname(n) != nil {
			return false
		}
	}
	return true
}

func writePEM(path, kind string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: b}), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// linkHost is the address for the links printed at the start: listening on
// every interface (0.0.0.0 or ::), it is this computer's address on the
// network, which other devices can open.
func linkHost(addr string) string {
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if ip := net.ParseIP(h); ip != nil && ip.IsUnspecified() {
		if lan := lanIP(); lan != "" {
			return net.JoinHostPort(lan, p)
		}
	}
	return addr
}

// lanIP is the address this computer uses on the network (the route to the
// internet), without sending anything.
func lanIP() string {
	c, err := net.Dial("udp", "192.0.2.1:9")
	if err != nil {
		return ""
	}
	defer c.Close()
	if a, ok := c.LocalAddr().(*net.UDPAddr); ok && !a.IP.IsLoopback() {
		return a.IP.String()
	}
	return ""
}
