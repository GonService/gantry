// Package secrets keeps passwords and tokens of the projects out of their
// .env files: in the system keychain (macOS Keychain, the Secret Service on
// Linux) or, where there is none, in a file only the user can read.
package secrets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Service is the name the entries get in the keychain.
const Service = "Gantry"

// Store reads and writes secret values by account name.
type Store interface {
	Name() string // what the user sees: "Chaveiro do macOS"…
	Get(account string) (string, error)
	Set(account, value string) error
	Delete(account string) error
}

var ErrNotFound = errors.New("segredo não encontrado no chaveiro")

// ErrValue is a value this store cannot keep safely; it stays in the .env.
var ErrValue = errors.New("este valor não pode ir para o chaveiro")

// Open picks the best store of this system; dir holds the fallback file.
func Open(dir string) Store {
	if os.Getenv("GANTRY_SECRETS") == "arquivo" {
		return &fileStore{path: filepath.Join(dir, "segredos.json")}
	}
	switch runtime.GOOS {
	case "darwin":
		if _, err := exec.LookPath("security"); err == nil {
			return macStore{}
		}
	case "linux":
		if _, err := exec.LookPath("secret-tool"); err == nil && os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "" {
			return secretTool{}
		}
	}
	return &fileStore{path: filepath.Join(dir, "segredos.json")}
}

/* macOS: security(1), with the values on stdin (not in the process list). */

type macStore struct{}

func (macStore) Name() string { return "Chaveiro do macOS" }

func runSecurity(stdin string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "security", "-i")
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if err != nil || strings.Contains(errb.String(), "could not be found") {
		msg := strings.TrimSpace(errb.String())
		if strings.Contains(msg, "could not be found") {
			return "", ErrNotFound
		}
		if msg == "" && err != nil {
			msg = err.Error()
		}
		return "", fmt.Errorf("chaveiro: %s", msg)
	}
	return out.String(), nil
}

// secQuote quotes an argument for security -i (double quotes, backslash).
// secQuote quotes a value for `security -i`, which reads one command per
// line: a line break (or any control character) would start another
// command, so they never get through.
func secQuote(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func (macStore) Get(account string) (string, error) {
	out, err := runSecurity("find-generic-password -s " + secQuote(Service) + " -a " + secQuote(account) + " -w\n")
	if err != nil {
		return "", err
	}
	v := strings.TrimRight(out, "\n")
	if v == "" {
		return "", ErrNotFound
	}
	return v, nil
}

func (macStore) Set(account, value string) error {
	if strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return ErrValue // line breaks and other control characters
	}
	if strings.ContainsAny(value, `"\`) {
		// The interactive mode of security(1) does not read quotes and
		// backslashes back reliably, and an argument would show in ps.
		return ErrValue
	}
	_, err := runSecurity("add-generic-password -U -s " + secQuote(Service) + " -a " + secQuote(account) + " -l " + secQuote("Gantry: "+account) + " -w " + secQuote(value) + "\n")
	return err
}

func (macStore) Delete(account string) error {
	_, err := runSecurity("delete-generic-password -s " + secQuote(Service) + " -a " + secQuote(account) + "\n")
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

/* Linux desktops: secret-tool (libsecret, GNOME Keyring or KWallet). */

type secretTool struct{}

func (secretTool) Name() string { return "Chaveiro do sistema (Secret Service)" }

func (secretTool) Get(account string) (string, error) {
	out, err := exec.Command("secret-tool", "lookup", "service", Service, "account", account).Output()
	if err != nil || len(out) == 0 {
		return "", ErrNotFound
	}
	return strings.TrimRight(string(out), "\n"), nil
}

func (secretTool) Set(account, value string) error {
	cmd := exec.Command("secret-tool", "store", "--label", "Gantry: "+account, "service", Service, "account", account)
	cmd.Stdin = strings.NewReader(value)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("chaveiro: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func (secretTool) Delete(account string) error {
	_ = exec.Command("secret-tool", "clear", "service", Service, "account", account).Run()
	return nil
}

/* Fallback: a JSON file readable only by the user. */

type fileStore struct {
	mu   sync.Mutex
	path string
}

func (f *fileStore) Name() string { return "arquivo protegido do Gantry (" + f.path + ")" }

// load reads the file; any problem other than a missing file is an error,
// so a damaged file is never overwritten with only the new entry.
func (f *fileStore) load() (map[string]string, error) {
	m := map[string]string{}
	b, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s está danificado: %v", f.path, err)
	}
	return m, nil
}

func (f *fileStore) save(m map[string]string) error {
	b, _ := json.MarshalIndent(m, "", "  ")
	if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
		return err
	}
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.path)
}

func (f *fileStore) Get(account string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return "", err
	}
	v, ok := m[account]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

func (f *fileStore) Set(account, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return err
	}
	m[account] = value
	return f.save(m)
}

func (f *fileStore) Delete(account string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return err
	}
	delete(m, account)
	return f.save(m)
}

// IsSecretName tells whether a variable looks like a password or token.
func IsSecretName(k string) bool {
	u := strings.ToUpper(k)
	for _, w := range []string{"SENHA", "PASS", "SECRET", "SEGREDO", "TOKEN", "API_KEY", "APIKEY", "PRIVATE_KEY", "CREDENTIAL"} {
		if strings.Contains(u, w) {
			return true
		}
	}
	return strings.HasSuffix(u, "_KEY")
}
