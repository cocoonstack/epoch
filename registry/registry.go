// Package registry stores and retrieves OCI blobs and manifests in S3,
// plus maintains a global catalog index. Vendor-agnostic; cocoonstack
// concepts live in the snapshot and cloudimg packages.
package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/cocoonstack/epoch/manifest"
	"github.com/cocoonstack/epoch/objectstore"
	"github.com/cocoonstack/epoch/utils"
)

const catalogCacheTTL = 10 * time.Second

// Registry is the storage facade. Safe for concurrent use.
type Registry struct {
	client *objectstore.Client

	catalogMu     sync.Mutex // guards read-modify-write of catalog.json
	catalogCache  []byte     // cached catalog.json bytes
	catalogExpiry time.Time  // expiry of catalogCache
}

// New creates a Registry backed by the given object store client.
func New(client *objectstore.Client) *Registry {
	return &Registry{client: client}
}

// NewFromEnv creates a Registry using S3 configuration from environment variables.
func NewFromEnv() (*Registry, error) {
	cfg, err := objectstore.ConfigFromEnv("epoch/")
	if err != nil {
		return nil, fmt.Errorf("load registry config: %w", err)
	}
	client, err := objectstore.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("init object store: %w", err)
	}
	return New(client), nil
}

// PushBlobFromStream uploads a blob, deduplicating if it already exists.
func (r *Registry) PushBlobFromStream(ctx context.Context, digest string, body io.Reader, size int64) error {
	if exists, _ := r.client.Exists(ctx, blobKey(digest)); exists {
		return nil
	}
	return r.client.Put(ctx, blobKey(digest), body, size)
}

// BlobExists reports whether a blob with the given digest exists.
func (r *Registry) BlobExists(ctx context.Context, digest string) (bool, error) {
	return r.client.Exists(ctx, blobKey(digest))
}

// StreamBlob returns a streaming reader and size. Caller must close the reader.
func (r *Registry) StreamBlob(ctx context.Context, digest string) (io.ReadCloser, int64, error) {
	return r.client.Get(ctx, blobKey(digest))
}

// BlobSize returns the size of a blob in bytes.
func (r *Registry) BlobSize(ctx context.Context, digest string) (int64, error) {
	return r.client.Head(ctx, blobKey(digest))
}

// DeleteBlob removes a blob from the object store.
func (r *Registry) DeleteBlob(ctx context.Context, digest string) error {
	return r.client.Delete(ctx, blobKey(digest))
}

// ManifestJSON fetches a manifest by repository name and tag.
func (r *Registry) ManifestJSON(ctx context.Context, name, tag string) ([]byte, error) {
	body, _, err := r.client.Get(ctx, manifestKey(name, tag))
	if err != nil {
		return nil, fmt.Errorf("get manifest %s:%s: %w", name, tag, err)
	}
	defer func() { _ = body.Close() }()
	return io.ReadAll(body)
}

// ManifestJSONByDigest fetches a manifest by repository name and content digest.
func (r *Registry) ManifestJSONByDigest(ctx context.Context, name, digest string) ([]byte, error) {
	body, _, err := r.client.Get(ctx, manifestDigestKey(name, digest))
	if err != nil {
		return nil, fmt.Errorf("get manifest %s@%s: %w", name, digest, err)
	}
	defer func() { _ = body.Close() }()
	return io.ReadAll(body)
}

// PushManifestJSON uploads a manifest under both the tag and digest keys, then updates the catalog.
func (r *Registry) PushManifestJSON(ctx context.Context, name, tag string, data []byte) error {
	if _, err := r.PushManifestJSONByDigest(ctx, name, data); err != nil {
		return err
	}

	tagKey := manifestKey(name, tag)
	if err := r.client.Put(ctx, tagKey, bytes.NewReader(data), int64(len(data))); err != nil {
		return fmt.Errorf("upload manifest %s:%s: %w", name, tag, err)
	}
	return r.updateCatalog(ctx, name, tag)
}

