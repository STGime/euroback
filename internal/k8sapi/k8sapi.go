// Package k8sapi is a minimal in-cluster Kubernetes API client for the two
// calls Eurobase needs: read and merge-patch one Secret's data. It uses the
// pod's service-account token (re-read on every request, since projected
// tokens rotate) and the cluster CA — no client-go dependency.
package k8sapi

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
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultSADir is where Kubernetes mounts the service-account credentials.
const DefaultSADir = "/var/run/secrets/kubernetes.io/serviceaccount"

// Client talks to the API server as the pod's service account.
type Client struct {
	base      string // https://host:port
	tokenPath string
	namespace string
	http      *http.Client
}

// InCluster builds a Client from the standard in-pod environment.
func InCluster() (*Client, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, errors.New("not running in a Kubernetes pod (KUBERNETES_SERVICE_HOST unset)")
	}
	ca, err := os.ReadFile(filepath.Join(DefaultSADir, "ca.crt"))
	if err != nil {
		return nil, fmt.Errorf("read cluster CA: %w", err)
	}
	ns, err := os.ReadFile(filepath.Join(DefaultSADir, "namespace"))
	if err != nil {
		return nil, fmt.Errorf("read namespace: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, errors.New("cluster CA: no certificates")
	}
	return New("https://"+net.JoinHostPort(host, port), filepath.Join(DefaultSADir, "token"),
		strings.TrimSpace(string(ns)), &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}), nil
}

// New builds a Client (tests pass an httptest server's URL and TLS config).
func New(base, tokenPath, namespace string, tlsConf *tls.Config) *Client {
	return &Client{base: base, tokenPath: tokenPath, namespace: namespace,
		http: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: tlsConf}}}
}

// ErrNotFound is returned when the Secret does not exist.
var ErrNotFound = errors.New("k8s: not found")

// GetSecretData returns the Secret's decoded data.
func (c *Client) GetSecretData(ctx context.Context, name string) (map[string][]byte, error) {
	body, err := c.do(ctx, http.MethodGet, name, "", nil)
	if err != nil {
		return nil, err
	}
	var s struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, fmt.Errorf("decode secret: %w", err)
	}
	out := make(map[string][]byte, len(s.Data))
	for k, v := range s.Data {
		b, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			return nil, fmt.Errorf("decode secret key %q: %w", k, err)
		}
		out[k] = b
	}
	return out, nil
}

// PatchSecretData merge-patches the given keys into the Secret's data.
func (c *Client) PatchSecretData(ctx context.Context, name string, data map[string][]byte) error {
	enc := make(map[string]string, len(data))
	for k, v := range data {
		enc[k] = base64.StdEncoding.EncodeToString(v)
	}
	patch, err := json.Marshal(map[string]any{"data": enc})
	if err != nil {
		return err
	}
	_, err = c.do(ctx, http.MethodPatch, name, "application/merge-patch+json", patch)
	return err
}

func (c *Client) do(ctx context.Context, method, name, contentType string, body []byte) ([]byte, error) {
	token, err := os.ReadFile(c.tokenPath)
	if err != nil {
		return nil, fmt.Errorf("read service-account token: %w", err)
	}
	url := fmt.Sprintf("%s/api/v1/namespaces/%s/secrets/%s", c.base, c.namespace, name)
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrNotFound
	case resp.StatusCode >= 300:
		return nil, fmt.Errorf("k8s %s secret %s: %s", method, name, resp.Status)
	}
	return b, nil
}
