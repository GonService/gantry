package cvm

import (
	"bufio"
	"context"
	"crypto/sha256"
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

// AlpineVersion is the Alpine release the "alpine" machines are made from
// (its cloud image: UEFI, cloud-init, the linux-virt kernel).
const AlpineVersion = "3.24.1"

// AlpineURL is the Alpine cloud image for the architecture (Go's names).
func AlpineURL(arch string) string {
	a := map[string]string{"arm64": "aarch64", "amd64": "x86_64"}[arch]
	minor := AlpineVersion[:strings.LastIndex(AlpineVersion, ".")]
	return "https://dl-cdn.alpinelinux.org/alpine/v" + minor + "/releases/cloud/generic_alpine-" + AlpineVersion + "-" + a + "-uefi-cloudinit-r0.qcow2"
}

// Source is where a machine's system image comes from and how it is checked.
type Source struct {
	URL string // https://… or a path on the Mac
	// How the sum is known: "sums" (Debian's SHA512SUMS of the folder),
	// "file" (a .sha512 next to the image, as Alpine has) or "given".
	Check string
	Sum   string // the given sum (sha256 or sha512), for "given"
}

// ImageSource is the image of a spec on this architecture.
func ImageSource(s Spec, arch string) Source {
	switch s.System() {
	case OSAlpine:
		if s.Image != "" {
			return Source{URL: s.Image, Check: "file"}
		}
		return Source{URL: AlpineURL(arch), Check: "file"}
	case OSCustom:
		c := Custom{}
		if s.Custom != nil {
			c = *s.Custom
		}
		return Source{URL: c.Image, Check: "given", Sum: strings.ToLower(c.Sum)}
	}
	if s.Image != "" {
		return Source{URL: s.Image, Check: "sums"}
	}
	return Source{URL: ImageURL(arch), Check: "sums"}
}

// fetchImage gets the image (once; it stays in the cache) and checks it:
// against the SHA512SUMS of Debian's folder, Alpine's .sha512, or the sum
// the user gave. If the published sum cannot be read at that moment, an
// image already checked before (its sum is kept beside it) is used; only
// with no such image the creation stops. A path on the Mac is used where
// it is, checked if a sum was given.
func fetchImage(ctx context.Context, src Source, log io.Writer, prog Progress) (string, error) {
	if !strings.HasPrefix(src.URL, "https://") && !strings.HasPrefix(src.URL, "http://") {
		p := src.URL
		if strings.HasPrefix(p, "~/") {
			h, _ := os.UserHomeDir()
			p = filepath.Join(h, p[2:])
		}
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("a imagem %s não existe no Mac", p)
		}
		if src.Sum != "" {
			if err := checkSum(p, src.Sum); err != nil {
				return "", err
			}
			fmt.Fprintf(log, "Imagem do Mac: %s (soma confere)\n", p)
		} else {
			fmt.Fprintf(log, "Imagem do Mac: %s (sem soma para conferir)\n", p)
		}
		return p, nil
	}
	if err := os.MkdirAll(cacheDir(), 0o755); err != nil {
		return "", err
	}
	url := src.URL
	dst := filepath.Join(cacheDir(), path.Base(url))
	var want string
	var err error
	switch src.Check {
	case "sums":
		want, err = publishedSum(ctx, url)
	case "file":
		want, err = sumFile(ctx, url+".sha512")
	default:
		want = src.Sum
	}
	if err != nil {
		if kept := keptSum(dst); kept != "" {
			if got, ferr := fileSHA512(dst); ferr == nil && got == kept {
				fmt.Fprintf(log, "Não consegui conferir agora (%v); uso a imagem já conferida antes: %s\n", err, dst)
				return dst, nil
			}
		}
		return "", fmt.Errorf("%v. Confira a conexão e tente de novo em alguns minutos", err)
	}
	if want != "" && checkSum(dst, want) == nil {
		fmt.Fprintf(log, "Imagem já baixada: %s (soma confere)\n", dst)
		keepSHA512(dst)
		return dst, nil
	}
	if want == "" {
		if _, err := os.Stat(dst); err == nil {
			fmt.Fprintf(log, "Imagem já baixada: %s (sem soma para conferir)\n", dst)
			return dst, nil
		}
	}
	fmt.Fprintf(log, "Baixando %s\n", url)
	if err := download(ctx, url, dst+".parcial", prog); err != nil {
		return "", err
	}
	if want != "" {
		if err := checkSum(dst+".parcial", want); err != nil {
			_ = os.Remove(dst + ".parcial")
			return "", err
		}
	}
	if err := os.Rename(dst+".parcial", dst); err != nil {
		return "", err
	}
	keepSHA512(dst)
	if want != "" {
		fmt.Fprintf(log, "Soma confere: %s…\n", want[:32])
	} else {
		fmt.Fprintln(log, "Sem soma para conferir: a imagem foi usada como veio.")
	}
	return dst, nil
}

