package kube

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Client talks to one cluster, as one context of a kubeconfig.
type Client struct {
	Server    string
	Namespace string // the context's default namespace
	tlsConf   *tls.Config
	http      *http.Client
	user      AuthInfo
	dir       string

	mu       sync.Mutex
	token    string
	tokenExp time.Time
	execCert *tls.Certificate
	execRun  chan struct{}        // closed when the running plugin finishes
	execErr  error                // the last plugin error, for whoever waited on it
	disc     map[string]discEntry // discovery, per apiVersion
	perms    map[permKey]permAnswer
	crds     *crdCache
	watches  map[string]*watchCache
	// OnPodLeaving: a pod seen by a watch of pods starts to go.
	OnPodLeaving func(LeavingPod)
	small        map[string]timed
}

// APIError is an answer of the API server with an error status.
type APIError struct {
	Status  int
	Message string
	Reason  string
	// Causes: what is wrong in the object, field by field (validation).
	Causes []Cause
}

// Cause is one problem of an object the cluster refused.
type Cause struct {
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"` // spec.template.spec.containers[0].image
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("o cluster respondeu %d", e.Status)
}

// NotFound says whether err is a 404 of the API.
func NotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

// NewClient builds the client of a context ("" is the current one).
func (c *Config) NewClient(context string) (*Client, error) {
	if context == "" {
		context = c.CurrentContext
	}
	var clusterName, userName, ns string
	found := false
	for _, x := range c.Contexts {
		if x.Name == context {
			clusterName, userName, ns, found = x.Context.Cluster, x.Context.User, x.Context.Namespace, true
		}
	}
	if !found {
		return nil, fmt.Errorf("contexto %q não existe", context)
	}
	cl := c.cluster(clusterName)
	if cl == nil || cl.Server == "" {
		return nil, fmt.Errorf("o contexto %q não tem o endereço do cluster", context)
	}
	k := &Client{Server: strings.TrimRight(cl.Server, "/"), Namespace: ns, dir: c.dir}
	if u := c.user(userName); u != nil {
		k.user = *u
	}
	if k.user.AuthProvider != nil && k.user.Exec == nil {
		return nil, fmt.Errorf("o kubeconfig usa auth-provider %q, que não é suportado; para GKE, instale o gke-gcloud-auth-plugin e rode gcloud container clusters get-credentials de novo", k.user.AuthProvider.Name)
	}
	tc := &tls.Config{InsecureSkipVerify: cl.InsecureSkipTLSVerify, ServerName: cl.TLSServerName}
	if ca, err := k.read(cl.CertificateAuthorityData, cl.CertificateAuthority); err != nil {
		return nil, fmt.Errorf("certificado do cluster: %v", err)
	} else if len(ca) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(ca) {
			return nil, errors.New("certificado do cluster inválido")
		}
		tc.RootCAs = pool
	}
	cert, err := k.read(k.user.ClientCertificateData, k.user.ClientCertificate)
	if err != nil {
		return nil, fmt.Errorf("certificado do usuário: %v", err)
	}
	if len(cert) > 0 {
		key, err := k.read(k.user.ClientKeyData, k.user.ClientKey)
		if err != nil {
			return nil, fmt.Errorf("chave do usuário: %v", err)
		}
		pair, err := tls.X509KeyPair(cert, key)
		if err != nil {
			return nil, fmt.Errorf("certificado do usuário: %v", err)
		}
		tc.Certificates = []tls.Certificate{pair}
	}
	if k.user.Exec != nil {
		// The plugin may hand a client certificate instead of a token.
		tc.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			k.mu.Lock()
			defer k.mu.Unlock()
			if k.execCert != nil {
				return k.execCert, nil
			}
			return &tls.Certificate{}, nil
		}
	}
	k.tlsConf = tc
	tr := &http.Transport{TLSClientConfig: tc, Proxy: http.ProxyFromEnvironment, IdleConnTimeout: 90 * time.Second, ForceAttemptHTTP2: true,
		// Long streams (logs, watches) sit quiet for minutes: pings keep load
		// balancers and NATs from dropping them and notice a dead connection.
		HTTP2: &http.HTTP2Config{SendPingTimeout: 30 * time.Second, PingTimeout: 15 * time.Second}}
	if cl.ProxyURL != "" {
		if pu, err := url.Parse(cl.ProxyURL); err == nil {
			tr.Proxy = http.ProxyURL(pu)
		}
	}
	k.http = &http.Client{Transport: tr}
	return k, nil
}

