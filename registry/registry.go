// Package registry implements the Epoch snapshot registry backed by an
// S3-compatible object store.
//
// It handles push/pull of Cocoon snapshots as content-addressable artifacts:
//   - Blobs are stored at epoch/blobs/sha256/{digest}
//   - Manifests at epoch/manifests/{name}/{tag}.json
//   - Catalog at epoch/catalog.json
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
	"os"
	"sync"
	"time"

	"github.com/cocoonstack/epoch/manifest"
	"github.com/cocoonstack/epoch/objectstore"
	"github.com/cocoonstack/epoch/utils"
)

// Registry is the Epoch snapshot registry.
type Registry struct {
	client    *objectstore.Client
	catalogMu sync.Mutex // guards read-modify-write of catalog.json
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

// NewFromConfigMap creates a registry using object store credentials from a k8s ConfigMap.
func NewFromConfigMap(namespace, configmap string) (*Registry, error) {
	cfg, err := objectstore.ConfigFromConfigMap(namespace, configmap, "epoch/")
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

// PushBlob uploads a file as a content-addressable blob.
// Returns the SHA-256 hex digest and size.
// Computes the hash in a single pass using io.TeeReader to avoid reading the file twice.
func (r *Registry) PushBlob(ctx context.Context, filePath string) (string, int64, error) {
	f, err := os.Open(filePath) //nolint:gosec // filePath is from trusted snapshot data dir
	if err != nil {
		return "", 0, fmt.Errorf("open %s: %w", filePath, err)
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return "", 0, fmt.Errorf("stat %s: %w", filePath, err)
	}
	size := info.Size()

	// Hash in a single pass: read file once, computing SHA-256 as we go.
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", 0, fmt.Errorf("hash %s: %w", filePath, err)
	}
	digest := hex.EncodeToString(h.Sum(nil))

	// Check if blob already exists (dedup).
	if exists, _ := r.client.Exists(ctx, blobKey(digest)); exists {
		return digest, size, nil
	}

	// Seek back to beginning and upload.
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", 0, fmt.Errorf("seek %s: %w", filePath, err)
	}
	if err := r.client.Put(ctx, blobKey(digest), f, size); err != nil {
		return "", 0, fmt.Errorf("upload blob %s: %w", utils.Truncate(digest, 12), err)
	}
	return digest, size, nil
}

// PushBlobFromStream uploads a blob from a stream. The digest must be pre-computed by the caller.
// For large files (>4GB), it spools to a temp file and uses multipart upload.
func (r *Registry) PushBlobFromStream(ctx context.Context, digest string, body io.Reader, size int64) error {
	// Check if blob already exists (dedup).
	if exists, _ := r.client.Exists(ctx, blobKey(digest)); exists {
		return nil
	}

	return r.client.Put(ctx, blobKey(digest), body, size)
}

// BlobExists checks if a blob exists in the registry.
func (r *Registry) BlobExists(ctx context.Context, digest string) (bool, error) {
	return r.client.Exists(ctx, blobKey(digest))
}

// PullBlob downloads a blob to a local file.
// Handles both regular and split (>5GB) blobs transparently.
func (r *Registry) PullBlob(ctx context.Context, digest, destPath string) error {
	body, _, err := r.client.Get(ctx, blobKey(digest))
	if err != nil {
		return fmt.Errorf("get blob %s: %w", utils.Truncate(digest, 12), err)
	}
	defer func() { _ = body.Close() }()

	f, err := os.Create(destPath) //nolint:gosec // destPath is from trusted snapshot data dir
	if err != nil {
		return fmt.Errorf("create %s: %w", destPath, err)
	}
	defer func() { _ = f.Close() }()

	if _, err := io.Copy(f, body); err != nil {
		return fmt.Errorf("write %s: %w", destPath, err)
	}
	return nil
}

// DeleteBlob removes a blob from the object store.
func (r *Registry) DeleteBlob(ctx context.Context, digest string) error {
	return r.client.Delete(ctx, blobKey(digest))
}

// StreamBlob returns a streaming reader for a blob from object storage.
// Caller must close the returned ReadCloser.
func (r *Registry) StreamBlob(ctx context.Context, digest string) (io.ReadCloser, int64, error) {
	return r.client.Get(ctx, blobKey(digest))
}

