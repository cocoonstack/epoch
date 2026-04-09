package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/cocoonstack/epoch/utils"
)

// detectManifestMediaType inspects raw manifest JSON to determine the correct
// Content-Type. Supports OCI image index, OCI image manifest, Docker manifest,
// and the epoch custom format.
func detectManifestMediaType(data []byte) string {
	var probe struct {
		MediaType string `json:"mediaType"`
	}
	if json.Unmarshal(data, &probe) == nil && probe.MediaType != "" {
		return probe.MediaType
	}
	return manifestMediaType
}

// GET /v2/{name}/manifests/{reference}
//
// Reference may be a tag or a digest (sha256:...). go-containerregistry and
// docker clients push by tag then pull by digest, so both must resolve.
func (s *Server) v2GetManifest(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ref := r.PathValue("reference")

	var (
		data []byte
		err  error
	)
	if strings.HasPrefix(ref, "sha256:") {
		data, err = s.reg.ManifestJSONByDigest(r.Context(), name, ref)
	} else {
		data, err = s.reg.ManifestJSON(r.Context(), name, ref)
	}
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
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data) //nolint:gosec // raw registry manifest bytes, not HTML rendering
}

// HEAD /v2/{name}/manifests/{reference}
func (s *Server) v2HeadManifest(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ref := r.PathValue("reference")

	var (
		data []byte
		err  error
	)
	if strings.HasPrefix(ref, "sha256:") {
		data, err = s.reg.ManifestJSONByDigest(r.Context(), name, ref)
	} else {
		data, err = s.reg.ManifestJSON(r.Context(), name, ref)
	}
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
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.WriteHeader(http.StatusOK)
}

// PUT /v2/{name}/manifests/{reference}
func (s *Server) v2PutManifest(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ref := r.PathValue("reference")

	data, err := io.ReadAll(io.LimitReader(r.Body, 50<<20)) // 50MB limit
	if err != nil {
		v2Error(w, http.StatusBadRequest, "MANIFEST_INVALID", "read body: "+err.Error())
		return
	}

	// Lenient JSON validation — accept epoch, OCI image manifest, OCI image
	// index, and Docker manifest formats. Only the epoch format has a "name"
	// field; validate it matches the URL when present.
	if !json.Valid(data) {
		v2Error(w, http.StatusBadRequest, "MANIFEST_INVALID", "invalid JSON")
		return
	}
	var epochProbe struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(data, &epochProbe) == nil && epochProbe.Name != "" && epochProbe.Name != name {
		v2Error(w, http.StatusBadRequest, "MANIFEST_INVALID", "manifest name does not match request path")
		return
	}

	if err := s.reg.PushManifestJSON(r.Context(), name, ref, data); err != nil {
		v2Error(w, http.StatusInternalServerError, "MANIFEST_INVALID", err.Error())
		return
	}

	digest := "sha256:" + utils.SHA256Hex(data)

	// Also store by digest so the manifest can be fetched by sha256:xxx reference.
	// Non-fatal on failure — the tag reference still resolves.
	_ = s.reg.StoreManifestByDigest(r.Context(), name, digest, data)

	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Location", fmt.Sprintf("/v2/%s/manifests/%s", name, ref))
	w.WriteHeader(http.StatusCreated)
}
