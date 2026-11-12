package cvm

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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

	got, err := fetchImage(ctx, Source{URL: url, Check: "sums"}, io.Discard, nil)
	if err != nil {
		t.Fatal(err)
	}
	if keptSum(got) != sum {
		t.Fatal("a soma conferida não ficou guardada ao lado da imagem")
	}

	listed = false
	var log strings.Builder
	again, err := fetchImage(ctx, Source{URL: url, Check: "sums"}, &log, nil)
	if err != nil || again != got {
		t.Fatalf("devia usar a imagem já conferida: %v", err)
	}
	if !strings.Contains(log.String(), "já conferida") {
		t.Errorf("sem aviso no log: %s", log.String())
	}

	os.Remove(filepath.Join(cacheDir(), "debian-13-generic-arm64.tar.xz"))
	if _, err := fetchImage(ctx, Source{URL: url, Check: "sums"}, io.Discard, nil); err == nil || !strings.Contains(err.Error(), "tente de novo") {
		t.Fatalf("sem imagem guardada devia falhar pedindo para tentar de novo: %v", err)
	}
}

// The image comes from a dated build, never from latest.
func TestImageURLPinned(t *testing.T) {
	u := ImageURL("arm64")
	if strings.Contains(u, "/latest/") || !strings.HasSuffix(u, "/trixie/"+ImageBuild+"/debian-13-generic-arm64-"+ImageBuild+".tar.xz") {
		t.Fatalf("imagem fora de um build datado: %s", u)
	}
}

// Alpine's .sha512, a custom image with a given sum, and a path on the Mac.
func TestImageSources(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	body := []byte("disco alpine")
	h := sha512.Sum512(body)
	sum := hex.EncodeToString(h[:])
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha512") {
			io.WriteString(w, sum+"  generic_alpine.qcow2\n")
			return
		}
		w.Write(body)
	}))
	defer srv.Close()
	ctx := context.Background()
	if _, err := fetchImage(ctx, Source{URL: srv.URL + "/generic_alpine.qcow2", Check: "file"}, io.Discard, nil); err != nil {
		t.Fatalf("alpine: %v", err)
	}
	if _, err := fetchImage(ctx, Source{URL: srv.URL + "/meu.raw", Check: "given", Sum: strings.Repeat("ab", 32)}, io.Discard, nil); err == nil {
		t.Fatal("aceitou uma imagem com a soma errada")
	}
	h2 := sha256.Sum256(body)
	if _, err := fetchImage(ctx, Source{URL: srv.URL + "/meu2.raw", Check: "given", Sum: hex.EncodeToString(h2[:])}, io.Discard, nil); err != nil {
		t.Fatalf("sha256 dado: %v", err)
	}
	local := filepath.Join(t.TempDir(), "disco.raw")
	os.WriteFile(local, body, 0o644)
	if p, err := fetchImage(ctx, Source{URL: local, Check: "given"}, io.Discard, nil); err != nil || p != local {
		t.Fatalf("caminho do Mac: %v %s", err, p)
	}
	if s := ImageSource(Spec{OS: OSAlpine}, "arm64"); !strings.Contains(s.URL, "aarch64-uefi-cloudinit") || s.Check != "file" {
		t.Fatalf("fonte do alpine: %+v", s)
	}
}

// A raw disk inside a tar is found by name; anything else is refused.
func TestExtractDiskFormats(t *testing.T) {
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, "x.qcow3"), []byte("x"), 0o644)
	if err := extractDisk(context.Background(), filepath.Join(d, "x.qcow3"), filepath.Join(d, "out")); err == nil {
		t.Fatal("aceitou um formato desconhecido")
	}
	raw := filepath.Join(d, "a.raw")
	os.WriteFile(raw, []byte("disco"), 0o644)
	if err := extractDisk(context.Background(), raw, filepath.Join(d, "sis.raw")); err != nil {
		t.Fatal(err)
	}
}

// A .qcow2 (made with qemu-img, when there is one) comes out as the same raw.
func TestQcow2ToRaw(t *testing.T) {
	qi, err := exec.LookPath("qemu-img")
	if err != nil {
		t.Skip("sem qemu-img")
	}
	d := t.TempDir()
	raw := filepath.Join(d, "orig.raw")
	b := make([]byte, 3<<20)
	for i := range b {
		b[i] = byte(i * 7)
	}
	os.WriteFile(raw, b, 0o644)
	q := filepath.Join(d, "img.qcow2")
	if out, err := exec.Command(qi, "convert", "-c", "-O", "qcow2", raw, q).CombinedOutput(); err != nil {
		t.Fatalf("%s %v", out, err)
	}
	out := filepath.Join(d, "sis.raw")
	if err := extractDisk(context.Background(), q, out); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(out)
	if string(got) != string(b) {
		t.Fatalf("raw diferente: %d bytes", len(got))
	}
}