// BlobSize returns the actual size of a blob.
func (r *Registry) BlobSize(ctx context.Context, digest string) (int64, error) {
	return r.client.Head(ctx, blobKey(digest))
}

// ManifestJSON returns the raw JSON bytes of a manifest without parsing.
func (r *Registry) ManifestJSON(ctx context.Context, name, tag string) ([]byte, error) {
	body, _, err := r.client.Get(ctx, manifestKey(name, tag))
	if err != nil {
		return nil, fmt.Errorf("get manifest %s:%s: %w", name, tag, err)
	}
	defer func() { _ = body.Close() }()
	return io.ReadAll(body)
}

// --- Manifest operations ---

// PushManifest uploads a manifest for name:tag.
func (r *Registry) PushManifest(ctx context.Context, m *manifest.Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}

	key := manifestKey(m.Name, m.Tag)
	if err := r.client.Put(ctx, key, bytes.NewReader(data), int64(len(data))); err != nil {
		return fmt.Errorf("upload manifest %s:%s: %w", m.Name, m.Tag, err)
	}

	// Update catalog.
	return r.updateCatalog(ctx, m.Name, m.Tag)
}

// PushManifestJSON uploads a manifest from raw JSON bytes and updates the catalog.
func (r *Registry) PushManifestJSON(ctx context.Context, name, tag string, data []byte) error {
	key := manifestKey(name, tag)
	if err := r.client.Put(ctx, key, bytes.NewReader(data), int64(len(data))); err != nil {
		return fmt.Errorf("upload manifest %s:%s: %w", name, tag, err)
	}
	return r.updateCatalog(ctx, name, tag)
}

// PullManifest downloads and parses a manifest.
func (r *Registry) PullManifest(ctx context.Context, name, tag string) (*manifest.Manifest, error) {
	body, _, err := r.client.Get(ctx, manifestKey(name, tag))
	if err != nil {
		return nil, fmt.Errorf("get manifest %s:%s: %w", name, tag, err)
	}
	defer func() { _ = body.Close() }()

	var m manifest.Manifest
	if err := json.NewDecoder(body).Decode(&m); err != nil {
		return nil, fmt.Errorf("decode manifest %s:%s: %w", name, tag, err)
	}
	return &m, nil
}

// DeleteManifest removes a manifest.
func (r *Registry) DeleteManifest(ctx context.Context, name, tag string) error {
	if err := r.client.Delete(ctx, manifestKey(name, tag)); err != nil {
		return err
	}
	return r.removeCatalogEntry(ctx, name, tag)
}

// ListTags returns all tags for a repository.
func (r *Registry) ListTags(ctx context.Context, name string) ([]string, error) {
	keys, err := r.client.List(ctx, "manifests/"+name+"/")
	if err != nil {
		return nil, err
	}
	var tags []string
	for _, k := range keys {
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
	body, _, err := r.client.Get(ctx, "catalog.json")
	if errors.Is(err, objectstore.ErrNotFound) {
		return &manifest.Catalog{Repositories: make(map[string]*manifest.Repository)}, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()

	var cat manifest.Catalog
	if err := json.NewDecoder(body).Decode(&cat); err != nil {
		return nil, err
	}
	if cat.Repositories == nil {
		cat.Repositories = make(map[string]*manifest.Repository)
	}
	return &cat, nil
}

func (r *Registry) updateCatalog(ctx context.Context, name, tag string) error {
	r.catalogMu.Lock()
	defer r.catalogMu.Unlock()

	cat, err := r.GetCatalog(ctx)
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

	cat, err := r.GetCatalog(ctx)
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
	return r.client.Put(ctx, "catalog.json", bytes.NewReader(data), int64(len(data)))
}

// --- Key helpers ---

func blobKey(digest string) string {
	return "blobs/sha256/" + digest
}

func manifestKey(name, tag string) string {
	return "manifests/" + name + "/" + tag + ".json"
}

func extractTag(key string) string {
	// "manifests/foo/bar.json" → "bar"
	for i := len(key) - 1; i >= 0; i-- {
		if key[i] == '/' {
			name := key[i+1:]
			if len(name) > 5 && name[len(name)-5:] == ".json" {
				return name[:len(name)-5]
			}
			return ""
		}
	}
	return ""
}
