// Package registryclient is a small HTTP client for talking to an Epoch
// (or any OCI Distribution-compatible) registry. It implements just enough
// of the spec for epoch's CLI tools to push and pull blobs and manifests.
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
)

const (
	defaultBaseURL    = "http://127.0.0.1:4300"
	maxErrorBodyBytes = 512
)

// Client wraps the OCI Distribution endpoints epoch CLIs use.
//
// All methods take a repository `name` so multi-segment names like
// `library/nginx` work; the client never tries to interpret it.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// New creates a client for the given base URL and bearer token. Empty baseURL
// falls back to http://127.0.0.1:4300. The TLS config skips verification
// because epoch is commonly deployed behind a self-signed cert in dev.
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

// --- Manifests ---

// manifestAcceptHeader lists every manifest media type the client knows how
// to handle. Sent on every GET /v2/.../manifests/... request — Docker Hub /
// GHCR will return 415 or a different schema if it is missing.
const manifestAcceptHeader = "application/vnd.oci.image.manifest.v1+json, " +
	"application/vnd.oci.image.index.v1+json, " +
	"application/vnd.docker.distribution.manifest.v2+json, " +
	"application/vnd.docker.distribution.manifest.list.v2+json"

// GetManifest downloads a manifest's raw bytes for `name:tag` and returns
// them along with the server-supplied Content-Type. Callers that need to
// classify the manifest pass the bytes to manifest.Classify.
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
	if resp.StatusCode != http.StatusOK {
		return nil, "", c.statusError(http.MethodGet, rawURL, resp)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	return data, resp.Header.Get("Content-Type"), nil
}

// PutManifest uploads a manifest with the given content type. The OCI spec
// requires the Content-Type header to match the manifest's `mediaType` field;
// callers MUST pass the right value (typically MediaTypeOCIManifest).
func (c *Client) PutManifest(ctx context.Context, name, tag string, data []byte, contentType string) error {
	return c.putBytes(ctx, c.v2URL(name, "manifests", tag), contentType, bytes.NewReader(data), int64(len(data)), http.StatusCreated)
}

// --- Blobs ---

// BlobExists returns true if the blob is present in the repository's blob
// store. The digest must include the `sha256:` prefix.
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

// GetBlob downloads a blob and returns the body for the caller to consume.
// The digest must include the `sha256:` prefix. Caller must close the body.
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

// PutBlob uploads a blob via epoch's monolithic single-PUT shortcut at
// `PUT /v2/<name>/blobs/<digest>`. The digest must include the `sha256:`
// prefix.
func (c *Client) PutBlob(ctx context.Context, name, digest string, body io.Reader, size int64) error {
	return c.putBytes(ctx, c.v2URL(name, "blobs", digest), "application/octet-stream", body, size, http.StatusCreated)
}

// --- Catalog / tag listing ---

// Catalog calls `GET /v2/_catalog` and returns the repository list.
func (c *Client) Catalog(ctx context.Context) ([]string, error) {
	var resp struct {
		Repositories []string `json:"repositories"`
	}
	if err := c.getJSON(ctx, c.baseURL+"/v2/_catalog", &resp); err != nil {
		return nil, err
	}
	return resp.Repositories, nil
}

// ListTags calls `GET /v2/<name>/tags/list` and returns the tag list.
func (c *Client) ListTags(ctx context.Context, name string) ([]string, error) {
	var resp struct {
		Tags []string `json:"tags"`
	}
	if err := c.getJSON(ctx, fmt.Sprintf("%s/v2/%s/tags/list", c.baseURL, name), &resp); err != nil {
		return nil, err
	}
	return resp.Tags, nil
}

// DeleteManifest calls `DELETE /v2/<name>/manifests/<reference>`.
func (c *Client) DeleteManifest(ctx context.Context, name, reference string) error {
	rawURL := c.v2URL(name, "manifests", reference)
	resp, err := c.do(ctx, http.MethodDelete, rawURL, nil, "", -1)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
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

// --- HTTP plumbing ---

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
