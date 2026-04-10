// Package registry implements the storage layer of the Epoch OCI registry.
//
// It is intentionally vendor-agnostic: cocoonstack-specific concepts (cloud
// images, snapshots) live in the [snapshot] and [cloudimg] packages on top
// of these primitives. The registry only knows how to store and retrieve
// blobs and manifests in an S3-compatible object store, plus maintain a
// global catalog index for the control-plane API.
//
// Object layout in the bucket (under the configured prefix, default `epoch/`):
//
//	catalog.json                            — global repository index
//	manifests/<name>/<tag>.json             — manifest by tag
//	manifests/<name>/_digests/<dgst>.json   — manifest by content digest
//	blobs/sha256/<dgst>                     — content-addressable blob
//
// All blobs are stored under their unprefixed hex digest. Callers that have
// a `sha256:<hex>` form must strip the prefix before calling [Registry.StreamBlob],
// [Registry.PullBlob], etc.
package registry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/cocoonstack/epoch/manifest"
	"github.com/cocoonstack/epoch/objectstore"
)

const catalogCacheTTL = 10 * time.Second

// Registry is the storage facade for an Epoch OCI registry. It is safe for
// concurrent use.
type Registry struct {
	client *objectstore.Client

	catalogMu     sync.Mutex // guards read-modify-write of catalog.json
	catalogCache  []byte     // cached catalog.json bytes
	catalogExpiry time.Time  // expiry of catalogCache
}

// New creates a registry backed by the given object store client.
func New(client *objectstore.Client) *Registry {
	return &Registry{client: client}
}

// NewFromEnv creates a registry using object store credentials from the environment.
func NewFromEnv() (*Registry, error) {
	cfg, err := objectstore.ConfigFromEnv("epoch/")
	if err != nil {
		return nil, err
	}
	client, err := objectstore.New(cfg)
	if err != nil {
		return nil, err
	}
	return New(client), nil
}

// --- Blob operations ---

// PushBlobFromStream uploads a blob whose digest the caller has already
// computed. The digest must be the unprefixed lowercase hex SHA-256.
// Existing blobs are deduplicated.
func (r *Registry) PushBlobFromStream(ctx context.Context, digest string, body io.Reader, size int64) error {
	if exists, _ := r.client.Exists(ctx, blobKey(digest)); exists {
		return nil
	}
	return r.client.Put(ctx, blobKey(digest), body, size)
}

// BlobExists checks if a blob exists. The digest must be unprefixed hex.
func (r *Registry) BlobExists(ctx context.Context, digest string) (bool, error) {
	return r.client.Exists(ctx, blobKey(digest))
}

// StreamBlob returns a streaming reader for a blob and its size.
// Caller must close the returned ReadCloser. The digest must be unprefixed hex.
func (r *Registry) StreamBlob(ctx context.Context, digest string) (io.ReadCloser, int64, error) {
	return r.client.Get(ctx, blobKey(digest))
}

// BlobSize returns the size of a blob without fetching its body.
func (r *Registry) BlobSize(ctx context.Context, digest string) (int64, error) {
	return r.client.Head(ctx, blobKey(digest))
}

// DeleteBlob removes a blob from the object store.
func (r *Registry) DeleteBlob(ctx context.Context, digest string) error {
	return r.client.Delete(ctx, blobKey(digest))
}

// --- Manifest operations ---

// ManifestJSON returns the raw JSON bytes of a manifest looked up by tag.
func (r *Registry) ManifestJSON(ctx context.Context, name, tag string) ([]byte, error) {
	body, _, err := r.client.Get(ctx, manifestKey(name, tag))
	if err != nil {
		return nil, fmt.Errorf("get manifest %s:%s: %w", name, tag, err)
	}
	defer func() { _ = body.Close() }()
	return io.ReadAll(body)
}

// ManifestJSONByDigest returns the raw JSON bytes of a manifest looked up by
// content digest. OCI clients (go-containerregistry, docker, buildah) push by
// tag and then re-fetch by digest after resolving — this read path is what
// closes that loop.
func (r *Registry) ManifestJSONByDigest(ctx context.Context, name, digest string) ([]byte, error) {
	body, _, err := r.client.Get(ctx, manifestDigestKey(name, digest))
	if err != nil {
		return nil, fmt.Errorf("get manifest %s@%s: %w", name, digest, err)
	}
	defer func() { _ = body.Close() }()
	return io.ReadAll(body)
}

// PushManifestJSON uploads a manifest from raw JSON bytes and updates the
// catalog. The bytes are written under both the tag key and the content
// digest key, so the manifest can later be fetched by either reference. The
// digest write happens first (it is idempotent and content-addressed), so a
// failure between writes leaves no dangling tag pointer.
func (r *Registry) PushManifestJSON(ctx context.Context, name, tag string, data []byte) error {
	h := sha256.Sum256(data)
	digest := "sha256:" + hex.EncodeToString(h[:])

	digestKey := manifestDigestKey(name, digest)
	if err := r.client.Put(ctx, digestKey, bytes.NewReader(data), int64(len(data))); err != nil {
		return fmt.Errorf("upload manifest %s@%s: %w", name, digest, err)
	}

	tagKey := manifestKey(name, tag)
	if err := r.client.Put(ctx, tagKey, bytes.NewReader(data), int64(len(data))); err != nil {
		return fmt.Errorf("upload manifest %s:%s: %w", name, tag, err)
	}
	return r.updateCatalog(ctx, name, tag)
}

