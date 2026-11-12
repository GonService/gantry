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

//go:embed provision-alpine.sh.tmpl
var provisionAlpineTmpl string

//go:embed provision-custom.sh.tmpl
var provisionCustomTmpl string

//go:embed user-data.tmpl
var userDataTmpl string

var (
	provisionT       = template.Must(template.New("provision").Parse(provisionTmpl))
	provisionAlpineT = template.Must(template.New("provision-alpine").Parse(provisionAlpineTmpl))
	provisionCustomT = template.Must(template.New("provision-custom").Parse(provisionCustomTmpl))
	userDataT        = template.Must(template.New("user-data").Parse(userDataTmpl))
)

// nerdctl-full, pinned with the sums of its SHA256SUMS.
const nerdctlVersion = "2.4.2"

var nerdctlSums = map[string]string{
	"arm64": "fe0ef0e9840ee8932640e6058f84b7fa98050f9726e44af44883a5b804ae830e",
	"amd64": "1f12c1a5840744fdf7029483c55a69813d44aa2ec1ef879d52d81ff6a3049ecd",
}

// Script is the provisioning script of a spec, exactly as it will run in the
// machine, and its sha256.
func Script(s Spec, home string) (string, string) {
	var b bytes.Buffer
	t := provisionT
	switch s.System() {
	case OSAlpine:
		t = provisionAlpineT
	case OSCustom:
		t = provisionCustomT
	}
	ds, ps := defDockerSock, defPodmanSock
	if c := s.Custom; c != nil {
		if c.DockerSock != "" {
			ds = c.DockerSock
		}
		if c.PodmanSock != "" {
			ps = c.PodmanSock
		}
	}
	_ = t.Execute(&b, map[string]any{
		"Alpine": AlpineVersion, "DockerSock": ds, "PodmanSock": ps, "Tune": guestTune(s.System()),
		"Name": s.Name, "Home": home, "Rosetta": s.Rosetta, "QEMU": s.QEMU,
		"Docker": s.Docker, "Podman": s.Podman, "SSHAgent": s.SSHAgent, "Shares": s.Shares,
		"Containerd": s.Containerd, "Nerdctl": nerdctlVersion, "NerdctlArm64": nerdctlSums["arm64"], "NerdctlAmd64": nerdctlSums["amd64"],
		"Extra":   strings.TrimSpace(strings.ReplaceAll(s.Extra, "\r\n", "\n")),
		"PortSSH": portSSH, "PortMem": portMem, "PortDocker": portDocker, "PortPodman": portPodman,
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
	// Debian has the sudo group and bash; Alpine has wheel and ash; a
	// custom system only gets a user with sudo (the script writes the rule).
	groups, shell := "[sudo]", "/bin/bash"
	switch s.System() {
	case OSAlpine:
		groups, shell = "[wheel]", "/bin/sh"
	case OSCustom:
		groups, shell = "", "/bin/sh"
	}
	_ = userDataT.Execute(&b, map[string]any{
		"Groups": groups, "Shell": shell,
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
