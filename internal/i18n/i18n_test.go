package i18n

import "testing"

func withDict(t *testing.T, m map[string]string) {
	t.Helper()
	once.Do(load)
	old := d
	d = dict{exact: map[string]string{}, byWord: map[string][]int{}}
	raw := m
	b := []byte("{")
	first := true
	for k, v := range raw {
		if !first {
			b = append(b, ',')
		}
		first = false
		b = append(b, []byte(quote(k)+":"+quote(v))...)
	}
	b = append(b, '}')
	saved := enJSON
	enJSON = b
	load()
	cache.Range(func(k, _ any) bool { cache.Delete(k); return true })
	Set("en")
	t.Cleanup(func() {
		d, enJSON = old, saved
		Set("pt")
		cache.Range(func(k, _ any) bool { cache.Delete(k); return true })
	})
}

func quote(s string) string {
	out := []byte{'"'}
	for _, r := range s {
		switch r {
		case '"', '\\':
			out = append(out, '\\', byte(r))
		case '\n':
			out = append(out, '\\', 'n')
		default:
			out = append(out, string(r)...)
		}
	}
	return string(append(out, '"'))
}

func TestTranslate(t *testing.T) {
	withDict(t, map[string]string{
		"cluster não encontrado":                     "cluster not found",
		"%s não existe neste cluster: %v":            "%s does not exist in this cluster: %v",
		"a revisão %d não existe mais":               "revision %d no longer exists",
		"abra a máquina %s para conectar ao cluster": "open machine %s to connect to the cluster",
		"o arquivo ":                    "the file ",
		" não existe mais no host":      " no longer exists on the host",
		"perdi a conexão com o cluster": "lost the connection to the cluster",
		"de %s para %s":                 "to %[2]s from %[1]s",
	})
	cases := map[string]string{
		"cluster não encontrado":                                                     "cluster not found",
		"  cluster não encontrado\n":                                                 "  cluster not found\n",
		"apps/v9 não existe neste cluster: 404":                                      "apps/v9 does not exist in this cluster: 404",
		"a revisão 7 não existe mais":                                                "revision 7 no longer exists",
		"o arquivo /x/compose.yaml não existe mais no host":                          "the file /x/compose.yaml no longer exists on the host",
		"perdi a conexão com o cluster: abra a máquina vm1 para conectar ao cluster": "lost the connection to the cluster: open machine vm1 to connect to the cluster",
		"de a para b":        "to b from a",
		"web-1":              "web-1",
		"connection refused": "connection refused",
	}
	for in, want := range cases {
		if got := T(in); got != want {
			t.Errorf("%q -> %q, queria %q", in, got, want)
		}
	}
	Set("pt")
	if got := T("cluster não encontrado"); got != "cluster não encontrado" {
		t.Errorf("em português mudou: %q", got)
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{"pt-BR,pt;q=0.9": "pt", "en-US": "en", "de": "en", "": "", "auto": ""} {
		if got := Normalize(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

// The real dictionary: lines with colors, short formats, a format that
// starts with punctuation.
func TestRealDictionary(t *testing.T) {
	Set("en")
	defer Set("pt")
	cases := map[string]string{
		"há 5m":                  "5m ago",
		"cluster não encontrado": "cluster not found",
	}
	for in, want := range cases {
		if got := T(in); got != want {
			t.Errorf("%q -> %q, queria %q", in, got, want)
		}
	}
	for _, in := range []string{"\x1b[36m==>\x1b[0m parando web-1 para a cópia\n", "Baixando alpine: 1 MB de 2 MB (50%)"} {
		if got := T(in); got == in || looksPortuguese(ansiRe.ReplaceAllString(got, "")) {
			t.Errorf("%q ficou %q", in, got)
		}
	}
}

// TestJSONWrappedError: an error in an API answer is translated with the
// message wrapped inside it (the error of a job that read a compose).
func TestJSONWrappedError(t *testing.T) {
	Set("en")
	defer Set("pt")
	msg := "não consegui ler o compose: x-vms.R1.memory: o 7200 do Dynamips roda com até 256 MB (com mais, a RAM cobre a ROM de boot e ele não inicia); use memory: 256M"
	got := JSON(map[string]any{"error": msg, "name": "não consegui ler o compose: x"}).(map[string]any)
	if got["error"] != "could not read the compose file: x-vms.R1.memory: the Dynamips 7200 runs with up to 256 MB (with more, the RAM covers the boot ROM and it does not start); use memory: 256M" {
		t.Fatalf("%q", got["error"])
	}
	if got["name"] != "não consegui ler o compose: x" {
		t.Fatalf("content changed: %q", got["name"])
	}
}
