package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/cocoonstack/epoch/internal/util"
)

// GET /v2/{name}/manifests/{reference}
func (s *Server) v2GetManifest(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ref := r.PathValue("reference")

	data, err := s.reg.ManifestJSON(r.Context(), name, ref)
	if err != nil {
		if isNotFound(err) {
			v2Error(w, 404, "MANIFEST_UNKNOWN", fmt.Sprintf("manifest %s:%s not found", name, ref))
			return
		}
		v2Error(w, 500, "INTERNAL_ERROR", err.Error())
		return
	}

	digest := util.SHA256Hex(data)
	w.Header().Set("Content-Type", manifestMediaType)
	w.Header().Set("Docker-Content-Digest", "sha256:"+digest)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.WriteHeader(200)
	_, _ = w.Write(data) //nolint:gosec // raw registry manifest bytes, not HTML rendering
}

// HEAD /v2/{name}/manifests/{reference}
func (s *Server) v2HeadManifest(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ref := r.PathValue("reference")

	data, err := s.reg.ManifestJSON(r.Context(), name, ref)
	if err != nil {
		if isNotFound(err) {
			w.WriteHeader(404)
			return
		}
		w.WriteHeader(500)
		return
	}

	digest := util.SHA256Hex(data)
	w.Header().Set("Content-Type", manifestMediaType)
	w.Header().Set("Docker-Content-Digest", "sha256:"+digest)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.WriteHeader(200)
}

// PUT /v2/{name}/manifests/{reference}
func (s *Server) v2PutManifest(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ref := r.PathValue("reference")

	data, err := io.ReadAll(io.LimitReader(r.Body, 50<<20)) // 50MB limit
	if err != nil {
		v2Error(w, 400, "MANIFEST_INVALID", "read body: "+err.Error())
		return
	}

	// Validate JSON.
	var m struct{ Name string }
	if err := json.Unmarshal(data, &m); err != nil {
		v2Error(w, 400, "MANIFEST_INVALID", "invalid JSON: "+err.Error())
		return
	}

	if err := s.reg.PushManifestJSON(r.Context(), name, ref, data); err != nil {
		v2Error(w, 500, "MANIFEST_INVALID", err.Error())
		return
	}

	digest := util.SHA256Hex(data)
	w.Header().Set("Docker-Content-Digest", "sha256:"+digest)
	w.Header().Set("Location", fmt.Sprintf("/v2/%s/manifests/%s", name, ref))
	w.WriteHeader(201)
}
