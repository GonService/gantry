package cvm

import (
	"bufio"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// ImageBuild is the Debian cloud build the machines are made from. A dated
// build never changes (latest is replaced every few days, and its
// SHA512SUMS may not list the image while that happens), so a machine made
// today and one made next month start from the same system. Debian keeps
// the dated builds for months; a newer Gantry moves this forward.
const ImageBuild = "20261103-2651"

// ImageURL is the system image for the architecture: Debian 13 (trixie),
// the "generic" cloud image (full kernel, with virtiofs and vsock), as a
// tar.xz with disk.raw, next to the SHA512SUMS of its build.
func ImageURL(arch string) string {
	return "https://cloud.debian.org/images/cloud/trixie/" + ImageBuild + "/debian-13-generic-" + arch + "-" + ImageBuild + ".tar.xz"
}

func cacheDir() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".gantry", "cache", "imagens")
}

// Progress reports a download: bytes done and total (0 if unknown).
type Progress func(done, total int64)

// fetchImage downloads the image (once; it stays in the cache) and checks
// it against the SHA512SUMS published next to it. If Debian cannot be
// reached at that moment, an image already checked before (its sum is kept
// beside it) is used; only with no such image the creation stops.
func fetchImage(ctx context.Context, url string, log io.Writer, prog Progress) (string, error) {
	if err := os.MkdirAll(cacheDir(), 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(cacheDir(), path.Base(url))
	want, err := publishedSum(ctx, url)
	if err != nil {
		if kept := keptSum(dst); kept != "" {
			if got, ferr := fileSHA512(dst); ferr == nil && got == kept {
				fmt.Fprintf(log, "Não consegui conferir no Debian agora (%v); uso a imagem já conferida antes: %s\n", err, dst)
				return dst, nil
			}
		}
		return "", fmt.Errorf("%v. O Debian pode estar publicando uma versão nova da imagem; tente de novo em alguns minutos", err)
	}
	if got, err := fileSHA512(dst); err == nil && got == want {
		fmt.Fprintf(log, "Imagem já baixada: %s (sha512 confere)\n", dst)
		keepSum(dst, want)
		return dst, nil
	}
	fmt.Fprintf(log, "Baixando %s\n", url)
	if err := download(ctx, url, dst+".parcial", prog); err != nil {
		return "", err
	}
	got, err := fileSHA512(dst + ".parcial")
	if err != nil {
		return "", err
	}
	if got != want {
		_ = os.Remove(dst + ".parcial")
		return "", fmt.Errorf("o sha512 da imagem não confere (esperado %s…, veio %s…)", want[:16], got[:16])
	}
	if err := os.Rename(dst+".parcial", dst); err != nil {
		return "", err
	}
	keepSum(dst, want)
	fmt.Fprintf(log, "sha512 confere: %s…\n", want[:32])
	return dst, nil
}

// keepSum and keptSum: the sum checked against Debian's SHA512SUMS, kept
// next to the image (file.sha512).
func keepSum(img, sum string) { _ = os.WriteFile(img+".sha512", []byte(sum+"\n"), 0o644) }

func keptSum(img string) string {
	b, err := os.ReadFile(img + ".sha512")
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(b))
	if len(s) != 128 {
		return ""
	}
	return s
}

// publishedSum reads the image's line in the SHA512SUMS of its folder.
func publishedSum(ctx context.Context, url string) (string, error) {
	sums := url[:strings.LastIndex(url, "/")+1] + "SHA512SUMS"
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, sums, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("não consegui ler %s: %v", sums, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("não consegui ler %s: %s", sums, resp.Status)
	}
	name := path.Base(url)
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name && len(f[0]) == 128 {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("%s não está em %s", name, sums)
}

func download(ctx context.Context, url, dst string, prog Progress) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download de %s: %s", url, resp.Status)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	var n int64
	last := time.Time{}
	buf := make([]byte, 1<<20)
	for {
		k, rerr := resp.Body.Read(buf)
		if k > 0 {
			if _, err := f.Write(buf[:k]); err != nil {
				return err
			}
			n += int64(k)
			if prog != nil && time.Since(last) > 300*time.Millisecond {
				prog(n, resp.ContentLength)
				last = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	if prog != nil {
		prog(n, resp.ContentLength)
	}
	return f.Sync()
}

func fileSHA512(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha512.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// extractDisk takes disk.raw out of the tar.xz into dst (the system disk),
// with the tar of the system (macOS reads xz).
func extractDisk(ctx context.Context, archive, dst string) error {
	tmp := dst + ".extraindo"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	out, err := exec.CommandContext(ctx, "tar", "-xJf", archive, "-C", tmp).CombinedOutput()
	if err != nil {
		return fmt.Errorf("tar: %s %v", strings.TrimSpace(string(out)), err)
	}
	raw := filepath.Join(tmp, "disk.raw")
	if _, err := os.Stat(raw); err != nil {
		return errors.New("a imagem não tem disk.raw")
	}
	return os.Rename(raw, dst)
}
