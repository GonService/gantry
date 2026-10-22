package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestUpdateVia(t *testing.T) {
	brew := t.TempDir()
	app := "/Applications/Gantry.app/Contents/MacOS/gantry"
	for _, c := range []struct {
		goos, exe string
		rooms     []string
		want      string
	}{
		{"darwin", app, []string{brew}, "brew"},
		{"darwin", app, []string{"/nao/existe"}, "zip"},
		{"darwin", "/usr/local/bin/gantry", nil, ""}, // not an app bundle
		{"linux", "/opt/gantry/gantry", []string{brew}, ""},
		{"windows", `C:\Gantry\Gantry.exe`, nil, ""},
	} {
		if got := updateVia(c.goos, c.exe, c.rooms); got != c.want {
			t.Errorf("%s %s: %q, want %q", c.goos, c.exe, got, c.want)
		}
	}
	if appBundle(app) != "/Applications/Gantry.app" {
		t.Fatal(appBundle(app))
	}
}

// A fake release server: the zip, and SHA256SUMS right or wrong.
func TestDownloadVerified(t *testing.T) {
	zip := []byte("PK zip do gantry 0.50.0")
	sum := sha256.Sum256(zip)
	sums := hex.EncodeToString(sum[:]) + "  Gantry-0.50.0-macos.zip\n" + strings.Repeat("0", 64) + "  gantry-servidor.tar.gz\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/zip":
			w.Write(zip)
		case "/sums":
			w.Write([]byte(sums))
		case "/badsums":
			w.Write([]byte(strings.Repeat("a", 64) + "  Gantry-0.50.0-macos.zip\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	rel := &ghRelease{Assets: []ghAsset{{Name: "Gantry-0.50.0-macos.zip", URL: srv.URL + "/zip"}, {Name: "SHA256SUMS", URL: srv.URL + "/sums"}}}
	p, err := downloadVerified(context.Background(), rel, "0.50.0", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != string(zip) {
		t.Fatal("zip")
	}
	rel.Assets[1].URL = srv.URL + "/badsums"
	if _, err := downloadVerified(context.Background(), rel, "0.50.0", t.TempDir()); err == nil || !strings.Contains(err.Error(), "não confere") {
		t.Fatalf("bad checksum: %v", err)
	}
	if _, err := downloadVerified(context.Background(), rel, "0.51.0", t.TempDir()); err == nil {
		t.Fatal("a version without assets was downloaded")
	}
}

func TestCheckSignature(t *testing.T) {
	sig := map[string]string{} // app -> team ("" unsigned, "bad" invalid)
	gatekeeper := true
	run := func(name string, a ...string) (string, error) {
		app := a[len(a)-1]
		switch {
		case name == "codesign" && a[0] == "-dv":
			if sig[app] == "" {
				return "code object is not signed at all", errors.New("exit 1")
			}
			return "Executable=" + app + "\nTeamIdentifier=" + sig[app] + "\n", nil
		case name == "codesign":
			if sig[app] == "bad" || sig[app] == "" {
				return app + ": invalid signature", errors.New("exit 1")
			}
			return "", nil
		case name == "spctl":
			if !gatekeeper {
				return app + ": rejected", errors.New("exit 3")
			}
			return app + ": accepted", nil
		}
		return "", errors.New("unexpected " + name)
	}
	cur, next := "/Applications/Gantry.app", "/tmp/x/Gantry.app"
	// Unsigned installed app: the next one goes as it is.
	if err := checkSignature(run, cur, next); err != nil {
		t.Fatal(err)
	}
	sig[cur], sig[next] = "ABCDE12345", "ABCDE12345"
	if err := checkSignature(run, cur, next); err != nil {
		t.Fatal(err)
	}
	sig[next] = "OUTRO99999"
	if err := checkSignature(run, cur, next); err == nil || !strings.Contains(err.Error(), "outro desenvolvedor") {
		t.Fatalf("other team: %v", err)
	}
	sig[next] = "bad"
	if err := checkSignature(run, cur, next); err == nil || !strings.Contains(err.Error(), "não é válida") {
		t.Fatalf("invalid: %v", err)
	}
	sig[next] = ""
	if err := checkSignature(run, cur, next); err == nil {
		t.Fatal("an unsigned update of a signed app passed")
	}
	sig[next], gatekeeper = "ABCDE12345", false
	if err := checkSignature(run, cur, next); err == nil || !strings.Contains(err.Error(), "Gatekeeper") {
		t.Fatalf("gatekeeper: %v", err)
	}
}