// DeleteManifest removes a manifest tag and updates the catalog. The
// content-addressed copy under `_digests/` is intentionally left in place so
// re-tagging by digest still works.
func (r *Registry) DeleteManifest(ctx context.Context, name, tag string) error {
	if err := r.client.Delete(ctx, manifestKey(name, tag)); err != nil {
		return err
	}
	return r.removeCatalogEntry(ctx, name, tag)
}

// ListTags returns all tags for a repository. Manifests stored under the
// per-name `_digests/` subdirectory (the by-digest copies) are skipped — they
// are content-addressed pointers to the same data, not user-facing tags.
func (r *Registry) ListTags(ctx context.Context, name string) ([]string, error) {
	keys, err := r.client.List(ctx, "manifests/"+name+"/")
	if err != nil {
		return nil, err
	}
	digestPrefix := "manifests/" + name + "/_digests/"
	var tags []string
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

// --- Catalog operations ---

// GetCatalog returns the global catalog.
func (r *Registry) GetCatalog(ctx context.Context) (*manifest.Catalog, error) {
	cat, _, err := r.GetCatalogWithDigest(ctx)
	return cat, err
}

// GetCatalogWithDigest returns the catalog together with the SHA-256 digest of its raw JSON.
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
		return nil, "", err
	}
	if cat.Repositories == nil {
		cat.Repositories = make(map[string]*manifest.Repository)
	}

	h := sha256.Sum256(raw)
	return &cat, hex.EncodeToString(h[:]), nil
}

// getCatalogRaw returns the raw catalog JSON bytes, populating the cache on
// miss. Acquires the catalog mutex; do not call from a context where the
// mutex is already held — use getCatalogRawLocked instead.
func (r *Registry) getCatalogRaw(ctx context.Context) ([]byte, error) {
	r.catalogMu.Lock()
	defer r.catalogMu.Unlock()
	return r.getCatalogRawLocked(ctx)
}

// getCatalogRawLocked is the cache-aware catalog reader. The caller must hold
// r.catalogMu — this is what lets updateCatalog and removeCatalogEntry call it
// without re-entering the (non-reentrant) mutex.
func (r *Registry) getCatalogRawLocked(ctx context.Context) ([]byte, error) {
	if r.catalogCache != nil && time.Now().Before(r.catalogExpiry) {
		return r.catalogCache, nil
	}

	body, _, err := r.client.Get(ctx, "catalog.json")
	if errors.Is(err, objectstore.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}

	r.catalogCache = raw
	r.catalogExpiry = time.Now().Add(catalogCacheTTL)
	return raw, nil
}

// getCatalogLocked returns the parsed catalog. Caller must hold r.catalogMu.
func (r *Registry) getCatalogLocked(ctx context.Context) (*manifest.Catalog, error) {
	raw, err := r.getCatalogRawLocked(ctx)
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return &manifest.Catalog{Repositories: make(map[string]*manifest.Repository)}, nil
	}
	var cat manifest.Catalog
	if err := json.Unmarshal(raw, &cat); err != nil {
		return nil, err
	}
	if cat.Repositories == nil {
		cat.Repositories = make(map[string]*manifest.Repository)
	}
	return &cat, nil
}

// invalidateCatalogCacheLocked drops the cached catalog. Caller must hold
// r.catalogMu — the function name and the locked-suffix convention exist to
// match the rest of the catalog accessors and prevent re-entrancy bugs.
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
		return err
	}
	if err := r.client.Put(ctx, "catalog.json", bytes.NewReader(data), int64(len(data))); err != nil {
		return err
	}
	r.invalidateCatalogCacheLocked()
	return nil
}

// --- Key helpers ---

func blobKey(digest string) string {
	return "blobs/sha256/" + digest
}

func manifestKey(name, tag string) string {
	return "manifests/" + name + "/" + tag + ".json"
}

// manifestDigestKey is the object store key for a manifest looked up by its
// content digest. The colon in `sha256:abc...` is replaced with a dash so the
// key works on object stores that disallow colons in path components.
func manifestDigestKey(name, digest string) string {
	return "manifests/" + name + "/_digests/" + strings.ReplaceAll(digest, ":", "-") + ".json"
}

func extractTag(key string) string {
	// "manifests/foo/bar.json" → "bar"
	idx := strings.LastIndex(key, "/")
	if idx < 0 {
		return ""
	}
	name, ok := strings.CutSuffix(key[idx+1:], ".json")
	if !ok || name == "" {
		return ""
	}
	return name
}