func (k *Client) read(data, path string) ([]byte, error) {
	if data != "" {
		return base64.StdEncoding.DecodeString(strings.TrimSpace(data))
	}
	if path == "" {
		return nil, nil
	}
	if !filepath.IsAbs(path) && k.dir != "" {
		path = filepath.Join(k.dir, path)
	}
	return os.ReadFile(Expand(path))
}

// TLS is the TLS configuration of the cluster (for WebSocket dials).
func (k *Client) TLS() *tls.Config { return k.tlsConf.Clone() }

// authorize adds the credentials to a request.
func (k *Client) authorize(ctx context.Context, h http.Header) error {
	u := k.user
	switch {
	case u.Exec != nil:
		tok, err := k.execToken(ctx)
		if err != nil {
			return err
		}
		if tok != "" {
			h.Set("Authorization", "Bearer "+tok)
		}
	case u.Token != "":
		h.Set("Authorization", "Bearer "+u.Token)
	case u.TokenFile != "":
		b, err := os.ReadFile(Expand(u.TokenFile))
		if err != nil {
			return err
		}
		h.Set("Authorization", "Bearer "+strings.TrimSpace(string(b)))
	case u.Username != "":
		h.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(u.Username+":"+u.Password)))
	}
	return nil
}

// execToken runs the credential plugin (cached until it expires). Only one
// run happens at a time; the others wait for it (or for their ctx) without
// holding the lock, so TLS handshakes and other clusters do not stall.
func (k *Client) execToken(ctx context.Context) (string, error) {
	for {
		k.mu.Lock()
		if (k.token != "" || k.execCert != nil) && (k.tokenExp.IsZero() || time.Until(k.tokenExp) > time.Minute) {
			tok := k.token
			k.mu.Unlock()
			return tok, nil
		}
		if wait := k.execRun; wait != nil {
			k.mu.Unlock()
			select {
			case <-wait:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			k.mu.Lock()
			err := k.execErr
			ok := k.token != "" || k.execCert != nil
			k.mu.Unlock()
			if err != nil && !ok {
				return "", err
			}
			continue
		}
		done := make(chan struct{})
		k.execRun = done
		k.mu.Unlock()

		tok, exp, cert, err := k.runPlugin()

		k.mu.Lock()
		k.execRun, k.execErr = nil, err
		if err == nil {
			k.token, k.tokenExp = tok, exp
			if cert != nil {
				k.execCert = cert
			}
		}
		k.mu.Unlock()
		close(done)
		if err != nil {
			return "", err
		}
		return tok, nil
	}
}

// runPlugin runs the exec credential plugin once. It does not use the
// caller's context: others may be waiting for the same answer.
func (k *Client) runPlugin() (string, time.Time, *tls.Certificate, error) {
	e := k.user.Exec
	cmdPath := e.Command
	if !filepath.IsAbs(cmdPath) && strings.Contains(cmdPath, string(filepath.Separator)) && k.dir != "" {
		cmdPath = filepath.Join(k.dir, cmdPath)
	}
	if _, err := exec.LookPath(cmdPath); err != nil {
		hint := pluginHints[filepath.Base(e.Command)]
		if hint == "" {
			hint = strings.TrimSpace(e.InstallHint)
		}
		if hint == "" {
			hint = "instale o programa e deixe-o no PATH"
		}
		return "", time.Time{}, nil, fmt.Errorf("o cluster entra com o %s, que não está instalado neste computador. Para instalar: %s", filepath.Base(e.Command), hint)
	}
	cctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, cmdPath, e.Args...)
	cmd.Env = os.Environ()
	for _, v := range e.Env {
		cmd.Env = append(cmd.Env, v.Name+"="+v.Value)
	}
	api := e.APIVersion
	if api == "" {
		api = "client.authentication.k8s.io/v1beta1"
	}
	info, _ := json.Marshal(map[string]any{"apiVersion": api, "kind": "ExecCredential", "spec": map[string]any{"interactive": false}})
	cmd.Env = append(cmd.Env, "KUBERNETES_EXEC_INFO="+string(info))
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", time.Time{}, nil, fmt.Errorf("%s: %s", filepath.Base(e.Command), msg)
	}
	var cred struct {
		Status struct {
			Token                 string    `json:"token"`
			ExpirationTimestamp   time.Time `json:"expirationTimestamp"`
			ClientCertificateData string    `json:"clientCertificateData"`
			ClientKeyData         string    `json:"clientKeyData"`
		} `json:"status"`
	}
	if err := json.Unmarshal(out.Bytes(), &cred); err != nil {
		return "", time.Time{}, nil, fmt.Errorf("%s respondeu algo inesperado: %v", filepath.Base(e.Command), err)
	}
	var cert *tls.Certificate
	if cred.Status.ClientCertificateData != "" {
		pair, err := tls.X509KeyPair([]byte(cred.Status.ClientCertificateData), []byte(cred.Status.ClientKeyData))
		if err != nil {
			return "", time.Time{}, nil, err
		}
		cert = &pair
	}
	if cred.Status.Token == "" && cert == nil {
		return "", time.Time{}, nil, fmt.Errorf("%s não devolveu token nem certificado", filepath.Base(e.Command))
	}
	return cred.Status.Token, cred.Status.ExpirationTimestamp, cert, nil
}

