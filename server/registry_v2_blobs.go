package server

import (
	"fmt"
	"io"
	"net/http"
)

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
