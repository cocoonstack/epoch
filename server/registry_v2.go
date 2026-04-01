package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/cocoonstack/epoch/internal/util"
	"github.com/cocoonstack/epoch/objectstore"
)

const manifestMediaType = "application/vnd.epoch.manifest.v1+json"

// GET /v2/
func (s *Server) v2Check(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	writeJSON(w, 200, map[string]any{})
}

// GET /v2/_catalog
func (s *Server) v2Catalog(w http.ResponseWriter, r *http.Request) {
	cat, err := s.reg.GetCatalog(r.Context())
	if err != nil {
		v2Error(w, 500, "INTERNAL_ERROR", err.Error())
		return
	}
	names := make([]string, 0, len(cat.Repositories))
	for name := range cat.Repositories {
		names = append(names, name)
	}
	writeJSON(w, 200, map[string]any{"repositories": names})
}

// GET /v2/{name}/tags/list
func (s *Server) v2TagsList(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	tags, err := s.reg.ListTags(r.Context(), name)
	if err != nil {
		// Fallback: read tags from catalog.
		cat, catErr := s.reg.GetCatalog(r.Context())
		if catErr != nil {
			v2Error(w, 404, "NAME_UNKNOWN", fmt.Sprintf("repository %q not found", name))
			return
		}
		repo, ok := cat.Repositories[name]
		if !ok {
			v2Error(w, 404, "NAME_UNKNOWN", fmt.Sprintf("repository %q not found", name))
			return
		}
		tags = make([]string, 0, len(repo.Tags))
		for t := range repo.Tags {
			tags = append(tags, t)
		}
	}
	if tags == nil {
		tags = []string{}
	}
	writeJSON(w, 200, map[string]any{"name": name, "tags": tags})
}

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
	_, _ = w.Write(data)
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

// GET /v2/{name}/blobs/{digest}
func (s *Server) v2GetBlob(w http.ResponseWriter, r *http.Request) {
	dgst := stripSHA256Prefix(r.PathValue("digest"))

	body, size, err := s.reg.StreamBlob(r.Context(), dgst)
	if err != nil {
		if isNotFound(err) {
			v2Error(w, 404, "BLOB_UNKNOWN", "blob not found")
			return
		}
		v2Error(w, 500, "INTERNAL_ERROR", err.Error())
		return
	}
	defer func() { _ = body.Close() }()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Docker-Content-Digest", "sha256:"+dgst)
	if size >= 0 {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
	}
	w.WriteHeader(200)
	_, _ = io.Copy(w, body)
}

// HEAD /v2/{name}/blobs/{digest}
func (s *Server) v2HeadBlob(w http.ResponseWriter, r *http.Request) {
	dgst := stripSHA256Prefix(r.PathValue("digest"))

	size, err := s.reg.BlobSize(r.Context(), dgst)
	if err != nil {
		if isNotFound(err) {
			w.WriteHeader(404)
			return
		}
		w.WriteHeader(500)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Docker-Content-Digest", "sha256:"+dgst)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
	w.WriteHeader(200)
}

// --- helpers ---

func stripSHA256Prefix(s string) string {
	return strings.TrimPrefix(s, "sha256:")
}

func isNotFound(err error) bool {
	return err == objectstore.ErrNotFound || strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "404")
}

// PUT /v2/{name}/blobs/sha256:{digest}
func (s *Server) v2PutBlob(w http.ResponseWriter, r *http.Request) {
	dgst := stripSHA256Prefix(r.PathValue("digest"))
	if dgst == "" {
		v2Error(w, 400, "DIGEST_INVALID", "missing or invalid digest")
		return
	}

	if err := s.reg.PushBlobFromStream(r.Context(), dgst, r.Body, r.ContentLength); err != nil {
		v2Error(w, 500, "BLOB_UPLOAD_UNKNOWN", err.Error())
		return
	}

	w.Header().Set("Docker-Content-Digest", "sha256:"+dgst)
	w.WriteHeader(201)
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