// Do sends a request to the API server. body may be nil, []byte or any
// value (sent as JSON); ctype overrides the content type.
func (k *Client) Do(ctx context.Context, method, path string, q url.Values, body any, ctype string) (*http.Response, error) {
	u := k.Server + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	case io.Reader:
		rd = b
	default:
		j, err := json.Marshal(b)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(j)
		if ctype == "" {
			ctype = "application/json"
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	req.Header.Set("Accept", "application/json")
	if a, ok := ctx.Value(acceptKey{}).(string); ok {
		req.Header.Set("Accept", a)
	}
	req.Header.Set("User-Agent", "gantry")
	if err := k.authorize(ctx, req.Header); err != nil {
		return nil, err
	}
	resp, err := k.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			return nil, fmt.Errorf("não consegui falar com o cluster (%s): %v", k.Server, ue.Err)
		}
		return nil, err
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		var st struct {
			Message, Reason string
			Details         struct {
				Causes []Cause `json:"causes"`
			} `json:"details"`
		}
		_ = json.Unmarshal(b, &st)
		if st.Message == "" {
			st.Message = strings.TrimSpace(string(b))
		}
		if resp.StatusCode == http.StatusUnauthorized {
			k.mu.Lock()
			k.token, k.execCert = "", nil // a fresh token next time
			k.mu.Unlock()
			if st.Message == "" || st.Message == "Unauthorized" {
				st.Message = "o cluster recusou as credenciais (401)"
			}
		}
		return nil, &APIError{Status: resp.StatusCode, Message: st.Message, Reason: st.Reason, Causes: st.Details.Causes}
	}
	return resp, nil
}

// Get reads a JSON document.
func (k *Client) Get(ctx context.Context, path string, q url.Values, out any) error {
	resp, err := k.Do(ctx, http.MethodGet, path, q, nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

// Raw returns the body of a GET (for YAML views and log tails).
func (k *Client) Raw(ctx context.Context, path string, q url.Values, limit int64) ([]byte, error) {
	resp, err := k.Do(ctx, http.MethodGet, path, q, nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// Version is the cluster's Kubernetes version.
// Transport is the HTTP transport with the cluster's TLS settings.
func (k *Client) Transport() http.RoundTripper { return k.http.Transport }

// AuthorizeRequest adds the cluster's credentials to an outgoing request.
func (k *Client) AuthorizeRequest(r *http.Request) error { return k.authorize(r.Context(), r.Header) }

type acceptKey struct{}

// metaOnly asks the API for the metadata of the objects only
// (PartialObjectMetadataList): names, labels, no specs nor Secret data.
func metaOnly(ctx context.Context) context.Context {
	return context.WithValue(ctx, acceptKey{}, "application/json;as=PartialObjectMetadataList;g=meta.k8s.io;v=v1,application/json")
}