// PushManifestJSONByDigest stores a manifest by digest only (no tag, no catalog update).
// Used for OCI index child manifests. Returns the computed digest.
func (r *Registry) PushManifestJSONByDigest(ctx context.Context, name string, data []byte) (string, error) {
	digest := "sha256:" + utils.SHA256Hex(data)

	digestKey := manifestDigestKey(name, digest)
	if err := r.client.Put(ctx, digestKey, bytes.NewReader(data), int64(len(data))); err != nil {
		return "", fmt.Errorf("upload manifest %s@%s: %w", name, digest, err)
	}
	return digest, nil
}

// DeleteManifest removes a manifest tag and updates the catalog.
func (r *Registry) DeleteManifest(ctx context.Context, name, tag string) error {
	if err := r.client.Delete(ctx, manifestKey(name, tag)); err != nil {
		return fmt.Errorf("delete manifest %s:%s: %w", name, tag, err)
	}
	return r.removeCatalogEntry(ctx, name, tag)
}

// DeleteManifestByDigest removes the digest-addressed manifest copy. It errors
// with ErrNotFound if the manifest does not exist, so callers can surface the
// canonical 404 instead of a silent 202 after the object store swallows the
// missing-object error on Delete.
func (r *Registry) DeleteManifestByDigest(ctx context.Context, name, digest string) error {
	key := manifestDigestKey(name, digest)
	exists, err := r.client.Exists(ctx, key)
	if err != nil {
		return fmt.Errorf("head manifest %s@%s: %w", name, digest, err)
	}
	if !exists {
		return fmt.Errorf("manifest %s@%s: %w", name, digest, objectstore.ErrNotFound)
	}
	if err := r.client.Delete(ctx, key); err != nil {
		return fmt.Errorf("delete manifest %s@%s: %w", name, digest, err)
	}
	return nil
}

// ListTags returns all tags for a repository, skipping by-digest copies.
func (r *Registry) ListTags(ctx context.Context, name string) ([]string, error) {
	keys, err := r.client.List(ctx, "manifests/"+name+"/")
	if err != nil {
		return nil, fmt.Errorf("list manifests for %s: %w", name, err)
	}
	digestPrefix := "manifests/" + name + "/_digests/"
	tags := make([]string, 0, len(keys))
	for _, k := range keys {
		if strings.HasPrefix(k, digestPrefix) {
			continue
		}
		// k is like "manifests/sre-agent/v2.json"
		tag := extractTag(k)
		if tag != "" {
			tags = append(tags, tag)
		}
	}
	return tags, nil
}

// GetCatalog returns the parsed global catalog index.
func (r *Registry) GetCatalog(ctx context.Context) (*manifest.Catalog, error) {
	cat, _, err := r.GetCatalogWithDigest(ctx)
	return cat, err
}

// GetCatalogWithDigest returns the parsed catalog and its SHA-256 digest.
func (r *Registry) GetCatalogWithDigest(ctx context.Context) (*manifest.Catalog, string, error) {
	raw, err := r.getCatalogRaw(ctx)
	if err != nil {
		return nil, "", err
	}
	if raw == nil {
		return &manifest.Catalog{Repositories: make(map[string]*manifest.Repository)}, "", nil
	}

	var cat manifest.Catalog
	if err := json.Unmarshal(raw, &cat); err != nil {
		return nil, "", fmt.Errorf("decode catalog: %w", err)
	}
	if cat.Repositories == nil {
		cat.Repositories = make(map[string]*manifest.Repository)
	}

	return &cat, utils.SHA256Hex(raw), nil
}

// getCatalogRaw acquires catalogMu; use getCatalogRawLocked if already held.
func (r *Registry) getCatalogRaw(ctx context.Context) ([]byte, error) {
	r.catalogMu.Lock()
	defer r.catalogMu.Unlock()
	return r.getCatalogRawLocked(ctx)
}

