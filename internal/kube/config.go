// Package kube talks to Kubernetes clusters with the standard library only:
// kubeconfig parsing (certificates, tokens, exec plugins such as
// gke-gcloud-auth-plugin), the REST API, log streams, exec over WebSocket
// and the API server's proxy to services and pods.
package kube

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/goccy/go-yaml"
)

// Config is a kubeconfig file (only what Gantry uses).
type Config struct {
	Clusters []struct {
		Name    string  `yaml:"name"`
		Cluster Cluster `yaml:"cluster"`
	} `yaml:"clusters"`
	Users []struct {
		Name string   `yaml:"name"`
		User AuthInfo `yaml:"user"`
	} `yaml:"users"`
	Contexts []struct {
		Name    string `yaml:"name"`
		Context struct {
			Cluster   string `yaml:"cluster"`
			User      string `yaml:"user"`
			Namespace string `yaml:"namespace"`
		} `yaml:"context"`
	} `yaml:"contexts"`
	CurrentContext string `yaml:"current-context"`

	dir string // the file's folder: relative paths are relative to it
}

type Cluster struct {
	Server                   string `yaml:"server,omitempty"`
	CertificateAuthority     string `yaml:"certificate-authority,omitempty"`
	CertificateAuthorityData string `yaml:"certificate-authority-data,omitempty"`
	InsecureSkipTLSVerify    bool   `yaml:"insecure-skip-tls-verify,omitempty"`
	TLSServerName            string `yaml:"tls-server-name,omitempty"`
	ProxyURL                 string `yaml:"proxy-url,omitempty"`
}

type AuthInfo struct {
	ClientCertificate     string `yaml:"client-certificate,omitempty"`
	ClientCertificateData string `yaml:"client-certificate-data,omitempty"`
	ClientKey             string `yaml:"client-key,omitempty"`
	ClientKeyData         string `yaml:"client-key-data,omitempty"`
	Token                 string `yaml:"token,omitempty"`
	TokenFile             string `yaml:"tokenFile,omitempty"`
	Username              string `yaml:"username,omitempty"`
	Password              string `yaml:"password,omitempty"`
	Exec                  *Exec  `yaml:"exec,omitempty"`
	AuthProvider          *struct {
		Name string `yaml:"name,omitempty"`
	} `yaml:"auth-provider,omitempty"`
}

// Exec is a credential plugin (kubectl's client.authentication.k8s.io).
type Exec struct {
	Command    string   `yaml:"command,omitempty"`
	Args       []string `yaml:"args,omitempty"`
	APIVersion string   `yaml:"apiVersion,omitempty"`
	Env        []struct {
		Name  string `yaml:"name,omitempty"`
		Value string `yaml:"value,omitempty"`
	} `yaml:"env,omitempty"`
	InstallHint string `yaml:"installHint,omitempty"`
}

// Parse reads a kubeconfig; dir resolves relative certificate paths.
func Parse(b []byte, dir string) (*Config, error) {
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("kubeconfig inválido: %v", err)
	}
	if len(c.Contexts) == 0 {
		return nil, errors.New("o kubeconfig não tem nenhum contexto")
	}
	c.dir = dir
	return &c, nil
}

// Load reads a kubeconfig file (~ allowed).
func Load(path string) (*Config, error) {
	path = Expand(path)
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b, filepath.Dir(path))
}

