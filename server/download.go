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

type registryBlobReader struct {
	reg blobStreamer
}

// ReadBlob downloads a blob by digest from the registry object store.
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

// GetManifest returns the manifest JSON, using a cached copy when available.
func (d *registryDownloader) GetManifest(ctx context.Context, name, _ string) ([]byte, string, error) {
	if name == d.manifestName && d.manifestRaw != nil {
		return d.manifestRaw, "", nil
	}
	raw, err := d.reg.ManifestJSON(ctx, name, "latest")
	return raw, "", err
}

// GetBlob downloads a blob by digest from the registry object store.
func (d *registryDownloader) GetBlob(ctx context.Context, _, digest string) (io.ReadCloser, error) {
	body, _, err := d.reg.StreamBlob(ctx, stripSHA256Prefix(digest))
	return body, err
}

// handleArtifactDownload streams a cloud image or snapshot. Auth-exempt.
//
// Canonical URL: /dl/{name}/{ref} — ref is a tag ("latest", "22h2-20260510")
// or a digest reference ("sha256:..."). The route also matches /dl/{name}
// (no ref); in that case ref defaults to "latest". Both routes funnel here.
//
// Backward-compat fallback: if the (name, ref) lookup 404s, retry as
// (name+"/"+ref, "latest"). That covers the pre-2026-05 2-segment form
// `/dl/simular/win11`, which the new route would split as name=simular,
// ref=win11 — the fallback re-joins to name=simular/win11 + implicit latest.
func (s *Server) handleArtifactDownload(w http.ResponseWriter, r *http.Request) {
	name := urlVar(r, "name")
	ref := urlVar(r, "ref")
	if ref == "" {
		ref = "latest"
	}
	logger := log.WithFunc("server.handleArtifactDownload")

	raw, useName, useRef, err := s.fetchManifestWithLegacyFallback(r, name, ref)
	if err != nil {
		if isNotFound(err) {
			http.Error(w, "artifact not found", http.StatusNotFound)
			return
		}
		logger.Errorf(r.Context(), err, "fetch manifest %s:%s", name, ref)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if useName != name || useRef != ref {
		// Surface that the legacy form resolved via fallback so callers can
		// migrate. The Deprecation header follows the IETF draft convention.
		w.Header().Set("Deprecation", "true")
		w.Header().Set("Link", `</dl/`+useName+`/`+useRef+`>; rel="successor-version"`)
		logger.Warnf(r.Context(), "legacy /dl/ form %s:%s resolved via fallback to %s:%s — caller should migrate to /dl/%s/%s", name, ref, useName, useRef, useName, useRef)
	}

	m, err := manifest.Parse(raw)
	if err != nil {
		logger.Errorf(r.Context(), err, "parse manifest %s:%s", useName, useRef)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	switch manifest.ClassifyParsed(m) {
	case manifest.KindCloudImage:
		s.streamCloudImage(w, r, useName, m)
	case manifest.KindSnapshot:
		s.streamSnapshot(w, r, useName, raw, m)
	case manifest.KindContainerImage:
		http.Error(w, "container image — pull via OCI client (oras / crane / docker)", http.StatusMethodNotAllowed)
	default:
		http.Error(w, "unknown artifact kind", http.StatusMethodNotAllowed)
	}
}

// fetchManifestWithLegacyFallback tries (name, ref) first; on 404 retries
// (name+"/"+ref, "latest"). Returns the (name, ref) pair that resolved so
// the caller can flag deprecation. Non-404 errors short-circuit immediately.
func (s *Server) fetchManifestWithLegacyFallback(r *http.Request, name, ref string) ([]byte, string, string, error) {
	raw, err := s.loadManifestRaw(r, name, ref)
	if err == nil {
		return raw, name, ref, nil
	}
	if !isNotFound(err) {
		return nil, name, ref, err
	}
	legacyName := name + "/" + ref
	legacy, lerr := s.loadManifestRaw(r, legacyName, "latest")
	if lerr != nil {
		return nil, name, ref, err
	}
	return legacy, legacyName, "latest", nil
}

type blobStreamer interface {
	StreamBlob(ctx context.Context, digest string) (io.ReadCloser, int64, error)
}

type manifestStreamer interface {
	blobStreamer
	ManifestJSON(ctx context.Context, name, tag string) ([]byte, error)
}

func (s *Server) streamCloudImage(w http.ResponseWriter, r *http.Request, name string, m *manifest.OCIManifest) {
	streamWithPreflight(r.Context(), w, manifest.MediaTypeGeneric,
		func(out io.Writer) error {
			return cloudimg.StreamParsed(r.Context(), m, &registryBlobReader{reg: s.reg}, out)
		},
		"stream cloud image", name)
}

func (s *Server) streamSnapshot(w http.ResponseWriter, r *http.Request, name string, raw []byte, m *manifest.OCIManifest) {
	dl := &registryDownloader{reg: s.reg, manifestRaw: raw, manifestName: name}
	streamWithPreflight(r.Context(), w, manifest.MediaTypeTar,
		func(out io.Writer) error {
			return snapshot.StreamParsed(r.Context(), m, dl, snapshot.StreamOptions{Name: name, Writer: out})
		},
		"stream snapshot", name)
}

// streamWithPreflight blocks on the first byte from streamFn so fetch
// errors before any output surface as 500. Mid-stream failures after
// WriteHeader still degrade to truncated 200 (chunked semantics).
func streamWithPreflight(ctx context.Context, w http.ResponseWriter, contentType string, streamFn func(io.Writer) error, errCtx, name string) {
	logger := log.WithFunc("server.streamWithPreflight")
	pr, pw := io.Pipe()
	go func() {
		err := streamFn(pw)
		_ = pw.CloseWithError(err)
	}()

	var first [1]byte
	n, err := io.ReadFull(pr, first[:])
	if err != nil || n == 0 {
		logger.Errorf(ctx, err, "%s %s (preflight)", errCtx, name)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		_ = pr.Close()
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	if _, writeErr := w.Write(first[:n]); writeErr != nil {
		_ = pr.Close()
		return
	}
	if _, copyErr := io.Copy(w, pr); copyErr != nil {
		logger.Errorf(ctx, copyErr, "%s %s (mid-stream)", errCtx, name)
	}
}