// checkSum compares a file with a sha256 or sha512 (by its length).
func checkSum(p, want string) error {
	want = strings.ToLower(want)
	var got string
	var err error
	if len(want) == 64 {
		got, err = fileSHA256(p)
	} else {
		got, err = fileSHA512(p)
	}
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("a soma da imagem não confere (esperado %s…, veio %s…)", want[:16], got[:16])
	}
	return nil
}

// sumFile reads a published .sha512 (the hash, maybe followed by the name).
func sumFile(ctx context.Context, url string) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("não consegui ler %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("não consegui ler %s: %s", url, resp.Status)
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	f := strings.Fields(string(b))
	if len(f) == 0 || len(f[0]) != 128 || !hexRe.MatchString(f[0]) {
		return "", fmt.Errorf("%s não traz um sha512", url)
	}
	return strings.ToLower(f[0]), nil
}

// keepSHA512 keeps, next to a checked image, its sha512 (file.sha512).
func keepSHA512(img string) {
	if sum, err := fileSHA512(img); err == nil {
		keepSum(img, sum)
	}
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

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
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

// extractDisk turns the image into the raw system disk dst: a raw disk is
// cloned (APFS: no copy), a .qcow2 converted, and a tar (.tar, .tar.gz,
// .tgz, .tar.xz) opened with the system's tar, taking disk.raw or the one
// .raw/.img inside.
func extractDisk(ctx context.Context, img, dst string) error {
	_ = os.Remove(dst)
	low := strings.ToLower(img)
	switch {
	case strings.HasSuffix(low, ".qcow2"):
		return qcow2ToRaw(img, dst)
	case strings.HasSuffix(low, ".raw"), strings.HasSuffix(low, ".img"):
		if out, err := exec.CommandContext(ctx, "cp", "-c", img, dst).CombinedOutput(); err != nil {
			if out2, err2 := exec.CommandContext(ctx, "cp", img, dst).CombinedOutput(); err2 != nil {
				return fmt.Errorf("cp: %s %s %v", strings.TrimSpace(string(out)), strings.TrimSpace(string(out2)), err2)
			}
		}
		return nil
	case strings.Contains(low, ".tar"), strings.HasSuffix(low, ".tgz"):
	default:
		return fmt.Errorf("formato de imagem desconhecido: %s (use .raw, .img, .qcow2 ou um .tar com o disco)", filepath.Base(img))
	}
	tmp := dst + ".extraindo"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	out, err := exec.CommandContext(ctx, "tar", "-xf", img, "-C", tmp).CombinedOutput()
	if err != nil {
		return fmt.Errorf("tar: %s %v", strings.TrimSpace(string(out)), err)
	}
	raw := filepath.Join(tmp, "disk.raw")
	if _, err := os.Stat(raw); err != nil {
		var found []string
		_ = filepath.WalkDir(tmp, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				if l := strings.ToLower(p); strings.HasSuffix(l, ".raw") || strings.HasSuffix(l, ".img") {
					found = append(found, p)
				}
			}
			return nil
		})
		if len(found) != 1 {
			return errors.New("o arquivo não tem um disco só (disk.raw, ou um .raw/.img) dentro")
		}
		raw = found[0]
	}
	return os.Rename(raw, dst)
}
