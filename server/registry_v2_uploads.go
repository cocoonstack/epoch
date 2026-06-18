package server

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/projecteru2/core/log"
)

const uploadBodyLimit = defaultUploadMaxBytes

func (s *Server) v2InitBlobUpload(w http.ResponseWriter, r *http.Request) {
	name := urlVar(r, "name")

	if digest := r.URL.Query().Get("digest"); digest != "" {
		s.persistMonolithicUpload(w, r, name, digest)
		return
	}

	id, err := s.uploads.Start()
	if err != nil {
		log.WithFunc("server.v2InitBlobUpload").Errorf(r.Context(), err, "start upload session failed")
		v2Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	w.Header().Set("Location", uploadLocation(name, id))
	w.Header().Set("Docker-Upload-UUID", id)
	w.Header().Set("Range", "0-0")
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) v2PatchBlobUpload(w http.ResponseWriter, r *http.Request) {
	name := urlVar(r, "name")
	id := urlVar(r, "uuid")

	body := io.LimitReader(r.Body, uploadBodyLimit)
	size, err := s.uploads.Append(id, body)
	if err != nil {
		drainBody(body)
		writeUploadAppendError(w, err)
		return
	}

	w.Header().Set("Location", uploadLocation(name, id))
	w.Header().Set("Docker-Upload-UUID", id)
	w.Header().Set("Range", "0-"+strconv.FormatInt(size-1, 10))
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) v2CompleteBlobUpload(w http.ResponseWriter, r *http.Request) {
	name := urlVar(r, "name")
	id := urlVar(r, "uuid")
	digest := r.URL.Query().Get("digest")

	body := io.LimitReader(r.Body, uploadBodyLimit)

	if digest == "" {
		drainBody(body)
		s.uploads.Cancel(id)
		v2Error(w, http.StatusBadRequest, "DIGEST_INVALID", "digest query parameter required")
		return
	}

	if _, err := s.uploads.Append(id, body); err != nil {
		drainBody(body)
		s.uploads.Cancel(id)
		writeUploadAppendError(w, err)
		return
	}

	fu, err := s.uploads.Finalize(id)
	if err != nil {
		writeUploadAppendError(w, err)
		return
	}
	defer func() { _ = fu.Close() }()

	s.persistVerifiedBlob(w, r, name, digest, fu)
}

// persistMonolithicUpload streams a single-PUT blob (digest known up front)
// straight to the object store while hashing inline — no disk spool, receive
// and upload overlap. The digest is verified after the stream drains; a
// mismatch deletes the object so the content-addressed key never keeps
// unverified bytes (the verify happens the instant the upload completes, so the
// window is negligible and content-addressing protects readers regardless).
func (s *Server) persistMonolithicUpload(w http.ResponseWriter, r *http.Request, name, digest string) {
	dgst := stripSHA256Prefix(digest)

	if exists, err := s.reg.BlobExists(r.Context(), dgst); err == nil && exists {
		drainBody(r.Body)
		s.blobCreated(w, name, digest)
		return
	}

	hasher := sha256.New()
	body := io.TeeReader(io.LimitReader(r.Body, uploadBodyLimit), hasher)
	if err := s.reg.PushBlobStreaming(r.Context(), dgst, body, r.ContentLength); err != nil {
		log.WithFunc("server.persistMonolithicUpload").Errorf(r.Context(), err, "stream blob sha256:%s (content-length=%d) failed", dgst, r.ContentLength)
		v2Error(w, http.StatusInternalServerError, "BLOB_UPLOAD_INVALID", err.Error())
		return
	}

	if got := "sha256:" + hex.EncodeToString(hasher.Sum(nil)); got != digest {
		_ = s.reg.DeleteBlob(r.Context(), dgst)
		v2Error(w, http.StatusBadRequest, "DIGEST_INVALID",
			fmt.Sprintf("digest mismatch: got %s, expected %s", got, digest))
		return
	}
	s.blobCreated(w, name, digest)
}

// persistVerifiedBlob verifies the digest then streams to the object store.
// Used by the chunked PATCH upload path, where the full blob is spooled to
// disk first so the digest can be checked before it reaches the object store.
func (s *Server) persistVerifiedBlob(w http.ResponseWriter, r *http.Request, name, digest string, fu *FinalizedUpload) {
	if got := fu.Digest(); got != digest {
		v2Error(w, http.StatusBadRequest, "DIGEST_INVALID",
			fmt.Sprintf("digest mismatch: got %s, expected %s", got, digest))
		return
	}

	rdr, err := fu.Reader()
	if err != nil {
		log.WithFunc("server.persistVerifiedBlob").Errorf(r.Context(), err, "open spooled blob %s failed", digest)
		v2Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	if err := s.reg.PushBlobFromStream(r.Context(), stripSHA256Prefix(digest), rdr, fu.Size()); err != nil {
		log.WithFunc("server.persistVerifiedBlob").Errorf(r.Context(), err, "push spooled blob %s (size=%d) failed", digest, fu.Size())
		v2Error(w, http.StatusInternalServerError, "BLOB_UPLOAD_INVALID", err.Error())
		return
	}
	s.blobCreated(w, name, digest)
}

func (s *Server) blobCreated(w http.ResponseWriter, name, digest string) {
	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", name, digest))
	w.Header().Set("Docker-Content-Digest", digest)
	w.WriteHeader(http.StatusCreated)
}

func uploadLocation(name, id string) string {
	return fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, id)
}

func writeUploadAppendError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errUploadNotFound):
		v2Error(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN", err.Error())
	case errors.Is(err, errUploadTooLarge):
		v2Error(w, http.StatusRequestEntityTooLarge, "SIZE_INVALID", err.Error())
	default:
		v2Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
	}
}

func drainBody(body io.Reader) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, uploadBodyLimit))
}
