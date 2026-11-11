package cvm

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// While Debian publishes a new build, SHA512SUMS may not list the image:
// an image checked before is used; with none, the error says to retry.
func TestFetchImageWhileDebianPublishes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	body := []byte("imagem de teste")
	h := sha512.Sum512(body)
	sum := hex.EncodeToString(h[:])
	listed := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "SHA512SUMS"):
			if listed {
				io.WriteString(w, sum+"  debian-13-generic-arm64.tar.xz\n")
			} else {
				io.WriteString(w, strings.Repeat("0", 128)+"  outra-coisa.tar.xz\n")
			}
		default:
			w.Write(body)
		}
	}))
	defer srv.Close()
	url := srv.URL + "/latest/debian-13-generic-arm64.tar.xz"
	ctx := context.Background()

	got, err := fetchImage(ctx, url, io.Discard, nil)
	if err != nil {
		t.Fatal(err)
	}
	if keptSum(got) != sum {
		t.Fatal("a soma conferida não ficou guardada ao lado da imagem")
	}

	listed = false
	var log strings.Builder
	again, err := fetchImage(ctx, url, &log, nil)
	if err != nil || again != got {
		t.Fatalf("devia usar a imagem já conferida: %v", err)
	}
	if !strings.Contains(log.String(), "já conferida") {
		t.Errorf("sem aviso no log: %s", log.String())
	}

	os.Remove(filepath.Join(cacheDir(), "debian-13-generic-arm64.tar.xz"))
	if _, err := fetchImage(ctx, url, io.Discard, nil); err == nil || !strings.Contains(err.Error(), "tente de novo") {
		t.Fatalf("sem imagem guardada devia falhar pedindo para tentar de novo: %v", err)
	}
}
