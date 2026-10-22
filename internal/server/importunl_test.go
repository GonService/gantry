package server

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImportUNL(t *testing.T) {
	e := newEnv(t)
	cfg := base64.StdEncoding.EncodeToString([]byte("hostname R1\nend\n"))
	unlFile := `<lab name="Lab da aula"><topology><nodes>
<node id="1" type="iol" template="iol" image="i86bi_linux-adventerprisek9-ms.154-1.T.bin" name="R1" left="10" top="10" ethernet="1" config_data="` + cfg + `"><interface id="0" name="e0/0" type="ethernet" network_id="1"/></node>
<node id="2" type="vpcs" template="vpcs" name="PC1" left="200" top="10"><interface id="0" name="eth0" type="ethernet" network_id="1"/></node>
</nodes><networks><network id="1" type="bridge" name="n1"/></networks></topology></lab>`
	post := func(body, query string) (int, map[string]any) {
		req, _ := http.NewRequest("POST", e.url+"/api/hosts/fake/labs/importar-unl?"+query, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+e.srv.Token())
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		var r map[string]any
		json.Unmarshal(b, &r)
		return resp.StatusCode, r
	}
	dir := filepath.Join(t.TempDir(), "aula")
	code, r := post(unlFile, "file=aula.unl&up=1&dir="+url.QueryEscape(dir))
	if code != 200 || r["ok"] != true || r["id"] != "lab-da-aula" {
		t.Fatalf("%d %v", code, r)
	}
	for _, f := range []string{"compose.yaml", "equipamentos/R1.cfg", "original/aula.unl", "IMPORTACAO.md", "README.md"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("sem %s", f)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "compose.yaml")); !strings.Contains(string(b), "image: vpcs") || !strings.Contains(string(b), "config: equipamentos/R1.cfg") {
		t.Fatalf("compose: %s", b)
	}
	// equipment Gantry does not run: nothing is written
	bad := strings.Replace(unlFile, `type="vpcs" template="vpcs"`, `type="qemu" template="vios"`, 1)
	dir2 := filepath.Join(t.TempDir(), "outro")
	code, r = post(bad, "dir="+url.QueryEscape(dir2))
	if r["ok"] != false || r["blocking"] == nil {
		t.Fatalf("%d %v", code, r)
	}
	if _, err := os.Stat(dir2); err == nil {
		t.Fatal("criou a pasta de um lab bloqueado")
	}
	if code, r = post("isto não é um lab", ""); code != 400 {
		t.Fatalf("%d %v", code, r)
	}
}
