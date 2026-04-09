package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/cocoonstack/epoch/utils"
)

// manifestBodyLimit caps the manifest body size accepted by PUT.
// 50 MB is well above any real OCI manifest (typically a few KB).
const manifestBodyLimit = 50 << 20

// loadManifestRaw fetches a manifest by reference, picking the by-digest path
// when the reference is `sha256:...` and the by-tag path otherwise. This keeps
// the GET/HEAD handlers from open-coding the same branch twice.
func (s *Server) loadManifestRaw(r *http.Request, name, ref string) ([]byte, error) {
	if strings.HasPrefix(ref, "sha256:") {
		return s.reg.ManifestJSONByDigest(r.Context(), name, ref)
	}
	return s.reg.ManifestJSON(r.Context(), name, ref)
}

// detectManifestMediaType peeks at the top-level `mediaType` field of the
// stored manifest JSON to round-trip the right Content-Type to OCI clients.
// Falls back to [defaultManifestMediaType] when the field is absent.
func detectManifestMediaType(data []byte) string {
	var probe struct {
		MediaType string `json:"mediaType"`
	}
	if err := json.Unmarshal(data, &probe); err == nil && probe.MediaType != "" {
		return probe.MediaType
	}
	return defaultManifestMediaType
}

// GET /v2/{name}/manifests/{reference}
//
// Reference may be a tag (e.g. `latest`) or a content digest
// (e.g. `sha256:abc...`). OCI clients commonly resolve a tag to a digest
// then re-fetch by digest, so both forms must work.
func (s *Server) v2GetManifest(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ref := r.PathValue("reference")

	data, err := s.loadManifestRaw(r, name, ref)
	if err != nil {
		if isNotFound(err) {
			v2Error(w, http.StatusNotFound, "MANIFEST_UNKNOWN", fmt.Sprintf("manifest %s:%s not found", name, ref))
			return
		}
		v2Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	digest := utils.SHA256Hex(data)
	w.Header().Set("Content-Type", detectManifestMediaType(data))
	w.Header().Set("Docker-Content-Digest", "sha256:"+digest)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data) //nolint:gosec // raw registry manifest bytes, not HTML rendering
}

// HEAD /v2/{name}/manifests/{reference}
//
// We deliberately fetch the body (rather than just a HEAD against the object
// store) so the headers are computed from real bytes — this is the only way
// to know the right Content-Type when the underlying object store does not
// retain it as metadata.
func (s *Server) v2HeadManifest(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ref := r.PathValue("reference")

	data, err := s.loadManifestRaw(r, name, ref)
	if err != nil {
		if isNotFound(err) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	digest := utils.SHA256Hex(data)
	w.Header().Set("Content-Type", detectManifestMediaType(data))
	w.Header().Set("Docker-Content-Digest", "sha256:"+digest)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
}

// DELETE /v2/{name}/manifests/{reference}
//
// Removes a manifest tag and updates the catalog. Per OCI Distribution spec,
// this is the documented way for OCI clients (oras, crane, docker manifest rm)
// to delete a tag from a registry. Blobs are intentionally left behind for GC.
func (s *Server) v2DeleteManifest(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ref := r.PathValue("reference")
	if err := s.reg.DeleteManifest(r.Context(), name, ref); err != nil {
		if isNotFound(err) {
			v2Error(w, http.StatusNotFound, "MANIFEST_UNKNOWN", fmt.Sprintf("manifest %s:%s not found", name, ref))
			return
		}
		v2Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// PUT /v2/{name}/manifests/{reference}
//
// Accepts any well-formed JSON manifest: OCI image manifest, OCI image index,
// Docker manifest v2 / list, or a cocoonstack snapshot/cloudimg artifact.
// Validation is intentionally minimal — the registry stores the bytes
// verbatim and clients re-fetch them unchanged. The registry layer is
// responsible for the dual tag/digest write.
func (s *Server) v2PutManifest(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ref := r.PathValue("reference")

	data, err := io.ReadAll(io.LimitReader(r.Body, manifestBodyLimit))
	if err != nil {
		v2Error(w, http.StatusBadRequest, "MANIFEST_INVALID", "read body: "+err.Error())
		return
	}
	if !json.Valid(data) {
		v2Error(w, http.StatusBadRequest, "MANIFEST_INVALID", "invalid JSON")
		return
	}

	if err := s.reg.PushManifestJSON(r.Context(), name, ref, data); err != nil {
		v2Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	digest := "sha256:" + utils.SHA256Hex(data)
	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Location", fmt.Sprintf("/v2/%s/manifests/%s", name, ref))
	w.WriteHeader(http.StatusCreated)
}
