package cvm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Create makes the machine and runs the provisioning once. approved is the
// sha256 of the script the user read: if the script of the spec is not that
// one anymore, nothing is created.
func Create(ctx context.Context, s Spec, approved string, log io.Writer, prog Progress) error {
	return create(ctx, s, approved, false, log, prog)
}

// Recreate makes the machine again with a new spec and script, keeping the
// data disk (images, volumes, containers) and its snapshots. A snapshot of
// the data is taken first.
func Recreate(ctx context.Context, s Spec, approved string, log io.Writer, prog Progress) error {
	return create(ctx, s, approved, true, log, prog)
}

func create(ctx context.Context, s Spec, approved string, recreate bool, log io.Writer, prog Progress) error {
	if !Supported() {
		return errors.New("a Container VM só roda no macOS por enquanto")
	}
	if err := s.Check(); err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	if strings.ContainsAny(home, " ,") {
		return fmt.Errorf("a pasta do usuário (%s) tem espaço ou vírgula, que as montagens da máquina não aceitam", home)
	}
	script, sum := Script(s, home)
	if sum != approved {
		return errors.New("o script mudou depois da revisão; revise de novo antes de criar")
	}
	dir := Dir(s.Name)
	if recreate {
		if !Exists(s.Name) {
			return fmt.Errorf("a máquina %s não existe", s.Name)
		}
		old, _ := Load(s.Name)
		if s.DataGiB < old.DataGiB {
			s.DataGiB = old.DataGiB
		}
		StopAgent(s.Name)
		StopWatch(s.Name)
		if err := Stop(ctx, s.Name, log); err != nil {
			return err
		}
		if _, err := TakeSnapshot(ctx, s.Name, "antes de recriar", true, log); err != nil {
			return err
		}
		for _, f := range []string{fSystem, fEFI, fConsole} {
			_ = os.Remove(filepath.Join(dir, f))
		}
		_ = os.RemoveAll(filepath.Join(dir, fSeed))
		fmt.Fprintln(log, "Sistema antigo removido; o disco de dados fica.")
	} else if Exists(s.Name) {
		return fmt.Errorf("a máquina %s já existe", s.Name)
	}
	if err := os.MkdirAll(filepath.Join(dir, fSeed), 0o700); err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(filepath.Join(dir, fSystem)) // a half machine is not a machine
		}
	}()

	fmt.Fprintln(log, "No Mac (seu usuário, sem sudo)")
	if Vfkit() == "" {
		brew, err := exec.LookPath("brew")
		if err != nil {
			for _, p := range []string{"/opt/homebrew/bin/brew", "/usr/local/bin/brew"} {
				if _, e := os.Stat(p); e == nil {
					brew, err = p, nil
				}
			}
		}
		if err != nil {
			return errors.New("o vfkit não está instalado e o Homebrew não foi encontrado: instale o vfkit (brew install vfkit) e tente de novo")
		}
		fmt.Fprintln(log, "a. brew install vfkit")
		cmd := exec.CommandContext(ctx, brew, "install", "vfkit")
		cmd.Env = append(os.Environ(), "HOMEBREW_NO_AUTO_UPDATE=1", "HOMEBREW_NO_ENV_HINTS=1", "NONINTERACTIVE=1")
		cmd.Stdout, cmd.Stderr = log, log
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("brew install vfkit: %v", err)
		}
	} else {
		fmt.Fprintf(log, "a. vfkit: %s\n", Vfkit())
	}

	url := s.Image
	if url == "" {
		url = ImageURL(runtime.GOARCH)
	}
	fmt.Fprintln(log, "b. Imagem do sistema")
	img, err := fetchImage(ctx, url, log, prog)
	if err != nil {
		return err
	}
	if prog != nil {
		prog(0, -1)
	}

	fmt.Fprintln(log, "c. Discos (arquivos esparsos: ocupam só o que for usado)")
	sys := filepath.Join(dir, fSystem)
	if err := extractDisk(ctx, img, sys+".novo"); err != nil {
		return err
	}
	if err := os.Truncate(sys+".novo", int64(s.SystemGiB)<<30); err != nil {
		return err
	}
	data := filepath.Join(dir, fData)
	if _, err := os.Stat(data); err == nil {
		fmt.Fprintln(log, "   o disco de dados de antes continua: os dados dos containers voltam")
	} else {
		f, err := os.Create(data)
		if err != nil {
			return err
		}
		f.Close()
		if err := os.Truncate(data, int64(s.DataGiB)<<30); err != nil {
			return err
		}
	}
	fmt.Fprintf(log, "   %s: %d GiB · %s: %d GiB\n", fSystem, s.SystemGiB, fData, s.DataGiB)

	fmt.Fprintln(log, "d. Chave SSH só desta máquina e o cloud-init")
	key := filepath.Join(dir, fKey)
	_ = os.Remove(key)
	_ = os.Remove(key + ".pub")
	_ = os.Remove(filepath.Join(dir, fKnown))
	if out, err := exec.CommandContext(ctx, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "gantry@"+s.Name, "-f", key).CombinedOutput(); err != nil {
		return fmt.Errorf("ssh-keygen: %s %v", strings.TrimSpace(string(out)), err)
	}
	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, fScript), []byte(script), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, fSeed, "user-data"), []byte(UserData(s, string(pub), script)), 0o600); err != nil {
		return err
	}
	instance := s.Name + "-" + time.Now().Format("20060102-150405")
	if err := os.WriteFile(filepath.Join(dir, fSeed, "meta-data"), []byte(MetaData(s, instance)), 0o600); err != nil {
		return err
	}
	if err := Save(s); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(dir, fConsole))
	if err := os.Rename(sys+".novo", sys); err != nil {
		return err
	}
	ok = true

	fmt.Fprintln(log, "e. Ligando a máquina")
	if err := Start(ctx, s.Name, log); err != nil {
		return err
	}
	fmt.Fprintf(log, "\nDentro da máquina: provision.sh (sha256 %s…)\n", sum[:16])
	rc, err := followConsole(ctx, s.Name, log, 40*time.Minute)
	if err != nil {
		return err
	}
	if rc != 0 {
		if l, e := Run(ctx, s.Name, "sudo tail -n 40 /var/log/gantry-provision.log"); e == nil {
			fmt.Fprintf(log, "\n%s\n", l)
		}
		return fmt.Errorf("o provision.sh terminou com erro (código %d); o log completo fica em /var/log/gantry-provision.log na máquina", rc)
	}
	_ = WriteShims(s.Name)
	fmt.Fprintln(log, "\nPronto: os engines da máquina aparecem em Este Mac.")
	return nil
}

// Disks reports the disk files of a machine.
func Disks(name string) []DiskUse {
	var out []DiskUse
	for _, f := range []string{fSystem, fData} {
		fi, err := os.Stat(filepath.Join(Dir(name), f))
		if err != nil {
			continue
		}
		out = append(out, DiskUse{Name: f, Size: fi.Size(), Occupied: occupied(fi)})
	}
	return out
}

// SavedScript is the script the machine was created with.
func SavedScript(name string) string {
	b, _ := os.ReadFile(filepath.Join(Dir(name), fScript))
	return string(b)
}
