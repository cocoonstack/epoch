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

// handleArtifactDownload streams a cloud image or snapshot by name. Auth-exempt.
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

	switch manifest.ClassifyParsed(m) {
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

func (s *Server) streamCloudImage(w http.ResponseWriter, r *http.Request, name string, m *manifest.OCIManifest, logger *log.Fields) {
	w.Header().Set("Content-Type", manifest.MediaTypeGeneric)
	w.WriteHeader(http.StatusOK)

	if streamErr := cloudimg.StreamParsed(r.Context(), m, &registryBlobReader{reg: s.reg}, w); streamErr != nil {
		logger.Errorf(r.Context(), streamErr, "stream cloud image %s", name)
	}
}

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

type registryBlobReader struct {
	reg blobStreamer
}

type blobStreamer interface {
	StreamBlob(ctx context.Context, digest string) (io.ReadCloser, int64, error)
}

func (r *registryBlobReader) ReadBlob(ctx context.Context, digest string) (io.ReadCloser, error) {
	body, _, err := r.reg.StreamBlob(ctx, stripSHA256Prefix(digest))
	return body, err
}

// registryDownloader adapts *registry.Registry to snapshot.Downloader.
type registryDownloader struct {
	reg          manifestStreamer
	manifestName string
	manifestRaw  []byte
}

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
