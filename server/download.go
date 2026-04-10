package server

import (
	"context"
	"io"
	"net/http"

	"github.com/projecteru2/core/log"

	"github.com/cocoonstack/epoch/cloudimg"
	"github.com/cocoonstack/epoch/manifest"
	"github.com/cocoonstack/epoch/snapshot"
)

// handleArtifactDownload streams a cocoonstack artifact's bytes by repository
// name. The handler classifies the manifest at <name>:latest and serves:
//
//   - cloud images  → concatenated raw disk bytes (application/octet-stream).
//     `cocoon image pull https://epoch.example/dl/<name>` consumes
//     this directly.
//   - snapshots     → cocoon-import tar (application/x-tar) — exactly what
//     `cocoon snapshot import` reads from stdin. Pipeable as
//     `curl https://epoch.example/dl/<name> | cocoon snapshot import --name <name>`.
//   - container images / unknown → 405.
//
// Both flows are auth-exempt by design (vk-cocoon and other consumers can
// pull without holding a registry token).
func (s *Server) handleArtifactDownload(w http.ResponseWriter, r *http.Request) {
	name := urlVar(r, "name")
	logger := log.WithFunc("server.handleArtifactDownload")

	raw, err := s.reg.ManifestJSON(r.Context(), name, "latest")
	if err != nil {
		if isNotFound(err) {
			http.Error(w, "artifact not found", http.StatusNotFound)
			return
		}
		logger.Errorf(r.Context(), err, "fetch manifest %s", name)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	m, err := manifest.Parse(raw)
	if err != nil {
		logger.Errorf(r.Context(), err, "parse manifest %s", name)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	kind, _ := manifest.Classify(raw)
	switch kind {
	case manifest.KindCloudImage:
		s.streamCloudImage(w, r, name, m, logger)
	case manifest.KindSnapshot:
		s.streamSnapshot(w, r, name, raw, m, logger)
	case manifest.KindContainerImage:
		http.Error(w, "container image — pull via OCI client (oras / crane / docker)", http.StatusMethodNotAllowed)
	default:
		http.Error(w, "unknown artifact kind", http.StatusMethodNotAllowed)
	}
}

// streamCloudImage writes a cloud-image manifest's concatenated disk bytes
// to w. After WriteHeader we cannot turn an error into an HTTP status; the
// best we can do is log and stop streaming so the client sees a truncated
// body.
func (s *Server) streamCloudImage(w http.ResponseWriter, r *http.Request, name string, m *manifest.OCIManifest, logger *log.Fields) {
	w.Header().Set("Content-Type", manifest.MediaTypeGeneric)
	w.WriteHeader(http.StatusOK)

	if streamErr := cloudimg.StreamParsed(r.Context(), m, &registryBlobReader{reg: s.reg}, w); streamErr != nil {
		logger.Errorf(r.Context(), streamErr, "stream cloud image %s", name)
	}
}

// streamSnapshot writes a snapshot manifest as the cocoon-import tar
// (snapshot.json envelope plus one tar entry per OCI layer). The downloader
// adapter wraps the in-process registry so snapshot.StreamParsed can fetch
// the config blob and layer bodies the same way the HTTP-side puller would.
func (s *Server) streamSnapshot(w http.ResponseWriter, r *http.Request, name string, raw []byte, m *manifest.OCIManifest, logger *log.Fields) {
	w.Header().Set("Content-Type", manifest.MediaTypeTar)
	w.WriteHeader(http.StatusOK)

	dl := &registryDownloader{reg: s.reg, manifestRaw: raw, manifestName: name}
	if streamErr := snapshot.StreamParsed(r.Context(), m, dl, snapshot.StreamOptions{
		Name:   name,
		Writer: w,
	}); streamErr != nil {
		logger.Errorf(r.Context(), streamErr, "stream snapshot %s", name)
	}
}

// registryBlobReader adapts the in-process *registry.Registry to
// cloudimg.BlobReader. It strips the `sha256:` prefix from descriptor digests
// because the registry stores blobs under their unprefixed hex digest.
type registryBlobReader struct {
	reg blobStreamer
}

// blobStreamer is the subset of *registry.Registry needed by the in-process
// blob adapters. Defined as an interface so server tests can substitute fakes
// without spinning up an object store.
type blobStreamer interface {
	StreamBlob(ctx context.Context, digest string) (io.ReadCloser, int64, error)
}

func (r *registryBlobReader) ReadBlob(ctx context.Context, digest string) (io.ReadCloser, error) {
	body, _, err := r.reg.StreamBlob(ctx, stripSHA256Prefix(digest))
	return body, err
}

// registryDownloader adapts the in-process *registry.Registry to
// snapshot.Downloader, the same way registryBlobReader adapts it to
// cloudimg.BlobReader. It re-serves the already-fetched manifest bytes from
// memory so snapshot.StreamParsed does not pay for a second S3 round-trip.
//
// The tag parameter in GetManifest is ignored — the /dl/{name} handler
// always fetches "latest" before constructing this adapter, and the cached
// manifestRaw is what StreamParsed will read. If a caller ever needs
// non-latest, this adapter must be extended to accept and propagate tags.
type registryDownloader struct {
	reg          manifestStreamer
	manifestName string
	manifestRaw  []byte
}

// manifestStreamer is the subset of *registry.Registry needed by
// registryDownloader.
type manifestStreamer interface {
	blobStreamer
	ManifestJSON(ctx context.Context, name, tag string) ([]byte, error)
}

func (d *registryDownloader) GetManifest(ctx context.Context, name, _ string) ([]byte, string, error) {
	if name == d.manifestName && d.manifestRaw != nil {
		return d.manifestRaw, "", nil
	}
	raw, err := d.reg.ManifestJSON(ctx, name, "latest")
	return raw, "", err
}

func (d *registryDownloader) GetBlob(ctx context.Context, _, digest string) (io.ReadCloser, error) {
	body, _, err := d.reg.StreamBlob(ctx, stripSHA256Prefix(digest))
	return body, err
}
