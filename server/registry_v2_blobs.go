package server

import (
	"io"
	"net/http"
	"strconv"
)

// GET /v2/{name}/blobs/{digest}
func (s *Server) v2GetBlob(w http.ResponseWriter, r *http.Request) {
	dgst := stripSHA256Prefix(r.PathValue("digest"))

	body, size, err := s.reg.StreamBlob(r.Context(), dgst)
	if err != nil {
		if isNotFound(err) {
			v2Error(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob not found")
			return
		}
		v2Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	defer func() { _ = body.Close() }()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Docker-Content-Digest", "sha256:"+dgst)
	if size >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, body)
}

// HEAD /v2/{name}/blobs/{digest}
func (s *Server) v2HeadBlob(w http.ResponseWriter, r *http.Request) {
	dgst := stripSHA256Prefix(r.PathValue("digest"))

	size, err := s.reg.BlobSize(r.Context(), dgst)
	if err != nil {
		if isNotFound(err) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Docker-Content-Digest", "sha256:"+dgst)
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.WriteHeader(http.StatusOK)
}

// PUT /v2/{name}/blobs/sha256:{digest}
func (s *Server) v2PutBlob(w http.ResponseWriter, r *http.Request) {
	dgst := stripSHA256Prefix(r.PathValue("digest"))
	if dgst == "" {
		v2Error(w, http.StatusBadRequest, "DIGEST_INVALID", "missing or invalid digest")
		return
	}

	if err := s.reg.PushBlobFromStream(r.Context(), dgst, r.Body, r.ContentLength); err != nil {
		v2Error(w, http.StatusInternalServerError, "BLOB_UPLOAD_UNKNOWN", err.Error())
		return
	}

	w.Header().Set("Docker-Content-Digest", "sha256:"+dgst)
	w.WriteHeader(http.StatusCreated)
}
