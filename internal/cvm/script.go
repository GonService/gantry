package cvm

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"text/template"
)

//go:embed provision.sh.tmpl
var provisionTmpl string

//go:embed user-data.tmpl
var userDataTmpl string

var (
	provisionT = template.Must(template.New("provision").Parse(provisionTmpl))
	userDataT  = template.Must(template.New("user-data").Parse(userDataTmpl))
)

// Script is the provisioning script of a spec, exactly as it will run in the
// machine, and its sha256.
func Script(s Spec, home string) (string, string) {
	var b bytes.Buffer
	_ = provisionT.Execute(&b, map[string]any{
		"Name": s.Name, "Home": home, "Rosetta": s.Rosetta, "QEMU": s.QEMU,
		"Docker": s.Docker, "Podman": s.Podman, "SSHAgent": s.SSHAgent,
		"Extra":   strings.TrimSpace(strings.ReplaceAll(s.Extra, "\r\n", "\n")),
		"PortSSH": portSSH, "PortDocker": portDocker, "PortPodman": portPodman,
	})
	return b.String(), Sum(b.String())
}

// Sum is the sha256 of a script, in hex.
func Sum(script string) string {
	h := sha256.Sum256([]byte(script))
	return hex.EncodeToString(h[:])
}

// UserData is the cloud-init user-data that creates the user with the key
// and runs the script once.
func UserData(s Spec, pubKey, script string) string {
	var b bytes.Buffer
	_ = userDataT.Execute(&b, map[string]any{
		"Name": s.Name, "Key": strings.TrimSpace(pubKey),
		"Script64": base64.StdEncoding.EncodeToString([]byte(script)),
	})
	return b.String()
}

// MetaData is the cloud-init meta-data; a new instance id makes cloud-init
// run again (a recreated machine).
func MetaData(s Spec, instance string) string {
	return "instance-id: " + instance + "\nlocal-hostname: " + s.Name + "\n"
}
