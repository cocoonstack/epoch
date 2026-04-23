// Package registryclient is a small HTTP client for OCI Distribution registries.
package registryclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	defaultBaseURL    = "http://127.0.0.1:8080"
	maxErrorBodyBytes = 512

	manifestAcceptHeader = "application/vnd.oci.image.manifest.v1+json, " +
		"application/vnd.oci.image.index.v1+json, " +
		"application/vnd.docker.distribution.manifest.v2+json, " +
		"application/vnd.docker.distribution.manifest.list.v2+json"
)

// ErrManifestNotFound distinguishes 404 from transport errors.
var ErrManifestNotFound = errors.New("registryclient: manifest not found")

// Client is an HTTP client for OCI Distribution registries.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// Option configures a Client. It returns an error so misconfigured
// options (e.g. unreadable CA cert) surface to the caller instead of
// panicking.
type Option func(*Client) error

// WithCACert loads a PEM-encoded CA certificate file and adds it to the
// TLS root pool so the client can verify registries using custom CAs.
func WithCACert(path string) Option {
	return func(c *Client) error {
		pem, err := os.ReadFile(path) //nolint:gosec // CA cert path from trusted caller configuration
		if err != nil {
			return fmt.Errorf("read CA cert %s: %w", path, err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return fmt.Errorf("no valid certificates in %s", path)
		}
		c.httpClient.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: pool} //nolint:gosec // CA pool explicitly configured by caller
		return nil
	}
}

// New creates a client. Empty baseURL defaults to http://127.0.0.1:8080.
func New(baseURL, token string, opts ...Option) (*Client, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")

	c := &Client{
		baseURL: baseURL,
		token:   strings.TrimSpace(token),
		httpClient: &http.Client{
			Transport: &http.Transport{
				MaxIdleConnsPerHost: 4,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
	for _, opt := range opts {
		if err := opt(c); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// BaseURL returns the registry base URL.
func (c *Client) BaseURL() string {
	return c.baseURL
}

// GetManifest downloads a manifest. Returns ErrManifestNotFound on 404.
func (c *Client) GetManifest(ctx context.Context, name, tag string) ([]byte, string, error) {
	rawURL := c.v2URL(name, "manifests", tag)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", manifestAcceptHeader)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.httpClient.Do(req) //nolint:gosec // baseURL is configured by trusted caller
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
		// fall through
	case http.StatusNotFound:
		return nil, "", fmt.Errorf("%s %s: %w", http.MethodGet, rawURL, ErrManifestNotFound)
	default:
		return nil, "", c.statusError(http.MethodGet, rawURL, resp)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	return data, resp.Header.Get("Content-Type"), nil
}

// PutManifest uploads a manifest under the given tag.
func (c *Client) PutManifest(ctx context.Context, name, tag string, data []byte, contentType string) error {
	return c.putBytes(ctx, c.v2URL(name, "manifests", tag), contentType, bytes.NewReader(data), int64(len(data)), http.StatusCreated)
}

// BlobExists checks whether a blob with the given digest exists.
func (c *Client) BlobExists(ctx context.Context, name, digest string) (bool, error) {
	rawURL := c.v2URL(name, "blobs", digest)
	resp, err := c.do(ctx, http.MethodHead, rawURL, nil, "", -1)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, c.statusError(http.MethodHead, rawURL, resp)
	}
}

// GetBlob downloads a blob. Caller must close the body.
func (c *Client) GetBlob(ctx context.Context, name, digest string) (io.ReadCloser, error) {
	rawURL := c.v2URL(name, "blobs", digest)
	resp, err := c.do(ctx, http.MethodGet, rawURL, nil, "", -1)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		return nil, c.statusError(http.MethodGet, rawURL, resp)
	}
	return resp.Body, nil
}

// PutBlob uploads a blob with the given digest.
func (c *Client) PutBlob(ctx context.Context, name, digest string, body io.Reader, size int64) error {
	return c.putBytes(ctx, c.v2URL(name, "blobs", digest), "application/octet-stream", body, size, http.StatusCreated)
}

// Catalog returns all repository names from /v2/_catalog.
func (c *Client) Catalog(ctx context.Context) ([]string, error) {
	var resp struct {
		Repositories []string `json:"repositories"`
	}
	if err := c.getJSON(ctx, c.baseURL+"/v2/_catalog", &resp); err != nil {
		return nil, err
	}
	return resp.Repositories, nil
}

// ListTags returns all tags for a repository.
func (c *Client) ListTags(ctx context.Context, name string) ([]string, error) {
	var resp struct {
		Tags []string `json:"tags"`
	}
	if err := c.getJSON(ctx, fmt.Sprintf("%s/v2/%s/tags/list", c.baseURL, name), &resp); err != nil {
		return nil, err
	}
	return resp.Tags, nil
}

// DeleteManifest removes a manifest. A 404 (already absent) is treated as success.
func (c *Client) DeleteManifest(ctx context.Context, name, reference string) error {
	rawURL := c.v2URL(name, "manifests", reference)
	resp, err := c.do(ctx, http.MethodDelete, rawURL, nil, "", -1)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return c.statusError(http.MethodDelete, rawURL, resp)
	}
	return nil
}

func (c *Client) getJSON(ctx context.Context, rawURL string, out any) error {
	resp, err := c.do(ctx, http.MethodGet, rawURL, nil, "", -1)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return c.statusError(http.MethodGet, rawURL, resp)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) v2URL(name, kind, ref string) string {
	return fmt.Sprintf("%s/v2/%s/%s/%s", c.baseURL, name, kind, ref)
}

func (c *Client) do(ctx context.Context, method, rawURL string, body io.Reader, contentType string, contentLength int64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if contentLength >= 0 {
		req.ContentLength = contentLength
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return c.httpClient.Do(req) //nolint:gosec // registry baseURL is configured by trusted caller
}

func (c *Client) putBytes(ctx context.Context, rawURL, contentType string, body io.Reader, size int64, wantStatus int) error {
	resp, err := c.do(ctx, http.MethodPut, rawURL, body, contentType, size)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != wantStatus {
		return c.statusError(http.MethodPut, rawURL, resp)
	}
	return nil
}

func (c *Client) statusError(method, rawURL string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	if len(body) == 0 {
		return fmt.Errorf("%s %s: %d", method, rawURL, resp.StatusCode)
	}
	return fmt.Errorf("%s %s: %d %s", method, rawURL, resp.StatusCode, strings.TrimSpace(string(body)))
}
