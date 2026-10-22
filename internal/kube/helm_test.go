package kube

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"strings"
	"testing"
)

func TestOpenRelease(t *testing.T) {
	js := `{"name":"x","manifest":"kind: Service"}`
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write([]byte(js))
	zw.Close()
	b64 := base64.StdEncoding.EncodeToString
	// As Helm writes it (base64 of the gzip, inside the Secret's base64),
	// and without Helm's own base64.
	for _, s := range []string{b64([]byte(b64(gz.Bytes()))), b64(gz.Bytes()), b64([]byte(js))} {
		o, err := openRelease(s)
		if err != nil || o.str("manifest") != "kind: Service" {
			t.Fatalf("%q: %v %v", s, o, err)
		}
	}
	for s, want := range map[string]string{"": "não tem o campo", "@@": "não é base64", b64([]byte("oi")): "não é JSON"} {
		if _, err := openRelease(s); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v", s, err)
		}
	}
}
