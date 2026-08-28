// Package k8sclient is a deliberately dependency-free client for the
// Kubernetes API server. It exists so gpu-guardian-operator can be built
// and audited with zero third-party dependencies -- only Go's standard
// library. In a larger production codebase this would typically be
// swapped for client-go + controller-runtime informers/caches, but for an
// operator this narrowly scoped, a thin REST client keeps the binary
// small, the supply chain minimal, and every HTTP call auditable.
package k8sclient

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const (
	saTokenPath  = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	saCACertPath = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
)

// Client talks to the Kubernetes API server using the pod's mounted
// ServiceAccount credentials (in-cluster) or a supplied bearer token +
// host for local/dev use against `kubectl proxy` or a kind cluster.
type Client struct {
	httpClient *http.Client
	host       string
	token      string
}

// NewInClusterClient builds a Client from the standard ServiceAccount
// projection Kubernetes mounts into every pod.
func NewInClusterClient() (*Client, error) {
	host := os.Getenv("KUBERNETES_SERVICE_HOST")
	port := os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, fmt.Errorf("not running in-cluster: KUBERNETES_SERVICE_HOST/PORT unset")
	}

	tokenBytes, err := os.ReadFile(saTokenPath)
	if err != nil {
		return nil, fmt.Errorf("reading service account token: %w", err)
	}

	caCert, err := os.ReadFile(saCACertPath)
	if err != nil {
		return nil, fmt.Errorf("reading service account CA cert: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("failed to parse service account CA cert")
	}

	return &Client{
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{RootCAs: pool},
			},
		},
		host:  fmt.Sprintf("https://%s:%s", host, port),
		token: string(tokenBytes),
	}, nil
}

// NewDevClient builds a Client for local development, e.g. against
// `kubectl proxy` (no TLS, no auth needed) which is the fastest way to
// exercise this operator against a kind/minikube cluster while iterating.
func NewDevClient(host string) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 15 * time.Second},
		host:       host,
	}
}

func (c *Client) do(method, path string, body any, out any) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, c.host+path, reqBody)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	switch method {
	case http.MethodPatch:
		req.Header.Set("Content-Type", "application/strategic-merge-patch+json")
	default:
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: unexpected status %d: %s", method, path, resp.StatusCode, string(respBody))
	}
	if out != nil {
		return json.Unmarshal(respBody, out)
	}
	return nil
}

// Get issues a GET against the given API path and decodes the JSON
// response into out.
func (c *Client) Get(path string, out any) error {
	return c.do(http.MethodGet, path, nil, out)
}

// Patch issues a strategic-merge-patch PATCH against the given API path.
func (c *Client) Patch(path string, patch any, out any) error {
	return c.do(http.MethodPatch, path, patch, out)
}

// Post issues a POST against the given API path.
func (c *Client) Post(path string, body any, out any) error {
	return c.do(http.MethodPost, path, body, out)
}