// getCatalogRawLocked reads catalog bytes with cache. Caller must hold catalogMu.
func (r *Registry) getCatalogRawLocked(ctx context.Context) ([]byte, error) {
	if r.catalogCache != nil && time.Now().Before(r.catalogExpiry) {
		return r.catalogCache, nil
	}

	body, _, err := r.client.Get(ctx, "catalog.json")
	if errors.Is(err, objectstore.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get catalog: %w", err)
	}
	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("read catalog body: %w", err)
	}

	r.catalogCache = raw
	r.catalogExpiry = time.Now().Add(catalogCacheTTL)
	return raw, nil
}

func (r *Registry) getCatalogLocked(ctx context.Context) (*manifest.Catalog, error) {
	raw, err := r.getCatalogRawLocked(ctx)
	if err != nil {
		return nil, fmt.Errorf("get catalog: %w", err)
	}
	if raw == nil {
		return &manifest.Catalog{Repositories: make(map[string]*manifest.Repository)}, nil
	}
	var cat manifest.Catalog
	if err := json.Unmarshal(raw, &cat); err != nil {
		return nil, fmt.Errorf("decode catalog: %w", err)
	}
	if cat.Repositories == nil {
		cat.Repositories = make(map[string]*manifest.Repository)
	}
	return &cat, nil
}

func (r *Registry) invalidateCatalogCacheLocked() {
	r.catalogCache = nil
	r.catalogExpiry = time.Time{}
}

func (r *Registry) updateCatalog(ctx context.Context, name, tag string) error {
	r.catalogMu.Lock()
	defer r.catalogMu.Unlock()

	cat, err := r.getCatalogLocked(ctx)
	if err != nil {
		return fmt.Errorf("get catalog: %w", err)
	}

	repo, ok := cat.Repositories[name]
	if !ok {
		repo = &manifest.Repository{Tags: make(map[string]string)}
		cat.Repositories[name] = repo
	}
	repo.Tags[tag] = manifestKey(name, tag)
	repo.UpdatedAt = time.Now()
	cat.UpdatedAt = time.Now()

	return r.putCatalog(ctx, cat)
}

func (r *Registry) removeCatalogEntry(ctx context.Context, name, tag string) error {
	r.catalogMu.Lock()
	defer r.catalogMu.Unlock()

	cat, err := r.getCatalogLocked(ctx)
	if err != nil {
		return err
	}
	if repo, ok := cat.Repositories[name]; ok {
		delete(repo.Tags, tag)
		if len(repo.Tags) == 0 {
			delete(cat.Repositories, name)
		}
		cat.UpdatedAt = time.Now()
		return r.putCatalog(ctx, cat)
	}
	return nil
}

func (r *Registry) putCatalog(ctx context.Context, cat *manifest.Catalog) error {
	data, err := json.MarshalIndent(cat, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal catalog: %w", err)
	}
	if err := r.client.Put(ctx, "catalog.json", bytes.NewReader(data), int64(len(data))); err != nil {
		return fmt.Errorf("put catalog: %w", err)
	}
	r.invalidateCatalogCacheLocked()
	return nil
}

func blobKey(digest string) string {
	return "blobs/sha256/" + digest
}

func manifestKey(name, tag string) string {
	return "manifests/" + name + "/" + tag + ".json"
}

// manifestDigestKey replaces the colon in the digest with a dash for object store compatibility.
func manifestDigestKey(name, digest string) string {
	return "manifests/" + name + "/_digests/" + strings.ReplaceAll(digest, ":", "-") + ".json"
}

func extractTag(key string) string {
	// "manifests/foo/bar.json" → "bar"
	key, ok := strings.CutSuffix(key, ".json")
	if !ok {
		return ""
	}
	_, tag, found := strings.Cut(key, "/") // skip "manifests"
	if !found {
		return ""
	}
	_, tag, found = strings.Cut(tag, "/") // skip name
	if !found || tag == "" {
		return ""
	}
	return tag
}
