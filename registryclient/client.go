package registryclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/cocoonstack/epoch/manifest"
)

const (
	defaultBaseURL      = "http://127.0.0.1:4300"
	manifestContentType = "application/vnd.epoch.manifest.v1+json"
	maxErrorBodyBytes   = 512
)

// Client wraps the Epoch HTTP API and registry endpoints.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// New creates a client for the given base URL and bearer token.
func New(baseURL, token string) *Client {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")

	return &Client{
		baseURL: baseURL,
		token:   strings.TrimSpace(token),
		httpClient: &http.Client{
			Transport: &http.Transport{
				TLSClientConfig:     &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // registry may use self-signed certs
				MaxIdleConnsPerHost: 4,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// BaseURL returns the configured base URL.
func (c *Client) BaseURL() string {
	return c.baseURL
}

// GetJSON performs a GET on /api/... and decodes JSON into out.
func (c *Client) GetJSON(ctx context.Context, path string, out any) error {
	rawURL := c.apiURL(path)
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

// Delete performs a DELETE on /api/... and expects a 2xx response.
func (c *Client) Delete(ctx context.Context, path string) error {
	rawURL := c.apiURL(path)
	resp, err := c.do(ctx, http.MethodDelete, rawURL, nil, "", -1)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return c.statusError(http.MethodDelete, rawURL, resp)
	}
	return nil
}

// GetManifestJSON downloads the raw manifest JSON for name:tag.
func (c *Client) GetManifestJSON(ctx context.Context, name, tag string) ([]byte, error) {
	return c.getBytes(ctx, http.MethodGet, c.v2URL(name, "manifests", tag), http.StatusOK)
}

// GetManifest downloads and decodes a manifest for name:tag.
func (c *Client) GetManifest(ctx context.Context, name, tag string) (*manifest.Manifest, error) {
	data, err := c.GetManifestJSON(ctx, name, tag)
	if err != nil {
		return nil, err
	}
	var m manifest.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("decode manifest %s:%s: %w", name, tag, err)
	}
	return &m, nil
}

// PutManifestJSON uploads raw manifest JSON for name:tag.
func (c *Client) PutManifestJSON(ctx context.Context, name, tag string, data []byte) error {
	return c.putBytes(ctx, c.v2URL(name, "manifests", tag), manifestContentType, bytes.NewReader(data), int64(len(data)), http.StatusCreated)
}

// BlobExists checks whether a blob exists in a repository.
func (c *Client) BlobExists(ctx context.Context, name, digest string) (bool, error) {
	rawURL := c.v2URL(name, "blobs", "sha256:"+digest)
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

// GetBlob downloads a blob and returns the body for the caller to consume.
func (c *Client) GetBlob(ctx context.Context, name, digest string) (io.ReadCloser, error) {
	rawURL := c.v2URL(name, "blobs", "sha256:"+digest)
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

// PutBlob uploads a blob with a fixed content length.
func (c *Client) PutBlob(ctx context.Context, name, digest string, body io.Reader, size int64) error {
	return c.putBytes(ctx, c.v2URL(name, "blobs", "sha256:"+digest), "application/octet-stream", body, size, http.StatusCreated)
}

func (c *Client) apiURL(path string) string {
	path = strings.TrimLeft(path, "/")
	return c.baseURL + "/api/" + path
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

func (c *Client) getBytes(ctx context.Context, method, rawURL string, wantStatus int) ([]byte, error) {
	resp, err := c.do(ctx, method, rawURL, nil, "", -1)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != wantStatus {
		return nil, c.statusError(method, rawURL, resp)
	}
	return io.ReadAll(resp.Body)
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
