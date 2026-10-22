package secrets

import (
	"os"
	"testing"
)

func TestFileStore(t *testing.T) {
	t.Setenv("GANTRY_SECRETS", "arquivo")
	dir := t.TempDir()
	s := Open(dir)
	if _, err := s.Get("a"); err != ErrNotFound {
		t.Fatalf("vazio: %v", err)
	}
	if err := s.Set("m/lab/PG_SENHA", `x"y\z`); err != nil {
		t.Fatal(err)
	}
	if v, err := s.Get("m/lab/PG_SENHA"); err != nil || v != `x"y\z` {
		t.Fatalf("get: %q %v", v, err)
	}
	st, _ := os.Stat(dir + "/segredos.json")
	if st.Mode().Perm() != 0o600 {
		t.Errorf("permissão %v", st.Mode().Perm())
	}
	_ = s.Delete("m/lab/PG_SENHA")
	if _, err := s.Get("m/lab/PG_SENHA"); err != ErrNotFound {
		t.Error("devia ter apagado")
	}
}

func TestSecretNames(t *testing.T) {
	for k, want := range map[string]bool{"PG_SENHA": true, "BETTER_AUTH_SECRET": true, "OPENAI_API_KEY": true, "GITHUB_TOKEN": true, "PORTA_HTTP": false, "PG_HOST": false, "SEGREDO_AUTH": true, "KEYCLOAK_PORT": false} {
		if IsSecretName(k) != want {
			t.Errorf("%s: %v", k, !want)
		}
	}
	if secQuote(`a"b\c`) != `"a\"b\\c"` {
		t.Errorf("secQuote %s", secQuote(`a"b\c`))
	}
}
