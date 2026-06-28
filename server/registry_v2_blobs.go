package server

import (
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/projecteru2/core/log"

	"github.com/cocoonstack/epoch/manifest"
)

const (
	defaultBlobRedirectTTL = time.Hour
	// maxBlobRedirectTTL is the presign expiry cap; over it every presign fails.
	maxBlobRedirectTTL = 7 * 24 * time.Hour
)

func (s *Server) v2GetBlob(w http.ResponseWriter, r *http.Request) {
	dgst := stripSHA256Prefix(urlVar(r, "digest"))

	if s.blobRedirect && s.redirectBlob(w, r, dgst) {
		return
	}

	body, size, err := s.reg.StreamBlob(r.Context(), dgst)
	if err != nil {
		if isNotFound(err) {
			v2Error(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob not found")
			return
		}
		log.WithFunc("server.v2GetBlob").Errorf(r.Context(), err, "stream blob sha256:%s failed", dgst)
		v2Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	defer func() { _ = body.Close() }()

	w.Header().Set("Content-Type", manifest.MediaTypeGeneric)
	w.Header().Set("Docker-Content-Digest", "sha256:"+dgst)
	if size >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, body)
}

// redirectBlob 307s the client to a presigned URL. Returns false without
// writing a response when it can't, so v2GetBlob falls back to streaming.
func (s *Server) redirectBlob(w http.ResponseWriter, r *http.Request, dgst string) bool {
	logger := log.WithFunc("server.redirectBlob")
	// presign succeeds even for a missing object, so HEAD first to return an
	// OCI BLOB_UNKNOWN rather than 307 to a backend 404.
	exists, err := s.reg.BlobExists(r.Context(), dgst)
	if err != nil {
		logger.Warnf(r.Context(), "blob exists check for %s failed, falling back to proxy: %v", dgst, err)
		return false
	}
	if !exists {
		v2Error(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob not found")
		return true
	}
	url, err := s.reg.PresignBlobGet(r.Context(), dgst, s.blobRedirectTTL)
	if err != nil {
		logger.Warnf(r.Context(), "presign blob %s failed, falling back to proxy: %v", dgst, err)
		return false
	}
	w.Header().Set("Docker-Content-Digest", "sha256:"+dgst)
	http.Redirect(w, r, url, http.StatusTemporaryRedirect)
	return true
}

func (s *Server) v2HeadBlob(w http.ResponseWriter, r *http.Request) {
	dgst := stripSHA256Prefix(urlVar(r, "digest"))

	size, err := s.reg.BlobSize(r.Context(), dgst)
	if err != nil {
		if isNotFound(err) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		log.WithFunc("server.v2HeadBlob").Errorf(r.Context(), err, "blob size sha256:%s failed", dgst)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", manifest.MediaTypeGeneric)
	w.Header().Set("Docker-Content-Digest", "sha256:"+dgst)
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.WriteHeader(http.StatusOK)
}

func (s *Server) v2PutBlob(w http.ResponseWriter, r *http.Request) {
	name := urlVar(r, "name")
	dgst := stripSHA256Prefix(urlVar(r, "digest"))
	if dgst == "" {
		v2Error(w, http.StatusBadRequest, "DIGEST_INVALID", "missing or invalid digest")
		return
	}
	s.persistMonolithicUpload(w, r, name, "sha256:"+dgst)
}

// clampBlobRedirectTTL keeps a TTL in the presign-valid range: sub-1s → default, over-7d → cap.
func clampBlobRedirectTTL(d time.Duration) time.Duration {
	if d < time.Second {
		return defaultBlobRedirectTTL
	}
	return min(d, maxBlobRedirectTTL)
}
