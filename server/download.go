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

const defaultTag = "latest"

var (
	_ cloudimg.BlobReader = (*registryBlobReader)(nil)
	_ snapshot.Downloader = (*registryDownloader)(nil)
)

type blobStreamer interface {
	StreamBlob(ctx context.Context, digest string) (io.ReadCloser, int64, error)
}

type manifestStreamer interface {
	blobStreamer
	ManifestJSON(ctx context.Context, name, tag string) ([]byte, error)
	ManifestJSONByDigest(ctx context.Context, name, digest string) ([]byte, error)
}

type registryBlobReader struct {
	reg blobStreamer
}

// ReadBlob downloads a blob by digest from the registry object store.
func (r *registryBlobReader) ReadBlob(ctx context.Context, digest string) (io.ReadCloser, error) {
	body, _, err := r.reg.StreamBlob(ctx, stripSHA256Prefix(digest))
	return body, err
}

// registryDownloader adapts *registry.Registry to snapshot.Downloader.
// The manifest{Name,Tag,Raw} fields form a one-entry cache for the
// already-fetched parent manifest; the tag is part of the key so
// image-index child fetches (GetManifest with child.Digest) miss and
// hit the registry by digest.
type registryDownloader struct {
	reg          manifestStreamer
	manifestName string
	manifestTag  string
	manifestRaw  []byte
}

// GetManifest returns the manifest JSON, using a cached copy when the
// (name, tag) pair matches what the caller pre-seeded.
func (d *registryDownloader) GetManifest(ctx context.Context, name, tag string) ([]byte, string, error) {
	if name == d.manifestName && tag == d.manifestTag && d.manifestRaw != nil {
		return d.manifestRaw, "", nil
	}
	raw, err := d.fetchManifest(ctx, name, tag)
	return raw, "", err
}

// GetBlob downloads a blob by digest from the registry object store.
func (d *registryDownloader) GetBlob(ctx context.Context, _, digest string) (io.ReadCloser, error) {
	body, _, err := d.reg.StreamBlob(ctx, stripSHA256Prefix(digest))
	return body, err
}

// fetchManifest routes sha256:-prefixed tags to ManifestJSONByDigest
// so image-index child fetches resolve correctly; bare tags fall back
// to ManifestJSON, defaulting an empty tag to "latest".
func (d *registryDownloader) fetchManifest(ctx context.Context, name, tag string) ([]byte, error) {
	if tag == "" {
		tag = defaultTag
	}
	if isDigestRef(tag) {
		return d.reg.ManifestJSONByDigest(ctx, name, tag)
	}
	return d.reg.ManifestJSON(ctx, name, tag)
}

// handleArtifactDownload streams a cloud image or snapshot. Auth-exempt.
// On 404 retries (name+"/"+ref, "latest") to keep the pre-2026-05
// /dl/{name-with-slash} form working under the new /dl/{name}/{ref} route,
// flagging the response with a Deprecation header so callers migrate.
func (s *Server) handleArtifactDownload(w http.ResponseWriter, r *http.Request) {
	name := urlVar(r, "name")
	ref := urlVar(r, "ref")
	if ref == "" {
		ref = defaultTag
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
		w.Header().Set("Deprecation", "true")
		w.Header().Set("Link", `</dl/`+useName+`/`+useRef+`>; rel="successor-version"`)
		logger.Warnf(r.Context(), "legacy /dl/ form %s:%s resolved via fallback to %s:%s", name, ref, useName, useRef)
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
		s.streamSnapshot(w, r, useName, useRef, raw, m)
	case manifest.KindContainerImage:
		http.Error(w, "container image — pull via OCI client (oras / crane / docker)", http.StatusMethodNotAllowed)
	default:
		http.Error(w, "unknown artifact kind", http.StatusMethodNotAllowed)
	}
}

// fetchManifestWithLegacyFallback returns the resolved (name, ref) so the
// caller can detect when the fallback path fired.
func (s *Server) fetchManifestWithLegacyFallback(r *http.Request, name, ref string) ([]byte, string, string, error) {
	raw, err := s.loadManifestRaw(r, name, ref)
	if err == nil {
		return raw, name, ref, nil
	}
	if !isNotFound(err) {
		return nil, name, ref, err
	}
	legacyName := name + "/" + ref
	legacy, lerr := s.loadManifestRaw(r, legacyName, defaultTag)
	if lerr != nil {
		return nil, name, ref, err
	}
	return legacy, legacyName, defaultTag, nil
}

func (s *Server) streamCloudImage(w http.ResponseWriter, r *http.Request, name string, m *manifest.OCIManifest) {
	streamWithPreflight(r.Context(), w, manifest.MediaTypeGeneric,
		func(out io.Writer) error {
			return cloudimg.StreamParsed(r.Context(), m, &registryBlobReader{reg: s.reg}, out)
		},
		"stream cloud image", name)
}

func (s *Server) streamSnapshot(w http.ResponseWriter, r *http.Request, name, tag string, raw []byte, m *manifest.OCIManifest) {
	dl := &registryDownloader{
		reg:          s.reg,
		manifestRaw:  raw,
		manifestName: name,
		manifestTag:  tag,
	}
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
	if err != nil {
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