// Expand turns ~/x into the home folder.
func Expand(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// DefaultPath is where kubectl looks: $KUBECONFIG (first file) or ~/.kube/config.
func DefaultPath() string {
	if k := os.Getenv("KUBECONFIG"); k != "" {
		return strings.Split(k, string(os.PathListSeparator))[0]
	}
	return Expand("~/.kube/config")
}

// ContextInfo describes a context for the user to pick.
type ContextInfo struct {
	Name      string `json:"name"`
	Cluster   string `json:"cluster"`
	Server    string `json:"server"`
	User      string `json:"user"`
	Namespace string `json:"namespace,omitempty"`
	Auth      string `json:"auth"` // certificado, token, plugin (gke-gcloud-auth-plugin)…
	Current   bool   `json:"current,omitempty"`
	// Exec is the command a credential plugin runs (shown before trusting
	// a pasted kubeconfig); Files are the files it reads.
	Exec  string   `json:"exec,omitempty"`
	Files []string `json:"files,omitempty"`
	// Proxy is the proxy-url the connections go through; Insecure, that
	// the server's certificate is not checked (insecure-skip-tls-verify).
	Proxy    string `json:"proxy,omitempty"`
	Insecure bool   `json:"insecure,omitempty"`
}

func (c *Config) ContextList() []ContextInfo {
	var out []ContextInfo
	for _, x := range c.Contexts {
		ci := ContextInfo{Name: x.Name, Cluster: x.Context.Cluster, User: x.Context.User, Namespace: x.Context.Namespace, Current: x.Name == c.CurrentContext}
		if cl := c.cluster(x.Context.Cluster); cl != nil {
			ci.Server = cl.Server
		}
		if u := c.user(x.Context.User); u != nil {
			ci.Auth = authKind(u)
			if u.Exec != nil {
				ci.Exec = strings.TrimSpace(u.Exec.Command + " " + strings.Join(u.Exec.Args, " "))
			}
			for _, f := range []string{u.TokenFile, u.ClientCertificate, u.ClientKey} {
				if f != "" {
					ci.Files = append(ci.Files, f)
				}
			}
		}
		if cl := c.cluster(x.Context.Cluster); cl != nil {
			if cl.CertificateAuthority != "" {
				ci.Files = append(ci.Files, cl.CertificateAuthority)
			}
			ci.Proxy, ci.Insecure = cl.ProxyURL, cl.InsecureSkipTLSVerify
		}
		out = append(out, ci)
	}
	return out
}

func authKind(u *AuthInfo) string {
	switch {
	case u.Exec != nil:
		return "plugin (" + filepath.Base(u.Exec.Command) + ")"
	case u.ClientCertificateData != "" || u.ClientCertificate != "":
		return "certificado"
	case u.Token != "" || u.TokenFile != "":
		return "token"
	case u.Username != "":
		return "usuário e senha"
	case u.AuthProvider != nil:
		return "auth-provider " + u.AuthProvider.Name + " (não suportado)"
	}
	return "nenhuma"
}

func (c *Config) cluster(name string) *Cluster {
	for i := range c.Clusters {
		if c.Clusters[i].Name == name {
			return &c.Clusters[i].Cluster
		}
	}
	return nil
}

func (c *Config) user(name string) *AuthInfo {
	for i := range c.Users {
		if c.Users[i].Name == name {
			return &c.Users[i].User
		}
	}
	return nil
}

// Extract keeps only one context (with its cluster and user) as a new,
// standalone kubeconfig: what Gantry stores for a pasted cluster. File
// paths become inline data, so the copy works from anywhere.
func (c *Config) Extract(context string) ([]byte, error) {
	var ctx *struct {
		Name    string `yaml:"name"`
		Context struct {
			Cluster   string `yaml:"cluster"`
			User      string `yaml:"user"`
			Namespace string `yaml:"namespace"`
		} `yaml:"context"`
	}
	for i := range c.Contexts {
		if c.Contexts[i].Name == context {
			ctx = &c.Contexts[i]
		}
	}
	if ctx == nil {
		return nil, fmt.Errorf("contexto %q não existe no kubeconfig", context)
	}
	cl, u := c.cluster(ctx.Context.Cluster), c.user(ctx.Context.User)
	if cl == nil {
		return nil, fmt.Errorf("o contexto %q aponta para um cluster que não existe", context)
	}
	cc, uu := *cl, AuthInfo{}
	if u != nil {
		uu = *u
	}
	inline := func(path string) (string, error) {
		if path == "" {
			return "", nil
		}
		if !filepath.IsAbs(path) && !strings.HasPrefix(path, "~") {
			if c.dir == "" {
				return "", fmt.Errorf("o kubeconfig aponta para o arquivo relativo %s; use o caminho completo", path)
			}
			path = filepath.Join(c.dir, path)
		}
		b, err := os.ReadFile(Expand(path))
		if err != nil {
			return "", err
		}
		return base64.StdEncoding.EncodeToString(b), nil
	}
	var err error
	if cc.CertificateAuthority != "" {
		if cc.CertificateAuthorityData, err = inline(cc.CertificateAuthority); err != nil {
			return nil, err
		}
		cc.CertificateAuthority = ""
	}
	if uu.ClientCertificate != "" {
		if uu.ClientCertificateData, err = inline(uu.ClientCertificate); err != nil {
			return nil, err
		}
		uu.ClientCertificate = ""
	}
	if uu.TokenFile != "" {
		tok, err := inline(uu.TokenFile)
		if err != nil {
			return nil, err
		}
		b, _ := base64.StdEncoding.DecodeString(tok)
		uu.Token, uu.TokenFile = strings.TrimSpace(string(b)), ""
	}
	if uu.ClientKey != "" {
		if uu.ClientKeyData, err = inline(uu.ClientKey); err != nil {
			return nil, err
		}
		uu.ClientKey = ""
	}
	out := map[string]any{
		"apiVersion":      "v1",
		"kind":            "Config",
		"current-context": ctx.Name,
		"clusters":        []any{map[string]any{"name": ctx.Context.Cluster, "cluster": cc}},
		"users":           []any{map[string]any{"name": ctx.Context.User, "user": uu}},
		"contexts":        []any{map[string]any{"name": ctx.Name, "context": ctx.Context}},
	}
	return yaml.Marshal(out)
}

// SetServer points every cluster of the file to another address, checking
// the certificate against serverName (a k3s reached through a tunnel).
func (c *Config) SetServer(server, serverName string) {
	for i := range c.Clusters {
		c.Clusters[i].Cluster.Server = server
		if serverName != "" {
			c.Clusters[i].Cluster.TLSServerName = serverName
		}
	}
}

// Untrusted makes a kubeconfig that came from somewhere Gantry does not
// trust (the k3s.yaml read from inside a project VM) safe to use on this
// computer: no credential plugins or auth providers (they run commands
// here), no file paths (they would read local files and send them to the
// API server) and no proxy. Only inline certificates, keys and tokens stay.
func (c *Config) Untrusted() {
	for i := range c.Clusters {
		cl := &c.Clusters[i].Cluster
		cl.CertificateAuthority, cl.ProxyURL = "", ""
	}
	for i := range c.Users {
		u := &c.Users[i].User
		u.Exec, u.AuthProvider = nil, nil
		u.ClientCertificate, u.ClientKey, u.TokenFile = "", "", ""
	}
	c.dir = ""
}

// MissingPlugin says which credential plugin a context needs that is not
// installed on this computer ("" when none is missing), and how to get it.
func (c *Config) MissingPlugin(context string) (command, hint string) {
	var userName string
	for _, x := range c.Contexts {
		if x.Name == context {
			userName = x.Context.User
		}
	}
	u := c.user(userName)
	if u == nil || u.Exec == nil || u.Exec.Command == "" {
		return "", ""
	}
	cmdPath := u.Exec.Command
	if !filepath.IsAbs(cmdPath) && strings.Contains(cmdPath, string(filepath.Separator)) && c.dir != "" {
		cmdPath = filepath.Join(c.dir, cmdPath)
	}
	if _, err := exec.LookPath(cmdPath); err == nil {
		if filepath.Base(cmdPath) == "kubectl" && len(u.Exec.Args) > 0 && u.Exec.Args[0] == "oidc-login" {
			if _, err := exec.LookPath("kubectl-oidc_login"); err != nil {
				return "kubectl oidc-login", pluginHints["kubectl-oidc_login"]
			}
		}
		return "", ""
	}
	name := filepath.Base(u.Exec.Command)
	hint = pluginHints[name]
	if hint == "" {
		hint = strings.TrimSpace(u.Exec.InstallHint)
	}
	if hint == "" {
		hint = "instale o " + name + " e deixe-o no PATH"
	}
	return name, hint
}

// pluginHints install the credential plugins of the usual clouds.
var pluginHints = map[string]string{
	"aws":                    "brew install awscli",
	"aws-iam-authenticator":  "brew install aws-iam-authenticator",
	"gke-gcloud-auth-plugin": "gcloud components install gke-gcloud-auth-plugin",
	"gcloud":                 "brew install --cask google-cloud-sdk",
	"kubelogin":              "brew install Azure/kubelogin/kubelogin",
	"kubectl-oidc_login":     "brew install int128/kubelogin/kubelogin",
	"doctl":                  "brew install doctl",
	"oci":                    "brew install oci-cli",
	"kubectl":                "brew install kubectl",
}
