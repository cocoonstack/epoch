package server

import (
	"context"
	"io"
	"net/http"

	"github.com/projecteru2/core/log"

	"github.com/cocoonstack/epoch/cloudimg"
	"github.com/cocoonstack/epoch/manifest"
)

// handleCloudImageDownload streams a cocoon cloud image's assembled disk
// bytes as `application/octet-stream`. The handler classifies the manifest
// at <name>:latest and only serves cloud images; snapshots and container
// images return 405 because they are not single contiguous artifacts.
//
// Used by `cocoon image pull https://epoch.example/dl/<name>` to fetch raw
// qcow2 / raw images by name without OCI credentials.
func (s *Server) handleCloudImageDownload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	logger := log.WithFunc("server.handleCloudImageDownload")

	raw, err := s.reg.ManifestJSON(r.Context(), name, "latest")
	if err != nil {
		if isNotFound(err) {
			http.Error(w, "image not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	kind, err := manifest.Classify(raw)
	if err != nil {
		http.Error(w, "invalid manifest: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if kind != manifest.KindCloudImage {
		http.Error(w, "not a cloud image (kind: "+kind.String()+")", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", manifest.MediaTypeGeneric)
	w.WriteHeader(http.StatusOK)

	// After WriteHeader we cannot turn an error into an HTTP status; the best
	// we can do is log and stop streaming so the client sees a truncated body.
	if streamErr := cloudimg.Stream(r.Context(), raw, &registryBlobReader{reg: s.reg}, w); streamErr != nil {
		logger.Errorf(r.Context(), streamErr, "stream cloud image %s", name)
	}
}

// registryBlobReader adapts the in-process *registry.Registry to
// cloudimg.BlobReader. It strips the `sha256:` prefix from descriptor digests
// because the registry stores blobs under their unprefixed hex digest.
type registryBlobReader struct {
	reg blobStreamer
}

// blobStreamer is the subset of *registry.Registry needed by registryBlobReader.
// Defined as an interface so server tests can substitute fakes without spinning
// up an object store.
type blobStreamer interface {
	StreamBlob(ctx context.Context, digest string) (io.ReadCloser, int64, error)
}

func (r *registryBlobReader) ReadBlob(ctx context.Context, digest string) (io.ReadCloser, error) {
	body, _, err := r.reg.StreamBlob(ctx, stripSHA256Prefix(digest))
	return body, err
}
